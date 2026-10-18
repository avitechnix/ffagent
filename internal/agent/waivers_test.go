package agent

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
)

// futureState is a planning state on the full-size league.
func futureState(t *testing.T) (*state, *espn.League) {
	t.Helper()
	f := fullLeague()
	l, _ := f.FetchLeague(context.Background())
	a := testAgent(f)
	s := a.newState(l, l.Teams[0])
	return s, l
}

// Reusing a solved week's lineup must give exactly what optimizing every
// week would.
func TestFutureLineupsMatchSolvingEveryWeek(t *testing.T) {
	s, l := futureState(t)
	rng := rand.New(rand.NewPCG(9, 9))
	for trial := 0; trial < 40; trial++ {
		team := l.Teams[trial%len(l.Teams)]
		roster := append([]*espn.Player(nil), team.Roster...)
		// Some players hurt, to vary who's out when.
		for _, p := range roster {
			if rng.IntN(8) == 0 {
				c := *p
				c.InjuryStatus = []string{espn.InjuryReserve, espn.InjurySuspension, espn.InjuryOut}[rng.IntN(3)]
				s.values[c.ID] = analysis.Value(l, &c, s.a.Opts)
			}
		}
		future, _ := s.futureRoster(roster)
		got, _ := s.futureLineups(future)
		for i, fw := range s.futureWeeks() {
			want := lineup.Optimize(l, future, func(p *espn.Player) float64 { return s.val(p).FutureWeek(l, fw.week) }).Total
			if math.Abs(got[i]-want) > 1e-9 {
				t.Fatalf("trial %d week %d: %.3f, want %.3f", trial, fw.week, got[i], want)
			}
		}
		s.weekly = map[[2]int]float64{}
	}
}

func TestFutureWeekValues(t *testing.T) {
	s, l := futureState(t)
	p := *l.Teams[0].Roster[0] // a QB
	team := p.ProTeamID
	bye := 0
	for w := 7; w <= 17; w++ {
		if l.OnBye(team, w) {
			bye = w
		}
	}
	v := analysis.Value(l, &p, s.a.Opts)
	if bye == 0 || v.FutureWeek(l, bye) != 0 || v.FutureWeek(l, week) != 0 || v.FutureWeek(l, 18) != 0 {
		t.Fatalf("bye week %d: %.1f", bye, v.FutureWeek(l, bye))
	}
	if got := v.FutureWeek(l, 16); math.Abs(got-v.PerGame) > 1e-9 {
		t.Errorf("healthy: %.2f, want %.2f a game", got, v.PerGame)
	}

	p.InjuryStatus = espn.InjuryReserve
	v = analysis.Value(l, &p, s.a.Opts)
	sum := 0.0
	for w := week + 1; w <= l.FinalWeek; w++ {
		x := v.FutureWeek(l, w)
		if w <= week+3 && x != 0 {
			t.Errorf("on IR: %.1f in week %d, before he can return", x, w)
		}
		sum += x
	}
	if math.Abs(sum-v.ROSValue) > 1e-6 {
		t.Errorf("IR weeks add up to %.2f, rest of season is %.2f", sum, v.ROSValue)
	}
}

// Two quarterbacks with the same bye leave that week empty; the agent
// should prefer a backup whose bye is different, all else equal.
func TestByeCoverage(t *testing.T) {
	s, l := futureState(t)
	me := l.Teams[0]
	qb := me.Roster[0]
	var backup *espn.Player
	for _, p := range me.Roster {
		if p.Position == "QB" && p.ID != qb.ID {
			backup = p
		}
	}
	same, other := *backup, *backup
	same.ProTeamID = qb.ProTeamID // same bye as the starter
	other.ProTeamID = qb.ProTeamID%32 + 1
	other.ID = backup.ID + 100000 // a different player
	if byeOf(l, other.ProTeamID) == byeOf(l, qb.ProTeamID) {
		t.Fatal("test setup: byes should differ")
	}
	with := func(b *espn.Player) []*espn.Player {
		var r []*espn.Player
		for _, p := range me.Roster {
			if p.ID != backup.ID {
				r = append(r, p)
			}
		}
		s.values[b.ID] = analysis.Value(l, b, s.a.Opts)
		s.weekly = map[[2]int]float64{}
		return append(r, b)
	}
	sameV := s.teamValue(with(&same), 0)
	otherV := s.teamValue(with(&other), 0)
	if otherV-sameV < 0.5*s.val(&other).PerGame {
		t.Errorf("a backup with a different bye is worth %.1f more, want about one game of his", otherV-sameV)
	}
	note := s.byeCover(with(&same), with(&other), &other)
	if !strings.Contains(note, qb.Name) || !strings.Contains(note, "on bye") {
		t.Errorf("bye note: %q", note)
	}
}

func TestPlayoffWeeksCountMore(t *testing.T) {
	s, l := futureState(t)
	if !l.PlayoffWeek(15) || l.PlayoffWeek(14) {
		t.Fatal("playoffs should be weeks 15-17")
	}
	w := map[int]float64{}
	for _, fw := range s.futureWeeks() {
		w[fw.week] = fw.weight
	}
	if w[14] != 1 || w[15] != s.a.Cfg.PlayoffWeekWeight || w[17] != s.a.Cfg.PlayoffWeekWeight || len(w) != 11 {
		t.Errorf("weights %v", w)
	}
}

func TestFAABBidFromLeagueHistory(t *testing.T) {
	s, l := futureState(t)
	me := l.Teams[0]
	c := *freeAgent(mk("Claim RB", "RB", espn.SlotBench, 10), espn.StatusWaivers)
	c.PercentChange = 0 // median demand

	// Not enough history: the bid is what he's worth to us.
	worthOnly, note := s.faabBid(&c, 60, 100, me)
	if note != "" || worthOnly != 35 {
		t.Fatalf("no history: $%d %q", worthOnly, note)
	}

	for i, amt := range []int{2, 4, 5, 7, 9, 12, 30} {
		s.a.bids = append(s.a.bids, espn.WaiverBid{Week: 3, PlayerID: me.Roster[2+i%3].ID, Amount: amt, Won: true})
	}
	s.a.bids = append(s.a.bids, espn.WaiverBid{Week: 3, PlayerID: me.Roster[2].ID, Amount: 50, Won: false}) // losing bids don't set the price
	bid, note := s.faabBid(&c, 60, 100, me)
	if bid != 8 || !strings.Contains(note, "median $7") {
		t.Errorf("median-demand player: $%d (%s), want $8", bid, note)
	}
	c.PercentChange = 15 // everyone's adding him
	if bid, _ := s.faabBid(&c, 60, 100, me); bid != 31 {
		t.Errorf("hot player: $%d, want $31", bid)
	}
	if bid, note := s.faabBid(&c, 10, 100, me); bid != 10 || !strings.Contains(note, "may go elsewhere") {
		t.Errorf("worth less than the going rate: $%d (%s)", bid, note)
	}
	for _, t := range l.Teams[1:] {
		t.FAABRemaining = 6
	}
	if bid, _ := s.faabBid(&c, 60, 100, me); bid != 7 {
		t.Errorf("rivals have $6 left: bid $%d, want $7", bid)
	}
}

func byeOf(l *espn.League, team int) int {
	for w := 1; w <= 18; w++ {
		if l.OnBye(team, w) {
			return w
		}
	}
	return 0
}
