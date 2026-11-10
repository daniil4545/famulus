package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func (c *Client) OpenPRs(ctx context.Context, repo, base string) ([]PullRequest, error) {
	q := url.Values{"state": {"open"}, "base": {base}}
	return repoPages[PullRequest](ctx, c, repo, "/pulls?"+q.Encode(), 0)
}

// MergedPRs returns up to limit pull requests merged into base, the latest merge first, out
// of the 100 most recently updated closed ones.
func (c *Client) MergedPRs(ctx context.Context, repo, base string, limit int) ([]PullRequest, error) {
	q := url.Values{"state": {"closed"}, "base": {base}, "sort": {"updated"}, "direction": {"desc"}}
	closed, err := repoPages[PullRequest](ctx, c, repo, "/pulls?"+q.Encode(), 1)
	if err != nil {
		return nil, err
	}
	merged := slices.DeleteFunc(closed, func(pr PullRequest) bool { return pr.MergedAt == "" })
	// RFC 3339 UTC timestamps sort as text
	slices.SortStableFunc(merged, func(a, b PullRequest) int { return strings.Compare(b.MergedAt, a.MergedAt) })
	return merged[:min(limit, len(merged))], nil
}

func (c *Client) Issue(ctx context.Context, repo string, number int) (Issue, error) {
	var issue Issue
	err := c.repoCall(ctx, repo, http.MethodGet, fmt.Sprintf("/issues/%d", number), nil, &issue)
	return issue, err
}

var closingIssue = regexp.MustCompile(`(?i)\b(?:closes|fixes|resolves)\s+#([1-9][0-9]*)\b`)

// LinkedIssues returns local issue references in body order, without duplicates.
func LinkedIssues(body string) []int {
	var numbers []int
	for _, match := range closingIssue.FindAllStringSubmatch(body, -1) {
		n, err := strconv.Atoi(match[1])
		if err == nil && !slices.Contains(numbers, n) {
			numbers = append(numbers, n)
		}
	}
	return numbers
}

type PRFile struct {
	Path      string `json:"filename"`
	Previous  string `json:"previous_filename"` // set on renames
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Patch     string `json:"patch"` // hunks; GitHub leaves it out for binary and huge files
}

func (c *Client) PRFiles(ctx context.Context, repo string, number int) ([]PRFile, error) {
	return repoPages[PRFile](ctx, c, repo, fmt.Sprintf("/pulls/%d/files?", number), 30)
}

type Review struct {
	User     User   `json:"user"`
	CommitID string `json:"commit_id"`
	State    string `json:"state"`
}

func (c *Client) Reviews(ctx context.Context, repo string, number int) ([]Review, error) {
	return repoPages[Review](ctx, c, repo, fmt.Sprintf("/pulls/%d/reviews?", number), 0)
}

func repoPages[T any](ctx context.Context, c *Client, repo, path string, maxPages int) ([]T, error) {
	var all []T
	for page := 1; maxPages == 0 || page <= maxPages; page++ {
		var batch []T
		if err := c.repoCall(ctx, repo, http.MethodGet, fmt.Sprintf("%s&per_page=100&page=%d", path, page), nil, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

type CI struct {
	State  string
	Failed []string
}

type checkRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	App        struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

func (c *Client) checkRuns(ctx context.Context, repo, sha string, q url.Values) ([]checkRun, error) {
	var all []checkRun
	q.Set("per_page", "100")
	for page := 1; ; page++ {
		q.Set("page", strconv.Itoa(page))
		var response struct {
			Runs []checkRun `json:"check_runs"`
		}
		path := "/commits/" + url.PathEscape(sha) + "/check-runs?" + q.Encode()
		if err := c.repoCall(ctx, repo, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		all = append(all, response.Runs...)
		if len(response.Runs) < 100 {
			return all, nil
		}
	}
}

type commitStatus struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

func (c *Client) commitStatuses(ctx context.Context, repo, sha string) ([]commitStatus, error) {
	var all []commitStatus
	for page := 1; ; page++ {
		var response struct {
			Statuses []commitStatus `json:"statuses"`
		}
		path := fmt.Sprintf("/commits/%s/status?per_page=100&page=%d", url.PathEscape(sha), page)
		if err := c.repoCall(ctx, repo, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		all = append(all, response.Statuses...)
		if len(response.Statuses) < 100 {
			return all, nil
		}
	}
}

func (c *Client) CISummary(ctx context.Context, repo, sha, skip string) (CI, error) {
	runs, err := c.checkRuns(ctx, repo, sha, url.Values{"filter": {"latest"}})
	if err != nil {
		return CI{}, err
	}
	statuses, err := c.commitStatuses(ctx, repo, sha)
	if err != nil {
		return CI{}, err
	}
	ci := CI{State: "success"}
	count, pending := 0, false
	for _, run := range runs {
		if run.Name == skip {
			continue
		}
		count++
		if run.Status != "completed" {
			pending = true
		} else if run.Conclusion != "success" && run.Conclusion != "neutral" && run.Conclusion != "skipped" {
			ci.Failed = append(ci.Failed, run.Name)
		}
	}
	for _, status := range statuses {
		count++
		switch status.State {
		case "success":
		case "failure", "error":
			ci.Failed = append(ci.Failed, status.Context)
		default:
			pending = true
		}
	}
	if len(ci.Failed) > 0 {
		ci.State = "failure"
	} else if pending || count == 0 {
		ci.State = "pending"
	}
	return ci, nil
}

type ReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

func (c *Client) CreateReview(ctx context.Context, repo string, number int, sha, event, body string, comments []ReviewComment) error {
	if sha == "" || (event != "APPROVE" && event != "REQUEST_CHANGES" && event != "COMMENT") {
		return errors.New("invalid review commit or event")
	}
	if event != "APPROVE" && body == "" {
		return fmt.Errorf("review body required for %s", event)
	}
	comments = slices.Clone(comments)
	for i := range comments {
		cm := &comments[i]
		if cm.Path == "" || cm.Line <= 0 || cm.Body == "" || (cm.Side != "" && cm.Side != "RIGHT") {
			return errors.New("invalid review line comment")
		}
		cm.Side = "RIGHT"
	}
	req := struct {
		CommitID string          `json:"commit_id"`
		Event    string          `json:"event"`
		Body     string          `json:"body"`
		Comments []ReviewComment `json:"comments,omitempty"`
	}{sha, event, body, comments}
	return c.repoCall(ctx, repo, http.MethodPost, fmt.Sprintf("/pulls/%d/reviews", number), req, nil)
}

type Check struct {
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	Title      string `json:"-"`
	Summary    string `json:"-"`
}

func (c *Client) UpsertCheck(ctx context.Context, repo, sha string, check Check) error {
	if sha == "" || (check.Status != "queued" && check.Status != "in_progress" && check.Status != "completed") {
		return errors.New("invalid check commit or status")
	}
	if (check.Status == "completed" && check.Conclusion != "success" && check.Conclusion != "failure") ||
		(check.Status != "completed" && check.Conclusion != "") {
		return errors.New("invalid check conclusion")
	}
	if check.Title == "" || check.Summary == "" {
		return errors.New("check output needs a title and a summary") // GitHub answers 422 otherwise
	}
	q := url.Values{"check_name": {"famulus"}, "app_id": {c.appID}, "filter": {"latest"}}
	runs, err := c.checkRuns(ctx, repo, sha, q)
	if err != nil {
		return err
	}
	req := struct {
		Check
		Name    string            `json:"name"`
		HeadSHA string            `json:"head_sha,omitempty"`
		Output  map[string]string `json:"output"`
	}{check, "famulus", sha, map[string]string{"title": check.Title, "summary": check.Summary}}
	var id int64
	for _, run := range runs {
		if run.Name == "famulus" && strconv.FormatInt(run.App.ID, 10) == c.appID && run.ID > id {
			id = run.ID
		}
	}
	if id != 0 {
		req.HeadSHA = ""
		return c.repoCall(ctx, repo, http.MethodPatch, fmt.Sprintf("/check-runs/%d", id), req, nil)
	}
	return c.repoCall(ctx, repo, http.MethodPost, "/check-runs", req, nil)
}
