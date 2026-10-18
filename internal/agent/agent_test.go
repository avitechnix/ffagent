package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/config"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/secrets"
)

const season, week = 2026, 6

type fakeESPN struct {
	league    func() *espn.League
	pool      []*espn.Player
	submitted []*espn.Transaction
	fetches   int
}

func (f *fakeESPN) FetchLeague(context.Context) (*espn.League, error) {
	f.fetches++
	return f.league(), nil
}

func (f *fakeESPN) FreeAgents(_ context.Context, _ *espn.League, pos string, teams []int, _ int) ([]*espn.Player, error) {
	var out []*espn.Player
	for _, p := range f.pool {
		if (pos == "" || p.Position == pos) && (len(teams) == 0 || contains(teams, p.ProTeamID)) {
			c := *p
			out = append(out, &c)
		}
	}
	return out, nil
}

func (f *fakeESPN) Submit(_ context.Context, t *espn.Transaction) (*espn.TransactionResult, error) {
	f.submitted = append(f.submitted, t)
	return &espn.TransactionResult{Status: "EXECUTED"}, nil
}

var nextID = 100

// mk creates a player whose history matches a steady per-game projection.
func mk(name, pos string, slot int, ppg float64) *espn.Player {
	nextID++
	p := &espn.Player{
		ID: nextID, Name: name, Position: pos, ProTeamID: 1 + nextID%30, ProTeam: "NFL",
		EligibleSlots: espn.EligibleSlotsFor(pos), InjuryStatus: espn.InjuryActive,
		LineupSlot: slot, Status: espn.StatusOnTeam,
	}
	for w := 1; w < week; w++ {
		p.Stats = append(p.Stats,
			espn.StatLine{Season: season, Week: w, Weekly: true, Projected: true, Points: ppg},
			espn.StatLine{Season: season, Week: w, Weekly: true, Points: ppg})
	}
	p.Stats = append(p.Stats,
		espn.StatLine{Season: season, Week: week, Weekly: true, Projected: true, Points: ppg},
		espn.StatLine{Season: season, Projected: true, Points: ppg * 17, Average: ppg})
	return p
}

func freeAgent(p *espn.Player, status string) *espn.Player {
	p.Status, p.LineupSlot, p.OnTeamID = status, espn.SlotUnknown, 0
	return p
}

func team(id int, name string, roster ...*espn.Player) *espn.Team {
	for _, p := range roster {
		p.OnTeamID = id
	}
	return &espn.Team{ID: id, Name: name, Roster: roster, FAABRemaining: 80}
}

func scenario() (*fakeESPN, *espn.Team) {
	nextID = 100
	injured := mk("Hurt Starter", "RB", espn.SlotRB, 18)
	injured.InjuryStatus = espn.InjuryOut
	me := team(1, "Me",
		mk("My QB", "QB", espn.SlotQB, 20),
		injured,
		mk("RB Two", "RB", espn.SlotRB, 13),
		mk("RB Bench", "RB", espn.SlotBench, 6),
		mk("WR One", "WR", espn.SlotWR, 15),
		mk("WR Two", "WR", espn.SlotWR, 12),
		mk("WR Flex", "WR", espn.SlotFlex, 9),
		mk("WR Bench", "WR", espn.SlotBench, 8),
		mk("My TE", "TE", espn.SlotTE, 8),
		mk("TE Bench", "TE", espn.SlotBench, 3),
		mk("QB Bench", "QB", espn.SlotBench, 12),
		mk("Bad Kicker", "K", espn.SlotK, 5),
		mk("My DST", "D/ST", espn.SlotDST, 7),
		mk("WR Scrub", "WR", espn.SlotBench, 4),
		mk("RB Scrub", "RB", espn.SlotBench, 3),
		mk("DST Bench", "D/ST", espn.SlotBench, 4),
	)
	opp := team(2, "Rival",
		mk("Opp QB", "QB", espn.SlotQB, 21),
		mk("Opp RB1", "RB", espn.SlotRB, 15),
		mk("Opp RB2", "RB", espn.SlotRB, 12),
		mk("Opp WR1", "WR", espn.SlotWR, 16),
		mk("Opp WR2", "WR", espn.SlotWR, 14),
		mk("Opp WR3", "WR", espn.SlotFlex, 13),
		mk("Opp WR4", "WR", espn.SlotBench, 12),
		mk("Opp TE", "TE", espn.SlotTE, 9),
		mk("Opp K", "K", espn.SlotK, 8),
		mk("Opp DST", "D/ST", espn.SlotDST, 8),
	)
	pool := []*espn.Player{
		freeAgent(mk("Waiver Wire RB", "RB", espn.SlotBench, 14), espn.StatusFreeAgent),
		freeAgent(mk("Meh WR", "WR", espn.SlotBench, 5), espn.StatusFreeAgent),
		freeAgent(mk("Good Kicker", "K", espn.SlotBench, 10), espn.StatusWaivers),
	}
	mkLeague := func() *espn.League {
		return &espn.League{
			ID: 1, Name: "Test", Season: season, CurrentWeek: week, CurrentMatchupPeriod: week, FinalWeek: 17,
			LineupSlotCounts: espn.StandardSlots(), UsesFAAB: true, FAABBudget: 100,
			Teams:    []*espn.Team{me.Clone(), opp.Clone()},
			Schedule: []espn.Matchup{{Period: week, HomeTeamID: 1, AwayTeamID: 2}},
			ByeWeeks: map[int]int{},
		}
	}
	return &fakeESPN{league: mkLeague, pool: pool}, me
}

func testAgent(f *fakeESPN) *Agent {
	cfg := config.Default()
	cfg.LeagueID, cfg.TeamID, cfg.SWID = 1, 1, secrets.New("{ME}")
	a := New(f, cfg)
	a.Now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	return a
}

func TestPlanHandlesInjuryPickupsAndLineup(t *testing.T) {
	f, me := scenario()
	a := testAgent(f)
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, act := range p.Actions {
		kinds = append(kinds, string(act.Kind)+": "+act.Summary)
	}
	t.Logf("actions:\n  %s", strings.Join(kinds, "\n  "))

	if len(p.Actions) < 3 {
		t.Fatalf("expected IR stash + RB add + K claim, got %d actions", len(p.Actions))
	}
	if p.Actions[0].Kind != ActIRStash || !strings.Contains(p.Actions[0].Summary, "Hurt Starter") {
		t.Errorf("first action should stash the injured RB on IR: %+v", p.Actions[0])
	}

	var sawRB, sawK bool
	for _, act := range p.Actions[1:] {
		switch {
		case act.Kind == ActAdd && strings.Contains(act.Summary, "Waiver Wire RB"):
			sawRB = true
			if len(act.Txn.Items) != 1 {
				t.Errorf("RB add should use the IR-freed spot without a drop: %+v", act.Txn.Items)
			}
			if !strings.Contains(act.Reason, "replaces Hurt Starter") {
				t.Errorf("reason should mention the IR replacement: %s", act.Reason)
			}
		case act.Kind == ActClaim && strings.Contains(act.Summary, "Good Kicker"):
			sawK = true
			// Kickers are streamable: keep the bid small.
			if act.Txn.BidAmount == nil || *act.Txn.BidAmount <= 0 || *act.Txn.BidAmount > 10 {
				t.Errorf("bad FAAB bid: %v", act.Txn.BidAmount)
			}
			if !strings.Contains(act.Summary, "drop") {
				t.Errorf("full roster: kicker claim needs a drop: %s", act.Summary)
			}
		}
	}
	if !sawRB || !sawK {
		t.Errorf("missing pickups: rb=%v k=%v", sawRB, sawK)
	}

	// Lineup: new RB starts; injured RB is on IR; pending kicker can't start yet.
	rb := findByName(p.Roster.Roster, "Waiver Wire RB")
	if rb == nil || !espn.IsStartingSlot(p.Lineup.Slots[rb.ID]) {
		t.Errorf("new RB should start")
	}
	if s := p.Lineup.Slots[me.Roster[1].ID]; s != espn.SlotIR {
		t.Errorf("injured RB slot = %s, want IR", espn.SlotName(s))
	}
	if k := findByName(p.Roster.Roster, "Bad Kicker"); k == nil || p.Lineup.Slots[k.ID] != espn.SlotK {
		t.Errorf("current kicker should keep starting until the claim clears")
	}
	if p.Matchup == nil || p.Matchup.Opponent == nil || p.Matchup.WinProb <= 0 || p.Matchup.WinProb >= 1 {
		t.Errorf("bad matchup report: %+v", p.Matchup)
	}
	for _, tr := range p.Trades {
		if tr.MyGain < a.Cfg.MinTradeGain || tr.TheirGain < -a.Cfg.TradeTolerance {
			t.Errorf("trade outside thresholds: %+v", tr)
		}
	}
}

func TestProtectedPlayersAreNeverDropped(t *testing.T) {
	f, _ := scenario()
	a := testAgent(f)
	a.Cfg.ProtectedPlayers = []string{"bad kicker"}
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range p.Actions {
		for _, it := range act.Txn.Items {
			if it.Type == espn.ItemDrop && findByID(p.Me.Roster, it.PlayerID).Name == "Bad Kicker" {
				t.Errorf("dropped a protected player: %s", act.Summary)
			}
		}
	}
}

func TestHealthyPlayerOnIRIsActivated(t *testing.T) {
	f, me := scenario()
	// Fill IR with a now-healthy player; roster is full so someone must go.
	back := mk("Back From IR", "WR", espn.SlotIR, 11)
	back.OnTeamID = 1
	me.Roster = append(me.Roster, back)
	a := testAgent(f)
	a.Cfg.MaxAcquisitionsPerRun = 0
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Actions) < 2 || p.Actions[0].Kind != ActDrop || p.Actions[1].Kind != ActIRActivate {
		t.Fatalf("expected drop then activation, got %+v", p.Actions)
	}
	if p.Actions[0].Txn.Items[0].PlayerID == back.ID {
		t.Errorf("shouldn't drop the player being activated")
	}
	// After activation the IR slot is free again, so the injured RB goes in.
	if len(p.Actions) < 3 || p.Actions[2].Kind != ActIRStash {
		t.Errorf("expected injured RB to be stashed after activation: %+v", p.Actions)
	}
}

func TestExecuteOrdersRosterMovesBeforeLineup(t *testing.T) {
	f, _ := scenario()
	a := testAgent(f)
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Execute(context.Background(), p, Scope{Roster: true, Lineup: true}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(f.submitted) != len(p.Actions)+1 {
		t.Fatalf("submitted %d txns, want %d roster + 1 lineup", len(f.submitted), len(p.Actions))
	}
	if last := f.submitted[len(f.submitted)-1]; last.Type != espn.TxnRoster || last.Items[0].Type != espn.ItemLineup {
		t.Errorf("lineup should be submitted last: %+v", last)
	}
	if f.fetches != 2 {
		t.Errorf("lineup should be re-planned from fresh data after roster changes (fetches=%d)", f.fetches)
	}
}

func findByName(ps []*espn.Player, name string) *espn.Player {
	for _, p := range ps {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func findByID(ps []*espn.Player, id int) *espn.Player {
	for _, p := range ps {
		if p.ID == id {
			return p
		}
	}
	return &espn.Player{}
}

func TestUnderdogStartsHighVariancePlayer(t *testing.T) {
	nextID = 500
	steady := mk("Steady WR", "WR", espn.SlotWR, 10)
	boom := mk("Boom Bust WR", "WR", espn.SlotBench, 9.5)
	boom.Stats = nil
	for w, act := range []float64{19, 0, 19, 0, 9.5} {
		boom.Stats = append(boom.Stats,
			espn.StatLine{Season: season, Week: w + 1, Weekly: true, Projected: true, Points: 9.5},
			espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: act})
	}
	boom.Stats = append(boom.Stats, espn.StatLine{Season: season, Week: week, Weekly: true, Projected: true, Points: 9.5})
	me := team(1, "Me", mk("QB", "QB", espn.SlotQB, 15), steady, boom)
	opp := team(2, "Juggernaut", mk("Opp QB", "QB", espn.SlotQB, 40), mk("Opp WR", "WR", espn.SlotWR, 20))
	f := &fakeESPN{league: func() *espn.League {
		return &espn.League{
			ID: 1, Season: season, CurrentWeek: week, CurrentMatchupPeriod: week, FinalWeek: 17,
			LineupSlotCounts: map[int]int{espn.SlotQB: 1, espn.SlotWR: 1, espn.SlotBench: 3},
			Teams:            []*espn.Team{me.Clone(), opp.Clone()},
			Schedule:         []espn.Matchup{{Period: week, HomeTeamID: 1, AwayTeamID: 2}},
		}
	}}
	a := testAgent(f)
	p, err := a.PlanLineup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Matchup.Lambda <= 0 {
		t.Fatalf("underdog should seek variance, lambda=%v (%s)", p.Matchup.Lambda, p.Matchup.Strategy)
	}
	if p.Lineup.Slots[boom.ID] != espn.SlotWR {
		t.Errorf("boom/bust WR should start as an underdog")
	}
	if p.Matchup.WinProb <= p.Matchup.BaselineWinProb {
		t.Errorf("win prob %.3f should beat baseline %.3f", p.Matchup.WinProb, p.Matchup.BaselineWinProb)
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// A pickup that projects fewer points this week than his drop is only
// made as a stash: the dropped player isn't in this week's lineup, so this
// week's score can't fall.
func TestPickupProjectsLowerOnlyAsAStash(t *testing.T) {
	f, _ := scenario()
	stash := freeAgent(mk("Bye Stash WR", "WR", espn.SlotBench, 16), espn.StatusFreeAgent)
	f.pool = append(f.pool, stash)
	base := f.league
	f.league = func() *espn.League {
		l := base()
		l.ByeWeeks[stash.ProTeamID] = week // great rest of season, 0 points this week
		return l
	}
	plan := func(a *Agent) (*Plan, *Action) {
		p, err := a.Plan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for i, act := range p.Actions {
			if strings.Contains(act.Summary, "Bye Stash WR") {
				return p, &p.Actions[i]
			}
		}
		return p, nil
	}

	p, act := plan(testAgent(f))
	if act == nil {
		t.Fatal("no stash of a 16-point WR on bye for a bench player")
	}
	if !strings.HasPrefix(act.Summary, "Stash Bye Stash WR") || !strings.Contains(act.Reason, "this week's projection doesn't change") {
		t.Errorf("stash isn't labeled and explained: %s — %s", act.Summary, act.Reason)
	}
	for _, x := range act.Txn.Items {
		if x.Type == espn.ItemDrop {
			if d := findByID(p.Me.Roster, x.PlayerID); d == nil || d.LineupSlot != espn.SlotBench {
				t.Errorf("stash drops a player in this week's lineup")
			}
		}
	}

	// With every bench player protected, a stash would have to drop a
	// starter, and this week's score would fall.
	a := testAgent(f)
	a.Cfg.ProtectedPlayers = []string{"RB Bench", "WR Bench", "TE Bench", "QB Bench", "WR Scrub", "RB Scrub", "DST Bench"}
	if _, act := plan(a); act != nil {
		t.Errorf("stashed by dropping a starter: %s", act.Summary)
	}

	a = testAgent(f)
	a.Cfg.PlanStashes = false
	if _, act := plan(a); act != nil {
		t.Errorf("stashed with plan_stashes off: %s", act.Summary)
	}
	a.Cfg.AllowLowerWeekPickups = true
	if _, act := plan(a); act == nil {
		t.Error("allow_lower_week_pickups should permit a rest-of-season pickup")
	}
}

func TestUnderdogDoesNotStartAFarLowerProjection(t *testing.T) {
	nextID = 600
	starter := mk("Drake Starter", "QB", espn.SlotQB, 19)
	boom := mk("Boom Backup", "QB", espn.SlotBench, 14)
	boom.Stats = nil
	for w, act := range []float64{40, 0, 35, 0, 2} {
		boom.Stats = append(boom.Stats,
			espn.StatLine{Season: season, Week: w + 1, Weekly: true, Projected: true, Points: 14},
			espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: act})
	}
	boom.Stats = append(boom.Stats, espn.StatLine{Season: season, Week: week, Weekly: true, Projected: true, Points: 14})
	me := team(1, "Me", starter, boom, mk("WR", "WR", espn.SlotWR, 10))
	opp := team(2, "Juggernaut", mk("Opp QB", "QB", espn.SlotQB, 45), mk("Opp WR", "WR", espn.SlotWR, 25))
	f := &fakeESPN{league: func() *espn.League {
		return &espn.League{
			ID: 1, Season: season, CurrentWeek: week, CurrentMatchupPeriod: week, FinalWeek: 17,
			LineupSlotCounts: map[int]int{espn.SlotQB: 1, espn.SlotWR: 1, espn.SlotBench: 3},
			Teams:            []*espn.Team{me.Clone(), opp.Clone()},
			Schedule:         []espn.Matchup{{Period: week, HomeTeamID: 1, AwayTeamID: 2}},
		}
	}}
	p, err := testAgent(f).PlanLineup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Lineup.Slots[starter.ID] != espn.SlotQB {
		t.Errorf("benched a 19-point QB for a 14-point boom/bust backup (lambda %v)", p.Matchup.Lambda)
	}
}

// A free agent whose game has already been played can't score for us this
// week: his points don't count against the player he'd replace, and the
// reason doesn't present them as a projection.
func TestPickupAfterHisGameWasPlayed(t *testing.T) {
	f, _ := scenario()
	played := freeAgent(mk("Already Played QB", "QB", espn.SlotBench, 30), espn.StatusFreeAgent)
	played.Locked, played.GameLeft = true, 0
	played.Stats = append(played.Stats, espn.StatLine{Season: season, Week: week, Weekly: true, Points: 30})
	f.pool = append(f.pool, played)

	// Into an open roster spot it's still worth it for the rest of the season.
	p, err := testAgent(f).Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, act := range p.Actions {
		if strings.Contains(act.Summary, "Already Played QB") {
			found = true
			if !strings.Contains(act.Reason, "already started") || strings.Contains(act.Reason, "this week (ESPN") {
				t.Errorf("reason: %s", act.Reason)
			}
		}
	}
	if !found {
		t.Fatal("rest-of-season upgrade not proposed")
	}

	// Replacing someone who can still play this week: he projects lower.
	a := testAgent(f)
	l := f.league()
	me := l.Teams[0]
	s := a.newState(l, me)
	var drop *espn.Player
	for _, pl := range me.Roster {
		if pl.Name == "QB Bench" {
			drop = pl
		}
	}
	c := *played
	c.OnTeamID, c.Status = me.ID, espn.StatusOnTeam
	if !s.projectsLower(&c, drop) {
		t.Error("a player whose game is over should count as 0 this week")
	}
	c.Locked = false
	if s.projectsLower(&c, drop) {
		t.Error("before his game, his projection counts")
	}
	c.Locked, c.Status = true, pendingClaim
	if s.projectsLower(&c, drop) {
		t.Error("a waiver claim leaves this week unchanged (the drop stays until it clears)")
	}
}
