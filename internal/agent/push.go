package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
	"github.com/avitechnix/ffagent/internal/push"
)

// Notifier pushes a notification to the phone app (implemented by
// *push.Store).
type Notifier interface {
	Notify(ctx context.Context, n push.Notification) (int, error)
}

// notify sends n in the background so a slow push service never holds up
// the watcher or the app. Call with w.mu held.
func (w *Watcher) notify(n push.Notification) {
	if w.Notifier == nil {
		return
	}
	w.pushes.Add(1)
	go func() {
		defer w.pushes.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if _, err := w.Notifier.Notify(ctx, n); err != nil {
			w.mu.Lock()
			fmt.Fprintf(w.Out, "%s  PUSH    couldn't notify: %v\n", stamp(w.Agent.Now()), err)
			w.mu.Unlock()
		}
	}()
}

// notifyProposal tells the phone that moves are waiting.
func (w *Watcher) notifyProposal(items []*approval.Item) {
	var waiting, auto []string
	for _, it := range items {
		if it.Status == approval.Approved {
			auto = append(auto, it.Summary)
		} else {
			waiting = append(waiting, it.Summary)
		}
	}
	if len(waiting) > 0 {
		title := plural(len(waiting), "move") + " to review"
		if len(waiting) == 1 {
			title = "A move to review"
		}
		w.notify(push.Notification{Title: title, Body: briefList(waiting), URL: "./#moves", Tag: "ffagent-moves"})
	}
	if len(auto) > 0 {
		w.notify(push.Notification{Title: "Benching a ruled-out starter", Body: briefList(auto) + ". Running it now, as you asked (auto_approve_ruled_out).", URL: "./#history", Tag: "ffagent-auto"})
	}
}

// notifyResults tells the phone what happened to moves that ran.
func (w *Watcher) notifyResults(ran []*approval.Item) {
	var done, failed []string
	for _, it := range ran {
		switch it.Status {
		case approval.Executed:
			done = append(done, it.Summary)
		case approval.Failed, approval.Skipped:
			failed = append(failed, it.Summary+": "+it.Result)
		}
	}
	if len(done) > 0 {
		w.notify(push.Notification{Title: "Done", Body: briefList(done), URL: "./#history", Tag: "ffagent-done"})
	}
	if len(failed) > 0 {
		w.notify(push.Notification{Title: "Not run", Body: briefList(failed), URL: "./#history", Tag: "ffagent-failed"})
	}
}

func briefList(xs []string) string {
	const max = 3
	if len(xs) > max {
		return strings.Join(xs[:max], "\n") + fmt.Sprintf("\n…and %d more", len(xs)-max)
	}
	return strings.Join(xs, "\n")
}

// Kickoff sweeps: inactives come out about 90 minutes before kickoff, and
// a starter ruled out then has to be benched before his game locks, so the
// news is checked every minute around each kickoff.
const (
	sweepBefore = 100 * time.Minute
	sweepAfter  = 5 * time.Minute
)

// newsInterval is how long to wait between news checks.
func (w *Watcher) newsInterval(every time.Duration, now time.Time) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.latest != nil && nearKickoff(w.latest.League, now) {
		return time.Minute
	}
	return every
}

func nearKickoff(l *espn.League, now time.Time) bool {
	for _, games := range l.ProGames {
		g, ok := games[l.CurrentWeek]
		if ok && !g.Start.IsZero() && now.After(g.Start.Add(-sweepBefore)) && now.Before(g.Start.Add(sweepAfter)) {
			return true
		}
	}
	return false
}

// ruledOut reports whether a player won't play this week, by the latest
// news or ESPN's designation.
func (a *Agent) ruledOut(p *espn.Player) bool {
	switch a.injuryStatus(p) {
	case espn.InjuryOut, espn.InjuryReserve, espn.InjurySuspension:
		return true
	}
	return false
}

// injuryStatus is the latest designation: the news if it has one, else ESPN's.
func (a *Agent) injuryStatus(p *espn.Player) string {
	if inj, ok := a.Injuries[p.ID]; ok && inj.Status != "" {
		return inj.Status
	}
	return p.InjuryStatus
}

// autoApprovable reports whether a lineup change only benches ruled-out
// starters for healthy players, so it can run without waiting (when the
// owner has opted in). Any other benching, or any roster move it depends
// on, needs the owner.
func (a *Agent) autoApprovable(moves []lineup.Move) (bool, []string) {
	var out []string
	for _, m := range moves {
		from, to := espn.IsStartingSlot(m.From), espn.IsStartingSlot(m.To)
		switch {
		case from && !to:
			if !a.ruledOut(m.Player) {
				return false, nil // benches someone who can play
			}
			out = append(out, m.Player.Name)
		case to && a.ruledOut(m.Player):
			return false, nil
		case to && !from && a.injuryStatus(m.Player) != "" && a.injuryStatus(m.Player) != espn.InjuryActive:
			return false, nil // the replacement is questionable or doubtful: your call
		}
	}
	return len(out) > 0, out
}
