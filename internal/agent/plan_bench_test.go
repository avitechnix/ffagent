package agent

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/espn"
)

// fullLeague is a full-size league: 10 teams of 16, 150 free agents, a
// real-style schedule with byes in weeks 5-14, and fantasy playoffs in
// weeks 15-17.
func fullLeague() *fakeESPN {
	nextID = 5000
	rng := rand.New(rand.NewPCG(3, 3))
	positions := []string{"QB", "QB", "RB", "RB", "RB", "RB", "RB", "WR", "WR", "WR", "WR", "WR", "TE", "TE", "K", "D/ST"}
	slots := []int{espn.SlotQB, espn.SlotBench, espn.SlotRB, espn.SlotRB, espn.SlotBench, espn.SlotBench, espn.SlotFlex,
		espn.SlotWR, espn.SlotWR, espn.SlotBench, espn.SlotBench, espn.SlotBench, espn.SlotTE, espn.SlotBench, espn.SlotK, espn.SlotDST}
	var teams []*espn.Team
	for t := 1; t <= 10; t++ {
		var roster []*espn.Player
		for i, pos := range positions {
			roster = append(roster, mk(fmt.Sprintf("T%d %s%d", t, pos, i), pos, slots[i], 4+rng.Float64()*18))
		}
		teams = append(teams, team(t, fmt.Sprintf("Team %d", t), roster...))
	}
	var pool []*espn.Player
	for i := 0; i < 150; i++ {
		status := espn.StatusFreeAgent
		if i%3 == 0 {
			status = espn.StatusWaivers
		}
		pool = append(pool, freeAgent(mk(fmt.Sprintf("FA %d", i), positions[i%len(positions)], espn.SlotBench, 2+rng.Float64()*10), status))
	}
	games := map[int]map[int]espn.ProGame{}
	kick := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	for team := 1; team <= 32; team++ {
		games[team] = map[int]espn.ProGame{}
		bye := 5 + team%10
		for w := 1; w <= 18; w++ {
			if w != bye {
				games[team][w] = espn.ProGame{Week: w, Start: kick.AddDate(0, 0, 7*(w-1))}
			}
		}
	}
	periods := map[int][]int{}
	for w := 1; w <= 17; w++ {
		periods[w] = []int{w}
	}
	var sched []espn.Matchup
	for i := 0; i < 10; i += 2 {
		sched = append(sched, espn.Matchup{Period: week, HomeTeamID: i + 1, AwayTeamID: i + 2})
	}
	return &fakeESPN{pool: pool, league: func() *espn.League {
		l := &espn.League{
			ID: 1, Season: season, CurrentWeek: week, CurrentMatchupPeriod: week, FinalWeek: 17,
			RegularSeason: 14, MatchupPeriods: periods, ProGames: games, ByeWeeks: map[int]int{},
			LineupSlotCounts: espn.StandardSlots(), UsesFAAB: true, FAABBudget: 100, Schedule: sched,
		}
		for _, t := range teams {
			l.Teams = append(l.Teams, t.Clone())
		}
		return l
	}}
}

func BenchmarkPlan(b *testing.B) {
	f := fullLeague()
	a := testAgent(f)
	a.Cfg.ProposeTrades = true
	for b.Loop() {
		if _, err := a.Plan(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
