package espn

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
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

// TradeOffer is a pending trade another team has offered you.
type TradeOffer struct {
	ID       string
	FromTeam int   // who proposed it
	Give     []int // players they'd send you
	Get      []int // players you'd send them
	Proposed time.Time
	Expires  time.Time // zero if ESPN didn't say
}

// TradeOffers reads pending trade offers made to team `me`.
func (c *Client) TradeOffers(ctx context.Context, l *League, me int) ([]TradeOffer, error) {
	filter, _ := json.Marshal(map[string]any{"transactions": map[string]any{
		"filterType": map[string]any{"value": []string{TxnTrade}},
	}})
	var raw struct {
		Transactions []struct {
			ID             string `json:"id"`
			Type           string `json:"type"`
			Status         string `json:"status"`
			TeamID         int    `json:"teamId"`
			ProposedDate   int64  `json:"proposedDate"`
			ExpirationDate int64  `json:"expirationDate"`
			Items          []struct {
				Type       string `json:"type"`
				PlayerID   int    `json:"playerId"`
				FromTeamID int    `json:"fromTeamId"`
				ToTeamID   int    `json:"toTeamId"`
			} `json:"items"`
		} `json:"transactions"`
	}
	u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(l.CurrentWeek, "mTransactions2")
	if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(filter)}, &raw); err != nil {
		return nil, err
	}
	var out []TradeOffer
	for _, t := range raw.Transactions {
		if t.Type != TxnTrade || t.Status != "PENDING" || t.TeamID == me {
			continue
		}
		o := TradeOffer{ID: t.ID, FromTeam: t.TeamID}
		if t.ProposedDate > 0 {
			o.Proposed = time.UnixMilli(t.ProposedDate)
		}
		if t.ExpirationDate > 0 {
			o.Expires = time.UnixMilli(t.ExpirationDate)
		}
		twoTeam := true // only offers between them and me can be graded
		for _, it := range t.Items {
			switch {
			case it.Type != ItemTrade:
			case it.ToTeamID == me && it.FromTeamID == t.TeamID:
				o.Give = append(o.Give, it.PlayerID)
			case it.FromTeamID == me && it.ToTeamID == t.TeamID:
				o.Get = append(o.Get, it.PlayerID)
			default:
				twoTeam = false
			}
		}
		if twoTeam && len(o.Give)+len(o.Get) > 0 {
			out = append(out, o)
		}
	}
	return out, nil
}
