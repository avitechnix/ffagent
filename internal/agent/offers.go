package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/espn"
)

// OfferSource is implemented by ESPN clients that can read pending trade
// offers (the real client does; test fakes needn't).
type OfferSource interface {
	TradeOffers(ctx context.Context, l *espn.League, me int) ([]espn.TradeOffer, error)
}

// offersTTL is how long pending offers are reused between plans.
const offersTTL = 10 * time.Minute

// OfferReview is the agent's read on a trade someone offered you.
type OfferReview struct {
	Offer     espn.TradeOffer
	Partner   *espn.Team
	Give      []*espn.Player // what they'd send you
	Get       []*espn.Player // what you'd send them
	MyGain    float64        // change in your team value
	TheirGain float64
	Grade     string // A (great for you) to F
	Verdict   string // Accept, Your call, Counter, Decline
	Reason    string
	Counter   *TradeIdea // a version that works for you, when theirs doesn't
}

// loadOffers refreshes pending trade offers, keeping the last good copy if
// ESPN fails.
func (a *Agent) loadOffers(ctx context.Context, l *espn.League, me int) []espn.TradeOffer {
	src, ok := a.ESPN.(OfferSource)
	if !ok {
		return nil
	}
	if a.offersAt.IsZero() || a.Now().Sub(a.offersAt) >= offersTTL {
		if offers, err := src.TradeOffers(ctx, l, me); err == nil {
			a.offers, a.offersAt = offers, a.Now()
		}
	}
	return a.offers
}

// reviewOffers grades each pending offer by what it does to your team
// value, the same measure trade ideas use.
func (s *state) reviewOffers(me *espn.Team, offers []espn.TradeOffer) []OfferReview {
	me = settled(me)
	var out []OfferReview
	for _, o := range offers {
		var partner *espn.Team
		for _, t := range s.l.Teams {
			if t.ID == o.FromTeam {
				partner = t
			}
		}
		if partner == nil {
			continue
		}
		give, ok1 := playersOn(partner, o.Give)
		get, ok2 := playersOn(me, o.Get)
		if !ok1 || !ok2 {
			continue // a player has moved since: ESPN will void the offer
		}
		r := OfferReview{Offer: o, Partner: partner, Give: give, Get: get}
		r.MyGain, r.TheirGain = s.tradeGains(me, partner, get, give)
		s.grade(&r)
		if r.MyGain < s.a.Cfg.MinTradeGain {
			r.Counter = s.counter(me, partner, get, give, r.MyGain)
		}
		out = append(out, r)
	}
	return out
}

func playersOn(t *espn.Team, ids []int) ([]*espn.Player, bool) {
	var ps []*espn.Player
	for _, id := range ids {
		p := t.Player(id)
		if p == nil {
			return nil, false
		}
		ps = append(ps, p)
	}
	return ps, true
}

// tradeGains is the change in each side's team value if I send `give` and
// receive `get`.
func (s *state) tradeGains(me, o *espn.Team, give, get []*espn.Player) (mine, theirs float64) {
	mine = s.teamValue(s.trim(swap(me.Roster, give, get), get), 0) - s.teamValue(me.Roster, 0)
	theirs = s.teamValue(s.trim(swap(o.Roster, get, give), give), 0) - s.teamValue(o.Roster, 0)
	return mine, theirs
}

func (s *state) grade(r *OfferReview) {
	m := s.a.Cfg.MinTradeGain
	switch g := r.MyGain; {
	case g >= 2*m:
		r.Grade, r.Verdict = "A", "Accept"
	case g >= m:
		r.Grade, r.Verdict = "B", "Accept"
	case g >= 0:
		r.Grade, r.Verdict = "C", "Your call"
	case g > -m:
		r.Grade, r.Verdict = "D", "Counter"
	default:
		r.Grade, r.Verdict = "F", "Decline"
	}
	var why []string
	switch r.Verdict {
	case "Accept":
		why = append(why, fmt.Sprintf("it adds %.1f to your team value", r.MyGain))
	case "Your call":
		why = append(why, fmt.Sprintf("about even: %+.1f to your team value", r.MyGain))
	default:
		why = append(why, fmt.Sprintf("it costs you %.1f in team value", -r.MyGain))
	}
	why = append(why, fmt.Sprintf("%+.1f for %s", r.TheirGain, r.Partner.Name))
	why = append(why, fmt.Sprintf("you'd get %s and give %s", rosList(s.values, r.Give), rosList(s.values, r.Get)))
	r.Reason = strings.Join(why, "; ")
}

// rosList names players with their rest-of-season projection.
func rosList(values map[int]*analysis.Valuation, ps []*espn.Player) string {
	if len(ps) == 0 {
		return "nothing"
	}
	var n []string
	for _, p := range ps {
		if v := values[p.ID]; v != nil {
			n = append(n, fmt.Sprintf("%s (%s, %.0f ROS)", p.Name, p.Position, v.ROSValue))
		} else {
			n = append(n, fmt.Sprintf("%s (%s)", p.Name, p.Position))
		}
	}
	return strings.Join(n, " + ")
}

// counter looks for a small change to their offer that makes it good for
// you and still plausible for them: one more of their players, one fewer
// of yours, or a lesser player of yours in place of one they asked for.
func (s *state) counter(me, o *espn.Team, give, get []*espn.Player, base float64) *TradeIdea {
	cfg := s.a.Cfg
	var cands []TradeIdea
	try := func(g, r []*espn.Player) {
		if len(g)+len(r) == 0 || len(g) > 3 || len(r) > 3 {
			return
		}
		mine, theirs := s.tradeGains(me, o, g, r)
		if mine < cfg.MinTradeGain || theirs < -cfg.TradeTolerance {
			return
		}
		c := TradeIdea{Partner: o, Give: g, Get: r, MyGain: mine, TheirGain: theirs}
		if c.Odds, c.oddsWeight = s.acceptance(g, r, theirs); c.oddsWeight >= 0.5 { // no long shots
			cands = append(cands, c)
		}
	}
	in := func(ps []*espn.Player, p *espn.Player) bool {
		for _, x := range ps {
			if x.ID == p.ID {
				return true
			}
		}
		return false
	}
	for _, p := range o.Roster { // ask for one more
		if p.LineupSlot != espn.SlotIR && tradeable(p) && !in(get, p) {
			try(give, append(append([]*espn.Player(nil), get...), p))
		}
	}
	for i := range give { // send one fewer
		if len(give) > 1 {
			try(append(append([]*espn.Player(nil), give[:i]...), give[i+1:]...), get)
		}
	}
	for i, q := range give { // send someone lesser instead
		for _, p := range s.droppable(me) {
			if tradeable(p) && !in(give, p) && p.Position == q.Position && s.val(p).ROSValue < s.val(q).ROSValue {
				g := append([]*espn.Player(nil), give...)
				g[i] = p
				try(g, get)
			}
		}
	}
	if len(cands) == 0 {
		return nil
	}
	sort.SliceStable(cands, func(i, j int) bool {
		return cands[i].oddsWeight*tradeScore(cands[i]) > cands[j].oddsWeight*tradeScore(cands[j])
	})
	c := cands[0]
	c.Action = s.tradeAction(me, c)
	c.Action.Summary = "Counter " + strings.TrimPrefix(c.Action.Summary, "Offer ")
	c.Action.Reason = fmt.Sprintf("their offer costs you %.1f; this version gains you %.1f and is %+.1f for them (%s)", -min(base, 0), c.MyGain, c.TheirGain, c.Odds)
	if base > 0 {
		c.Action.Reason = fmt.Sprintf("their offer gains you %.1f; this version gains you %.1f and is %+.1f for them (%s)", base, c.MyGain, c.TheirGain, c.Odds)
	}
	return &c
}

// tradeScore favors trades good for us that the other team also has a
// reason to accept.
func tradeScore(t TradeIdea) float64 { return t.MyGain + 0.5*min(t.TheirGain, t.MyGain) }

// acceptance is a rough read on whether the other manager says yes. Most
// managers judge a trade by whether it helps their lineup and by whether
// they get about as many projected points as they give up, so a trade that
// fills their hole but sends them fewer points is a long shot.
func (s *state) acceptance(give, get []*espn.Player, theirGain float64) (string, float64) {
	var sent, received float64 // from their side
	for _, p := range get {
		sent += s.val(p).ROSValue
	}
	for _, p := range give {
		received += s.val(p).ROSValue
	}
	ratio := 1.0
	if sent > 0 {
		ratio = received / sent
	}
	tol := s.a.Cfg.TradeTolerance
	switch {
	case theirGain >= 5 && ratio >= 0.9:
		return "they'd likely accept: it helps their lineup and they get about as many projected points as they give", 1
	case theirGain >= -tol && ratio >= 0.8:
		return "a fair ask: it's close for them", 0.6
	}
	return "a long shot: they'd be giving up more projected points than they get", 0.2
}
