package yard

import (
	"slices"
	"testing"

	"github.com/daniil4545/famulus/internal/github"
	"github.com/daniil4545/famulus/internal/state"
)

func ownerPR(n int, sha string, labels ...string) github.PullRequest {
	pr := github.PullRequest{Number: n, State: "open", User: github.User{Login: "owner"}}
	pr.Head.SHA = sha
	for _, l := range labels {
		pr.Labels = append(pr.Labels, github.Label{Name: l})
	}
	return pr
}

func reviewYard(t *testing.T, gh *fakeGitHub) *Yard {
	t.Helper()
	y, _, _ := newYard(t, gh)
	y.Config.Repos[0].Review = true
	y.Config.GateFiles = []string{".github/workflows/**", "Makefile", "*.mk"}
	y.Bot = "bot[bot]"
	return y
}

// spec: the decision table of the code checks, one row each.
func TestReviewDecisionTable(t *testing.T) {
	cases := []struct {
		name   string
		ci     map[string]github.CI
		files  []github.PRFile
		labels []string
		check  string
		posted bool
	}{
		{"ci running", map[string]github.CI{"s1": {State: "pending"}}, nil, nil, "s1 in_progress : Waiting for CI", false},
		{"ci red", map[string]github.CI{"s1": {State: "failure", Failed: []string{"gate"}}}, nil, nil, "s1 in_progress : Waiting for green CI", false},
		{"main red", map[string]github.CI{"main": {State: "failure", Failed: []string{"app"}}}, nil, nil, "s1 in_progress : main is red", false},
		{"gate file without label", nil, []github.PRFile{{Path: "src/a.go"}, {Path: ".github/workflows/ci.yml"}}, nil, "s1 completed failure: Gate files changed without the label", true},
		{"gate file with label", nil, []github.PRFile{{Path: "tools/x.mk"}}, []string{"gate-change"}, "s1 queued : " + reviewNext, false},
		{"gate file renamed away", nil, []github.PRFile{{Path: "ci.txt", Previous: ".github/workflows/ci.yml"}}, nil, "s1 completed failure: Gate files changed without the label", true},
		{"clean", nil, []github.PRFile{{Path: "src/a.go"}}, nil, "s1 queued : " + reviewNext, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh := newGitHub()
			gh.open = []github.PullRequest{ownerPR(7, "s1", c.labels...)}
			if c.ci != nil {
				gh.ci = c.ci
			}
			gh.files[7] = c.files
			y := reviewYard(t, gh)
			y.Pass(t.Context())
			if !slices.Equal(gh.checks, []string{c.check}) || (len(gh.posted) == 1) != c.posted {
				t.Fatalf("checks = %q, posted = %q", gh.checks, gh.posted)
			}
		})
	}
}

// R2: nobody else's pull request is read; drafts wait.
func TestReviewSkipsOthersAndDrafts(t *testing.T) {
	gh := newGitHub()
	stranger := ownerPR(1, "a")
	stranger.User.Login = "stranger"
	draft := ownerPR(2, "b")
	draft.Draft = true
	gh.open = []github.PullRequest{stranger, draft}
	y := reviewYard(t, gh)
	y.Pass(t.Context())
	if len(gh.checks) != 0 || len(gh.posted) != 0 {
		t.Fatalf("checks = %q, posted = %q", gh.checks, gh.posted)
	}
}

func TestReviewOncePerSHAAndAfterRestart(t *testing.T) {
	gh := newGitHub()
	gh.open = []github.PullRequest{ownerPR(7, "s1")}
	gh.files[7] = []github.PRFile{{Path: "Makefile"}}
	y := reviewYard(t, gh)
	y.Pass(t.Context())
	y.Pass(t.Context())
	if len(gh.posted) != 1 || len(gh.checks) != 1 {
		t.Fatalf("same SHA reviewed again: posted = %q, checks = %q", gh.posted, gh.checks)
	}

	// a restart that lost the state file finds its own review on the commit and does not repeat it
	y.State.Reviews = map[string]*state.PRReview{}
	y.Pass(t.Context())
	if len(gh.posted) != 1 {
		t.Fatalf("review repeated after restart: %q", gh.posted)
	}

	// a new push is a new SHA and a new decision
	gh.open = []github.PullRequest{ownerPR(7, "s2")}
	gh.files[7] = []github.PRFile{{Path: "src/a.go"}}
	y.Pass(t.Context())
	if gh.checks[len(gh.checks)-1] != "s2 queued : "+reviewNext {
		t.Fatalf("new push: checks = %q", gh.checks)
	}
}

func TestQueuedCheckIsNotResent(t *testing.T) {
	gh := newGitHub()
	gh.open = []github.PullRequest{ownerPR(7, "s1")}
	y := reviewYard(t, gh)
	for range 3 {
		y.Pass(t.Context())
	}
	if len(gh.checks) != 1 {
		t.Fatalf("unchanged check sent %d times", len(gh.checks))
	}
}
