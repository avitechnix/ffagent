package analysis

import (
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/espn"
)

// A starting QB goes to IR. If ESPN already projects his backup as the
// starter, the agent must not add the opportunity on top (the Hurts/McKee
// case that made a backup outrank a healthy starter on another team).
func TestOpportunityIsAFloorNotABonus(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	l := league(6)
	run := func(backupESPN float64) *Valuation {
		hurt := withHistory("QB", []float64{22, 22, 22, 22, 22}, []float64{22, 22, 22, 22, 22}, 22)
		hurt.InjuryStatus = espn.InjuryReserve
		backup := withHistory("QB", []float64{2, 2, 2, 2, 2}, []float64{1, 3, 2, 0.5, 2}, backupESPN)
		backup.ID = 2
		players := []*espn.Player{hurt, backup}
		vals := ValueAll(l, players, DefaultOptions())
		inj := map[int]Injury{hurt.ID: {PlayerID: hurt.ID, ProTeamID: 1, Status: espn.InjuryReserve, Date: now.Add(-time.Hour)}}
		ApplyOpportunity(l, players, vals, inj, now, 48*time.Hour)
		return vals[backup.ID]
	}

	// ESPN hasn't caught up: the backup is lifted to the floor (75% of ~22).
	if v := run(2); v.WeekProjection < 15 || v.WeekProjection > 18 || v.OpportunityBoost == 0 {
		t.Errorf("unpriced backup: %.1f this week, boost %.1f", v.WeekProjection, v.OpportunityBoost)
	}
	// ESPN already projects him as the starter: nothing is added.
	v := run(15)
	if v.WeekProjection > 17 {
		t.Errorf("priced-in backup projected %.1f; the opportunity was counted twice", v.WeekProjection)
	}
	// A healthy starter on another team (Maye, ~19) still outranks him.
	if v.WeekProjection >= 19 {
		t.Errorf("backup %.1f would outrank a 19-point starter", v.WeekProjection)
	}
}
