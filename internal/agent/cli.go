package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/daniil4545/famulus/internal/config"
)

// claude: `claude -p --output-format json` prints one JSON object with the result and the
// session id; the container is the sandbox, so permission prompts are off.
func (c CLI) claude(ctx context.Context, req Request) (Result, error) {
	args := []string{"-p", "--output-format", "json", "--dangerously-skip-permissions"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.Session != "" {
		args = append(args, "--resume", req.Session)
	}
	out, err := c.run(ctx, config.Claude, req.Dir, req.Prompt, nil, args...)
	return settle(config.Claude, out, err, parseClaude)
}

func parseClaude(out []byte) (Result, error) {
	var res struct {
		Result  string `json:"result"`
		Session string `json:"session_id"`
		IsError bool   `json:"is_error"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(lastLine(out), &res); err != nil {
		return Result{}, fmt.Errorf("claude output: %w", err)
	}
	if res.IsError {
		if isLimit(res.Result) {
			return Result{Session: res.Session}, &LimitError{Agent: config.Claude, Message: tail(res.Result)}
		}
		return Result{Session: res.Session}, fmt.Errorf("claude: %s: %s", res.Subtype, tail(res.Result))
	}
	if strings.TrimSpace(res.Result) == "" {
		return Result{Session: res.Session}, errNoText
	}
	return Result{Session: res.Session, Text: res.Result}, nil
}

// codex: `codex exec --json` prints JSONL events, the first carries the thread id that
// `codex exec resume` takes; the final message goes to the -o file.
func (c CLI) codex(ctx context.Context, req Request) (Result, error) {
	tmp, err := os.MkdirTemp("", "codex-")
	if err != nil {
		return Result{}, fmt.Errorf("codex temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // a leftover temp file harms nothing
	last := filepath.Join(tmp, "last.txt")
	args := []string{"exec"}
	if req.Session != "" {
		args = append(args, "resume", req.Session)
	}
	args = append(args, "--json", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "-o", last)
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "-")
	out, err := c.run(ctx, config.Codex, req.Dir, req.Prompt, nil, args...)
	text, _ := os.ReadFile(last) //nolint:gosec // our own temp file; a missing file means no final message
	return settle(config.Codex, out, err, func(out []byte) (Result, error) {
		return parseCodex(out, string(text), req.Session)
	})
}

func parseCodex(out []byte, last, session string) (Result, error) {
	var failure string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Thread  string `json:"thread_id"`
			Message string `json:"message"`
			Error   *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Thread != "":
			session = ev.Thread
		case ev.Type == "turn.failed" && ev.Error != nil:
			failure = ev.Error.Message
		case ev.Type == "error":
			failure = ev.Message
		}
	}
	if failure != "" {
		if isLimit(failure) {
			return Result{Session: session}, &LimitError{Agent: config.Codex, Message: tail(failure)}
		}
		return Result{Session: session}, fmt.Errorf("codex: %s", tail(failure))
	}
	if strings.TrimSpace(last) == "" {
		return Result{Session: session}, errNoText
	}
	return Result{Session: session, Text: strings.TrimSpace(last)}, nil
}

// opencode: `opencode run --format json` prints JSONL events with the session id; the final
// message is the text parts of the last assistant message. Its error log goes to stderr: on a
// used-up free tier the CLI logs the error and then waits silently instead of exiting.
func (c CLI) opencode(ctx context.Context, req Request) (Result, error) {
	args := []string{"run", "--format", "json", "--print-logs", "--log-level", "ERROR"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Session != "" {
		args = append(args, "--session", req.Session)
	}
	args = append(args, req.Prompt)
	out, err := c.run(ctx, config.OpenCode, req.Dir, "", openCodeLimitLog, args...)
	return settle(config.OpenCode, out, err, parseOpenCode)
}

func parseOpenCode(out []byte) (Result, error) {
	var session, lastMessage string
	var texts []string
	var failure *openCodeError
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Session string `json:"sessionID"`
			Part    struct {
				Message string `json:"messageID"`
				Text    string `json:"text"`
			} `json:"part"`
			Error openCodeError `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			continue
		}
		if ev.Session != "" {
			session = ev.Session
		}
		switch ev.Type {
		case "error":
			failure = &ev.Error
		case "text":
			if ev.Part.Message != lastMessage {
				lastMessage, texts = ev.Part.Message, nil
			}
			texts = append(texts, ev.Part.Text)
		}
	}
	if failure != nil {
		return Result{Session: session}, failure.err()
	}
	text := strings.TrimSpace(strings.Join(texts, "\n"))
	if text == "" {
		return Result{Session: session}, errNoText
	}
	return Result{Session: session, Text: text}, nil
}

// openCodeError is the error event of OpenCode; a used-up free tier has type
// provider.free-limit and HTTP status 429.
type openCodeError struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	Status     int    `json:"status"`
	RetryAfter int64  `json:"retryAfterMs"`
}

func (e openCodeError) err() error {
	limit := e.Type == "provider.free-limit" || e.Type == "provider.quota" || e.Status == 429 || isLimit(e.Message)
	if limit {
		return &LimitError{Agent: config.OpenCode, Message: tail(e.Message), RetryAfter: time.Duration(e.RetryAfter) * time.Millisecond}
	}
	return fmt.Errorf("opencode: %s", tail(e.Message))
}

// openCodeLimitLog matches the live log line of a used-up free tier:
// `AI_APICallError: Free usage limit reached`.
var openCodeLimitLog = regexp.MustCompile(`(?i)free usage (limit|exceeded)|FreeUsageLimitError`)

// settle picks the error that tells the most: a limit reported in the output or in the
// CLI's error text, then the CLI failure, then the parse result.
func settle(name string, out []byte, runErr error, parse func([]byte) (Result, error)) (Result, error) {
	res, err := parse(out)
	if _, ok := errors.AsType[*LimitError](err); ok {
		return res, err
	}
	if _, ok := errors.AsType[*LimitError](runErr); ok {
		return res, runErr
	}
	if runErr != nil {
		if isLimit(runErr.Error()) {
			return res, &LimitError{Agent: name, Message: tail(runErr.Error())}
		}
		return res, runErr
	}
	return res, err
}

func lastLine(out []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	return lines[len(lines)-1]
}
