package yard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/daniil4545/famulus/internal/agent"
	"github.com/daniil4545/famulus/internal/config"
	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/state"
	agentrules "github.com/daniil4545/famulus/rules"
)

const retryPrompt = "Your last message was not a valid answer (%s). Reply with the JSON object only, as the rules say."

type answer struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []finding `json:"findings"`
}

type finding struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Why      string `json:"why"`
	Scenario string `json:"scenario"`
}

// agentReview runs the review agent on a PR whose code checks passed and publishes its
// verdict. The agent works in a detached worktree that is removed afterwards.
func (y *Yard) agentReview(ctx context.Context, repo config.Repo, pr github.PullRequest, files []github.PRFile) error {
	if y.paused(config.Claude) {
		return y.pausedCheck(ctx, repo, pr)
	}
	// a restart after the post: the bot's review on this commit stands, its check is finished
	if given, err := y.givenReview(ctx, repo, pr); err != nil || given != "" {
		if err != nil {
			return err
		}
		event, title := givenEvents[given][0], givenEvents[given][1]
		return y.verdict(ctx, repo, pr, event, "Review already published on this commit.", title, nil)
	}
	cached := state.Key(repo.Name, pr.Number) + "@" + pr.Head.SHA
	if ans, ok := y.answers[cached]; ok {
		return y.publishOnce(ctx, repo, pr, ans, files, cached)
	}
	if y.reviewsFull() {
		return y.setCheck(ctx, repo, pr, github.Check{
			Status: "queued", Title: "Daily review limit reached",
			Summary: fmt.Sprintf("%d agent reviews ran today (UTC); this one runs tomorrow.", y.Config.ReviewDailyLimit),
		})
	}
	dir, prompt, err := y.reviewInput(ctx, repo, pr, files)
	if err != nil {
		return err
	}
	defer y.dropReview(ctx, repo, pr.Number)
	// every model run counts, a failed one too: the cap is about cost
	y.countReview()
	if err := y.State.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	ans, usage, err := y.askReviewer(ctx, dir, prompt)
	y.Log.Info("review run", "repo", repo.Name, "pr", pr.Number, "sha", pr.Head.SHA, "cost_usd", usage.CostUSD,
		"input_tokens", usage.Input, "output_tokens", usage.Output, "cache_read_tokens", usage.CacheRead,
		"cache_write_tokens", usage.CacheWrite, "turns", usage.Turns)
	if limit, ok := errors.AsType[*agent.LimitError](err); ok {
		y.pauseReviews(limit)
		return y.pausedCheck(ctx, repo, pr)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("review interrupted: %w", ctx.Err())
	}
	if err != nil {
		y.Log.Error("agent review failed", "repo", repo.Name, "pr", pr.Number, "sha", pr.Head.SHA, "err", err)
		// the cause may carry CLI output; the public comment names only the kind
		reason := "the agent run failed (details in the runner log)"
		switch {
		case errors.Is(err, errBadAnswer):
			reason = "the agent answered twice without a valid verdict"
		case errors.Is(err, errLeak):
			reason = "the answer held a runner secret and was not published"
		}
		return y.verdict(ctx, repo, pr, "COMMENT", "famulus review failed: "+reason+". Push a new commit to retry.", "Review failed", nil)
	}
	return y.publishOnce(ctx, repo, pr, ans, files, cached)
}

// reviewInput builds the review worktree of the PR head and the full prompt with the diff.
func (y *Yard) reviewInput(ctx context.Context, repo config.Repo, pr github.PullRequest, files []github.PRFile) (dir, prompt string, err error) {
	prompt, err = y.reviewPrompt(ctx, repo, pr, files)
	if err != nil {
		return "", "", err
	}
	dir, diff, err := y.Work.ReviewTree(ctx, repo.Name, repo.Base, pr.Number, pr.Head.SHA)
	if err != nil {
		return "", "", fmt.Errorf("review tree: %w", err)
	}
	return dir, prompt + "\n\n## Diff\n\n```diff\n" + diff + "\n```\n", nil
}

func (y *Yard) dropReview(ctx context.Context, repo config.Repo, number int) {
	if err := y.Work.RemoveReview(context.WithoutCancel(ctx), repo.Name, number); err != nil {
		y.Log.Warn("remove review tree", "repo", repo.Name, "pr", number, "err", err)
	}
}

// Preview is the answer the guard would publish on a PR, with what the model runs spent.
type Preview struct {
	SHA string `json:"sha"`
	answer
	Usage agent.Usage `json:"usage"`
}

// PreviewReview runs the agent review of one PR and returns the answer; nothing is
// published, saved or counted, and the code checks are skipped. It measures a prompt change.
func (y *Yard) PreviewReview(ctx context.Context, repo config.Repo, number int) (Preview, error) {
	pr, err := y.GitHub.PR(ctx, repo.Name, number)
	if err != nil {
		return Preview{}, fmt.Errorf("pull request: %w", err)
	}
	files, err := y.GitHub.PRFiles(ctx, repo.Name, number)
	if err != nil {
		return Preview{}, fmt.Errorf("pr files: %w", err)
	}
	dir, prompt, err := y.reviewInput(ctx, repo, pr, files)
	if err != nil {
		return Preview{}, err
	}
	defer y.dropReview(ctx, repo, number)
	ans, usage, err := y.askReviewer(ctx, dir, prompt)
	return Preview{SHA: pr.Head.SHA, answer: ans, Usage: usage}, err
}

func (y *Yard) publishOnce(ctx context.Context, repo config.Repo, pr github.PullRequest, ans answer, files []github.PRFile, key string) error {
	err := y.publish(ctx, repo, pr, ans, files)
	status, refused := errors.AsType[*github.HTTPError](err)
	if err != nil && (!refused || !permanent(status.Status)) && ctx.Err() == nil {
		if y.answers == nil {
			y.answers = map[string]answer{}
		}
		y.answers[key] = ans
		return err
	}
	delete(y.answers, key)
	return err
}

// publish posts the verdict with findings on their lines, falling back to the body.
func (y *Yard) publish(ctx context.Context, repo config.Repo, pr github.PullRequest, ans answer, files []github.PRFile) error {
	event, title := "APPROVE", "Approved"
	if ans.Verdict == "request_changes" {
		event, title = "REQUEST_CHANGES", "Changes requested"
	}
	body, comments := reviewBody(ans, files)
	err := y.verdict(ctx, repo, pr, event, body, title, comments)
	if status, ok := errors.AsType[*github.HTTPError](err); ok && status.Status == http.StatusUnprocessableEntity && len(comments) > 0 {
		// GitHub refuses the whole review for one line it does not accept: all in the body
		body, _ = reviewBody(ans, nil)
		err = y.verdict(ctx, repo, pr, event, body, title, nil)
	}
	if status, ok := errors.AsType[*github.HTTPError](err); ok && permanent(status.Status) {
		// a refused publish stays refused: no new model run on this SHA
		y.recordVerdict(repo, pr, "PUBLISH_FAILED")
		if err := y.setCheck(ctx, repo, pr, github.Check{
			Status: "completed", Conclusion: "failure", Title: "Publishing the review failed",
			Summary: fmt.Sprintf("GitHub answered %d to the review. Push a new commit to retry.", status.Status),
		}); err != nil {
			y.Log.Warn("publish failure check", "repo", repo.Name, "pr", pr.Number, "err", err)
		}
	}
	return err
}

func (y *Yard) pausedCheck(ctx context.Context, repo config.Repo, pr github.PullRequest) error {
	return y.setCheck(ctx, repo, pr, github.Check{
		Status: "queued", Title: "Agent review paused",
		Summary: "The review agent's plan is used up until " + y.State.Paused[config.Claude].Format("2006-01-02 15:04") + " UTC.",
	})
}

// givenEvents maps a review state GitHub reports to the event and check title that gave it.
var givenEvents = map[string][2]string{
	"APPROVED":          {"APPROVE", "Approved"},
	"CHANGES_REQUESTED": {"REQUEST_CHANGES", "Changes requested"},
	"COMMENTED":         {"COMMENT", "Review failed"},
}

var (
	errBadAnswer = errors.New("no valid answer")
	errLeak      = errors.New("the answer holds a runner secret")
)

// askReviewer runs a new session and asks once more when the answer does not parse.
func (y *Yard) askReviewer(ctx context.Context, dir, prompt string) (answer, agent.Usage, error) {
	runCtx, cancel := context.WithTimeout(ctx, y.Config.Timeout)
	defer cancel()
	req := agent.Request{Dir: dir, Prompt: prompt, Model: y.Config.Models[config.Claude], Effort: y.Config.Effort[config.Claude], ReadOnly: true}
	var spent agent.Usage
	for try := range 2 {
		res, err := y.Agents.Run(runCtx, config.Claude, req)
		spent = spent.Add(res.Usage)
		if err != nil {
			return answer{}, spent, fmt.Errorf("review agent: %w", err)
		}
		if agent.Leaks(res.Text) {
			return answer{}, spent, errLeak
		}
		ans, err := parseAnswer(res.Text)
		if err == nil {
			return ans, spent, nil
		}
		y.Log.Warn("review answer rejected", "try", try+1, "err", err)
		req.Session, req.Prompt = res.Session, fmt.Sprintf(retryPrompt, err)
	}
	return answer{}, spent, errBadAnswer
}

// givenReview returns the state of the bot's review on the head SHA, empty when there is none.
func (y *Yard) givenReview(ctx context.Context, repo config.Repo, pr github.PullRequest) (string, error) {
	reviews, err := y.GitHub.Reviews(ctx, repo.Name, pr.Number)
	if err != nil {
		return "", fmt.Errorf("pr reviews: %w", err)
	}
	i := slices.IndexFunc(reviews, func(r github.Review) bool {
		_, known := givenEvents[r.State]
		return r.User.Login == y.Bot && r.CommitID == pr.Head.SHA && known
	})
	if i < 0 {
		return "", nil
	}
	return reviews[i].State, nil
}

// reviewPrompt carries only the owner's text (R2): the PR is the owner's, and linked issues
// written by anyone else are left out.
func (y *Yard) reviewPrompt(ctx context.Context, repo config.Repo, pr github.PullRequest, files []github.PRFile) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n## Pull request #%d: %s\n\n%s\n", strings.TrimSpace(agentrules.Review), pr.Number, pr.Title, pr.Body)
	for _, n := range github.LinkedIssues(pr.Body) {
		issue, err := y.GitHub.Issue(ctx, repo.Name, n)
		if errors.Is(err, github.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("linked issue #%d: %w", n, err)
		}
		if issue.User.Login == y.Config.Owner {
			fmt.Fprintf(&b, "\n## Issue #%d: %s\n\n%s\n", n, issue.Title, issue.Body)
		}
	}
	b.WriteString("\n## Changed files\n\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s (%s, +%d -%d)\n", f.Path, f.Status, f.Additions, f.Deletions)
	}
	return b.String(), nil
}

// parseAnswer takes the JSON object of the final message; the agent's output is untrusted,
// so everything a publish relies on is checked here.
func parseAnswer(text string) (answer, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return answer{}, errors.New("no JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(text[start : end+1]))
	dec.DisallowUnknownFields()
	var a answer
	if err := dec.Decode(&a); err != nil {
		return answer{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return answer{}, errors.New("more than one JSON object")
	}
	if strings.TrimSpace(a.Summary) == "" {
		return answer{}, errors.New("empty summary")
	}
	must := false
	for i, f := range a.Findings {
		if f.File == "" || f.Why == "" || f.Line < 0 || (f.Severity != "must" && f.Severity != "should") {
			return answer{}, fmt.Errorf("finding %d needs file, why, line >= 0 and severity must or should", i+1)
		}
		must = must || f.Severity == "must"
	}
	if (a.Verdict != "approve" || must) && (a.Verdict != "request_changes" || !must) {
		return answer{}, fmt.Errorf("verdict %q does not match the findings: request_changes exactly when a must finding exists", a.Verdict)
	}
	return a, nil
}

// reviewBody puts findings on their lines when the line is in the diff; GitHub refuses a
// comment elsewhere, so those go to the body.
func reviewBody(a answer, files []github.PRFile) (string, []github.ReviewComment) {
	var body strings.Builder
	body.WriteString(a.Summary)
	var comments []github.ReviewComment
	for _, f := range a.Findings {
		text := f.Severity + ": " + f.Why
		if f.Scenario != "" {
			text += "\n\nScenario: " + f.Scenario
		}
		if inDiff(files, f.File, f.Line) {
			comments = append(comments, github.ReviewComment{Path: f.File, Line: f.Line, Side: "RIGHT", Body: text})
			continue
		}
		where := f.File
		if f.Line > 0 {
			where += ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(&body, "\n\n- `%s` %s", where, strings.ReplaceAll(text, "\n\n", " "))
	}
	text := body.String()
	if len(text) > maxBody {
		// GitHub refuses a review body over 65536 characters
		text = text[:maxBody] + "\n\n[cut: the review was longer]"
	}
	return text, comments
}

const maxBody = 60000

var hunk = regexp.MustCompile(`(?m)^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// inDiff tells whether a line of the new file is inside a hunk of the PR's patch.
func inDiff(files []github.PRFile, file string, line int) bool {
	i := slices.IndexFunc(files, func(f github.PRFile) bool { return f.Path == file })
	if i < 0 || line <= 0 {
		return false
	}
	for _, m := range hunk.FindAllStringSubmatch(files[i].Patch, -1) {
		start, _ := strconv.Atoi(m[1])
		size := 1
		if m[2] != "" {
			size, _ = strconv.Atoi(m[2])
		}
		if line >= start && line < start+size {
			return true
		}
	}
	return false
}

// permanent tells a refusal of the review itself from a passing one (auth, rate limit, outage).
func permanent(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge || status == http.StatusUnprocessableEntity
}
