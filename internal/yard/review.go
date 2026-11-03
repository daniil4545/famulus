package yard

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/daniil4545/famulus/internal/config"
	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/state"
)

const (
	checkName  = "famulus"
	gateLabel  = "gate-change"
	reviewNext = "Code checks passed; the agent review comes next"
)

// reviewRepo guards the owner's open pull requests: the code decides what it can, the agent
// review comes after. Nobody else's PR is read (R2).
func (y *Yard) reviewRepo(ctx context.Context, repo config.Repo, result *passResult) error {
	prs, err := y.GitHub.OpenPRs(ctx, repo.Name, repo.Base)
	if err != nil {
		return fmt.Errorf("list pull requests: %w", err)
	}
	for _, pr := range prs {
		if pr.Draft || pr.User.Login != y.Config.Owner {
			continue
		}
		rec := y.State.Reviews[state.Key(repo.Name, pr.Number)]
		if rec != nil && rec.SHA == pr.Head.SHA && rec.Verdict != "" {
			continue
		}
		if err := y.reviewPR(ctx, repo, pr); err != nil {
			result.failed(err)
			y.Log.Error("review failed", "repo", repo.Name, "pr", pr.Number, "sha", pr.Head.SHA, "err", err)
		}
	}
	return nil
}

// reviewPR runs the decision table of the code checks for one head SHA.
func (y *Yard) reviewPR(ctx context.Context, repo config.Repo, pr github.PullRequest) error {
	sha := pr.Head.SHA
	ci, err := y.GitHub.CISummary(ctx, repo.Name, sha, checkName)
	if err != nil {
		return fmt.Errorf("pr ci: %w", err)
	}
	// Pending and red CI keep the check in progress, never neutral: neutral would pass a
	// required check once the owner requires it.
	switch ci.State {
	case "pending":
		return y.setCheck(ctx, repo, pr, github.Check{Status: "in_progress", Title: "Waiting for CI", Summary: "The review starts when CI is green."})
	case "failure":
		return y.setCheck(ctx, repo, pr, github.Check{
			Status: "in_progress", Title: "Waiting for green CI",
			Summary: "Failed: " + strings.Join(ci.Failed, ", ") + ". The review starts when CI is green.",
		})
	}
	base, err := y.GitHub.CISummary(ctx, repo.Name, repo.Base, checkName)
	if err != nil {
		return fmt.Errorf("base ci: %w", err)
	}
	if base.State == "failure" {
		return y.setCheck(ctx, repo, pr, github.Check{
			Status: "in_progress", Title: repo.Base + " is red",
			Summary: "Failed on " + repo.Base + ": " + strings.Join(base.Failed, ", ") + ". Fix " + repo.Base + " first.",
		})
	}
	files, err := y.GitHub.PRFiles(ctx, repo.Name, pr.Number)
	if err != nil {
		return fmt.Errorf("pr files: %w", err)
	}
	if gate := gateFiles(files, y.Config.GateFiles); len(gate) > 0 && !hasLabel(pr.Labels, gateLabel) {
		body := fmt.Sprintf("This PR changes files of the CI gate: %s. A PR runs its own workflow, so such a change needs the owner's label `%s`.",
			strings.Join(gate, ", "), gateLabel)
		return y.verdict(ctx, repo, pr, "REQUEST_CHANGES", body, "Gate files changed without the label")
	}
	return y.setCheck(ctx, repo, pr, github.Check{Status: "queued", Title: reviewNext, Summary: "CI and " + repo.Base + " are green; gate files untouched or labeled."})
}

// verdict publishes one review on the head SHA once: a review the bot already left on that
// commit (a restart after the post) is taken as given.
func (y *Yard) verdict(ctx context.Context, repo config.Repo, pr github.PullRequest, event, body, title string) error {
	sha := pr.Head.SHA
	reviews, err := y.GitHub.Reviews(ctx, repo.Name, pr.Number)
	if err != nil {
		return fmt.Errorf("pr reviews: %w", err)
	}
	given := slices.ContainsFunc(reviews, func(r github.Review) bool { return r.User.Login == y.Bot && r.CommitID == sha })
	if !given {
		if err := y.GitHub.CreateReview(ctx, repo.Name, pr.Number, sha, event, body, nil); err != nil {
			return fmt.Errorf("publish review: %w", err)
		}
	}
	conclusion := "success"
	if event == "REQUEST_CHANGES" {
		conclusion = "failure"
	}
	if err := y.setCheck(ctx, repo, pr, github.Check{Status: "completed", Conclusion: conclusion, Title: title, Summary: body}); err != nil {
		return err
	}
	rec := y.State.Reviews[state.Key(repo.Name, pr.Number)]
	rec.Verdict = event
	if err := y.State.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	y.Log.Info("review", "repo", repo.Name, "pr", pr.Number, "sha", sha, "verdict", event)
	return nil
}

// setCheck sets the famulus check of the head SHA, skipping a call that would change nothing.
// A new SHA starts a new record: the old SHA's check stays as it was.
func (y *Yard) setCheck(ctx context.Context, repo config.Repo, pr github.PullRequest, check github.Check) error {
	key := state.Key(repo.Name, pr.Number)
	rec := y.State.Reviews[key]
	if rec == nil || rec.SHA != pr.Head.SHA {
		rec = &state.PRReview{SHA: pr.Head.SHA}
		y.State.Reviews[key] = rec
	}
	text := check.Status + " " + check.Conclusion + ": " + check.Title + "\n" + check.Summary
	if rec.Check == text {
		return nil
	}
	if err := y.GitHub.UpsertCheck(ctx, repo.Name, pr.Head.SHA, check); err != nil {
		return fmt.Errorf("famulus check: %w", err)
	}
	rec.Check, rec.At = text, y.now().UTC()
	if err := y.State.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// gateFiles returns the changed paths matching a pattern: "dir/**" matches below dir, any
// other pattern matches the whole path or its base name.
func gateFiles(files []github.PRFile, patterns []string) []string {
	var out []string
	for _, f := range files {
		for _, p := range patterns {
			dir, tree := strings.CutSuffix(p, "/**")
			whole, _ := path.Match(p, f.Path)
			base, _ := path.Match(p, path.Base(f.Path))
			if (tree && strings.HasPrefix(f.Path, dir+"/")) || (!tree && (whole || base)) {
				out = append(out, f.Path)
				break
			}
		}
	}
	return out
}

func hasLabel(labels []github.Label, name string) bool {
	return slices.ContainsFunc(labels, func(l github.Label) bool { return l.Name == name })
}
