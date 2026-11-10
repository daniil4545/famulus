package yard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/daniil4545/famulus/internal/github"
)

const alertLabel = "famulus:alert"

type alert struct {
	lastGood, down, lastUpdate time.Time
	failures                   int
	pending                    bool
	reported                   bool
	// told is set once the outage went to Notify, until the recovery went there too.
	told bool
	last error
}

func (a *alert) failed(at time.Time, last error) {
	if a.failures == 0 {
		a.down = at
	}
	a.failures++
	a.last = last
}

// clear ends the outage; a recovery message that did not get through keeps its outage start
// and is sent on a later pass.
func (a *alert) clear(at time.Time) {
	*a = alert{lastGood: at, told: a.told, down: a.down}
}

func (y *Yard) reportAlert(ctx context.Context, at time.Time, errs int, last error) error {
	a := &y.alert
	if errs > 0 {
		a.failed(at, last)
	}
	a.pending = a.pending || a.failures >= 3 || (a.failures > 0 && at.Sub(a.lastGood) >= 5*time.Minute)
	y.notifyOutage(ctx, at, errs)
	if y.Config.AlertRepo == "" {
		if errs == 0 {
			a.clear(at)
		}
		return nil
	}
	issues, err := y.GitHub.OpenIssues(ctx, y.Config.AlertRepo, alertLabel)
	if err != nil {
		return fmt.Errorf("find alert: %w", err)
	}
	if len(issues) == 0 && a.pending && !a.reported {
		body := fmt.Sprintf("Runner outage from %s. Last error: %s.", a.down.UTC().Format(time.RFC3339), alertCause(a.last))
		i, err := y.GitHub.CreateIssue(ctx, y.Config.AlertRepo, "famulus: failure", body, []string{alertLabel})
		if err != nil {
			return fmt.Errorf("open alert: %w", err)
		}
		issues = append(issues, i)
		a.lastUpdate = at
	}
	if len(issues) > 0 {
		a.reported = true
		if err := y.syncAlert(ctx, issues[0], at, errs); err != nil {
			return err
		}
	}
	if errs == 0 {
		a.clear(at)
	}
	return nil
}

// notifyOutage tells the owner about an outage and its end over Notify, a channel that does
// not need the GitHub App: a broken App key fails the alert issue too. A failed send is
// tried again on the next pass.
func (y *Yard) notifyOutage(ctx context.Context, at time.Time, errs int) {
	a := &y.alert
	if y.Notify == nil {
		return
	}
	var text string
	switch {
	case errs > 0 && a.pending && !a.told:
		text = fmt.Sprintf("famulus is failing since %s. Last error: %s.", a.down.UTC().Format(time.RFC3339), alertCause(a.last))
	case errs == 0 && a.told:
		text = fmt.Sprintf("famulus recovered, down from %s to %s.", a.down.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339))
	default:
		return
	}
	if err := y.Notify(ctx, text); err != nil {
		y.Log.Warn("notify failed", "err", err)
		return
	}
	a.told = errs > 0
}

func (y *Yard) syncAlert(ctx context.Context, i github.Issue, at time.Time, errs int) error {
	if errs == 0 {
		return y.recoverAlert(ctx, i, at)
	}
	return y.updateAlert(ctx, i, at)
}

func (y *Yard) recoverAlert(ctx context.Context, i github.Issue, at time.Time) error {
	from := outageStart(i, y.alert.down)
	notes, err := y.GitHub.Notes(ctx, y.Config.AlertRepo, i.Number, false, i.Created.Add(-time.Second))
	if err != nil {
		return fmt.Errorf("read recovery: %w", err)
	}
	recovered := false
	for _, n := range notes {
		recovered = recovered || strings.HasPrefix(n.Body, "recovered, down from ")
	}
	if !recovered {
		text := fmt.Sprintf("recovered, down from %s to %s", from.UTC().Format(time.RFC3339), at.UTC().Format(time.RFC3339))
		if err := y.GitHub.Comment(ctx, y.Config.AlertRepo, i.Number, text); err != nil {
			return fmt.Errorf("comment recovery: %w", err)
		}
	}
	if err := y.GitHub.CloseIssue(ctx, y.Config.AlertRepo, i.Number); err != nil {
		return fmt.Errorf("close alert: %w", err)
	}
	return nil
}

func outageStart(i github.Issue, down time.Time) time.Time {
	from := i.Created
	if body, ok := strings.CutPrefix(i.Body, "Runner outage from "); ok {
		stamp, _, _ := strings.Cut(body, ". Last error:")
		if parsed, err := time.Parse(time.RFC3339, stamp); err == nil {
			from = parsed
		}
	}
	if !down.IsZero() && down.Before(from) {
		from = down
	}
	return from
}

func (y *Yard) updateAlert(ctx context.Context, i github.Issue, at time.Time) error {
	a := &y.alert
	if at.Sub(a.lastUpdate) < time.Hour || at.Sub(i.Created) < time.Hour {
		return nil
	}
	notes, err := y.GitHub.Notes(ctx, y.Config.AlertRepo, i.Number, false, at.Add(-time.Hour))
	if err != nil {
		return fmt.Errorf("read alert updates: %w", err)
	}
	for _, n := range notes {
		if strings.HasPrefix(n.Body, "Runner still failing.") {
			a.lastUpdate = n.Created
			return nil
		}
	}
	if err := y.GitHub.Comment(ctx, y.Config.AlertRepo, i.Number, "Runner still failing. Last error: "+alertCause(a.last)); err != nil {
		return fmt.Errorf("update alert: %w", err)
	}
	a.lastUpdate = at
	return nil
}

// CLI output and remote error messages may contain credentials. Only known categories
// cross the public issue boundary; the full cause stays in the runner log.
func alertCause(err error) string {
	if where, ok := errors.AsType[*repoError](err); ok {
		return where.repo + ": " + alertKind(err)
	}
	return alertKind(err)
}

func alertKind(err error) string {
	var dns *net.DNSError
	var network net.Error
	var status *github.HTTPError
	switch {
	case errors.Is(err, github.ErrNotFound):
		return "GitHub API answered 404 (repository, issue or App installation not found)"
	case errors.As(err, &status):
		return fmt.Sprintf("GitHub API answered %d", status.Status)
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, context.Canceled):
		return "operation canceled"
	case errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.As(err, &network):
		return "network request failed"
	default:
		return "operation failed (details in runner log)"
	}
}
