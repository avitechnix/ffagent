// Package analysis turns raw ESPN stat lines into player valuations: how a
// player has performed against projections, what to expect this week, how
// volatile they are, and their rest-of-season value.
package analysis

import (
	"fmt"
	"math"
	"sort"

	"github.com/avitechnix/ffagent/internal/espn"
)

// Valuation is the agent's view of one player.
type Valuation struct {
	Player *espn.Player

	// History against ESPN projections.
	WeeksPlayed    int
	ActualTotal    float64 // sum of actual points in weeks with a projection
	ProjectedTotal float64 // sum of ESPN projections for those weeks
	ProjectedWeeks int     // weeks played that had an ESPN projection
	Accuracy       float64 // ActualTotal / ProjectedTotal (1.0 = met projections)
	AccuracyFactor float64 // shrunk, clamped multiplier applied to projections
	SeasonAvg      float64 // actual points per game played
	RecentAvg      float64 // average of the last 3 games played
	WeeklyActuals  []float64
	BiggestMiss    float64 // worst actual - projected in a week
	BiggestBeat    float64 // best actual - projected in a week

	// This week.
	ESPNWeekProjection float64
	WeekProjection     float64 // adjusted expected points this week
	WeekStdDev         float64
	WeekActual         float64 // points already scored this week, when locked
	OnBye              bool
	InjuryFactor       float64
	OpportunityBoost   float64 // points per week added because a teammate is out

	// Rest of season.
	PerGame        float64 // expected points per game going forward
	RemainingGames int
	ROSValue       float64

	Notes []string

	// boostPerGame is a next-man-up's extra points per game, for the next
	// boostGames games after this week's.
	boostPerGame, boostGames float64
	rosFactor                float64 // the injury discount in ROSValue
}

// Players on IR or suspended miss at least this many games after this
// week's. IR is four games, counting this week.
var returnDelay = map[string]int{espn.InjuryReserve: 3, espn.InjurySuspension: 1}

// FutureWeek is the points a player is expected to score in a later week:
// nothing on a bye, nothing while he's still out, then his per-game rate.
// For a player on IR or suspended, the rate after his return is set so the
// weeks add up to his rest-of-season value, which already prices in the
// chance he's out longer. Others keep the same discount every week.
func (v *Valuation) FutureWeek(l *espn.League, week int) float64 {
	p := v.Player
	if week <= l.CurrentWeek || week > l.FinalWeek || l.OnBye(p.ProTeamID, week) || v.PerGame <= 0 {
		return 0
	}
	level := v.rosFactor
	if delay, ok := returnDelay[p.EffectiveInjury()]; ok {
		out, played := 0, 0 // games missed / left after his return, counting this week's
		for w := l.CurrentWeek; w <= l.FinalWeek; w++ {
			if l.OnBye(p.ProTeamID, w) {
				continue
			}
			if w <= l.CurrentWeek+delay {
				out++
			} else {
				played++
			}
		}
		if week <= l.CurrentWeek+delay || played == 0 {
			return 0
		}
		level = math.Min(1, level*float64(out+played)/float64(played))
	}
	pts := v.PerGame * level
	if v.boostGames > 0 {
		n := 0 // which future game this is
		for w := l.CurrentWeek + 1; w <= week; w++ {
			if !l.OnBye(p.ProTeamID, w) {
				n++
			}
		}
		if float64(n) <= math.Ceil(v.boostGames) {
			pts += v.boostPerGame * math.Min(1, v.boostGames-float64(n-1))
		}
	}
	return pts
}

// Options tunes the valuation model.
type Options struct {
	// AccuracyPriorWeeks is how many "phantom" weeks at 100% accuracy are
	// blended in before trusting a player's projection history.
	AccuracyPriorWeeks float64
	// MinAccuracy and MaxAccuracy clamp the projection adjustment. Both 1
	// (the default) uses ESPN's projections as they are.
	MinAccuracy, MaxAccuracy float64
	// LongTermInjuryFactor scales ROS value for players on injured reserve.
	LongTermInjuryFactor float64
}

// DefaultOptions use ESPN's projections unadjusted.
func DefaultOptions() Options {
	return Options{AccuracyPriorWeeks: 8, MinAccuracy: 1, MaxAccuracy: 1, LongTermInjuryFactor: 0.45}
}

// WithTrackRecord lets a player's record against ESPN's projections move
// them by up to the given fraction (e.g. 0.2 for ±20%).
func (o Options) WithTrackRecord(max float64) Options {
	o.MinAccuracy, o.MaxAccuracy = 1-max, 1+max
	return o
}

// positionCV is the typical weekly coefficient of variation by position,
// used as a prior for volatility when history is thin.
var positionCV = map[string]float64{
	"QB": 0.35, "RB": 0.50, "WR": 0.55, "TE": 0.60, "K": 0.45, "D/ST": 0.65,
}

// gamesPerSeason approximates games in an NFL fantasy season.
const gamesPerSeason = 17.0

// WeekInjuryFactor is the expected fraction of a projection a player will
// score this week given their injury designation.
func WeekInjuryFactor(status string) float64 {
	switch status {
	case espn.InjuryQuestionable:
		return 0.85
	case espn.InjuryDoubtful:
		return 0.2
	case espn.InjuryOut, espn.InjuryReserve, espn.InjurySuspension:
		return 0
	case espn.InjuryDayToDay:
		return 0.9
	}
	return 1
}

// weekInjuryAdjustment is how much of ESPN's weekly projection to keep given
// the player's injury status. ESPN's projection already reflects ESPN's own
// designation, so only fresher news from the injury report (or an OUT
// player ESPN still projects) changes it. Without an ESPN projection, the
// designation discounts the agent's own estimate.
func weekInjuryAdjustment(p *espn.Player, hasProj bool) float64 {
	st := p.EffectiveInjury()
	f := WeekInjuryFactor(st)
	if !hasProj || f == 0 || (p.NewsStatus != "" && p.NewsStatus != p.InjuryStatus) {
		return f
	}
	return 1
}

// rosInjuryFactor is the expected availability over the remaining season.
func rosInjuryFactor(status string, o Options) float64 {
	switch status {
	case espn.InjuryQuestionable, espn.InjuryDayToDay:
		return 0.97
	case espn.InjuryDoubtful:
		return 0.9
	case espn.InjuryOut:
		return 0.8
	case espn.InjuryReserve:
		return o.LongTermInjuryFactor
	case espn.InjurySuspension:
		return 0.6
	}
	return 1
}

// Value computes a valuation for a player in the league's current week.
func Value(l *espn.League, p *espn.Player, o Options) *Valuation {
	v := &Valuation{Player: p, AccuracyFactor: 1, Accuracy: 1, InjuryFactor: 1}
	season, week := l.Season, l.CurrentWeek

	// Projection accuracy over completed weeks.
	minDiff, maxDiff := math.Inf(1), math.Inf(-1)
	projectedWeeks := 0
	for w := 1; w < week; w++ {
		act, okA := p.WeekActual(season, w)
		if !okA {
			continue
		}
		proj, okP := p.WeekProjection(season, w)
		if act == 0 && (!okP || proj <= 0) {
			continue // didn't play (bye/inactive/injured): not a game played
		}
		v.WeeklyActuals = append(v.WeeklyActuals, act)
		if !okP || proj <= 0 {
			// The league didn't return this week's projection: compare
			// with ESPN's default-scoring actual and projection instead,
			// which are consistent with each other.
			if ra, rp, ok := p.RefWeek(season, w); ok && rp > 0 {
				act, proj, okP = ra, rp, true
			}
		}
		if okP && proj > 0 {
			v.ActualTotal += act
			v.ProjectedTotal += proj
			projectedWeeks++
			d := act - proj
			minDiff = math.Min(minDiff, d)
			maxDiff = math.Max(maxDiff, d)
		}
	}
	v.WeeksPlayed = len(v.WeeklyActuals)
	if !math.IsInf(minDiff, 0) {
		v.BiggestMiss, v.BiggestBeat = minDiff, maxDiff
	}
	v.ProjectedWeeks = projectedWeeks
	if v.ProjectedTotal > 0 {
		v.Accuracy = v.ActualTotal / v.ProjectedTotal
		// Shrink toward 100% with AccuracyPriorWeeks phantom weeks. Only
		// weeks with both a projection and a result count as evidence.
		prior := o.AccuracyPriorWeeks * v.ProjectedTotal / float64(projectedWeeks)
		shrunk := (v.ActualTotal + prior) / (v.ProjectedTotal + prior)
		v.AccuracyFactor = clamp(shrunk, o.MinAccuracy, o.MaxAccuracy)
	}
	if v.WeeksPlayed > 0 {
		v.SeasonAvg = mean(v.WeeklyActuals)
		k := len(v.WeeklyActuals)
		v.RecentAvg = mean(v.WeeklyActuals[max(0, k-3):])
	} else if sa, ok := p.SeasonActual(season); ok && sa.Average > 0 {
		v.SeasonAvg, v.RecentAvg = sa.Average, sa.Average
	}

	// This week.
	v.OnBye = l.OnBye(p.ProTeamID, week)
	v.InjuryFactor = WeekInjuryFactor(p.EffectiveInjury())
	espnProj, hasProj := p.WeekProjection(season, week)
	v.ESPNWeekProjection = espnProj

	// Per-game expectation going forward. ESPN's projections carry the
	// player's current role (a starter's injury, a benching), so they anchor
	// it: this week's projection when he's healthy and playing, blended with
	// ESPN's season projection, adjusted by his record against ESPN's
	// numbers. Observed production is blended in, but never outweighs the
	// projections: a hot September doesn't make up for a lost job.
	projPerGame := 0.0
	if sp, ok := p.SeasonProjection(season); ok {
		projPerGame = sp.Average
		if projPerGame == 0 && sp.Points > 0 {
			projPerGame = sp.Points / gamesPerSeason
		}
	}
	if hasProj && espnProj > 0 && !v.OnBye && v.InjuryFactor == 1 {
		if projPerGame > 0 {
			projPerGame = 0.6*espnProj + 0.4*projPerGame
		} else {
			projPerGame = espnProj
		}
	}
	projPerGame *= v.AccuracyFactor
	observed := 0.6*v.SeasonAvg + 0.4*v.RecentAvg
	switch {
	case v.WeeksPlayed == 0:
		v.PerGame = projPerGame
	case projPerGame == 0:
		v.PerGame = observed
	default:
		w := math.Min(0.5, float64(v.WeeksPlayed)/(float64(v.WeeksPlayed)+6))
		v.PerGame = w*observed + (1-w)*projPerGame
	}

	base := espnProj
	if !hasProj {
		base = v.PerGame / v.AccuracyFactor
	}
	if v.OnBye {
		v.WeekProjection = 0
	} else {
		v.WeekProjection = base * v.AccuracyFactor * weekInjuryAdjustment(p, hasProj)
	}
	v.WeekStdDev = stdDev(v, p.Position)
	if p.Locked {
		// Game underway or over: points so far plus the pre-game projection
		// for the part of the game still to play, the way ESPN's app
		// projects live games. A defense's points can fall as it allows
		// points, so ESPN blends its points so far with the projection
		// instead.
		act, _ := p.WeekActual(season, week)
		left := math.Max(0, math.Min(1, p.GameLeft))
		v.WeekActual = act
		if p.Position == "D/ST" {
			v.WeekProjection = act*(1-left) + v.WeekProjection*left
		} else {
			v.WeekProjection = act + v.WeekProjection*left
		}
		v.WeekStdDev *= math.Sqrt(left)
	}

	// Rest of season.
	for w := week; w <= l.FinalWeek; w++ {
		if !l.OnBye(p.ProTeamID, w) {
			v.RemainingGames++
		}
	}
	ros := rosInjuryFactor(p.EffectiveInjury(), o)
	v.rosFactor = ros
	v.ROSValue = v.PerGame * float64(v.RemainingGames) * ros

	v.addNotes(o)
	return v
}

func stdDev(v *Valuation, pos string) float64 {
	if v.WeekProjection <= 0 {
		return 0
	}
	cv, ok := positionCV[pos]
	if !ok {
		cv = 0.5
	}
	prior := cv * v.WeekProjection
	n := float64(len(v.WeeklyActuals))
	if n < 2 {
		return prior
	}
	m := mean(v.WeeklyActuals)
	var ss float64
	for _, x := range v.WeeklyActuals {
		ss += (x - m) * (x - m)
	}
	sample := math.Sqrt(ss / (n - 1))
	// Scale observed volatility to this week's expectation, then shrink.
	if m > 0 {
		sample = sample / m * v.WeekProjection
	}
	const k = 4.0
	return math.Sqrt((n*sample*sample + k*prior*prior) / (n + k))
}

func (v *Valuation) addNotes(o Options) {
	p := v.Player
	if v.OnBye {
		v.Notes = append(v.Notes, "on bye")
	}
	if st := p.EffectiveInjury(); st != espn.InjuryActive {
		v.Notes = append(v.Notes, st)
	}
	if v.ProjectedWeeks >= 3 {
		switch {
		case v.Accuracy >= 1.15:
			v.Notes = append(v.Notes, fmt.Sprintf("beating projections by %.0f%%", (v.Accuracy-1)*100))
		case v.Accuracy <= 0.85:
			v.Notes = append(v.Notes, fmt.Sprintf("missing projections by %.0f%%", (1-v.Accuracy)*100))
		}
	}
	if v.WeeksPlayed >= 3 {
		if v.SeasonAvg > 0 {
			switch r := v.RecentAvg / v.SeasonAvg; {
			case r >= 1.25:
				v.Notes = append(v.Notes, "trending up")
			case r <= 0.75:
				v.Notes = append(v.Notes, "trending down")
			}
		}
	}
	if p.PercentChange >= 5 {
		v.Notes = append(v.Notes, fmt.Sprintf("hot pickup (+%.1f%% owned)", p.PercentChange))
	}
}

// ValueAll values a set of players and returns them keyed by player ID.
func ValueAll(l *espn.League, players []*espn.Player, o Options) map[int]*Valuation {
	out := make(map[int]*Valuation, len(players))
	for _, p := range players {
		out[p.ID] = Value(l, p, o)
	}
	return out
}

// SortByROS sorts valuations by descending rest-of-season value.
func SortByROS(vs []*Valuation) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].ROSValue > vs[j].ROSValue })
}

// WinProbability is P(A > B) for independent normal team scores.
func WinProbability(meanA, varA, meanB, varB float64) float64 {
	sd := math.Sqrt(varA + varB)
	if sd == 0 {
		switch {
		case meanA > meanB:
			return 1
		case meanA < meanB:
			return 0
		}
		return 0.5
	}
	return 0.5 * (1 + math.Erf((meanA-meanB)/(sd*math.Sqrt2)))
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func clamp(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
}
