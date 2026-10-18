package agent

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/config"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/secrets"
)

// replayESPN serves recorded ESPN data: real 2026 projections and results
// for the players in a week 6 matchup, with a private league's limits
// (only the latest weekly projections in league responses) and the live
// scores and game clocks from 12:23 CT on Sunday of week 6.
func replayESPN(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	file := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		views := r.URL.Query()["view"]
		var body []byte
		switch {
		case r.URL.Path == "/scoreboard":
			body = file("scoreboard.json")
		case strings.Contains(r.URL.Path, "/leaguedefaults/"):
			body = file("reference.json")
		case len(views) == 1 && views[0] == "proTeamSchedules_wl":
			body = file("proschedule.json")
		case len(views) == 1 && views[0] == "kona_playercard":
			body = file("playercard.json")
		case len(views) == 1 && views[0] == "kona_player_info":
			body = []byte(`{"players":[]}`)
		default:
			body = file("league.json")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
}

// TestReplayWeek6 runs the whole pipeline (ESPN client, valuation, live
// projections, matchup) on recorded real data and checks it against what
// ESPN's app showed at the same moment. Each number here was a bug once.
func TestReplayWeek6(t *testing.T) {
	srv := replayESPN(t, "week6")
	defer srv.Close()
	at := time.Date(2026, 10, 18, 17, 23, 0, 0, time.UTC)
	c := espn.NewClient(1, 2026, secrets.New("s2"), secrets.New("{ME-SWID}"))
	c.ReadBase, c.WriteBase, c.Scoreboard = srv.URL, srv.URL, srv.URL+"/scoreboard"
	c.Now = func() time.Time { return at }
	cfg := config.Default()
	cfg.LeagueID, cfg.TeamID, cfg.Season, cfg.SWID = 1, 1, 2026, secrets.New("{ME-SWID}")
	a := New(c, cfg)
	a.Now = func() time.Time { return at }

	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// What the ESPN app showed under each starter's score (live projections).
	app := map[string]float64{
		"Drake Maye": 17.28, "Braelon Allen": 14.25, "Tony Pollard": 13.46, "CeeDee Lamb": 18.61,
		"Tetairoa McMillan": 21.06, "Brock Bowers": 19.77, "DK Metcalf": 10.2, "Jaguars D/ST": -2.0, "Dominic Zvada": 10.14,
		"Bryce Young": 17.76, "Jonathan Taylor": 16.65, "Bucky Irving": 15.45, "Malik Nabers": 15.33,
		"Christian Watson": 16.33, "Isaiah Likely": 19.54, "Parker Washington": 10.4, "Patriots D/ST": 7.39, "Spencer Shrader": 9.23,
	}
	for _, team := range p.League.Teams {
		for _, pl := range team.Roster {
			want, ok := app[pl.Name]
			if !ok {
				continue
			}
			if got := p.Values[pl.ID].WeekProjection; math.Abs(got-want) > 0.02 {
				t.Errorf("%s: agent %.2f, ESPN app %.2f", pl.Name, got, want)
			}
		}
	}
	// Team totals as ESPN showed them, with each lineup as set.
	if math.Abs(p.CurrentProjected-122.8) > 0.1 {
		t.Errorf("my projected total %.1f, ESPN 122.8", p.CurrentProjected)
	}
	if math.Abs(p.Matchup.OppProjected-128.1) > 0.1 {
		t.Errorf("opponent projected %.1f, ESPN 128.1", p.Matchup.OppProjected)
	}
	// Track records from every week's projection, not one game.
	for name, acc := range map[string]float64{"CeeDee Lamb": 1.30, "Drake Maye": 0.98, "Tony Pollard": 0.75} {
		for _, pl := range p.Me.Roster {
			if pl.Name == name {
				v := p.Values[pl.ID]
				if v.ProjectedWeeks != 5 || math.Abs(v.Accuracy-acc) > 0.01 {
					t.Errorf("%s: %.2f of ESPN over %d weeks, want %.2f over 5", name, v.Accuracy, v.ProjectedWeeks, acc)
				}
			}
		}
	}
	if probs := planProblems(p, 0, at); len(probs) != 0 {
		t.Errorf("health check on good data: %v", probs)
	}
}
