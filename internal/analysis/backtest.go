package analysis

import (
	"math"
	"math/rand/v2"
	"sort"
)

// Backtesting projection signals. Each signal scales ESPN's projection by
// (1 + b·signal), with b fitted per position on past weeks, and the result
// is scored on a week it wasn't fitted on. A signal is only worth using if
// it beats ESPN's projection on its own, by more than chance.

// BTGame is one NFL game with its betting line.
type BTGame struct {
	Week, Home, Away  int
	OverUnder, Spread float64 // spread from the home side (-3 = home favored by 3)
}

// BTPlayer is one player's weekly projections and actuals.
type BTPlayer struct {
	Position  string
	Team      int
	Projected map[int]float64 // week -> ESPN projection
	Actual    map[int]float64 // week -> points (missing = didn't play)
}

// SignalResult is how one signal did against ESPN's projections.
type SignalResult struct {
	Signal   string
	Weeks    []int // weeks scored
	N        int   // player-weeks scored
	ESPN     float64
	Adjusted float64 // mean absolute error with the signal
	Gain     float64 // ESPN - Adjusted: positive = the signal helped
	Low      float64 // 95% interval for Gain (paired bootstrap)
	High     float64
	Coef     map[string]float64 // fitted on all weeks
}

// Beats reports whether the signal beat ESPN by more than chance.
func (r SignalResult) Beats() bool { return r.N > 0 && r.Low > 0 }

const (
	// btRidge pulls fitted coefficients toward 0 (no change to ESPN).
	btRidge = 50.0
	// dvpPhantomWeeks shrinks a defense's record toward league average.
	dvpPhantomWeeks = 3.0
	btBootstrap     = 4000
)

// Signal names.
const (
	SignalVegas = "vegas" // team's implied points vs the week's average
	SignalDvP   = "dvp"   // opponent's points allowed to the position vs average
)

type btRow struct {
	pos       string
	week      int
	proj, act float64
	sig       map[string]float64
}

type teamWeek struct{ team, week int }

type btSide struct {
	opp         int
	imp, oppImp float64
}

// Backtest scores the signals over weeks 1..through.
func Backtest(players []BTPlayer, games []BTGame, through int) []SignalResult {
	tw := map[teamWeek]btSide{}
	impByWeek := map[int][]float64{}
	for _, g := range games {
		if g.Week > through {
			continue
		}
		home := g.OverUnder/2 - g.Spread/2
		away := g.OverUnder - home
		tw[teamWeek{g.Home, g.Week}] = btSide{opp: g.Away, imp: home, oppImp: away}
		tw[teamWeek{g.Away, g.Week}] = btSide{opp: g.Home, imp: away, oppImp: home}
		impByWeek[g.Week] = append(impByWeek[g.Week], home, away)
	}

	// Points each defense allowed to each position, by week.
	allowed := map[[3]int]float64{} // {defense, position index, week}
	posIndex := map[string]int{}
	for _, p := range players {
		if _, ok := posIndex[p.Position]; !ok {
			posIndex[p.Position] = len(posIndex)
		}
		for w := 1; w <= through; w++ {
			side, ok := tw[teamWeek{p.Team, w}]
			if _, proj := p.Projected[w]; ok && proj {
				allowed[[3]int{side.opp, posIndex[p.Position], w}] += p.Actual[w]
			}
		}
	}
	teamsInWeek := map[int][]int{}
	for k := range tw {
		teamsInWeek[k.week] = append(teamsInWeek[k.week], k.team)
	}
	dvp := func(opp int, pos string, week int) float64 {
		var mine, lg []float64
		for w := 1; w < week; w++ {
			if _, ok := tw[teamWeek{opp, w}]; ok {
				mine = append(mine, allowed[[3]int{opp, posIndex[pos], w}])
			}
			var all []float64
			for _, t := range teamsInWeek[w] {
				all = append(all, allowed[[3]int{t, posIndex[pos], w}])
			}
			lg = append(lg, mean(all))
		}
		avg := mean(lg)
		if len(mine) == 0 || avg <= 0 {
			return 0
		}
		return (sum(mine)+dvpPhantomWeeks*avg)/(float64(len(mine))+dvpPhantomWeeks)/avg - 1
	}

	// The player-weeks the agent cares about: anyone ESPN gives a real
	// projection, and every kicker and defense.
	var sample []btRow
	for _, p := range players {
		for w := 1; w <= through; w++ {
			proj, ok := p.Projected[w]
			side, played := tw[teamWeek{p.Team, w}]
			if !ok || !played || (proj < 3 && p.Position != "K" && p.Position != "D/ST") {
				continue
			}
			imp := side.imp
			if p.Position == "D/ST" {
				imp = side.oppImp // a defense scores when the other team doesn't
			}
			sample = append(sample, btRow{pos: p.Position, week: w, proj: proj, act: p.Actual[w], sig: map[string]float64{
				SignalVegas: imp/mean(impByWeek[w]) - 1,
				SignalDvP:   dvp(side.opp, p.Position, w),
			}})
		}
	}

	var out []SignalResult
	for _, s := range []struct {
		name  string
		sigs  []string
		first int // first week a signal exists for
	}{
		{SignalVegas, []string{SignalVegas}, 1},
		{SignalDvP, []string{SignalDvP}, 2},
		{SignalVegas + "+" + SignalDvP, []string{SignalVegas, SignalDvP}, 2},
	} {
		out = append(out, evaluate(sample, s.name, s.sigs, s.first, through))
	}
	return out
}

// evaluate fits on all weeks but one and scores that week, for each week.
func evaluate(sample []btRow, name string, sigs []string, first, through int) SignalResult {
	res := SignalResult{Signal: name}
	in := func(r btRow) bool { return r.week >= first && r.week <= through }
	var espnErr, adjErr []float64
	for w := first; w <= through; w++ {
		var train []btRow
		for _, r := range sample {
			if in(r) && r.week != w {
				train = append(train, r)
			}
		}
		if len(train) == 0 {
			continue
		}
		coef := map[string]map[string]float64{}
		for _, s := range sigs {
			coef[s] = fitSignal(train, s)
		}
		scored := false
		for _, r := range sample {
			if r.week != w {
				continue
			}
			scale := 1.0
			for _, s := range sigs {
				scale += coef[s][r.pos] * r.sig[s]
			}
			espnErr = append(espnErr, math.Abs(r.act-r.proj))
			adjErr = append(adjErr, math.Abs(r.act-r.proj*scale))
			scored = true
		}
		if scored {
			res.Weeks = append(res.Weeks, w)
		}
	}
	res.N = len(espnErr)
	if res.N == 0 {
		return res
	}
	res.ESPN, res.Adjusted = mean(espnErr), mean(adjErr)
	res.Gain = res.ESPN - res.Adjusted
	res.Low, res.High = pairedInterval(espnErr, adjErr)
	var all []btRow
	for _, r := range sample {
		if in(r) {
			all = append(all, r)
		}
	}
	res.Coef = map[string]float64{}
	for _, s := range sigs {
		for pos, b := range fitSignal(all, s) {
			key := pos
			if len(sigs) > 1 {
				key = s + " " + pos
			}
			res.Coef[key] = b
		}
	}
	return res
}

// fitSignal finds, per position, the b minimizing the squared error of
// proj·(1 + b·signal) against actual points, with a ridge toward 0.
func fitSignal(rows []btRow, sig string) map[string]float64 {
	num, den := map[string]float64{}, map[string]float64{}
	for _, r := range rows {
		x := r.proj * r.sig[sig]
		num[r.pos] += x * (r.act - r.proj)
		den[r.pos] += x * x
	}
	out := map[string]float64{}
	for pos := range num {
		out[pos] = num[pos] / (den[pos] + btRidge)
	}
	return out
}

// pairedInterval is a 95% bootstrap interval for mean(a - b).
func pairedInterval(a, b []float64) (lo, hi float64) {
	d := make([]float64, len(a))
	for i := range a {
		d[i] = a[i] - b[i]
	}
	rng := rand.New(rand.NewPCG(1, 2)) // fixed, so results are repeatable
	means := make([]float64, btBootstrap)
	for i := range means {
		s := 0.0
		for range d {
			s += d[rng.IntN(len(d))]
		}
		means[i] = s / float64(len(d))
	}
	sort.Float64s(means)
	return means[int(0.025*btBootstrap)], means[int(0.975*btBootstrap)]
}

func sum(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}
