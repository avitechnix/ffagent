package analysis

import (
	"math/rand/v2"
	"testing"
)

// btLeague builds five weeks of 16 games and 320 players whose actual points
// are ESPN's projection scaled by how much `truth` of the Vegas signal
// ESPN missed, plus noise.
func btLeague(truth float64) ([]BTPlayer, []BTGame) {
	rng := rand.New(rand.NewPCG(7, 7))
	var games []BTGame
	for w := 1; w <= 5; w++ {
		for g := 0; g < 16; g++ {
			ou := 38 + rng.Float64()*14
			games = append(games, BTGame{Week: w, Home: 2*g + 1, Away: 2*g + 2, OverUnder: ou, Spread: rng.Float64()*14 - 7})
		}
	}
	implied := func(team, w int) float64 {
		for _, g := range games {
			if g.Week == w && (g.Home == team || g.Away == team) {
				h := g.OverUnder/2 - g.Spread/2
				if g.Home == team {
					return h
				}
				return g.OverUnder - h
			}
		}
		return 0
	}
	var players []BTPlayer
	for i := 0; i < 320; i++ {
		p := BTPlayer{Position: []string{"QB", "RB", "WR", "TE"}[i%4], Team: i%32 + 1, Projected: map[int]float64{}, Actual: map[int]float64{}}
		base := 6 + rng.Float64()*14
		for w := 1; w <= 5; w++ {
			v := implied(p.Team, w)/22.5 - 1
			p.Projected[w] = base
			p.Actual[w] = max(0, base*(1+truth*v)+rng.NormFloat64()*4)
		}
		players = append(players, p)
	}
	return players, games
}

func TestBacktestFindsARealSignal(t *testing.T) {
	players, games := btLeague(1.0)
	r := Backtest(players, games, 5)[0]
	if r.Signal != SignalVegas || r.N != 1600 || !r.Beats() {
		t.Fatalf("missed a real signal: %+v", r)
	}
	if b := r.Coef["RB"]; b < 0.6 || b > 1.3 {
		t.Errorf("fitted coefficient %.2f, want about 1", b)
	}
}

func TestBacktestDoesNotInventASignal(t *testing.T) {
	players, games := btLeague(0)
	for _, r := range Backtest(players, games, 5) {
		if r.Beats() {
			t.Errorf("%s 'beat' ESPN on pure noise: %+v", r.Signal, r)
		}
		if r.Low > r.Gain || r.High < r.Gain {
			t.Errorf("%s: interval [%.3f, %.3f] doesn't contain %.3f", r.Signal, r.Low, r.High, r.Gain)
		}
	}
}

func TestBacktestDefenseVsPosition(t *testing.T) {
	// Half the defenses give up 50% more to RBs every week, and ESPN never
	// adjusts for it. The signal only exists from week 2 on.
	players, games := btLeague(0)
	for i := range players {
		p := &players[i]
		if p.Position != "RB" {
			continue
		}
		for w := 1; w <= 5; w++ {
			for _, g := range games {
				opp := g.Home
				if g.Home == p.Team {
					opp = g.Away
				}
				if g.Week == w && (g.Home == p.Team || g.Away == p.Team) && opp%4 < 2 {
					p.Actual[w] *= 1.5
				}
			}
		}
	}
	r := Backtest(players, games, 5)[1]
	if r.Signal != SignalDvP || r.Weeks[0] != 2 || !r.Beats() {
		t.Fatalf("defense vs position: %+v", r)
	}
}
