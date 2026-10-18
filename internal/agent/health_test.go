package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/espn"
)

func TestHealthFlagsProjectionDrift(t *testing.T) {
	f, _ := scenario()
	a := testAgent(f)
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probs := planProblems(p, 0, a.Now()); len(probs) != 0 {
		t.Fatalf("healthy plan flagged: %v", probs)
	}
	// A projection that drifts from ESPN's with no news explaining it.
	pl := p.Me.Roster[0]
	p.Values[pl.ID].WeekProjection += 3
	probs := planProblems(p, 0, a.Now())
	if len(probs) != 1 || !strings.Contains(probs[0], pl.Name) {
		t.Errorf("drift not flagged: %v", probs)
	}
	// With the track-record adjustment on, differences are expected.
	if probs := planProblems(p, 0.1, a.Now()); len(probs) != 0 {
		t.Errorf("track-record adjustment flagged: %v", probs)
	}
}

func TestHealthFlagsMissingData(t *testing.T) {
	f, _ := scenario()
	a := testAgent(f)
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, pl := range p.Me.Roster {
		var keep []espn.StatLine
		for _, s := range pl.Stats {
			if !s.Projected || !s.Weekly || s.Week != week {
				// drop every past weekly projection and this week's
				if !(s.Projected && s.Weekly) {
					keep = append(keep, s)
				}
			}
		}
		pl.Stats = keep
	}
	p = mustReplan(t, a, p)
	probs := strings.Join(planProblems(p, 0, a.Now()), "\n")
	for _, want := range []string{"no projection this week", "past weekly projections are missing"} {
		if !strings.Contains(probs, want) {
			t.Errorf("missing %q in %s", want, probs)
		}
	}
}

// mustReplan re-values a plan's roster after its stats were edited.
func mustReplan(t *testing.T, a *Agent, p *Plan) *Plan {
	t.Helper()
	s := a.newState(p.League, p.Me)
	p.Values = s.values
	return p
}

func TestHealthAlertsByEmail(t *testing.T) {
	w, f, fm, _, _, _ := approvalWatcher(t)
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	sent := len(fm.sent)
	now := w.Agent.Now()

	w.mu.Lock()
	w.noteFailure(ctx, errors.New("espn: GET league: 401 Unauthorized"), now)
	w.mu.Unlock()
	if len(fm.sent) != sent+1 || !strings.Contains(fm.sent[sent].Body, "cookies have probably expired") {
		t.Fatalf("login failure not emailed: %+v", fm.sent[sent:])
	}
	if snap := w.Snapshot(); len(snap.Status.Health) != 1 {
		t.Errorf("app health: %v", snap.Status.Health)
	}
	// Same problem again: no repeat until the daily reminder.
	w.mu.Lock()
	w.noteFailure(ctx, errors.New("espn: GET league: 401 Unauthorized"), now.Add(time.Hour))
	w.mu.Unlock()
	if len(fm.sent) != sent+1 {
		t.Errorf("repeated alert within a day")
	}
	// Generic errors only alert after several in a row.
	w.mu.Lock()
	w.failures, w.health = 0, Health{}
	w.noteFailure(ctx, errors.New("timeout"), now)
	w.mu.Unlock()
	if len(fm.sent) != sent+1 {
		t.Errorf("one timeout shouldn't alert")
	}
	// A good plan clears it and says so.
	w.mu.Lock()
	w.setHealth(ctx, now, []string{"x"})
	w.mu.Unlock()
	f.fetches = 0
	w.lastFull = time.Time{}
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	last := fm.sent[len(fm.sent)-1]
	if last.Subject != "ffagent: all clear" {
		t.Errorf("no all-clear: %q", last.Subject)
	}
}
