package agent

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
)

// planPickups finds the free-agent adds and waiver claims that most improve
// the team, one at a time, each paired with the best player to drop (or an
// open roster spot, e.g. one freed by an IR move).
func (s *state) planPickups(t *espn.Team) []Action {
	var acts []Action
	cfg := s.a.Cfg
	size := s.l.RosterSize()
	faab := t.FAABRemaining
	taken := map[int]bool{}

	for n := 0; n < cfg.MaxAcquisitionsPerRun; n++ {
		base := s.teamValue(t.Roster, cfg.ThisWeekWeight)
		open := size - activeCount(t)
		drops := s.droppable(t)

		thisWeek := s.weekTotal(t.Roster)

		var best struct {
			add, drop *espn.Player
			roster    []*espn.Player
			gain      float64
			stash     bool
		}
		best.gain = math.Inf(-1)
		try := func(c, d *espn.Player) {
			stash := false
			if d != nil && !cfg.AllowLowerWeekPickups && s.projectsLower(c, d) {
				if !cfg.PlanStashes {
					return
				}
				stash = true // only if this week's lineup doesn't change
			}
			r := s.withMove(t.Roster, c, d)
			if d != nil && d.Position != c.Position && !s.regularStarter(r, c) {
				return // a different position only for someone who'd start
			}
			if stash && s.weekTotal(r) < thisWeek-0.05 {
				return // the drop is in this week's lineup
			}
			g := s.teamValue(r, cfg.ThisWeekWeight) - base
			if g <= best.gain || (stash && g < stashGainFactor*cfg.MinPickupGain) {
				return
			}
			best.add, best.drop, best.roster, best.gain, best.stash = c, d, r, g, stash
		}
		for _, fa := range s.pool {
			if taken[fa.ID] {
				continue
			}
			c := *fa
			c.OnTeamID = t.ID
			c.LineupSlot = espn.SlotBench
			if fa.Status == espn.StatusWaivers {
				c.Status = pendingClaim
			} else {
				c.Status = espn.StatusOnTeam
			}
			if open > 0 {
				try(&c, nil)
			}
			for _, d := range drops {
				try(&c, d)
			}
		}
		if best.add == nil || best.gain < cfg.MinPickupGain {
			break
		}

		act := s.pickupAction(t, best.add, best.drop, best.gain, &faab, best.stash, s.byeCover(t.Roster, best.roster, best.add))
		if best.drop == nil && len(s.freedBy) > 0 {
			act.Depends = []int{s.freedBy[0]} // uses the spot an IR move opened
			s.freedBy = s.freedBy[1:]
		}
		acts = append(acts, act)
		taken[best.add.ID] = true
		s.added[best.add.ID] = true
		t.Roster = best.roster
	}
	return acts
}

// regularStarter reports whether p is in the roster's best lineup in a
// typical week (no byes), not just as cover for a bye or an injury. A
// pickup may replace a player at another position only then: otherwise the
// gain is depth on paper, like a third tight end for a running back.
func (s *state) regularStarter(roster []*espn.Player, p *espn.Player) bool {
	future, _ := s.futureRoster(roster)
	weeks := float64(max(1, len(s.l.RemainingWeeks())))
	a := lineup.Optimize(s.l, future, func(q *espn.Player) float64 { return s.val(q).ROSValue / weeks })
	return espn.IsStartingSlot(a.Slots[p.ID])
}

// stashGainFactor: a stash (a pickup that projects less this week than his
// drop) must be worth this many times min_pickup_gain, since it pays off
// only later.
const stashGainFactor = 2

// weekTotal is this week's best lineup total for a roster.
func (s *state) weekTotal(roster []*espn.Player) float64 {
	return lineup.Optimize(s.l, startable(roster), s.weekScorer(0)).Total
}

// byeCover names the weeks a pickup matters most because players at his
// position are on bye, e.g. "covers week 9, when CeeDee Lamb is on bye".
func (s *state) byeCover(before, after []*espn.Player, c *espn.Player) string {
	oldF, _ := s.futureRoster(before)
	newF, _ := s.futureRoster(after)
	oldW, starts := s.futureLineups(oldF)
	newW, _ := s.futureLineups(newF)
	weeks := s.futureWeeks()
	if len(weeks) == 0 {
		return ""
	}
	delta := make([]float64, len(weeks))
	for i := range weeks {
		delta[i] = newW[i] - oldW[i]
	}
	typical := append([]float64(nil), delta...)
	sort.Float64s(typical)
	median := typical[len(typical)/2]
	var parts []string
	for i, fw := range weeks {
		if delta[i] < 3 || delta[i] < median+3 {
			continue
		}
		var out []string
		for _, p := range oldF {
			if starts[p.ID] && p.Position == c.Position && s.l.OnBye(p.ProTeamID, fw.week) {
				out = append(out, p.Name)
			}
		}
		if len(out) > 0 {
			parts = append(parts, fmt.Sprintf("week %d, when %s %s on bye", fw.week, strings.Join(out, " and "), isAre(len(out))))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "covers " + strings.Join(parts, "; ")
}

// projectsLower reports whether c is expected to score fewer points than d
// this week, by the agent's adjusted projection or by ESPN's. ESPN's is
// skipped for a "next man up" whose teammate was just ruled out: ESPN is
// slow to raise those, which is why the agent can beat others to them.
func (s *state) projectsLower(c, d *espn.Player) bool {
	if c.Status == pendingClaim {
		return false // the dropped player stays until the claim clears, so this week is unchanged
	}
	cv, dv := s.val(c), s.val(d)
	const eps = 0.05
	cWeek, cESPN := cv.WeekProjection, cv.ESPNWeekProjection
	if c.Locked {
		cWeek, cESPN = 0, 0 // his game has started: none of this week's points would be ours
	}
	if cWeek+eps < dv.WeekProjection {
		return true
	}
	return cv.OpportunityBoost == 0 && !cv.OnBye && !dv.OnBye && cESPN+eps < dv.ESPNWeekProjection
}

// withMove returns a new roster with c added and d (if any) removed. For a
// waiver claim the dropped player stays (as a pending drop) until it clears.
func (s *state) withMove(roster []*espn.Player, c, d *espn.Player) []*espn.Player {
	out := make([]*espn.Player, 0, len(roster)+1)
	for _, p := range roster {
		if d != nil && p.ID == d.ID {
			if c.Status == pendingClaim {
				pd := *p
				pd.Status = pendingDrop
				out = append(out, &pd)
			}
			continue
		}
		out = append(out, p)
	}
	return append(out, c)
}

func (s *state) pickupAction(t *espn.Team, c, d *espn.Player, gain float64, faab *int, stash bool, cover string) Action {
	week := s.l.CurrentWeek
	v := s.val(c)
	items := []espn.TxnItem{espn.AddItem(c.ID, t.ID)}
	verb := "Add"
	if stash {
		verb = "Stash"
	}
	summary := fmt.Sprintf("%s %s (%s, %s)", verb, c.Name, c.Position, c.ProTeam)
	if d != nil {
		items = append(items, espn.DropItem(d.ID, t.ID))
		summary += fmt.Sprintf(", drop %s (%s)", d.Name, d.Position)
	}
	// Lead with the reason in plain words, then the numbers behind it.
	var why []string
	if stash {
		why = append(why, fmt.Sprintf("a stash for later: %s isn't in your best lineup this week, so this week's projection doesn't change, and %s is worth more over the rest of the season", d.Name, c.Name))
	}
	if len(v.Notes) > 0 {
		why = append(why, c.Name+": "+strings.Join(v.Notes, ", "))
	}
	if d != nil && d.Position != c.Position {
		why = append(why, fmt.Sprintf("a %s for a %s, because %s would be in your starting lineup most weeks", c.Position, d.Position, c.Name))
	}
	if cover != "" {
		why = append(why, cover)
	}
	if d == nil {
		if r := s.replacementFor(c); r != nil {
			why = append(why, "replaces "+r.Name+" (IR)")
		}
	}
	var proj string
	switch {
	case c.Locked:
		// Points he's already scored this week can't count for us.
		proj = fmt.Sprintf("%s's game this week has already started, so he'd help from next week: %.0f rest of season, %.1f per game", c.Name, v.ROSValue, v.PerGame)
	case c.Status == pendingClaim:
		proj = fmt.Sprintf("%s would join once waivers clear: %.0f rest of season, %.1f per game", c.Name, v.ROSValue, v.PerGame)
	default:
		proj = fmt.Sprintf("%s projects %.1f pts this week (ESPN %.1f) and %.0f rest of season", c.Name, v.WeekProjection, v.ESPNWeekProjection, v.ROSValue)
	}
	if d != nil {
		dv := s.val(d)
		if c.Locked || c.Status == pendingClaim {
			proj += fmt.Sprintf(" (vs %.0f and %.1f per game for %s)", dv.ROSValue, dv.PerGame, d.Name)
		} else {
			proj += fmt.Sprintf(" (vs %.1f (ESPN %.1f) and %.0f for %s)", dv.WeekProjection, dv.ESPNWeekProjection, dv.ROSValue, d.Name)
		}
	}
	why = append(why, proj, fmt.Sprintf("team value +%.1f", gain))
	if c.Status != pendingClaim {
		return Action{
			Kind:    ActAdd,
			Summary: summary,
			Reason:  strings.Join(why, "; "),
			Txn:     espn.NewTransaction(s.member(), espn.TxnFreeAgent, t.ID, week, items...),
		}
	}
	bid := -1
	if s.l.UsesFAAB {
		var note string
		bid, note = s.faabBid(c, gain, *faab, t)
		*faab -= bid
		summary += fmt.Sprintf(" — bid $%d", bid)
		if note != "" {
			why = append(why, note)
		}
	}
	return Action{
		Kind:    ActClaim,
		Summary: summary,
		Reason:  strings.Join(why, "; "),
		Txn:     espn.NewWaiverClaim(s.member(), t.ID, week, bid, items...),
	}
}

// replacementFor returns the IR-stashed player a pickup at the same position
// is replacing, if any.
func (s *state) replacementFor(c *espn.Player) *espn.Player {
	for i, r := range s.irReplacements {
		if r.Position == c.Position {
			s.irReplacements = append(s.irReplacements[:i:i], s.irReplacements[i+1:]...)
			return r
		}
	}
	return nil
}
