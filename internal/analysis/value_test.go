package analysis

import (
	"math"
	"strings"
	"testing"

	"github.com/avitechnix/ffagent/internal/espn"
)

const season = 2026

func league(week int) *espn.League {
	return &espn.League{Season: season, CurrentWeek: week, FinalWeek: 17, ByeWeeks: map[int]int{1: 9}}
}

// withHistory builds a player with weekly projections and actuals for
// weeks 1..len(actual), plus a projection for the next week.
func withHistory(pos string, proj, actual []float64, next float64) *espn.Player {
	p := &espn.Player{ID: 1, Name: "Test", Position: pos, ProTeamID: 1, InjuryStatus: espn.InjuryActive}
	for i := range actual {
		w := i + 1
		p.Stats = append(p.Stats,
			espn.StatLine{Season: season, Week: w, Weekly: true, Projected: true, Points: proj[i]},
			espn.StatLine{Season: season, Week: w, Weekly: true, Points: actual[i]})
	}
	p.Stats = append(p.Stats,
		espn.StatLine{Season: season, Week: len(actual) + 1, Weekly: true, Projected: true, Points: next},
		espn.StatLine{Season: season, Projected: true, Points: 17 * next, Average: next})
	return p
}

func TestOutperformerGetsBoostedButShrunk(t *testing.T) {
	// Projected 10 every week, scored 15: 150% accuracy over 5 weeks.
	p := withHistory("WR", []float64{10, 10, 10, 10, 10}, []float64{15, 15, 15, 15, 15}, 10)
	v := Value(league(6), p, DefaultOptions())
	if math.Abs(v.Accuracy-1.5) > 1e-9 {
		t.Fatalf("accuracy %.3f, want 1.5", v.Accuracy)
	}
	if v.AccuracyFactor <= 1 || v.AccuracyFactor > 1.2 {
		t.Errorf("accuracy factor %.3f should be in (1, 1.2]", v.AccuracyFactor)
	}
	if v.WeekProjection <= v.ESPNWeekProjection {
		t.Errorf("adjusted %.1f should beat ESPN %.1f", v.WeekProjection, v.ESPNWeekProjection)
	}
	if v.PerGame <= 10 {
		t.Errorf("per-game %.1f should lean toward observed 15", v.PerGame)
	}
}

func TestInjuryAndBye(t *testing.T) {
	p := withHistory("RB", []float64{12, 12}, []float64{12, 12}, 12)
	p.InjuryStatus = espn.InjuryOut
	v := Value(league(3), p, DefaultOptions())
	if v.WeekProjection != 0 {
		t.Errorf("OUT player projected %.1f, want 0", v.WeekProjection)
	}
	if v.ROSValue <= 0 {
		t.Errorf("OUT player should keep ROS value")
	}

	p2 := withHistory("RB", nil, nil, 12)
	v2 := Value(league(9), p2, DefaultOptions())
	if !v2.OnBye || v2.WeekProjection != 0 {
		t.Errorf("bye week not detected: %+v", v2)
	}
	if v2.RemainingGames != 8 { // weeks 9..17 minus the bye
		t.Errorf("remaining games %d, want 8", v2.RemainingGames)
	}
}

func TestWinProbability(t *testing.T) {
	if p := WinProbability(100, 100, 100, 100); math.Abs(p-0.5) > 1e-9 {
		t.Errorf("even matchup = %.3f", p)
	}
	if p := WinProbability(110, 50, 100, 50); p < 0.8 || p > 0.9 {
		t.Errorf("10 pt favorite with sd 10 = %.3f, want ~0.84", p)
	}
}

func TestLostRoleOutweighsHotStart(t *testing.T) {
	// Scored 20 a game as a starter, then lost the job: ESPN now projects 5.
	p := withHistory("RB", []float64{18, 18, 18, 18, 6}, []float64{22, 20, 21, 19, 5}, 5)
	v := Value(league(6), p, DefaultOptions())
	if v.PerGame > 11 {
		t.Errorf("per-game %.1f still trusts the hot start over ESPN's current 5", v.PerGame)
	}
}

func TestMissedGamesAreNotGamesPlayed(t *testing.T) {
	// Out for weeks 3-4: ESPN reports 0 actual and no projection.
	p := withHistory("WR", []float64{12, 12, 12, 12, 12}, []float64{12, 12, 0, 0, 12}, 12)
	p.Stats = p.Stats[:0:0]
	for w, a := range []float64{12, 12, 0, 0, 12} {
		if a > 0 {
			p.Stats = append(p.Stats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Projected: true, Points: 12})
		}
		p.Stats = append(p.Stats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: a})
	}
	v := Value(league(6), p, DefaultOptions())
	if v.WeeksPlayed != 3 || v.SeasonAvg != 12 {
		t.Errorf("played %d at %.1f a game, want 3 at 12", v.WeeksPlayed, v.SeasonAvg)
	}
}

// CeeDee Lamb, 2026 weeks 1-5, when only week 5's projection was known: a
// single bad week must not be read as five weeks of missing projections.
func TestOneProjectedWeekIsWeakEvidence(t *testing.T) {
	p := &espn.Player{ID: 1, Name: "Lamb", Position: "WR", ProTeamID: 1, InjuryStatus: espn.InjuryActive}
	for w, a := range []float64{15.4, 35.3, 20.2, 41.3, 2.9} {
		p.Stats = append(p.Stats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: a})
	}
	p.Stats = append(p.Stats,
		espn.StatLine{Season: season, Week: 5, Weekly: true, Projected: true, Points: 19.13},
		espn.StatLine{Season: season, Week: 6, Weekly: true, Projected: true, Points: 18.61},
		espn.StatLine{Season: season, Projected: true, Points: 224.16, Average: 18.68})
	v := Value(league(6), p, DefaultOptions())
	if v.ProjectedWeeks != 1 {
		t.Fatalf("projected weeks %d, want 1", v.ProjectedWeeks)
	}
	if v.AccuracyFactor < 0.85 {
		t.Errorf("accuracy factor %.2f from one week; this week %.1f vs ESPN 18.61", v.AccuracyFactor, v.WeekProjection)
	}
	for _, n := range v.Notes {
		if strings.Contains(n, "projections") {
			t.Errorf("note %q from a single week", n)
		}
	}

	// With every week's projection (what ESPN returns now), he's beating them.
	for w, pr := range []float64{17.26, 18.0, 17.31, 16.9} {
		p.Stats = append(p.Stats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Projected: true, Points: pr})
	}
	v = Value(league(6), p, DefaultOptions())
	if v.ProjectedWeeks != 5 || v.Accuracy < 1.25 || v.WeekProjection < 18.61 {
		t.Errorf("full history: %d weeks, accuracy %.2f, this week %.1f", v.ProjectedWeeks, v.Accuracy, v.WeekProjection)
	}
}

// Private leagues may not return past weekly projections; ESPN's public
// default-scoring lines fill them in.
func TestReferenceFillsMissingProjections(t *testing.T) {
	p := &espn.Player{ID: 1, Name: "Lamb", Position: "WR", ProTeamID: 1, InjuryStatus: espn.InjuryActive}
	for w, a := range []float64{15.4, 35.3, 20.2, 41.3, 2.9} {
		p.Stats = append(p.Stats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: a})
		p.RefStats = append(p.RefStats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Points: a})
	}
	for w, pr := range []float64{17.26, 18.0, 17.31, 16.9, 19.13} {
		p.RefStats = append(p.RefStats, espn.StatLine{Season: season, Week: w + 1, Weekly: true, Projected: true, Points: pr})
	}
	p.Stats = append(p.Stats,
		espn.StatLine{Season: season, Week: 5, Weekly: true, Projected: true, Points: 19.13},
		espn.StatLine{Season: season, Week: 6, Weekly: true, Projected: true, Points: 18.61})
	v := Value(league(6), p, DefaultOptions())
	if v.ProjectedWeeks != 5 || v.Accuracy < 1.25 || v.WeekProjection < 18.61 {
		t.Errorf("%d weeks, accuracy %.2f, this week %.1f", v.ProjectedWeeks, v.Accuracy, v.WeekProjection)
	}
}
