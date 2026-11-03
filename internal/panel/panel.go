// Package panel serves a small read-only admin page: runner health, tasks from state.json and
// the latest log lines.
package panel

import (
	"bytes"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/daniil4545/famulus/internal/state"
)

// Log keeps the last lines written to it; it sits next to stdout behind the slog handler.
type Log struct {
	mu    sync.Mutex
	lines []string
	limit int
	part  []byte
}

func NewLog(limit int) *Log {
	return &Log{limit: limit}
}

func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.part = append(l.part, p...)
	for {
		i := bytes.IndexByte(l.part, '\n')
		if i < 0 {
			break
		}
		l.lines = append(l.lines, string(l.part[:i]))
		l.part = l.part[i+1:]
	}
	if extra := len(l.lines) - l.limit; extra > 0 {
		l.lines = slices.Delete(l.lines, 0, extra)
	}
	return len(p), nil
}

func (l *Log) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.lines)
}

// Health is what the loop reports about itself.
type Health struct {
	mu       sync.Mutex
	started  time.Time
	lastPass time.Time
}

func NewHealth(now time.Time) *Health {
	return &Health{started: now}
}

func (h *Health) Passed(at time.Time) {
	h.mu.Lock()
	h.lastPass = at
	h.mu.Unlock()
}

func (h *Health) times() (started, lastPass time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.started, h.lastPass
}

type Panel struct {
	StatePath string
	Health    *Health
	Log       *Log
	Bot       string
	Now       func() time.Time
	Logger    *slog.Logger
}

type row struct {
	Key, Repo, Title, Status, Agent, Age string
	Issue, PR                            int
	updated                              time.Time
}

type count struct {
	Status string
	N      int
}

type page struct {
	Bot, Uptime, LastPass string
	Counts                []count
	Paused                map[string]string
	Rows                  []row
	Log                   []string
}

// statusOrder is the board from left to right: running, waiting, needs the owner, review, done.
var statusOrder = []state.Status{state.Working, state.Paused, state.Blocked, state.Failed, state.Review, state.Done}

func (p *Panel) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	store, err := state.Load(p.StatePath)
	if err != nil {
		p.Logger.Warn("panel: read state", "err", err)
		http.Error(w, "state is unreadable, see the runner log", http.StatusInternalServerError)
		return
	}
	now := p.Now()
	started, lastPass := p.Health.times()
	pg := page{
		Bot:      p.Bot,
		Uptime:   now.Sub(started).Round(time.Minute).String(),
		LastPass: "not yet",
		Paused:   map[string]string{},
		Log:      p.Log.Lines(),
	}
	if !lastPass.IsZero() {
		pg.LastPass = now.Sub(lastPass).Round(time.Second).String() + " ago"
	}
	for name, until := range store.Paused {
		if now.Before(until) {
			pg.Paused[name] = until.UTC().Format("15:04 UTC")
		}
	}
	counts := map[state.Status]int{}
	for key, t := range store.Tasks {
		counts[t.Status]++
		r := row{Key: key, Repo: t.Repo, Title: t.Title, Status: string(t.Status), Agent: t.Agent, Issue: t.Issue, PR: t.PR, updated: t.Updated}
		if !t.Updated.IsZero() {
			r.Age = age(now.Sub(t.Updated))
		}
		pg.Rows = append(pg.Rows, r)
	}
	for _, s := range statusOrder {
		if counts[s] > 0 {
			pg.Counts = append(pg.Counts, count{Status: string(s), N: counts[s]})
		}
	}
	// board order, then the most recent change first inside a column
	slices.SortFunc(pg.Rows, func(a, b row) int {
		if d := slices.Index(statusOrder, state.Status(a.Status)) - slices.Index(statusOrder, state.Status(b.Status)); d != 0 {
			return d
		}
		if c := b.updated.Compare(a.updated); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	slices.Reverse(pg.Log)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, pg); err != nil {
		p.Logger.Warn("panel: render", "err", err)
	}
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d d ago", int(d.Hours()/24))
}

var tmpl = template.Must(template.New("panel").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta http-equiv="refresh" content="10">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>famulus</title>
<style>
:root{--bg:#fff;--fg:#1d1d1f;--mut:#6e6e73;--line:#e5e5ea;--warn:#b25000}
@media (prefers-color-scheme:dark){:root{--bg:#161618;--fg:#f2f2f7;--mut:#98989d;--line:#2c2c2e;--warn:#ff9f0a}}
body{background:var(--bg);color:var(--fg);font:14px/1.45 -apple-system,system-ui,sans-serif;margin:0 auto;max-width:1000px;padding:16px}
h1{font-size:18px;margin:0 0 4px}h2{font-size:14px;margin:20px 0 6px;color:var(--mut)}
.mut{color:var(--mut)}.warn{color:var(--warn)}a{color:inherit}
table{border-collapse:collapse;width:100%}td{border-top:1px solid var(--line);padding:5px 8px 5px 0;vertical-align:top}
pre{font:12px/1.4 ui-monospace,monospace;white-space:pre-wrap;word-break:break-all;margin:0}
</style></head><body>
<h1>famulus</h1>
<div class="mut">bot {{.Bot}} · up {{.Uptime}} · last pass {{.LastPass}}{{range $name, $until := .Paused}} · <span class="warn">{{$name}} paused until {{$until}}</span>{{end}}</div>
<div class="mut">{{range $i, $c := .Counts}}{{if $i}} · {{end}}{{$c.Status}} {{$c.N}}{{end}}</div>
<h2>Tasks</h2>
<table>{{range .Rows}}<tr>
<td><a href="https://github.com/{{.Repo}}/issues/{{.Issue}}">{{.Key}}</a></td><td>{{.Title}}</td>
<td>{{.Status}}</td><td class="mut">{{.Age}}</td><td class="mut">{{.Agent}}</td>
<td>{{if .PR}}<a href="https://github.com/{{.Repo}}/pull/{{.PR}}">PR #{{.PR}}</a>{{end}}</td></tr>
{{else}}<tr><td class="mut">no tasks yet</td></tr>{{end}}</table>
<h2>Log, newest first</h2>
<pre>{{range .Log}}{{.}}
{{end}}</pre>
</body></html>
`))
