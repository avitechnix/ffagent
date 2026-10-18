package espn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/secrets"
)

func TestBacktestData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/summary" && r.URL.Query().Get("event") == "401":
			w.Write([]byte(`{"pickcenter":[{"provider":{"name":"Nobody"}},{"provider":{"name":"DraftKings"},"overUnder":47.5,"spread":-3.5}]}`))
		case r.URL.Path == "/summary":
			w.Write([]byte(`{"pickcenter":[]}`))
		case strings.HasSuffix(r.URL.Path, "/leaguedefaults/3"):
			if f := r.Header.Get("x-fantasy-filter"); !strings.Contains(f, `"1120262"`) || !strings.Contains(f, `"0020262"`) {
				t.Errorf("filter doesn't ask for week 2 lines: %s", f)
			}
			w.Write([]byte(`{"players":[{"player":{"id":7,"fullName":"A Back","defaultPositionId":2,"proTeamId":12,"stats":[
				{"seasonId":2026,"scoringPeriodId":2,"statSourceId":1,"statSplitTypeId":1,"appliedTotal":14.2},
				{"seasonId":2026,"scoringPeriodId":2,"statSourceId":0,"statSplitTypeId":1,"appliedTotal":20.5},
				{"seasonId":2025,"scoringPeriodId":2,"statSourceId":0,"statSplitTypeId":1,"appliedTotal":3},
				{"seasonId":2026,"scoringPeriodId":0,"statSourceId":1,"statSplitTypeId":0,"appliedTotal":240}]}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := NewClient(1, 2026, secrets.Secret{}, secrets.Secret{})
	c.ReadBase, c.Scoreboard = srv.URL, srv.URL+"/scoreboard"
	ctx := context.Background()

	lines, err := c.GameLines(ctx, []int{401, 402})
	if err != nil {
		t.Fatal(err)
	}
	ln, ok := lines[401]
	if len(lines) != 1 || !ok || ln.OverUnder != 47.5 || ln.Spread != -3.5 || ln.Provider != "DraftKings" {
		t.Fatalf("lines %+v", lines)
	}
	if h, a := ln.Implied(); h != 25.5 || a != 22 {
		t.Errorf("implied %.1f-%.1f, want 25.5-22", h, a)
	}

	pool, err := c.ReferencePool(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pool) != 1 || len(pool[0].RefStats) != 2 {
		t.Fatalf("pool %+v", pool)
	}
	if a, p, ok := pool[0].RefWeek(2026, 2); !ok || a != 20.5 || p != 14.2 {
		t.Errorf("week 2: %.1f actual, %.1f projected", a, p)
	}
}

func TestCompletedWeeks(t *testing.T) {
	sun := time.Date(2026, 9, 13, 17, 0, 0, 0, time.UTC)
	l := &League{ProGames: map[int]map[int]ProGame{
		1: {1: {Start: sun}, 2: {Start: sun.AddDate(0, 0, 7)}},
		2: {1: {Start: sun.Add(8 * time.Hour)}, 3: {Start: sun.AddDate(0, 0, 14)}}, // Monday night; bye in week 2
	}}
	for _, c := range []struct {
		now  time.Time
		want int
	}{
		{sun.Add(6 * time.Hour), 0}, // Monday's game isn't over
		{sun.Add(14 * time.Hour), 1},
		{sun.AddDate(0, 0, 8), 2},
		{sun.AddDate(0, 0, 30), 3},
	} {
		if got := CompletedWeeks(l, c.now); got != c.want {
			t.Errorf("at %s: %d, want %d", c.now, got, c.want)
		}
	}
}
