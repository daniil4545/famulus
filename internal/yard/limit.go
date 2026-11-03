package yard

import (
	"context"
	"fmt"
	"time"

	"github.com/daniil4545/famulus/internal/agent"
	"github.com/daniil4545/famulus/internal/state"
)

// pause rests the agent until its plan is back and parks the task; the issue gets one comment
// per pause, the task restarts by itself when the pause ends.
func (y *Yard) pause(ctx context.Context, t *state.Task, limit *agent.LimitError) error {
	wait := limit.RetryAfter
	if wait <= 0 {
		wait = y.Config.LimitPause
	}
	until := y.now().Add(wait).UTC()
	y.State.Paused[t.Agent] = until
	y.Log.Warn("agent paused", "task", state.Key(t.Repo, t.Issue), "agent", t.Agent, "until", until, "err", limit.Message)
	if !t.PauseNoted {
		t.PauseNoted = true
		msg := fmt.Sprintf("famulus could not run %s: its limit is reached (%s).\n\nThe runner retries by itself after %s UTC; nothing to do.%s",
			t.Agent, limit.Message, until.Format("2006-01-02 15:04"), footer(t))
		if err := y.GitHub.Comment(ctx, t.Repo, t.Issue, msg); err != nil {
			y.Log.Warn("pause comment", "task", state.Key(t.Repo, t.Issue), "err", err)
		}
	}
	return y.setStatus(ctx, t, state.Paused)
}

// dailyFull tells whether today's cap of new issues is reached; retries and PR notes do not count.
func (y *Yard) dailyFull() bool {
	return y.Config.DailyLimit > 0 && y.State.StartDay == y.today() && y.State.Started >= y.Config.DailyLimit
}

func (y *Yard) countStart() {
	if day := y.today(); y.State.StartDay != day {
		y.State.StartDay = day
		y.State.Started = 0
	}
	y.State.Started++
}

func (y *Yard) today() string {
	return y.now().UTC().Format(time.DateOnly)
}

func (y *Yard) paused(name string) bool {
	until, ok := y.State.Paused[name]
	return ok && y.now().Before(until)
}

func (y *Yard) now() time.Time {
	if y.Clock != nil {
		return y.Clock()
	}
	return time.Now()
}
