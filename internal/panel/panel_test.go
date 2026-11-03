package panel

import (
	"fmt"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daniil4545/famulus/internal/state"
)

func TestLogKeepsLastLines(t *testing.T) {
	l := NewLog(2)
	fmt.Fprint(l, "one\ntw")
	fmt.Fprint(l, "o\nthree\n")
	if got := strings.Join(l.Lines(), "|"); got != "two|three" {
		t.Fatalf("lines = %q", got)
	}
}

func TestRowsFollowBoardOrderThenRecency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	add := func(key string, status state.Status, ago time.Duration) {
		store.Tasks[key] = &state.Task{Repo: "o/r", Title: key, Status: status, Updated: now.Add(-ago)}
	}
	add("done-new", state.Done, time.Minute)
	add("pr-old", state.Review, 3*time.Hour)
	add("pr-new", state.Review, 10*time.Minute)
	add("working", state.Working, 2*time.Hour)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	p := &Panel{
		StatePath: path, Health: NewHealth(now), Log: NewLog(1),
		Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler),
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))
	body := rec.Body.String()
	last := -1
	for _, key := range []string{"working", "pr-new", "pr-old", "done-new"} {
		i := strings.Index(body, ">"+key+"<")
		if i < last {
			t.Fatalf("%s is out of order: want working, pr-new, pr-old, done-new", key)
		}
		last = i
	}
	if !strings.Contains(body, "10 min ago") || !strings.Contains(body, "working 1 · pr 2 · done 1") {
		t.Fatal("page must show the age of a change and counts in board order")
	}
}

func TestPageShowsTasksAndEscapesTitles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	store.Tasks["o/r#1"] = &state.Task{Repo: "o/r", Issue: 1, Title: `<script>x</script>`, Status: state.Review, Agent: "opencode", PR: 4}
	store.Paused["opencode"] = now.Add(time.Hour)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	log := NewLog(10)
	fmt.Fprintln(log, "msg=pushed task=o/r#1")
	p := &Panel{
		StatePath: path, Health: NewHealth(now.Add(-time.Hour)), Log: log, Bot: "bot[bot]",
		Now: func() time.Time { return now }, Logger: slog.New(slog.DiscardHandler),
	}

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/", nil))
	body := rec.Body.String()
	for _, want := range []string{"o/r#1", "https://github.com/o/r/pull/4", "opencode paused until 13:00 UTC", "msg=pushed", "&lt;script&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "<script>x") {
		t.Error("an issue title must be escaped: it is text from GitHub")
	}
}
