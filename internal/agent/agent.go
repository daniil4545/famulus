// Package agent runs a coding agent CLI headless in a worktree: Claude Code, Codex or
// OpenCode. A run takes a prompt and an optional session to resume and returns the session
// and the agent's final message.
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/daniil4545/famulus/internal/config"
)

type Request struct {
	Dir     string
	Prompt  string
	Session string
	Model   string
	Effort  string
	// ReadOnly runs the agent without shell, writes, network and MCP, confined to Dir:
	// the review agent reads code written by anyone.
	ReadOnly bool
}

type Result struct {
	Session string
	Text    string
	// Usage is what the run spent; only claude reports it.
	Usage Usage
}

// Usage of one or several runs. CostUSD is the CLI's figure at list prices, also on a plan.
type Usage struct {
	CostUSD    float64 `json:"cost_usd"`
	Input      int     `json:"input_tokens"`
	Output     int     `json:"output_tokens"`
	CacheRead  int     `json:"cache_read_tokens"`
	CacheWrite int     `json:"cache_write_tokens"`
	Turns      int     `json:"turns"`
}

func (u Usage) Add(v Usage) Usage {
	return Usage{
		CostUSD: u.CostUSD + v.CostUSD, Input: u.Input + v.Input, Output: u.Output + v.Output,
		CacheRead: u.CacheRead + v.CacheRead, CacheWrite: u.CacheWrite + v.CacheWrite, Turns: u.Turns + v.Turns,
	}
}

type CLI struct {
	// Commands overrides the binary of an agent, for tests.
	Commands map[string]string
}

func (c CLI) Run(ctx context.Context, name string, req Request) (Result, error) {
	if req.ReadOnly && name != config.Claude {
		return Result{}, fmt.Errorf("%s: read-only runs exist only for claude", name)
	}
	switch name {
	case config.Claude:
		return c.claude(ctx, req)
	case config.Codex:
		return c.codex(ctx, req)
	case config.OpenCode:
		return c.opencode(ctx, req)
	}
	return Result{}, fmt.Errorf("unknown agent %q", name)
}

// run starts the CLI in the worktree with the runner's own secrets removed from the
// environment: the agent needs its CLI login, not the GitHub App key. A stderr line matching
// stop ends the run with a LimitError: a CLI may wait silently for hours on a used-up plan.
func (c CLI) run(ctx context.Context, name, dir, stdin string, stop *regexp.Regexp, args ...string) ([]byte, error) {
	binary := name
	if c.Commands[name] != "" {
		binary = c.Commands[name]
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...) //nolint:gosec // a known agent CLI, arguments built by the runner
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = agentEnv(os.Environ())
	// children of a killed CLI may hold its pipes open
	cmd.WaitDelay = 2 * time.Second
	var stdout bytes.Buffer
	stderr := &stopWatch{stop: stop, cancel: cancel}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	err := cmd.Run()
	if stderr.hit != "" {
		msg := stderr.hit
		if m := logError.FindStringSubmatch(msg); m != nil {
			msg = m[1]
		}
		return stdout.Bytes(), &LimitError{Agent: name, Message: tail(msg)}
	}
	if ctx.Err() != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, ctx.Err())
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, tail(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// logError picks the error text out of a key=value log line: error.error="...".
var logError = regexp.MustCompile(`error\.error="([^"]*)"`)

// stopWatch keeps stderr and cancels the run at the first complete line matching stop.
// exec writes stderr from one goroutine and Run waits for it, so no lock is needed.
type stopWatch struct {
	stop   *regexp.Regexp
	cancel func()
	all    bytes.Buffer
	part   []byte
	hit    string
}

func (w *stopWatch) Write(p []byte) (int, error) {
	w.all.Write(p)
	if w.stop == nil || w.hit != "" {
		return len(p), nil
	}
	w.part = append(w.part, p...)
	for {
		i := bytes.IndexByte(w.part, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.part[:i])
		w.part = w.part[i+1:]
		if w.stop.MatchString(line) {
			w.hit = line
			w.cancel()
			return len(p), nil
		}
	}
}

func (w *stopWatch) String() string {
	return w.all.String()
}

func agentEnv(env []string) []string {
	var kept []string
	for _, kv := range env {
		if !strings.HasPrefix(kv, "FAMULUS_") {
			kept = append(kept, kv)
		}
	}
	return kept
}

// secretEnv names the variables whose values must never leave the runner in agent text.
var secretEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN", "FAMULUS_APP_KEY_B64"}

// Leaks reports whether text carries the value of a runner secret, also spaced out or
// reversed. The sandbox is the defence; this only stops the plainest copy.
func Leaks(text string) bool {
	plain := alnum(text)
	for _, name := range secretEnv {
		v := alnum(os.Getenv(name))
		if len(v) < 8 {
			continue
		}
		r := []rune(v)
		slices.Reverse(r)
		if strings.Contains(plain, v) || strings.Contains(plain, string(r)) {
			return true
		}
	}
	return false
}

func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

// tail keeps the end of an error text, where CLIs print the cause.
func tail(s string) string {
	const limit = 500
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return "..." + s[len(s)-limit:]
	}
	return s
}

var errNoText = errors.New("agent returned no final message")
