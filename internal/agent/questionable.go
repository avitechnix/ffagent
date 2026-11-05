package agent

import (
	"fmt"
	"strings"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/espn"
)

// Lineup decisions for questionable players. ESPN's projection for one is
// roughly what he scores if he plays, so a lineup choice that counts it in
// full overrates him: he may sit, or play a reduced role. The projections
// shown stay ESPN's; only the choice of who starts weighs the risk.
//
// The chance he sits also helps his backup, who takes over the role when he
// does. So the backup's decision score is his ESPN projection if the
// starter plays, and what he scores as the lead if he doesn't.

// applyQuestionable sets decision scores for questionable players and
// their same-position teammates. Call after valuations and opportunity
// boosts are in place.
func (s *state) applyQuestionable() {
	s.decision = map[int]float64{}
	type key struct {
		team int
		pos  string
	}
	group := map[key][]*analysis.Valuation{}
	for _, v := range s.values {
		if p := v.Player; p.ProTeamID != 0 {
			k := key{p.ProTeamID, p.Position}
			group[k] = append(group[k], v)
		}
	}
	for _, h := range s.values {
		c := h.PlayChance
		if c <= 0 || c >= 1 {
			continue
		}
		hp := h.Player
		s.decision[hp.ID] = c * h.WeekProjection
		for _, x := range group[key{hp.ProTeamID, hp.Position}] {
			xp := x.Player
			// Only a healthy player behind him inherits his role. "Behind" is
			// ESPN's call for this week: a backup who has filled in has the
			// better recent average, but projects lower with the starter back.
			if xp.ID == hp.ID || x.PlayChance < 1 || x.OnBye || xp.Locked || x.WeekProjection >= h.WeekProjection || x.WeekProjection <= 0 {
				continue
			}
			lead, why := s.asLead(x, h)
			if lead <= x.WeekProjection {
				continue
			}
			d := c*x.WeekProjection + (1-c)*lead
			if d > s.decision[xp.ID] {
				s.decision[xp.ID] = d
				x.Notes = append(x.Notes, fmt.Sprintf("%s is %s; if he sits, %s takes over (%s)", hp.Name, strings.ToLower(hp.EffectiveInjury()), xp.Name, why))
			}
		}
	}
}

// asLead estimates what x scores when h, ahead of him, sits. If h has
// missed the last two weeks, x's recent games are exactly that; otherwise
// it's the usual next-man-up share of h's production.
func (s *state) asLead(x, h *analysis.Valuation) (float64, string) {
	missed := s.l.CurrentWeek > 2
	for w := s.l.CurrentWeek - 2; w < s.l.CurrentWeek && missed; w++ {
		if s.l.OnBye(h.Player.ProTeamID, w) {
			continue
		}
		if pts, ok := h.Player.WeekActual(s.l.Season, w); ok && pts > 0 {
			missed = false
		}
	}
	if missed && x.RecentAvg > 0 {
		return x.RecentAvg, fmt.Sprintf("%.1f a game in his recent games without %s", x.RecentAvg, h.Player.Name)
	}
	if share, ok := analysis.OpportunityShare(h.Player.Position); ok && h.PerGame > 0 {
		f := share * h.PerGame
		return f, fmt.Sprintf("about %.1f as the lead", f)
	}
	return 0, ""
}

// decisionPoints is what the lineup choice counts a player for this week:
// his projection, weighed for a questionable player's chance of sitting.
func (s *state) decisionPoints(p *espn.Player) float64 {
	if d, ok := s.decision[p.ID]; ok {
		return d
	}
	return s.val(p).WeekProjection
}
