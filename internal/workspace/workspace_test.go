package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote makes a bare repository o/r with one commit on main under a temp "server" folder.
func remote(t *testing.T) string {
	t.Helper()
	server := t.TempDir()
	bare := filepath.Join(server, "o", "r.git")
	seed := t.TempDir()
	run(t, seed, "init", "-q", "-b", "main")
	run(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init")
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "clone", "-q", "--bare", seed, bare)
	return server + "/"
}

func TestPrepareCommitPushRemove(t *testing.T) {
	ctx := context.Background()
	server := remote(t)
	ws := New(t.TempDir(), server, func(context.Context, string) (string, error) { return "tok-secret", nil }, "bot[bot]", "1+bot[bot]@users.noreply.github.com")

	dir, err := ws.Prepare(ctx, "o/r", "main", "agent/1-x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := run(t, dir, "branch", "--show-current"); got != "agent/1-x" {
		t.Fatalf("branch = %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	committed, err := ws.CommitLeftovers(ctx, dir, "chore: leftovers")
	if err != nil || !committed {
		t.Fatalf("committed = %v, err = %v", committed, err)
	}
	if ahead, err := ws.Ahead(ctx, dir, "main"); err != nil || ahead != 1 {
		t.Fatalf("ahead = %d, err = %v", ahead, err)
	}
	if author := run(t, dir, "log", "-1", "--format=%an <%ae>"); author != "bot[bot] <1+bot[bot]@users.noreply.github.com>" {
		t.Fatalf("author = %q", author)
	}
	if err := ws.Push(ctx, "o/r", dir, "agent/1-x"); err != nil {
		t.Fatal(err)
	}
	if got := run(t, server+"o/r.git", "rev-parse", "agent/1-x"); got != run(t, dir, "rev-parse", "HEAD") {
		t.Fatal("remote branch must point at the pushed commit")
	}
	config := run(t, filepath.Join(ws.root, "repos", "o", "r"), "config", "--list")
	if strings.Contains(config, "tok-secret") || strings.Contains(config, "extraheader") {
		t.Fatal("the token must not be saved in the git config")
	}

	again, err := ws.Prepare(ctx, "o/r", "main", "agent/1-x", 1)
	if err != nil || again != dir {
		t.Fatalf("second Prepare = %q, %v; want the same worktree", again, err)
	}
	if err := ws.Remove(ctx, "o/r", "agent/1-x", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree must be gone")
	}

	// after a lost volume the branch comes back from the remote with the agent's commit
	fresh, err := ws.Prepare(ctx, "o/r", "main", "agent/1-x", 1)
	if err != nil {
		t.Fatal(err)
	}
	if ahead, _ := ws.Ahead(ctx, fresh, "main"); ahead != 1 {
		t.Fatalf("restored branch ahead = %d, want 1", ahead)
	}
}

func TestPushWithoutForce(t *testing.T) {
	ctx := context.Background()
	server := remote(t)
	token := func(context.Context, string) (string, error) { return "t", nil }
	first := New(t.TempDir(), server, token, "a", "a@a")
	second := New(t.TempDir(), server, token, "b", "b@b")
	var dirs []string
	for _, ws := range []*Workspace{first, second} {
		dir, err := ws.Prepare(ctx, "o/r", "main", "agent/2", 2)
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, dir)
	}
	for i, ws := range []*Workspace{first, second} {
		dir := dirs[i]
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte{byte('0' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.CommitLeftovers(ctx, dir, "c"); err != nil {
			t.Fatal(err)
		}
		err := ws.Push(ctx, "o/r", dir, "agent/2")
		if i == 1 && err == nil {
			t.Fatal("a diverged push must be rejected, not forced")
		}
	}
}

// A review sees the PR head and its diff; what the agent leaves there never reaches the remote.
func TestReviewTree(t *testing.T) {
	ctx := context.Background()
	server := remote(t)
	bare := filepath.Join(server, "o", "r.git")
	seed := t.TempDir()
	run(t, seed, "clone", "-q", bare, ".")
	if err := os.WriteFile(filepath.Join(seed, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// spec: docs/spec.md.S30: files the agent CLI reads itself and symlinks never reach the agent
	for _, f := range []string{".claude/settings.json", "sub/CLAUDE.md", ".mcp.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(seed, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(seed, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc", filepath.Join(seed, "out")); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "add", ".")
	run(t, seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "add a")
	run(t, seed, "push", "-q", "origin", "HEAD:refs/pull/7/head")
	sha := run(t, seed, "rev-parse", "HEAD")
	ws := New(t.TempDir(), server, func(context.Context, string) (string, error) { return "tok", nil }, "bot", "bot@x")

	dir, diff, err := ws.ReviewTree(ctx, "o/r", "main", 7, sha)
	if err != nil {
		t.Fatal(err)
	}
	if run(t, dir, "rev-parse", "HEAD") != sha || !strings.Contains(diff, "+package a") {
		t.Fatalf("head or diff wrong: %q", diff)
	}
	for _, f := range []string{".claude", "sub/CLAUDE.md", ".mcp.json", "out"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); !os.IsNotExist(err) || !strings.Contains(diff, f) {
			t.Fatalf("%s: left in the tree (%v) or missing from the diff", f, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "junk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a second review of the same PR starts clean
	if dir, _, err = ws.ReviewTree(ctx, "o/r", "main", 7, sha); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "junk")); !os.IsNotExist(err) {
		t.Fatalf("junk survived: %v", err)
	}
	if err := ws.RemoveReview(ctx, "o/r", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("review tree left: %v", err)
	}
}

// spec: docs/spec.md.R5: git and the hooks it runs never see the App key.
func TestGitHidesRunnerSecrets(t *testing.T) {
	t.Setenv("FAMULUS_APP_KEY_B64", "app-key-value")
	out, err := git(t.Context(), t.TempDir(), nil, "-c", "alias.e=!env", "e")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "FAMULUS_") || !strings.Contains(out, "PATH=") {
		t.Fatalf("git environment: %s", out)
	}
}
