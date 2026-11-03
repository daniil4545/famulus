// Package github talks to the GitHub REST API as a GitHub App installed on the watched
// repositories: issues, labels, comments, reviews and pull requests.
package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

// ErrNotFound marks a 404 answer.
var ErrNotFound = errors.New("not found")

// HTTPError is a GitHub answer with an error status other than 404.
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("http %d: %s", e.Status, e.Message) }

type Client struct {
	baseURL string
	http    *http.Client
	appID   string
	key     *rsa.PrivateKey
	now     func() time.Time

	mu     sync.Mutex
	tokens map[string]installToken // by repository
}

type installToken struct {
	value   string
	expires time.Time
}

func NewClient(baseURL, appID string, key *rsa.PrivateKey) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
		appID:   appID,
		key:     key,
		now:     time.Now,
		tokens:  map[string]installToken{},
	}
}

type Issue struct {
	Number  int       `json:"number"`
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Created time.Time `json:"created_at"`
	Labels  []Label   `json:"labels"`
	PR      *struct{} `json:"pull_request"`
}

type Label struct {
	Name string `json:"name"`
}

func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

type User struct {
	Login string `json:"login"`
}

// Note is anything a person wrote on an issue or pull request: a comment, a review or a
// line comment of a review.
type Note struct {
	Kind    string
	Author  string
	Body    string
	Path    string
	Line    int
	Created time.Time
}

type PullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	URL    string `json:"html_url"`
}

// Token returns an installation token for the repository, cached until 5 minutes before it
// expires.
func (c *Client) Token(ctx context.Context, repo string) (string, error) {
	c.mu.Lock()
	cached, ok := c.tokens[repo]
	c.mu.Unlock()
	if ok && c.now().Before(cached.expires.Add(-5*time.Minute)) {
		return cached.value, nil
	}
	jwt, err := appJWT(c.appID, c.key, c.now())
	if err != nil {
		return "", fmt.Errorf("sign app token: %w", err)
	}
	var inst struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, "Bearer "+jwt, http.MethodGet, "/repos/"+repo+"/installation", nil, &inst); err != nil {
		return "", fmt.Errorf("app installation on %s: %w", repo, err)
	}
	var tok struct {
		Token   string    `json:"token"`
		Expires time.Time `json:"expires_at"`
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID)
	if err := c.call(ctx, "Bearer "+jwt, http.MethodPost, path, struct{}{}, &tok); err != nil {
		return "", fmt.Errorf("installation token for %s: %w", repo, err)
	}
	c.mu.Lock()
	c.tokens[repo] = installToken{value: tok.Token, expires: tok.Expires}
	c.mu.Unlock()
	return tok.Token, nil
}

// Identity returns the commit author of the App bot: name and noreply e-mail.
func (c *Client) Identity(ctx context.Context) (name, email string, err error) {
	jwt, err := appJWT(c.appID, c.key, c.now())
	if err != nil {
		return "", "", fmt.Errorf("sign app token: %w", err)
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := c.call(ctx, "Bearer "+jwt, http.MethodGet, "/app", nil, &app); err != nil {
		return "", "", fmt.Errorf("app: %w", err)
	}
	name = app.Slug + "[bot]"
	var user struct {
		ID int64 `json:"id"`
	}
	if err := c.call(ctx, "", http.MethodGet, "/users/"+url.PathEscape(name), nil, &user); err != nil {
		return "", "", fmt.Errorf("bot user %s: %w", name, err)
	}
	return name, fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, name), nil
}

// OpenIssues lists open issues with the label; pull requests are left out.
// simplified: first 100 only; revisit if a repository queues more.
func (c *Client) OpenIssues(ctx context.Context, repo, label string) ([]Issue, error) {
	var all []Issue
	q := url.Values{"state": {"open"}, "labels": {label}, "per_page": {"100"}}
	if err := c.repoCall(ctx, repo, http.MethodGet, "/issues?"+q.Encode(), nil, &all); err != nil {
		return nil, err
	}
	issues := all[:0]
	for _, i := range all {
		if i.PR == nil {
			issues = append(issues, i)
		}
	}
	return issues, nil
}

func (c *Client) AddLabels(ctx context.Context, repo string, number int, labels ...string) error {
	body := map[string][]string{"labels": labels}
	return c.repoCall(ctx, repo, http.MethodPost, fmt.Sprintf("/issues/%d/labels", number), body, nil)
}

// RemoveLabel removes the label; a label that is not there is not an error.
func (c *Client) RemoveLabel(ctx context.Context, repo string, number int, label string) error {
	path := fmt.Sprintf("/issues/%d/labels/%s", number, url.PathEscape(label))
	err := c.repoCall(ctx, repo, http.MethodDelete, path, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) Comment(ctx context.Context, repo string, number int, body string) error {
	return c.repoCall(ctx, repo, http.MethodPost, fmt.Sprintf("/issues/%d/comments", number), map[string]string{"body": body}, nil)
}

func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (Issue, error) {
	for _, label := range labels {
		err := c.repoCall(ctx, repo, http.MethodGet, "/labels/"+url.PathEscape(label), nil, nil)
		if errors.Is(err, ErrNotFound) {
			err = c.repoCall(ctx, repo, http.MethodPost, "/labels", map[string]string{"name": label, "color": "b60205"}, nil)
		}
		if err != nil {
			return Issue{}, fmt.Errorf("ensure issue label: %w", err)
		}
	}
	var issue Issue
	err := c.repoCall(ctx, repo, http.MethodPost, "/issues", map[string]any{"title": title, "body": body, "labels": labels}, &issue)
	return issue, err
}

func (c *Client) CloseIssue(ctx context.Context, repo string, number int) error {
	return c.repoCall(ctx, repo, http.MethodPatch, fmt.Sprintf("/issues/%d", number), map[string]string{"state": "closed"}, nil)
}

// Notes returns what people wrote on an issue or pull request after since, oldest first:
// comments, and for a pull request also reviews and line comments.
// Issue comments are paginated for long-lived alerts; review kinds take the first 100.
func (c *Client) Notes(ctx context.Context, repo string, number int, pull bool, since time.Time) ([]Note, error) {
	q := url.Values{"per_page": {"100"}, "since": {since.UTC().Format(time.RFC3339)}}
	comments, err := c.issueComments(ctx, repo, number, q)
	if err != nil {
		return nil, err
	}
	var notes []Note
	for _, cm := range comments {
		notes = append(notes, Note{Kind: "comment", Author: cm.User.Login, Body: cm.Body, Created: cm.Created})
	}
	if pull {
		var lines []comment
		if err := c.repoCall(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d/comments?%s", number, q.Encode()), nil, &lines); err != nil {
			return nil, err
		}
		for _, cm := range lines {
			notes = append(notes, Note{Kind: "line", Author: cm.User.Login, Body: cm.Body, Path: cm.Path, Line: cm.Line, Created: cm.Created})
		}
		var reviews []struct {
			User      User      `json:"user"`
			Body      string    `json:"body"`
			State     string    `json:"state"`
			Submitted time.Time `json:"submitted_at"`
		}
		if err := c.repoCall(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d/reviews?per_page=100", number), nil, &reviews); err != nil {
			return nil, err
		}
		for _, r := range reviews {
			if r.Body == "" && r.State != "CHANGES_REQUESTED" {
				continue
			}
			notes = append(notes, Note{Kind: "review " + r.State, Author: r.User.Login, Body: r.Body, Created: r.Submitted})
		}
	}
	// the since filter of the API is by update time; keep only notes written after since
	kept := notes[:0]
	for _, n := range notes {
		if n.Created.After(since) {
			kept = append(kept, n)
		}
	}
	slices.SortStableFunc(kept, func(a, b Note) int { return a.Created.Compare(b.Created) })
	return kept, nil
}

type comment struct {
	User    User      `json:"user"`
	Body    string    `json:"body"`
	Path    string    `json:"path"`
	Line    int       `json:"line"`
	Created time.Time `json:"created_at"`
}

func (c *Client) issueComments(ctx context.Context, repo string, number int, q url.Values) ([]comment, error) {
	var comments []comment
	for page := 1; ; page++ {
		var batch []comment
		path := fmt.Sprintf("/issues/%d/comments?%s&page=%d", number, q.Encode(), page)
		if err := c.repoCall(ctx, repo, http.MethodGet, path, nil, &batch); err != nil {
			return nil, err
		}
		comments = append(comments, batch...)
		if len(batch) < 100 {
			return comments, nil
		}
	}
}

func (c *Client) CreatePR(ctx context.Context, repo, head, base, title, body string) (PullRequest, error) {
	var pr PullRequest
	req := map[string]string{"head": head, "base": base, "title": title, "body": body}
	err := c.repoCall(ctx, repo, http.MethodPost, "/pulls", req, &pr)
	return pr, err
}

func (c *Client) PR(ctx context.Context, repo string, number int) (PullRequest, error) {
	var pr PullRequest
	err := c.repoCall(ctx, repo, http.MethodGet, fmt.Sprintf("/pulls/%d", number), nil, &pr)
	return pr, err
}

func (c *Client) repoCall(ctx context.Context, repo, method, path string, body, out any) error {
	tok, err := c.Token(ctx, repo)
	if err != nil {
		return err
	}
	if err := c.call(ctx, "token "+tok, method, "/repos/"+repo+path, body, out); err != nil {
		return fmt.Errorf("%s %s%s: %w", method, repo, path, err)
	}
	return nil
}

func (c *Client) call(ctx context.Context, auth, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", "2022-11-28")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is read in full below
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var msg struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &msg) // the status alone is enough when the body is not JSON
		return &HTTPError{Status: resp.StatusCode, Message: msg.Message}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
