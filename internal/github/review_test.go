package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reviewClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token test-token" {
			t.Error("missing installation token")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "42", nil)
	c.tokens["o/r"] = installToken{value: "test-token", expires: time.Now().Add(time.Hour)}
	return c
}

func TestLinkedIssues(t *testing.T) {
	for _, tc := range []struct {
		body string
		want []int
	}{
		{"Closes #12; fIXes #2\nRESOLVES #12 resolves #7", []int{12, 2, 7}},
		{"#2 discloses #3 Closes o/r#4 Fixes #0 Closes #-5", nil},
		{"Closes #99999999999999999999999999999999", nil},
	} {
		if got := LinkedIssues(tc.body); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("LinkedIssues(%q) = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestReviewReads(t *testing.T) {
	ctx := context.Background()
	for _, endpoint := range []string{"pulls", "pulls/7/files", "pulls/7/reviews"} {
		t.Run(endpoint, func(t *testing.T) {
			pages := 0
			c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
				pages++
				if r.Method != http.MethodGet || r.URL.Path != "/repos/o/r/"+endpoint || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != strconv.Itoa(pages) {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				if endpoint == "pulls" && (r.URL.Query().Get("base") != "release/a&b" || r.URL.Query().Get("state") != "open") {
					t.Error("missing PR filters")
				}
				item := `{"number":7,"title":"PR","body":"Fixes #8","draft":true,"user":{"login":"owner"},"head":{"sha":"abc","ref":"topic"},"labels":[{"name":"agent"}],"filename":"a.go","status":"modified","additions":3,"deletions":2,"commit_id":"abc","state":"APPROVED"}`
				count := 100
				if pages == 2 {
					count = 1
				}
				fmt.Fprint(w, "["+strings.TrimSuffix(strings.Repeat(item+",", count), ",")+"]")
			})
			switch endpoint {
			case "pulls":
				prs, err := c.OpenPRs(ctx, "o/r", "release/a&b")
				if err != nil || len(prs) != 101 {
					t.Fatalf("PRs: %v, %v", prs, err)
				}
				p := prs[100]
				if p.Number != 7 || p.Title != "PR" || p.Body != "Fixes #8" || !p.Draft || p.User.Login != "owner" || p.Head.SHA != "abc" || p.Head.Ref != "topic" || p.Labels[0].Name != "agent" {
					t.Fatalf("PR = %+v", p)
				}
			case "pulls/7/files":
				files, err := c.PRFiles(ctx, "o/r", 7)
				if err != nil || len(files) != 101 || files[100] != (PRFile{Path: "a.go", Status: "modified", Additions: 3, Deletions: 2}) {
					t.Fatalf("files: %v, %v", files, err)
				}
			case "pulls/7/reviews":
				reviews, err := c.Reviews(ctx, "o/r", 7)
				if err != nil || len(reviews) != 101 || reviews[100] != (Review{User{"owner"}, "abc", "APPROVED"}) {
					t.Fatalf("reviews: %v, %v", reviews, err)
				}
			}
			if pages != 2 {
				t.Fatalf("pages = %d", pages)
			}
		})
	}
	c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/issues/8" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `{"number":8,"title":"Task","body":"Details","user":{"login":"owner"}}`)
	})
	issue, err := c.Issue(ctx, "o/r", 8)
	if err != nil || issue.Number != 8 || issue.Title != "Task" || issue.Body != "Details" || issue.User.Login != "owner" {
		t.Fatalf("issue: %+v, %v", issue, err)
	}
}

func TestCISummary(t *testing.T) {
	for _, tc := range []struct {
		name, checks, statuses, state string
		failed                        []string
	}{
		{"none", `[]`, `[]`, "pending", nil},
		{"own only", `[{"name":"famulus","status":"completed","conclusion":"failure"}]`, `[]`, "pending", nil},
		{"green checks", `[{"name":"test","status":"completed","conclusion":"success"},{"name":"skip","status":"completed","conclusion":"skipped"},{"name":"neutral","status":"completed","conclusion":"neutral"}]`, `[]`, "success", nil},
		{"green legacy", `[]`, `[{"context":"build","state":"success"}]`, "success", nil},
		{"running", `[{"name":"test","status":"in_progress"}]`, `[]`, "pending", nil},
		{"legacy pending", `[]`, `[{"context":"build","state":"pending"}]`, "pending", nil},
		{"failed", `[{"name":"test","status":"completed","conclusion":"timed_out"},{"name":"wait","status":"queued"}]`, `[{"context":"build","state":"error"}]`, "failure", []string{"test", "build"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				switch r.URL.Path {
				case "/repos/o/r/commits/abc/check-runs":
					if r.URL.Query().Get("filter") != "latest" {
						t.Error("missing latest check filter")
					}
					fmt.Fprintf(w, `{"check_runs":%s}`, tc.checks)
				case "/repos/o/r/commits/abc/status":
					fmt.Fprintf(w, `{"state":"pending","statuses":%s}`, tc.statuses)
				default:
					t.Errorf("unexpected URL: %s", r.URL)
				}
			})
			got, err := c.CISummary(context.Background(), "o/r", "abc", "famulus")
			if err != nil || got.State != tc.state || !reflect.DeepEqual(got.Failed, tc.failed) {
				t.Fatalf("CI = %+v, %v; want %s %v", got, err, tc.state, tc.failed)
			}
		})
	}
}

func TestCIPaginationAndErrors(t *testing.T) {
	for _, failAt := range []string{"", "check-runs", "status"} {
		t.Run(failAt, func(t *testing.T) {
			calls := 0
			c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				kind := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if kind == failAt && r.URL.Query().Get("page") == "2" {
					http.Error(w, `{"message":"denied"}`, http.StatusForbidden)
					return
				}
				item, field := `{"name":"test","status":"completed","conclusion":"success"}`, "check_runs"
				if kind == "status" {
					item, field = `{"context":"build","state":"success"}`, "statuses"
				}
				items := strings.TrimSuffix(strings.Repeat(item+",", 100), ",")
				if r.URL.Query().Get("page") == "2" {
					items = ""
				}
				fmt.Fprintf(w, `{"%s":[%s]}`, field, items)
			})
			got, err := c.CISummary(context.Background(), "o/r", "abc", "famulus")
			if failAt != "" {
				var apiErr *HTTPError
				if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil || got.State != "success" || calls != 4 {
				t.Fatalf("CI = %+v, %v; calls %d", got, err, calls)
			}
		})
	}
}

func TestCreateReview(t *testing.T) {
	for _, event := range []string{"APPROVE", "REQUEST_CHANGES", "COMMENT"} {
		t.Run(event, func(t *testing.T) {
			calls := 0
			c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/pulls/7/reviews" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var body struct {
					CommitID string          `json:"commit_id"`
					Event    string          `json:"event"`
					Body     string          `json:"body"`
					Comments []ReviewComment `json:"comments"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.CommitID != "abc" || body.Event != event || body.Body != "Review" || !reflect.DeepEqual(body.Comments, []ReviewComment{{"a.go", 3, "RIGHT", "Fix"}}) {
					t.Errorf("review = %+v", body)
				}
				w.WriteHeader(http.StatusCreated)
			})
			comments := []ReviewComment{{Path: "a.go", Line: 3, Body: "Fix"}}
			if err := c.CreateReview(context.Background(), "o/r", 7, "abc", event, "Review", comments); err != nil || calls != 1 || comments[0].Side != "" {
				t.Fatalf("create: %v, calls %d, input %v", err, calls, comments)
			}
		})
	}
}

func TestUpsertCheck(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			writes := 0
			wantStatus, wantConclusion := "", ""
			c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.Path != "/repos/o/r/commits/abc/check-runs" || r.URL.Query().Get("app_id") != "42" || r.URL.Query().Get("check_name") != "famulus" {
						t.Errorf("lookup = %s", r.URL)
					}
					own := ""
					if existing {
						own = `,{"id":8,"name":"famulus","app":{"id":42}}`
					}
					fmt.Fprintf(w, `{"check_runs":[{"id":99,"name":"famulus","app":{"id":9}},{"id":88,"name":"other","app":{"id":42}}%s]}`, own)
					return
				}
				writes++
				method, path := http.MethodPost, "/repos/o/r/check-runs"
				if existing {
					method, path = http.MethodPatch, path+"/8"
				}
				var body struct {
					Name       string            `json:"name"`
					SHA        string            `json:"head_sha"`
					Status     string            `json:"status"`
					Conclusion string            `json:"conclusion"`
					Output     map[string]string `json:"output"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if r.Method != method || r.URL.Path != path || body.Name != "famulus" || body.Output["title"] != "Review" || body.Output["summary"] != "Details" || (existing && body.SHA != "") || (!existing && body.SHA != "abc") {
					t.Errorf("write %s %s: %+v", r.Method, r.URL, body)
				}
				if body.Status != wantStatus || body.Conclusion != wantConclusion {
					t.Errorf("status/conclusion = %q/%q, want %q/%q", body.Status, body.Conclusion, wantStatus, wantConclusion)
				}
				w.WriteHeader(http.StatusCreated)
			})
			for _, status := range []string{"queued", "in_progress", "completed", "completed"} {
				check := Check{Status: status, Title: "Review", Summary: "Details"}
				if status == "completed" {
					check.Conclusion = "success"
					if writes == 3 {
						check.Conclusion = "failure"
					}
				}
				wantStatus, wantConclusion = check.Status, check.Conclusion
				if err := c.UpsertCheck(context.Background(), "o/r", "abc", check); err != nil {
					t.Fatal(err)
				}
			}
			if writes != 4 {
				t.Fatalf("writes = %d", writes)
			}
		})
	}
}

// Closed PRs that were not merged are left out; the limit cuts the rest.
func TestMergedPRs(t *testing.T) {
	c := reviewClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/repos/o/r/pulls" || q.Get("state") != "closed" || q.Get("base") != "main" || q.Get("sort") != "updated" || q.Get("direction") != "desc" {
			t.Errorf("request = %s", r.URL)
		}
		fmt.Fprint(w, `[{"number":5,"title":"e","merged_at":"2026-01-05T00:00:00Z"},{"number":4,"title":"closed","merged_at":null},
			{"number":2,"title":"b","merged_at":"2026-01-02T00:00:00Z"},{"number":3,"title":"c","merged_at":"2026-01-03T00:00:00Z"}]`)
	})
	prs, err := c.MergedPRs(context.Background(), "o/r", "main", 2)
	if err != nil || len(prs) != 2 || prs[0].Number != 5 || prs[1].Number != 3 {
		t.Fatalf("merged = %+v, %v", prs, err)
	}
}
