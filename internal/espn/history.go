package espn

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// WaiverBid is one FAAB claim from the league's transaction history.
type WaiverBid struct {
	Week     int
	TeamID   int
	PlayerID int
	Amount   int
	Won      bool // the claim went through; false = outbid or invalid
}

// WaiverBids reads every waiver claim this season, won and lost, from the
// league's transaction log (one request per week so far).
func (c *Client) WaiverBids(ctx context.Context, l *League) ([]WaiverBid, error) {
	filter, _ := json.Marshal(map[string]any{"transactions": map[string]any{
		"filterType": map[string]any{"value": []string{"WAIVER", "WAIVER_ERROR"}},
	}})
	var out []WaiverBid
	seen := map[string]bool{}
	for w := 1; w <= l.CurrentWeek; w++ {
		var raw struct {
			Transactions []struct {
				ID              string `json:"id"`
				Type            string `json:"type"`
				Status          string `json:"status"`
				BidAmount       int    `json:"bidAmount"`
				TeamID          int    `json:"teamId"`
				ScoringPeriodID int    `json:"scoringPeriodId"`
				Items           []struct {
					Type     string `json:"type"`
					PlayerID int    `json:"playerId"`
				} `json:"items"`
			} `json:"transactions"`
		}
		u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(w, "mTransactions2")
		if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(filter)}, &raw); err != nil {
			return nil, err
		}
		for _, t := range raw.Transactions {
			if seen[t.ID] && t.ID != "" {
				continue
			}
			seen[t.ID] = true
			won := t.Type == TxnWaiver && t.Status == "EXECUTED"
			lost := t.Type == "WAIVER_ERROR" || (t.Type == TxnWaiver && strings.HasPrefix(t.Status, "FAILED"))
			if !won && !lost {
				continue // pending, cancelled
			}
			b := WaiverBid{Week: t.ScoringPeriodID, TeamID: t.TeamID, Amount: t.BidAmount, Won: won}
			if b.Week == 0 {
				b.Week = w
			}
			for _, it := range t.Items {
				if it.Type == ItemAdd {
					b.PlayerID = it.PlayerID
				}
			}
			out = append(out, b)
		}
	}
	return out, nil
}
