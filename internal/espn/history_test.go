package espn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/avitechnix/ffagent/internal/secrets"
)

func TestWaiverBids(t *testing.T) {
	var weeks []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") != "mTransactions2" || !strings.Contains(r.Header.Get("x-fantasy-filter"), "WAIVER_ERROR") {
			t.Errorf("request %s %s", r.URL, r.Header.Get("x-fantasy-filter"))
		}
		weeks = append(weeks, r.URL.Query().Get("scoringPeriodId"))
		// ESPN may repeat earlier transactions; they're counted once.
		w.Write([]byte(`{"transactions":[
			{"id":"a","type":"WAIVER","status":"EXECUTED","bidAmount":17,"teamId":3,"scoringPeriodId":2,
			 "items":[{"type":"ADD","playerId":11},{"type":"DROP","playerId":12}]},
			{"id":"b","type":"WAIVER_ERROR","status":"FAILED_INVALIDPLAYERSOURCE","bidAmount":9,"teamId":1,"scoringPeriodId":2,"items":[{"type":"ADD","playerId":11}]},
			{"id":"c","type":"WAIVER","status":"PENDING","bidAmount":4,"teamId":1,"scoringPeriodId":2,"items":[{"type":"ADD","playerId":13}]},
			{"id":"d","type":"WAIVER","status":"CANCELED","bidAmount":40,"teamId":5,"scoringPeriodId":2,"items":[{"type":"ADD","playerId":14}]}
		]}`))
	}))
	defer srv.Close()
	c := NewClient(1, 2026, secrets.New("s2"), secrets.New("{SW}"))
	c.ReadBase = srv.URL
	bids, err := c.WaiverBids(context.Background(), &League{CurrentWeek: 3})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(weeks, ",") != "1,2,3" {
		t.Errorf("read weeks %v", weeks)
	}
	want := []WaiverBid{{Week: 2, TeamID: 3, PlayerID: 11, Amount: 17, Won: true}, {Week: 2, TeamID: 1, PlayerID: 11, Amount: 9}}
	if len(bids) != 2 || bids[0] != want[0] || bids[1] != want[1] {
		t.Errorf("bids %+v", bids)
	}
}
