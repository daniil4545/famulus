package yard

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daniil4545/famulus/internal/agent"
	"github.com/daniil4545/famulus/internal/github"
)

type alertServer struct {
	at                            time.Time
	issues                        []github.Issue
	notes                         []github.Note
	created, closed               int
	offline, failClose, lostClose bool
	requests                      []string
}

func newAlertServer(t *testing.T, y *Yard) *alertServer {
	t.Helper()
	a := &alertServer{at: t0}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		if a.offline {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":1}`)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			fmt.Fprintf(w, `{"token":"test","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case strings.Contains(r.URL.Path, "/labels/"):
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/repos/o/alerts/issues" && r.Method == http.MethodGet:
			if r.URL.Query().Get("labels") != alertLabel {
				t.Error("missing alert filter")
			}
			json.NewEncoder(w).Encode(a.issues)
		case r.URL.Path == "/repos/o/alerts/issues" && r.Method == http.MethodPost:
			var body struct {
				Title  string   `json:"title"`
				Body   string   `json:"body"`
				Labels []string `json:"labels"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Title != "famulus: failure" || len(body.Labels) != 1 || body.Labels[0] != alertLabel {
				t.Errorf("issue body: %+v", body)
			}
			if strings.Contains(body.Body, "private-value") {
				t.Error("secret published")
			}
			a.created++
			i := github.Issue{Number: a.created, Body: body.Body, Created: a.at}
			a.issues = append(a.issues, i)
			json.NewEncoder(w).Encode(i)
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodGet:
			since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
			var notes []map[string]any
			for _, n := range a.notes {
				if n.Created.After(since) {
					notes = append(notes, map[string]any{"body": n.Body, "created_at": n.Created})
				}
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			start := min(max(0, page-1)*100, len(notes))
			json.NewEncoder(w).Encode(notes[start:min(start+100, len(notes))])
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == http.MethodPost:
			var body struct {
				Body string `json:"body"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Body, "private-value") {
				t.Error("secret in comment")
			}
			a.notes = append(a.notes, github.Note{Body: body.Body, Created: a.at})
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/repos/o/alerts/issues/1" && r.Method == http.MethodPatch:
			var body struct {
				State string `json:"state"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.State != "closed" {
				t.Error("not closed")
			}
			if a.failClose {
				http.Error(w, "try later", http.StatusServiceUnavailable)
				return
			}
			a.closed++
			a.issues = nil
			if a.lostClose {
				http.Error(w, "response lost", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	y.GitHub = github.NewClient(srv.URL, "1", key)
	y.Config.AlertRepo = "o/alerts"
	y.alert.lastGood = t0
	return a
}

func TestAlertLostCloseResponseDoesNotCreateAnotherIssue(t *testing.T) {
	y, _, _ := newYard(t, newGitHub())
	a := newAlertServer(t, y)
	for range 3 {
		if err := y.reportAlert(t.Context(), a.at, 1, context.DeadlineExceeded); err != nil {
			t.Fatal(err)
		}
	}
	a.lostClose = true
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err == nil {
		t.Fatal("response loss not simulated")
	}
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err != nil {
		t.Fatal(err)
	}
	if a.created != 1 || a.closed != 1 {
		t.Fatalf("outage duplicated: %+v", a)
	}
}

func TestAlertRecoveryAfterManyComments(t *testing.T) {
	y, _, _ := newYard(t, newGitHub())
	a := newAlertServer(t, y)
	y.alert.failures = 2
	if err := y.reportAlert(t.Context(), a.at, 1, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	for n := range 100 {
		a.notes = append(a.notes, github.Note{Body: "Runner still failing.", Created: a.at.Add(time.Duration(n) * time.Hour)})
	}
	a.at = a.at.Add(101 * time.Hour)
	a.failClose = true
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err == nil {
		t.Fatal("close failure not simulated")
	}
	a.failClose = false
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err != nil {
		t.Fatal(err)
	}
	if len(a.notes) != 101 {
		t.Fatalf("recovery duplicated: comments=%d", len(a.notes))
	}
}

func TestAlertLifecycleAndRestart(t *testing.T) {
	y, _, _ := newYard(t, newGitHub())
	a := newAlertServer(t, y)
	fail := func() {
		t.Helper()
		if err := y.reportAlert(t.Context(), a.at, 1, errors.New("private-value")); err != nil {
			t.Fatal(err)
		}
	}
	for n := range 3 {
		a.at = t0.Add(time.Duration(n) * time.Minute)
		fail()
		if a.created != (n+1)/3 {
			t.Fatalf("created too early: %d", a.created)
		}
	}
	for range 2 {
		y.alert = alert{lastGood: a.at}
		fail()
	}
	if a.created != 1 || len(a.notes) != 0 {
		t.Fatal("restart duplicated alert")
	}
	a.at = t0.Add(62 * time.Minute)
	fail()
	y.alert = alert{lastGood: a.at}
	a.at = a.at.Add(time.Minute)
	fail()
	if len(a.notes) != 1 {
		t.Fatalf("hourly limit: %+v", a.notes)
	}
	a.failClose = true
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err == nil {
		t.Fatal("close failure lost")
	}
	a.failClose = false
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err != nil {
		t.Fatal(err)
	}
	if a.closed != 1 || len(a.notes) != 2 || !strings.HasPrefix(a.notes[1].Body, "recovered, down from ") {
		t.Fatalf("recovery: %+v", a)
	}
}

func TestAlertOfflineAndFiveMinuteThreshold(t *testing.T) {
	y, _, _ := newYard(t, newGitHub())
	a := newAlertServer(t, y)
	a.offline = true
	for _, after := range []time.Duration{time.Minute, 5 * time.Minute} {
		a.at = t0.Add(after)
		if err := y.reportAlert(t.Context(), a.at, 1, context.DeadlineExceeded); err == nil {
			t.Fatal("offline error lost")
		}
	}
	a.offline = false
	a.at = a.at.Add(time.Minute)
	if err := y.reportAlert(t.Context(), a.at, 0, nil); err != nil {
		t.Fatal(err)
	}
	if a.created != 1 || a.closed != 1 || !strings.Contains(a.notes[0].Body, t0.Add(time.Minute).Format(time.RFC3339)) {
		t.Fatalf("outage lost: %+v", a)
	}
}

func TestAlertCauseNamesRepoAndStatus(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&repoError{repo: "o/r", err: fmt.Errorf("list issues: %w", github.ErrNotFound)}, "o/r: GitHub API answered 404 (repository, issue or App installation not found)"},
		{&github.HTTPError{Status: 403, Message: "token ghs_secret"}, "GitHub API answered 403"},
		{errors.New("agent printed sk-secret"), "operation failed (details in runner log)"},
	}
	for _, c := range cases {
		if got := alertCause(c.err); got != c.want {
			t.Errorf("alertCause(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// A failed run is reported on its own issue: the task failed, not the runner.
func TestReportedTaskFailuresDoNotAlert(t *testing.T) {
	gh := newGitHub(issue(4, "A"), issue(5, "B"), issue(6, "C"))
	y, _, agents := newYard(t, gh)
	agents.next = func(agent.Request) (int, string, error) { return 0, "", errors.New("model overloaded") }
	var errs []int
	y.Passed = func(_ time.Time, n int, _ error) { errs = append(errs, n) }
	for range 3 {
		y.Pass(t.Context())
	}
	if !slices.Equal(errs, []int{0, 0, 0}) || y.alert.failures != 0 {
		t.Fatalf("pass errors = %v, alert failures = %d", errs, y.alert.failures)
	}
}

// spec: S33: a broken App key fails every GitHub call, the alert issue too; Notify still
// hears the outage once, and the recovery once.
func TestOutageNotifiedWithoutGitHub(t *testing.T) {
	y, _, _ := newYard(t, newGitHub())
	a := newAlertServer(t, y)
	a.offline = true
	var sent []string
	refuse := true
	y.Notify = func(_ context.Context, text string) error {
		if refuse {
			return errors.New("telegram down")
		}
		sent = append(sent, text)
		return nil
	}
	broken := &github.HTTPError{Status: 401}
	for n := range 5 {
		// the first send fails and is tried again on the next pass
		refuse = n == 2
		if err := y.reportAlert(t.Context(), a.at.Add(time.Duration(n)*time.Minute), 1, broken); err == nil {
			t.Fatal("offline GitHub not simulated")
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "failing since "+t0.UTC().Format(time.RFC3339)) || !strings.Contains(sent[0], "GitHub API answered 401") {
		t.Fatalf("outage texts = %q", sent)
	}
	a.offline = false
	for range 2 {
		if err := y.reportAlert(t.Context(), a.at.Add(10*time.Minute), 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 2 || !strings.HasPrefix(sent[1], "famulus recovered, down from ") {
		t.Fatalf("texts = %q", sent)
	}
}
