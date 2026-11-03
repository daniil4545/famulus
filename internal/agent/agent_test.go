package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daniil4545/famulus/internal/config"
)

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// opencode fixtures are live output of opencode run --format json (v2.1.4, 25.10.2026);
// claude and codex ones follow their documented formats.
func TestParse(t *testing.T) {
	cases := []struct {
		name        string
		parse       func() (Result, error)
		session     string
		text        string
		errContains string
	}{
		{
			"opencode ok", func() (Result, error) { return parseOpenCode(readFile(t, "opencode-ok.jsonl")) },
			"ses_e8cfaca3cffe0la20T6MPLvH2y", "OK", "",
		},
		{
			"opencode error", func() (Result, error) { return parseOpenCode(readFile(t, "opencode-error.jsonl")) },
			"ses_e8cfaed0cffe40q0q8h8x0Fzgt", "", "API key has expired",
		},
		{"opencode keeps only the last message", func() (Result, error) {
			return parseOpenCode([]byte(`{"type":"text","sessionID":"s","part":{"messageID":"m1","text":"thinking aloud"}}
{"type":"tool_use","sessionID":"s"}
{"type":"text","sessionID":"s","part":{"messageID":"m2","text":"## Summary"}}
{"type":"text","sessionID":"s","part":{"messageID":"m2","text":"done"}}`))
		}, "s", "## Summary\ndone", ""},
		{"claude ok", func() (Result, error) {
			return parseClaude([]byte(`{"type":"result","subtype":"success","is_error":false,"result":"Fixed","session_id":"c-1"}`))
		}, "c-1", "Fixed", ""},
		{"claude error", func() (Result, error) {
			return parseClaude([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","session_id":"c-2"}`))
		}, "c-2", "", "error_max_turns"},
		{"codex ok", func() (Result, error) {
			return parseCodex([]byte(`{"type":"thread.started","thread_id":"t-1"}
{"type":"turn.completed"}`), "Done\n", "")
		}, "t-1", "Done", ""},
		{"codex failed turn", func() (Result, error) {
			return parseCodex([]byte(`{"type":"thread.started","thread_id":"t-2"}
{"type":"turn.failed","error":{"message":"usage limit reached"}}`), "", "")
		}, "t-2", "", "usage limit reached"},
		{"codex resume keeps the session", func() (Result, error) { return parseCodex(nil, "ok", "t-3") }, "t-3", "ok", ""},
		{"empty final message", func() (Result, error) { return parseCodex(nil, "  ", "t-4") }, "t-4", "", "no final message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.parse()
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("err = %v, want %q", err, tc.errContains)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if res.Session != tc.session || res.Text != tc.text {
				t.Fatalf("got %+v, want session %q text %q", res, tc.session, tc.text)
			}
		})
	}
}

func TestLimitErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		retry time.Duration
	}{
		{"opencode free tier", func() error {
			_, err := parseOpenCode([]byte(`{"type":"error","sessionID":"s","error":{"type":"provider.free-limit","message":"Free usage exceeded, subscribe to Go","status":429,"retryAfterMs":90000}}`))
			return err
		}(), 90 * time.Second},
		{"opencode plain 429", func() error {
			_, err := parseOpenCode([]byte(`{"type":"error","error":{"type":"provider.unknown","message":"slow down","status":429}}`))
			return err
		}(), 0},
		{"claude usage limit", func() error {
			_, err := parseClaude([]byte(`{"type":"result","is_error":true,"result":"Claude AI usage limit reached|1792940000","session_id":"c"}`))
			return err
		}(), 0},
		{"codex usage limit", func() error {
			_, err := parseCodex([]byte(`{"type":"turn.failed","error":{"message":"You've hit your usage limit"}}`), "", "t")
			return err
		}(), 0},
		{"limit only in the CLI error", func() error {
			_, err := settle(config.OpenCode, nil, errors.New("opencode: exit status 1: 429 Too Many Requests"), parseOpenCode)
			return err
		}(), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			limit, ok := errors.AsType[*LimitError](tc.err)
			if !ok || limit.RetryAfter != tc.retry {
				t.Fatalf("err = %v, want a limit error with retry %v", tc.err, tc.retry)
			}
		})
	}
	if _, err := parseOpenCode(readFile(t, "opencode-error.jsonl")); err == nil {
		t.Fatal("an expired key must fail")
	} else if _, ok := errors.AsType[*LimitError](err); ok {
		t.Fatal("an expired key is not a limit: retrying would never help")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"type":"text","sessionID":"s","part":{"messageID":"m","text":"x"}}`))
	f.Add([]byte(`{"result":"x","session_id":"s"}`))
	f.Add([]byte("\n\n{"))
	f.Fuzz(func(t *testing.T, out []byte) {
		for _, parse := range []func([]byte) (Result, error){
			parseOpenCode, parseClaude,
			func(b []byte) (Result, error) { return parseCodex(b, "", "") },
		} {
			if res, err := parse(out); err == nil && strings.TrimSpace(res.Text) == "" {
				t.Fatalf("no error and no text for %q", out)
			}
		}
	})
}

// A fake CLI records its arguments and environment, so the test sees what the agent gets.
func TestRunPassesResumeAndHidesRunnerSecrets(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake")
	body := `#!/bin/sh
echo "$@" > args.txt
env > env.txt
echo '{"type":"result","is_error":false,"result":"ok","session_id":"c-9"}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAMULUS_APP_KEY_B64", "secret-key")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "login")

	cli := CLI{Commands: map[string]string{config.Claude: script}}
	res, err := cli.Run(context.Background(), config.Claude, Request{Dir: dir, Prompt: "p", Session: "c-8", Model: "sonnet", Effort: "medium"})
	if err != nil || res.Session != "c-9" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	args, env := string(readAt(t, dir, "args.txt")), string(readAt(t, dir, "env.txt"))
	if !strings.Contains(args, "--resume c-8") || !strings.Contains(args, "--model sonnet") || !strings.Contains(args, "--effort medium") {
		t.Fatalf("args = %q", args)
	}
	if strings.Contains(env, "FAMULUS_APP_KEY_B64") || !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=login") {
		t.Fatal("the agent must get its CLI login and never the App key")
	}
}

// spec: S11. On a used-up free tier OpenCode logs the error and waits silently for hours;
// the run must end at the log line, not at the task timeout.
func TestOpenCodeLimitInLogEndsWaitingRun(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake")
	body := `#!/bin/sh
echo "$@" > args.txt
echo '{"type":"step_start","sessionID":"s-1"}'
echo 'level=ERROR message="stream error" error.error="AI_APICallError: Free usage limit reached."' >&2
sleep 30
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	start := time.Now()
	cli := CLI{Commands: map[string]string{config.OpenCode: script}}
	res, err := cli.Run(ctx, config.OpenCode, Request{Dir: dir, Prompt: "p"})
	limit, ok := errors.AsType[*LimitError](err)
	if !ok || limit.Message != "AI_APICallError: Free usage limit reached." {
		t.Fatalf("err = %v, want a limit error with only the error text of the log line", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("run took %s: it waited for the CLI instead of ending at the log line", took)
	}
	if res.Session != "s-1" {
		t.Fatalf("session = %q, want s-1 to resume after the pause", res.Session)
	}
	if args := string(readAt(t, dir, "args.txt")); !strings.Contains(args, "--print-logs") {
		t.Fatalf("args = %q, want --print-logs", args)
	}
}

func readAt(t *testing.T, dir, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
