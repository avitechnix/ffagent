package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/mail"
)

// Health is the agent's own check of the data it's working from, so
// problems surface as an alert instead of as odd recommendations.
type Health struct {
	Problems  []string  `json:"problems,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	// Since is when the current set of problems first appeared.
	Since time.Time `json:"since,omitempty"`
}

// OK reports whether the last check found nothing wrong.
func (h Health) OK() bool { return len(h.Problems) == 0 }

const (
	// failuresBeforeAlert is how many league reads in a row must fail before
	// a generic error becomes an alert. Login failures alert at once.
	failuresBeforeAlert = 3
	healthReminder      = 24 * time.Hour
)

// noteFailure records a failed league read. Call with w.mu held.
func (w *Watcher) noteFailure(ctx context.Context, err error, now time.Time) {
	w.failures++
	msg := err.Error()
	switch {
	case isAuthError(msg):
		w.setHealth(ctx, now, []string{"ESPN refused the login: your espn_s2/SWID cookies have probably expired. Refresh them with: sudo ffagentctl secrets set"})
	case w.failures >= failuresBeforeAlert:
		w.setHealth(ctx, now, []string{fmt.Sprintf("Couldn't read your league from ESPN %d times in a row. Last error: %s", w.failures, msg)})
	}
}

func isAuthError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") || strings.Contains(m, "403") || strings.Contains(m, "unauthorized") || strings.Contains(m, "forbidden")
}

// checkHealth looks over a fresh plan for signs the data is wrong or
// incomplete. Call with w.mu held.
func (w *Watcher) checkHealth(ctx context.Context, p *Plan, now time.Time) {
	w.failures = 0
	w.setHealth(ctx, now, planProblems(p, w.Agent.Cfg.TrackRecordAdjustment, now))
}

// planProblems is the data check itself.
func planProblems(p *Plan, trackRecord float64, now time.Time) []string {
	var probs []string
	for _, warn := range p.Warnings {
		probs = append(probs, "ESPN data warning: "+warn)
	}
	l := p.League
	var noProj, mismatch []string
	withHistory, eligible := 0, 0
	for _, pl := range p.Me.Roster {
		v := p.Values[pl.ID]
		if v == nil || pl.LineupSlot == espn.SlotIR {
			continue
		}
		if !pl.Locked && !v.OnBye && v.InjuryFactor > 0 {
			if _, ok := pl.WeekProjection(l.Season, l.CurrentWeek); !ok {
				noProj = append(noProj, pl.Name)
			} else if trackRecord == 0 && v.OpportunityBoost == 0 && (pl.NewsStatus == "" || pl.NewsStatus == pl.InjuryStatus) &&
				math.Abs(v.WeekProjection-v.ESPNWeekProjection) > 0.05 {
				mismatch = append(mismatch, fmt.Sprintf("%s (agent %.1f, ESPN %.1f)", pl.Name, v.WeekProjection, v.ESPNWeekProjection))
			}
		}
		if v.WeeksPlayed >= 2 {
			eligible++
			if v.ProjectedWeeks >= 2 {
				withHistory++
			}
		}
	}
	if len(noProj) > 0 && len(noProj)*3 > len(p.Me.Roster) {
		sort.Strings(noProj)
		probs = append(probs, fmt.Sprintf("ESPN returned no projection this week for %d of your players (%s); they're valued from past production instead.", len(noProj), strings.Join(noProj, ", ")))
	}
	if len(mismatch) > 0 {
		probs = append(probs, "Projections differ from ESPN's with no injury news to explain it: "+strings.Join(mismatch, "; ")+". Please report this.")
	}
	if l.CurrentWeek >= 4 && eligible >= 4 && withHistory*2 < eligible {
		probs = append(probs, fmt.Sprintf("ESPN's past weekly projections are missing for %d of %d players, so track records cover too few games.", eligible-withHistory, eligible))
	}
	if l.AnyGameLive(now) && len(l.Live) == 0 {
		probs = append(probs, "Live game clocks from ESPN's scoreboard are unavailable; live projections are estimated from kickoff times.")
	}
	return probs
}

// setHealth records the check and emails when something new goes wrong, a
// daily reminder while it lasts, and once more when it clears.
func (w *Watcher) setHealth(ctx context.Context, now time.Time, probs []string) {
	prev := w.health
	w.health = Health{Problems: probs, CheckedAt: now, Since: prev.Since}
	changed := strings.Join(prev.Problems, "\n") != strings.Join(probs, "\n")
	if changed {
		w.health.Since = now
	}
	for _, p := range probs {
		if changed {
			fmt.Fprintf(w.Out, "%s  HEALTH  %s\n", stamp(now), p)
		}
	}
	if w.Mailer == nil || w.Agent.Cfg.Email.NotifyTo == "" {
		return
	}
	var subject, body string
	switch {
	case len(probs) > 0 && (changed || now.Sub(w.healthMailed) >= healthReminder):
		subject = "ffagent needs attention"
		body = "The agent found a problem with the data it's working from:\n\n- " + strings.Join(probs, "\n- ") +
			"\n\nUntil it's fixed, treat its recommendations with care. You'll get a reminder daily while this lasts, and a note when it's resolved.\n"
	case len(probs) == 0 && len(prev.Problems) > 0:
		subject = "ffagent: all clear"
		body = "The earlier problem is resolved; the agent's data checks pass again.\n"
	default:
		return
	}
	if _, err := w.Mailer.Send(ctx, mail.Message{To: w.Agent.Cfg.Email.NotifyTo, Subject: subject, Body: body, MessageID: w.Mailer.NewMessageID()}); err != nil {
		fmt.Fprintf(w.Out, "%s  HEALTH  couldn't send the alert email: %v\n", stamp(now), err)
		return
	}
	w.healthMailed = now
}
