package agent

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
	"github.com/avitechnix/ffagent/internal/news"
	"github.com/avitechnix/ffagent/internal/push"
)

type fakeNotifier struct {
	mu   sync.Mutex
	sent []push.Notification
}

func (f *fakeNotifier) Notify(_ context.Context, n push.Notification) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return 1, nil
}

func (f *fakeNotifier) titles() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ts []string
	for _, n := range f.sent {
		ts = append(ts, n.Title)
	}
	return strings.Join(ts, " | ")
}

// ruledOutWatcher: my starting RB1 is ruled out; a healthy RB3 is on the
// bench and there's no one worth adding, so the only move is a lineup swap.
func ruledOutWatcher(t *testing.T, autoApprove bool) (*Watcher, *fakeESPN, *fakeMail, *fakeNotifier) {
	f, _, _ := handcuffLeague()
	f.pool = nil
	a := testAgent(f)
	a.Cfg.Email.Address, a.Cfg.Email.NotifyTo, a.Cfg.Email.DigestHour = "bot@example.com", owner, 25
	a.Cfg.AutoApproveRuledOut = autoApprove
	l, _ := f.FetchLeague(context.Background())
	rb1 := findByName(l.Teams[0].Roster, "My RB1")
	nf := &fakeNews{reports: map[int]news.Report{
		rb1.ID: {PlayerID: rb1.ID, Name: rb1.Name, ProTeamID: rb1.ProTeamID, Position: "RB", Status: espn.InjuryOut, Date: a.Now().Add(-10 * time.Minute)},
	}}
	fm, fn := &fakeMail{}, &fakeNotifier{}
	w := &Watcher{Agent: a, News: nf, Apply: true, Out: io.Discard, Mailer: fm, Notifier: fn, StatePath: filepath.Join(t.TempDir(), "state.json")}
	return w, f, fm, fn
}

func lineupSubmits(f *fakeESPN) int {
	n := 0
	for _, txn := range f.submitted {
		if txn.Type == espn.TxnRoster {
			n++
		}
	}
	return n
}

func TestRuledOutStarterIsBenchedWithoutWaiting(t *testing.T) {
	w, f, fm, fn := ruledOutWatcher(t, true)
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.approvals.Items) != 1 || w.approvals.Items[0].Status != approval.Approved || w.approvals.Items[0].Via != "auto" {
		for _, it := range w.approvals.Items {
			t.Logf("%+v", *it)
		}
		t.Fatalf("expected one auto-approved lineup change")
	}
	if !strings.Contains(fm.sent[0].Body, "Approved automatically because My RB1 is ruled out") || !strings.Contains(fm.sent[0].Subject, "approved automatically") {
		t.Errorf("proposal email doesn't say it was approved automatically:\n%s", fm.sent[0].Body)
	}
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if lineupSubmits(f) != 1 || w.approvals.Items[0].Status != approval.Executed {
		t.Fatalf("lineup change didn't run: %d submitted, status %s (%s)", lineupSubmits(f), w.approvals.Items[0].Status, w.approvals.Items[0].Result)
	}
	if len(fm.sent) != 2 {
		t.Errorf("expected a results email, got %d emails", len(fm.sent))
	}
	w.pushes.Wait()
	if got := fn.titles(); got != "Benching a ruled-out starter | Done" {
		t.Errorf("notifications: %s", got)
	}
}

func TestRuledOutSwapWaitsUnlessOptedIn(t *testing.T) {
	w, f, _, fn := ruledOutWatcher(t, false)
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if lineupSubmits(f) != 0 || len(w.approvals.Pending()) != 1 {
		t.Fatalf("ran without approval")
	}
	w.pushes.Wait()
	if got := fn.titles(); got != "A move to review" {
		t.Errorf("notifications: %s", got)
	}
	if n := fn.sent[0]; n.URL != "./#moves" || !strings.Contains(n.Body, "Set lineup") {
		t.Errorf("notification %+v", n)
	}
}

func TestAutoApproveOnlyBenchesRuledOutPlayers(t *testing.T) {
	f, _, _ := handcuffLeague()
	a := testAgent(f)
	out := mk("Out", "RB", espn.SlotRB, 10)
	q := mk("Questionable", "RB", espn.SlotBench, 8)
	healthy := mk("Healthy", "RB", espn.SlotBench, 8)
	starter := mk("Starter", "RB", espn.SlotRB, 9)
	out.InjuryStatus = espn.InjuryOut
	q.InjuryStatus = espn.InjuryQuestionable
	cases := []struct {
		name  string
		moves []lineup.Move
		want  bool
	}{
		{"swap out for healthy", []lineup.Move{{Player: out, From: espn.SlotRB, To: espn.SlotBench}, {Player: healthy, From: espn.SlotBench, To: espn.SlotRB}}, true},
		{"also benches a healthy starter", []lineup.Move{{Player: out, From: espn.SlotRB, To: espn.SlotBench}, {Player: starter, From: espn.SlotRB, To: espn.SlotBench}, {Player: healthy, From: espn.SlotBench, To: espn.SlotRB}}, false},
		{"replacement is questionable", []lineup.Move{{Player: out, From: espn.SlotRB, To: espn.SlotBench}, {Player: q, From: espn.SlotBench, To: espn.SlotRB}}, false},
		{"no one ruled out", []lineup.Move{{Player: starter, From: espn.SlotRB, To: espn.SlotBench}, {Player: healthy, From: espn.SlotBench, To: espn.SlotRB}}, false},
	}
	for _, c := range cases {
		if got, _ := a.autoApprovable(c.moves); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestKickoffSweepChecksEveryMinute(t *testing.T) {
	kick := time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC)
	l := &espn.League{CurrentWeek: 6, ProGames: map[int]map[int]espn.ProGame{1: {6: {Start: kick}}, 2: {7: {Start: kick.Add(-time.Hour)}}}}
	w := &Watcher{latest: &Plan{League: l}}
	for _, c := range []struct {
		at   time.Time
		want time.Duration
	}{
		{kick.Add(-3 * time.Hour), 5 * time.Minute},
		{kick.Add(-95 * time.Minute), time.Minute}, // inactives are out
		{kick.Add(2 * time.Minute), time.Minute},
		{kick.Add(time.Hour), 5 * time.Minute}, // game under way; slots locked
	} {
		if got := w.newsInterval(5*time.Minute, c.at); got != c.want {
			t.Errorf("at %s: %s, want %s", c.at.Sub(kick), got, c.want)
		}
	}
}

func TestNoAutoApproveWhenTheLineupNeedsARosterMove(t *testing.T) {
	w, f, _, _ := ruledOutWatcher(t, true)
	f.pool = append(f.pool, freeAgent(mk("Waiver Hero", "RB", espn.SlotBench, 15), espn.StatusFreeAgent))
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.submitted) != 0 {
		t.Fatalf("ran %d transactions without approval", len(f.submitted))
	}
	kinds := ""
	for _, it := range w.approvals.Items {
		kinds += it.Kind + " "
		if it.Status != approval.Pending {
			t.Errorf("%s is %s", it.Summary, it.Status)
		}
	}
	if !strings.Contains(kinds, "ADD") || !strings.Contains(kinds, "LINEUP") {
		t.Fatalf("expected a pickup and a lineup change, got %s", kinds)
	}
}
