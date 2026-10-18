package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/avitechnix/ffagent/internal/espn"
)

// BidHistory is implemented by ESPN clients that can read the league's
// past waiver claims (the real client does; test fakes needn't).
type BidHistory interface {
	WaiverBids(ctx context.Context, l *espn.League) ([]espn.WaiverBid, error)
}

// bidHistoryTTL is how long the league's claim history is reused. Waivers
// process at most daily.
const bidHistoryTTL = 6 * time.Hour

// minBidHistory is how many winning bids the league needs before they
// inform ours.
const minBidHistory = 5

// loadBids refreshes the league's FAAB claim history, keeping the last
// good copy if ESPN fails.
func (a *Agent) loadBids(ctx context.Context, l *espn.League) {
	h, ok := a.ESPN.(BidHistory)
	if !ok || !l.UsesFAAB || (a.bidsAt.Add(bidHistoryTTL).After(a.Now()) && a.bidsWeek == l.CurrentWeek) {
		return
	}
	bids, err := h.WaiverBids(ctx, l)
	if err != nil {
		return
	}
	a.bids, a.bidsAt, a.bidsWeek = bids, a.Now(), l.CurrentWeek
}

// streamed positions are picked up week to week, so they go for less.
func streamed(pos string) bool { return pos == "K" || pos == "D/ST" }

// faabBid decides a claim's bid. What it's worth to us scales with the
// upgrade: small upgrades get token bids, big ones approach
// max_faab_fraction of what's left. When the league has enough history,
// the bid is cut to what it should take to win: this season's winning bids
// at the player's position group, read at a percentile set by how fast
// he's being added across ESPN, and never more than $1 over what any rival
// has left.
func (s *state) faabBid(c *espn.Player, gain float64, remaining int, me *espn.Team) (int, string) {
	if remaining <= 0 {
		return 0, ""
	}
	frac := math.Min(s.a.Cfg.MaxFAABFraction, gain/(gain+100))
	if streamed(c.Position) {
		frac *= 0.15
	}
	worth := min(remaining, int(math.Ceil(float64(remaining)*frac)))

	var won []int
	for _, b := range s.a.bids {
		if b.Won && streamed(s.bidPosition(b.PlayerID)) == streamed(c.Position) {
			won = append(won, b.Amount)
		}
	}
	if len(won) < minBidHistory {
		return worth, ""
	}
	sort.Ints(won)
	// ESPN's ownership change: a player being added everywhere is the one
	// rivals bid on.
	pct := math.Max(0.25, math.Min(0.95, 0.5+c.PercentChange/20))
	price := won[int(math.Round(pct*float64(len(won)-1)))] + 1
	rival := 0
	for _, t := range s.l.Teams {
		if t.ID != me.ID {
			rival = max(rival, t.FAABRemaining)
		}
	}
	price = min(price, rival+1)

	group := "this season's winning bids"
	if streamed(c.Position) {
		group = "this season's winning bids for kickers and defenses"
	}
	hist := fmt.Sprintf("%s: median $%d, top $%d (%d claims)", group, won[len(won)/2], won[len(won)-1], len(won))
	switch {
	case price <= worth:
		return max(0, price), fmt.Sprintf("bid $%d of the $%d he's worth to you; %s; rivals have at most $%d left", price, worth, hist, rival)
	default:
		return worth, fmt.Sprintf("bid $%d, all he's worth to you, though claims like this have taken about $%d (%s), so he may go elsewhere", worth, price, hist)
	}
}

// bidPosition is a claimed player's position, if the agent knows him.
func (s *state) bidPosition(id int) string {
	if v, ok := s.values[id]; ok {
		return v.Player.Position
	}
	return ""
}
