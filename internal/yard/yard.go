// Package yard is the polling loop: it takes labeled issues to an agent, turns the agent's
// commits into a pull request and passes the owner's later notes back to the same session.
// The model of states and passes is in docs/spec.md.
package yard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/daniil4545/famulus/internal/agent"
	"github.com/daniil4545/famulus/internal/config"
	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/state"
)

type GitHub interface {
	OpenIssues(ctx context.Context, repo, label string) ([]github.Issue, error)
	AddLabels(ctx context.Context, repo string, number int, labels ...string) error
	RemoveLabel(ctx context.Context, repo string, number int, label string) error
	Comment(ctx context.Context, repo string, number int, body string) error
	Notes(ctx context.Context, repo string, number int, pull bool, since time.Time) ([]github.Note, error)
	CreatePR(ctx context.Context, repo, head, base, title, body string) (github.PullRequest, error)
	PR(ctx context.Context, repo string, number int) (github.PullRequest, error)
}

type Workspace interface {
	Prepare(ctx context.Context, repo, base, branch string, issue int) (string, error)
	Head(ctx context.Context, dir string) (string, error)
	CommitLeftovers(ctx context.Context, dir, message string) (bool, error)
	Ahead(ctx context.Context, dir, base string) (int, error)
	Push(ctx context.Context, repo, dir, branch string) error
	Remove(ctx context.Context, repo, branch string, issue int) error
}

type Runner interface {
	Run(ctx context.Context, name string, req agent.Request) (agent.Result, error)
}

type Yard struct {
	Config config.Config
	GitHub GitHub
	Work   Workspace
	Agents Runner
	State  *state.Store
	Log    *slog.Logger
	// Clock replaces time.Now in tests.
	Clock func() time.Time
	// Passed, when set, hears the end of every pass.
	Passed func(time.Time)
}

const leftoverMessage = "chore: commit changes the agent left uncommitted"

const maxNudges = 2

var (
	// questions is how the rules make an agent ask: a Markdown heading "Questions".
	questions  = regexp.MustCompile(`(?mi)^#+\s*questions\b`)
	errStopped = errors.New("the agent stopped without a result twice after a nudge: no commits and no questions")
)

// Run makes a pass every interval until the context ends.
func (y *Yard) Run(ctx context.Context) {
	ticker := time.NewTicker(y.Config.Interval)
	defer ticker.Stop()
	for {
		y.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass looks at every repository once and runs at most one agent at a time.
func (y *Yard) Pass(ctx context.Context) {
	for _, repo := range y.Config.Repos {
		if ctx.Err() != nil {
			return
		}
		if err := y.passRepo(ctx, repo); err != nil {
			y.Log.Error("pass failed", "repo", repo.Name, "err", err)
		}
	}
	if y.Passed != nil {
		y.Passed(y.now())
	}
}

func (y *Yard) passRepo(ctx context.Context, repo config.Repo) error {
	for _, t := range y.State.Tasks {
		if t.Repo == repo.Name && t.Status == state.Review && !y.paused(t.Agent) {
			if err := y.followPR(ctx, repo, t); err != nil {
				y.Log.Error("follow pr failed", "task", state.Key(t.Repo, t.Issue), "pr", t.PR, "err", err)
			}
		}
	}
	issues, err := y.GitHub.OpenIssues(ctx, repo.Name, y.Config.Label)
	if err != nil {
		return fmt.Errorf("list issues: %w", err)
	}
	for _, issue := range issues {
		t := y.State.Tasks[state.Key(repo.Name, issue.Number)]
		name := y.agentFor(repo, issue)
		if t != nil {
			name = t.Agent
		}
		var err error
		switch {
		case issue.HasLabel(y.label(state.Failed)), issue.HasLabel(y.label(state.Done)), y.paused(name):
			continue
		case t != nil && t.Status == state.Review:
			continue
		case t != nil && t.Status == state.Blocked:
			err = y.followIssue(ctx, repo, t)
		case t == nil && y.dailyFull():
			continue
		case t != nil && t.PR != 0:
			// a failed or interrupted run on an open PR: the next pass follows the PR again
			err = y.setStatus(ctx, t, state.Review)
		default:
			// new, failed and then unlabeled by the owner, paused, or cut short by a restart
			err = y.start(ctx, repo, issue)
		}
		if err != nil {
			y.Log.Error("issue failed", "task", state.Key(repo.Name, issue.Number), "err", err)
		}
	}
	return nil
}

func (y *Yard) start(ctx context.Context, repo config.Repo, issue github.Issue) error {
	t := &state.Task{
		Repo:   repo.Name,
		Issue:  issue.Number,
		Title:  issue.Title,
		Agent:  y.agentFor(repo, issue),
		Branch: branchName(issue),
		Since:  issue.Created,
	}
	if prev := y.State.Tasks[state.Key(t.Repo, t.Issue)]; prev != nil {
		t.Status = prev.Status // so the old status label goes away
		t.PauseNoted = prev.PauseNoted
	} else {
		y.countStart()
	}
	y.State.Tasks[state.Key(t.Repo, t.Issue)] = t
	if err := y.setStatus(ctx, t, state.Working); err != nil {
		return err
	}
	notes, err := y.ownerNotes(ctx, t, t.Issue, false)
	if err != nil {
		return y.fail(ctx, t, err)
	}
	prompt := issuePrompt(repo, issue, notes, t.Branch)
	return y.work(ctx, repo, t, prompt, notes)
}

// followIssue resumes a blocked task when the owner answered on the issue.
func (y *Yard) followIssue(ctx context.Context, repo config.Repo, t *state.Task) error {
	notes, err := y.ownerNotes(ctx, t, t.Issue, false)
	if err != nil || len(notes) == 0 {
		return err
	}
	if err := y.setStatus(ctx, t, state.Working); err != nil {
		return err
	}
	return y.work(ctx, repo, t, answerPrompt(t, notes), notes)
}

// followPR closes a merged or closed task, or resumes the session with new owner notes from
// the pull request and the issue.
func (y *Yard) followPR(ctx context.Context, repo config.Repo, t *state.Task) error {
	pr, err := y.GitHub.PR(ctx, t.Repo, t.PR)
	if err != nil {
		return fmt.Errorf("read pr: %w", err)
	}
	if pr.State != "open" {
		y.Log.Info("pr finished", "task", state.Key(t.Repo, t.Issue), "pr", t.PR, "merged", pr.Merged)
		if err := y.Work.Remove(ctx, t.Repo, t.Branch, t.Issue); err != nil {
			y.Log.Warn("remove worktree", "task", state.Key(t.Repo, t.Issue), "err", err)
		}
		return y.setStatus(ctx, t, state.Done)
	}
	notes, err := y.ownerNotes(ctx, t, t.PR, true)
	if err != nil {
		return err
	}
	issueNotes, err := y.ownerNotes(ctx, t, t.Issue, false)
	if err != nil {
		return err
	}
	notes = append(notes, issueNotes...)
	if len(notes) == 0 {
		return nil
	}
	if err := y.setStatus(ctx, t, state.Working); err != nil {
		return err
	}
	return y.work(ctx, repo, t, reviewPrompt(t, notes), notes)
}

// work runs the agent and publishes the outcome: commits go to the branch and the pull
// request, the final message goes to the pull request or, without commits, to the issue.
func (y *Yard) work(ctx context.Context, repo config.Repo, t *state.Task, prompt string, notes []github.Note) error {
	key := state.Key(t.Repo, t.Issue)
	dir, err := y.Work.Prepare(ctx, t.Repo, repo.Base, t.Branch, t.Issue)
	if err != nil {
		return y.fail(ctx, t, err)
	}
	headBefore, err := y.Work.Head(ctx, dir)
	if err != nil {
		return y.fail(ctx, t, err)
	}
	res, head, err := y.runToResult(ctx, t, dir, prompt, headBefore, repo)
	if limit, ok := errors.AsType[*agent.LimitError](err); ok {
		return y.pause(ctx, t, limit)
	}
	if err != nil {
		return y.fail(ctx, t, err)
	}
	for _, n := range notes {
		if n.Created.After(t.Since) {
			t.Since = n.Created
		}
	}
	ahead, err := y.Work.Ahead(ctx, dir, repo.Base)
	if err != nil {
		return y.fail(ctx, t, err)
	}
	if head != headBefore {
		if err := y.Work.Push(ctx, t.Repo, dir, t.Branch); err != nil {
			return y.fail(ctx, t, err)
		}
		y.Log.Info("pushed", "task", key, "branch", t.Branch, "head", head)
	}
	text := res.Text + footer(t)
	switch {
	case t.PR != 0:
		if err := y.GitHub.Comment(ctx, t.Repo, t.PR, text); err != nil {
			return y.fail(ctx, t, err)
		}
		return y.setStatus(ctx, t, state.Review)
	case ahead > 0:
		pr, err := y.GitHub.CreatePR(ctx, t.Repo, t.Branch, repo.Base, t.Title, fmt.Sprintf("%s\n\nCloses #%d%s", res.Text, t.Issue, footer(t)))
		if err != nil {
			return y.fail(ctx, t, err)
		}
		t.PR = pr.Number
		y.Log.Info("pr opened", "task", key, "pr", pr.Number, "url", pr.URL)
		return y.setStatus(ctx, t, state.Review)
	default:
		if err := y.GitHub.Comment(ctx, t.Repo, t.Issue, text); err != nil {
			return y.fail(ctx, t, err)
		}
		return y.setStatus(ctx, t, state.Blocked)
	}
}

// runToResult runs the agent and commits what it left. Before a pull request exists, a run with
// no commits and no questions gets the same session back up to maxNudges times: small models
// often end their turn mid-task.
func (y *Yard) runToResult(ctx context.Context, t *state.Task, dir, prompt, headBefore string, repo config.Repo) (agent.Result, string, error) {
	for nudge := 0; ; nudge++ {
		res, err := y.runAgent(ctx, t, dir, prompt, repo)
		if err != nil {
			return res, "", err
		}
		if _, err := y.Work.CommitLeftovers(ctx, dir, leftoverMessage); err != nil {
			return res, "", fmt.Errorf("commit leftovers: %w", err)
		}
		head, err := y.Work.Head(ctx, dir)
		if err != nil {
			return res, "", fmt.Errorf("read head: %w", err)
		}
		if t.PR != 0 || head != headBefore || questions.MatchString(res.Text) {
			return res, head, nil
		}
		if nudge == maxNudges {
			return res, "", errStopped
		}
		y.Log.Info("agent stopped without a result, nudging", "task", state.Key(t.Repo, t.Issue), "nudge", nudge+1)
		prompt = nudgePrompt
	}
}

// runAgent resumes the session when there is one; a resume that fails, for example after the
// session store was lost, falls back to a new session with the full context.
func (y *Yard) runAgent(ctx context.Context, t *state.Task, dir, prompt string, repo config.Repo) (agent.Result, error) {
	key := state.Key(t.Repo, t.Issue)
	req := agent.Request{Dir: dir, Prompt: prompt, Session: t.Session, Model: y.Config.Models[t.Agent], Effort: y.Config.Effort[t.Agent]}
	if t.Session == "" && !strings.HasPrefix(prompt, rules) {
		req.Prompt = rules + "\n\n" + restartPrompt(repo, t) + "\n\n" + prompt
	}
	runCtx, cancel := context.WithTimeout(ctx, y.Config.Timeout)
	defer cancel()
	started := time.Now()
	y.Log.Info("agent started", "task", key, "agent", t.Agent, "session", t.Session)
	res, err := y.Agents.Run(runCtx, t.Agent, req)
	_, limited := errors.AsType[*agent.LimitError](err)
	if err != nil && !limited && t.Session != "" && ctx.Err() == nil {
		y.Log.Warn("resume failed, starting a new session", "task", key, "err", err)
		req.Session = ""
		req.Prompt = rules + "\n\n" + restartPrompt(repo, t) + "\n\n" + prompt
		res, err = y.Agents.Run(runCtx, t.Agent, req)
	}
	y.Log.Info("agent finished", "task", key, "agent", t.Agent, "took", time.Since(started).Round(time.Second), "err", err)
	if res.Session != "" {
		t.Session = res.Session
	}
	if err != nil {
		return res, fmt.Errorf("agent %s: %w", t.Agent, err)
	}
	return res, nil
}

func (y *Yard) fail(ctx context.Context, t *state.Task, cause error) error {
	if ctx.Err() != nil {
		// the runner is stopping: the task stays as it is and restarts on the next start
		y.Log.Warn("run interrupted", "task", state.Key(t.Repo, t.Issue), "err", cause)
		return cause
	}
	msg := fmt.Sprintf("famulus could not finish this run: %s\n\nRemove the `%s` label to try again.%s",
		cause, y.label(state.Failed), footer(t))
	if err := y.GitHub.Comment(ctx, t.Repo, t.Issue, msg); err != nil {
		y.Log.Warn("failure comment", "task", state.Key(t.Repo, t.Issue), "err", err)
	}
	if err := y.setStatus(ctx, t, state.Failed); err != nil {
		y.Log.Warn("failure label", "task", state.Key(t.Repo, t.Issue), "err", err)
	}
	return cause
}

// setStatus moves the task and its issue label together and saves the state file.
func (y *Yard) setStatus(ctx context.Context, t *state.Task, status state.Status) error {
	prev := t.Status
	t.Status = status
	t.Updated = y.now().UTC()
	if status == state.Review || status == state.Blocked {
		t.PauseNoted = false
	}
	if err := y.State.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	if prev != "" && prev != status {
		if err := y.GitHub.RemoveLabel(ctx, t.Repo, t.Issue, y.label(prev)); err != nil {
			return fmt.Errorf("remove label: %w", err)
		}
	}
	if status == state.Working || prev == "" {
		// a failed label set by an earlier attempt may still be there
		if err := y.GitHub.RemoveLabel(ctx, t.Repo, t.Issue, y.label(state.Failed)); err != nil {
			return fmt.Errorf("remove label: %w", err)
		}
	}
	y.Log.Info("status", "task", state.Key(t.Repo, t.Issue), "from", prev, "to", status)
	if err := y.GitHub.AddLabels(ctx, t.Repo, t.Issue, y.label(status)); err != nil {
		return fmt.Errorf("add label: %w", err)
	}
	return nil
}

// ownerNotes returns the owner's notes newer than the task's mark; nobody else's text reaches
// the agent.
func (y *Yard) ownerNotes(ctx context.Context, t *state.Task, number int, pull bool) ([]github.Note, error) {
	all, err := y.GitHub.Notes(ctx, t.Repo, number, pull, t.Since)
	if err != nil {
		return nil, fmt.Errorf("read notes of #%d: %w", number, err)
	}
	var notes []github.Note
	for _, n := range all {
		if strings.EqualFold(n.Author, y.Config.Owner) {
			notes = append(notes, n)
		}
	}
	return notes, nil
}

func (y *Yard) label(s state.Status) string {
	return y.Config.Label + ":" + string(s)
}

// agentFor picks the agent: a label agent:<name> on the issue, else the repository's, else
// the default.
func (y *Yard) agentFor(repo config.Repo, issue github.Issue) string {
	for _, name := range []string{config.Claude, config.Codex, config.OpenCode} {
		if issue.HasLabel(y.Config.Label + ":" + name) {
			return name
		}
	}
	if repo.Agent != "" {
		return repo.Agent
	}
	return y.Config.Agent
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func branchName(issue github.Issue) string {
	slug := strings.Trim(nonWord.ReplaceAllString(strings.ToLower(issue.Title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.TrimRight(slug[:40], "-")
	}
	if slug == "" {
		return fmt.Sprintf("agent/%d", issue.Number)
	}
	return fmt.Sprintf("agent/%d-%s", issue.Number, slug)
}

func footer(t *state.Task) string {
	if t.Session == "" {
		return fmt.Sprintf("\n\n---\n<sub>famulus · %s</sub>", t.Agent)
	}
	return fmt.Sprintf("\n\n---\n<sub>famulus · %s · session `%s`</sub>", t.Agent, t.Session)
}
