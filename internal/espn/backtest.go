package espn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GameLine is a game's betting line as ESPN's game page shows it.
type GameLine struct {
	OverUnder float64
	// Spread is from the home team's side: -3 means the home team is
	// favored by 3.
	Spread   float64
	Provider string
}

// Implied returns each team's implied points: the over/under split by the
// spread.
func (g GameLine) Implied() (home, away float64) {
	home = g.OverUnder/2 - g.Spread/2
	return home, g.OverUnder - home
}

// ProSchedule reads the NFL schedule for the season.
func (c *Client) ProSchedule(ctx context.Context) (*League, error) {
	l := &League{Season: c.Season}
	if err := c.loadProSchedule(ctx, l); err != nil {
		return nil, err
	}
	return l, nil
}

// summaryURL is ESPN's public game page data, next to the scoreboard.
func (c *Client) summaryURL(gameID int) string {
	base := c.Scoreboard
	if base == "" {
		base = DefaultScoreboard
	}
	return strings.TrimSuffix(base, "/scoreboard") + "/summary?event=" + strconv.Itoa(gameID)
}

// GameLines reads the betting line for each game, from ESPN's public game
// pages. Games without a line are left out.
func (c *Client) GameLines(ctx context.Context, gameIDs []int) (map[int]GameLine, error) {
	out := map[int]GameLine{}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 6) // be polite to ESPN
	var wg sync.WaitGroup
	for _, id := range gameIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			var raw struct {
				PickCenter []struct {
					Provider  struct{ Name string } `json:"provider"`
					OverUnder *float64              `json:"overUnder"`
					Spread    *float64              `json:"spread"`
				} `json:"pickcenter"`
			}
			err := c.do(ctx, http.MethodGet, c.summaryURL(id), nil, nil, &raw)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("game %d: %w", id, err)
				}
				return
			}
			for _, p := range raw.PickCenter {
				if p.OverUnder != nil && p.Spread != nil && *p.OverUnder > 0 {
					out[id] = GameLine{OverUnder: *p.OverUnder, Spread: *p.Spread, Provider: p.Provider.Name}
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ReferencePool returns the most-owned players at each fantasy position
// with ESPN's weekly projections and actuals (default PPR scoring) for
// weeks 1 through `through`, from ESPN's public player data.
func (c *Client) ReferencePool(ctx context.Context, through, limit int) ([]*Player, error) {
	y := strconv.Itoa(c.Season)
	ids := []string{"00" + y, "10" + y}
	for w := 1; w <= through; w++ {
		ids = append(ids, "11"+y+strconv.Itoa(w), "00"+y+strconv.Itoa(w))
	}
	filter := map[string]any{"players": map[string]any{
		"filterSlotIds":                     map[string]any{"value": []int{SlotQB, SlotRB, SlotWR, SlotTE, SlotK, SlotDST}},
		"sortPercOwned":                     map[string]any{"sortPriority": 1, "sortAsc": false},
		"limit":                             limit,
		"filterStatsForTopScoringPeriodIds": map[string]any{"value": 30, "additionalValue": ids},
	}}
	b, _ := json.Marshal(filter)
	var resp rawPlayersResponse
	u := c.referencePath() + "?" + viewsQuery(through, "kona_player_info")
	if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(b)}, &resp); err != nil {
		return nil, err
	}
	out := make([]*Player, 0, len(resp.Players))
	for _, e := range resp.Players {
		p := e.toPlayer(SlotUnknown, "")
		for _, s := range e.Player.statLines() {
			if s.Weekly && s.Season == c.Season {
				p.RefStats = append(p.RefStats, s)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// CompletedWeeks is the last week whose games have all finished (by
// kickoff time plus five hours).
func CompletedWeeks(l *League, now time.Time) int {
	last := 0
	for w := 1; w <= 18; w++ {
		scheduled, done := false, true
		for _, games := range l.ProGames {
			if g, ok := games[w]; ok {
				scheduled = true
				if now.Before(g.Start.Add(5 * time.Hour)) {
					done = false
				}
			}
		}
		if !scheduled || !done {
			break
		}
		last = w
	}
	return last
}
