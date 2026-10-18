// Package espn is a client for ESPN's (unofficial, undocumented) fantasy
// football v3 API. It reads league state and submits roster transactions.
package espn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/secrets"
)

const (
	DefaultReadBase  = "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl"
	DefaultWriteBase = "https://lm-api-writes.fantasy.espn.com/apis/v3/games/ffl"
	// DefaultScoreboard is ESPN's public NFL scoreboard, for live game clocks.
	DefaultScoreboard = "https://site.api.espn.com/apis/site/v2/sports/football/nfl/scoreboard"
)

// Client talks to the ESPN fantasy API for one league and season.
type Client struct {
	LeagueID  int
	Season    int
	ESPNS2    secrets.Secret // espn_s2 cookie (required for private leagues and writes)
	SWID      secrets.Secret // SWID cookie, including braces
	ReadBase  string
	WriteBase string
	// Scoreboard is the public NFL scoreboard URL ("" to skip live clocks).
	Scoreboard string
	HTTP       *http.Client
	Now        func() time.Time
}

// NewClient returns a client with default endpoints.
func NewClient(leagueID, season int, espnS2, swid secrets.Secret) *Client {
	return &Client{
		LeagueID:   leagueID,
		Season:     season,
		ESPNS2:     espnS2,
		SWID:       NormalizeSWIDSecret(swid),
		ReadBase:   DefaultReadBase,
		WriteBase:  DefaultWriteBase,
		Scoreboard: DefaultScoreboard,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Now:        time.Now,
	}
}

// NormalizeSWID ensures the SWID is wrapped in braces, as ESPN expects.
func NormalizeSWID(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.HasPrefix(s, "{") {
		s = "{" + s
	}
	if !strings.HasSuffix(s, "}") {
		s += "}"
	}
	return strings.ToUpper(s)
}

// NormalizeSWIDSecret normalizes a SWID held as a secret.
func NormalizeSWIDSecret(s secrets.Secret) secrets.Secret {
	return secrets.New(NormalizeSWID(s.Reveal()))
}

func (c *Client) leaguePath(base string) string {
	return fmt.Sprintf("%s/seasons/%d/segments/0/leagues/%d", base, c.Season, c.LeagueID)
}

func (c *Client) do(ctx context.Context, method, rawURL string, body any, headers map[string]string, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if !c.ESPNS2.Empty() {
		req.AddCookie(&http.Cookie{Name: "espn_s2", Value: c.ESPNS2.Reveal()})
	}
	if !c.SWID.Empty() {
		req.AddCookie(&http.Cookie{Name: "SWID", Value: c.SWID.Reveal()})
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("espn: %s %s: %d (ESPN rejected the credentials; refresh them with 'ffagent secrets set')", method, req.URL.Path, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("espn: %s %s: %d: %s", method, req.URL.Path, resp.StatusCode, c.scrub(truncate(string(data), 500)))
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("espn: decoding %s: %w", req.URL.Path, err)
	}
	return nil
}

// scrub removes credentials from text that might be shown, in case ESPN
// echoes request data back in an error.
func (c *Client) scrub(s string) string {
	for _, sec := range []secrets.Secret{c.ESPNS2, c.SWID} {
		if v := sec.Reveal(); v != "" {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
			s = strings.ReplaceAll(s, strings.Trim(v, "{}"), "[REDACTED]")
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func viewsQuery(scoringPeriod int, views ...string) string {
	q := url.Values{}
	for _, v := range views {
		q.Add("view", v)
	}
	if scoringPeriod > 0 {
		q.Set("scoringPeriodId", strconv.Itoa(scoringPeriod))
	}
	return q.Encode()
}

// FetchLeague loads settings, teams, rosters, schedule and pro schedule.
// Roster player stats are enriched with weekly history via the player card view.
func (c *Client) FetchLeague(ctx context.Context) (*League, error) {
	var raw rawLeague
	u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(0, "mSettings", "mStatus", "mTeam", "mRoster", "mMatchup")
	if err := c.do(ctx, http.MethodGet, u, nil, nil, &raw); err != nil {
		return nil, err
	}
	// Re-fetch rosters for the current scoring period so weekly projections
	// and lineup locks reflect this week.
	if raw.ScoringPeriodID > 0 {
		var cur rawLeague
		u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(raw.ScoringPeriodID, "mRoster")
		if err := c.do(ctx, http.MethodGet, u, nil, nil, &cur); err == nil && len(cur.Teams) > 0 {
			byID := map[int]rawTeam{}
			for _, t := range cur.Teams {
				byID[t.ID] = t
			}
			for i, t := range raw.Teams {
				if ct, ok := byID[t.ID]; ok {
					raw.Teams[i].Roster = ct.Roster
				}
			}
		}
	}
	league := raw.toLeague()

	if err := c.loadProSchedule(ctx, league); err != nil {
		// Bye weeks are helpful but not essential.
		league.ByeWeeks = map[int]int{}
	}
	c.loadLive(ctx, league) // optional: without it, game progress is estimated from kickoff times
	now := c.Now()
	for _, t := range league.Teams {
		for _, p := range t.Roster {
			if league.GameStarted(p.ProTeamID, league.CurrentWeek, now) {
				p.Locked = true
				p.GameLeft = league.GameLeft(p.ProTeamID, league.CurrentWeek, now)
			}
			p.ProTeam = league.ProTeamAbbrev(p.ProTeamID)
		}
	}

	var ids []int
	for _, t := range league.Teams {
		for _, p := range t.Roster {
			ids = append(ids, p.ID)
		}
	}
	if err := c.enrichStats(ctx, league, ids); err != nil {
		return league, fmt.Errorf("espn: loading player history: %w", err)
	}
	var roster []*Player
	for _, t := range league.Teams {
		roster = append(roster, t.Roster...)
	}
	if err := c.addReference(ctx, league, roster); err != nil {
		return league, fmt.Errorf("espn: loading projection history: %w", err)
	}
	return league, nil
}

// loadLive reads this week's game clocks from ESPN's public NFL scoreboard
// into l.Live. No cookies are sent: it's a public endpoint on another host.
// Pro team IDs there are the same as fantasy pro team IDs.
func (c *Client) loadLive(ctx context.Context, l *League) {
	if c.Scoreboard == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Scoreboard, nil)
	if err != nil {
		return
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var sb struct {
		Week   struct{ Number int } `json:"week"`
		Events []struct {
			Competitions []struct {
				Status struct {
					Period int     `json:"period"`
					Clock  float64 `json:"clock"`
					Type   struct {
						State string `json:"state"`
					} `json:"type"`
				} `json:"status"`
				Competitors []struct {
					Team struct {
						ID string `json:"id"`
					} `json:"team"`
				} `json:"competitors"`
			} `json:"competitions"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&sb); err != nil {
		return
	}
	if sb.Week.Number != 0 && sb.Week.Number != l.CurrentWeek {
		return // the scoreboard has moved on to another week
	}
	live := map[int]LiveGame{}
	for _, ev := range sb.Events {
		for _, comp := range ev.Competitions {
			st := comp.Status
			var g LiveGame
			switch st.Type.State {
			case "pre":
				g.Left = 1
			case "post":
				g.Final = true
			default:
				g.Left = gameLeft(st.Period, st.Clock)
			}
			for _, t := range comp.Competitors {
				if id, err := strconv.Atoi(t.Team.ID); err == nil {
					live[id] = g
				}
			}
		}
	}
	l.Live = live
}

// gameLeft is the fraction of regulation left at a period and game clock
// (seconds left in the period). Overtime counts as nearly over.
func gameLeft(period int, clock float64) float64 {
	if period < 1 {
		return 1
	}
	if period > 4 {
		return 0.02
	}
	left := (float64(4-period)*900 + math.Min(math.Max(clock, 0), 900)) / 3600
	return math.Max(0.02, left)
}

// referencePath is ESPN's public player data under default PPR scoring.
func (c *Client) referencePath() string {
	return fmt.Sprintf("%s/seasons/%d/segments/0/leaguedefaults/3", c.ReadBase, c.Season)
}

// addReference loads RefStats for players: weekly actuals and projections
// for every week so far, from ESPN's public player data, which (unlike a
// private league's endpoint) returns past weekly projections.
func (c *Client) addReference(ctx context.Context, l *League, players []*Player) error {
	byID := map[int][]*Player{}
	var ids []int
	for _, p := range players {
		if _, ok := byID[p.ID]; !ok {
			ids = append(ids, p.ID)
		}
		byID[p.ID] = append(byID[p.ID], p)
	}
	for len(ids) > 0 {
		batch := ids[:min(len(ids), 250)]
		ids = ids[len(batch):]
		filter := map[string]any{"players": map[string]any{
			"filterIds":                         map[string]any{"value": batch},
			"filterStatsForTopScoringPeriodIds": c.statsFilterValue(l.CurrentWeek),
		}}
		b, _ := json.Marshal(filter)
		var resp rawPlayersResponse
		u := c.referencePath() + "?" + viewsQuery(l.CurrentWeek, "kona_playercard")
		if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(b)}, &resp); err != nil {
			return err
		}
		for _, e := range resp.Players {
			var weekly []StatLine
			for _, s := range e.Player.statLines() {
				if s.Weekly {
					weekly = append(weekly, s)
				}
			}
			for _, p := range byID[e.Player.ID] {
				p.RefStats = weekly
			}
		}
	}
	return nil
}

func (c *Client) loadProSchedule(ctx context.Context, l *League) error {
	var raw rawProSchedule
	u := fmt.Sprintf("%s/seasons/%d?view=proTeamSchedules_wl", c.ReadBase, c.Season)
	if err := c.do(ctx, http.MethodGet, u, nil, nil, &raw); err != nil {
		return err
	}
	l.ByeWeeks = map[int]int{}
	l.ProGames = map[int]map[int]ProGame{}
	l.ProTeamAbbrevs = map[int]string{}
	for _, pt := range raw.Settings.ProTeams {
		l.ProTeamAbbrevs[pt.ID] = strings.ToUpper(pt.Abbrev)
		if pt.ByeWeek > 0 {
			l.ByeWeeks[pt.ID] = pt.ByeWeek
		}
		games := map[int]ProGame{}
		for wk, gs := range pt.ProGamesByScoringPeriod {
			w, err := strconv.Atoi(wk)
			if err != nil || len(gs) == 0 {
				continue
			}
			g := gs[0]
			games[w] = ProGame{Week: w, Start: time.UnixMilli(g.Date), HomeID: g.HomeProTeamID, AwayID: g.AwayProTeamID}
		}
		if len(games) > 0 {
			l.ProGames[pt.ID] = games
		}
	}
	return nil
}

// statsFilterValue asks ESPN to include, in player responses, weekly
// actuals for recent scoring periods, season totals and projections, and
// ESPN's weekly projection for every week so far. Stat line IDs are
// {source}{split}{season}[{week}]: "002026" is 2026 actual season totals,
// "102026" the 2026 season projection, "1120265" the week 5 projection.
// ESPN only returns past weekly projections when they're named this way.
func (c *Client) statsFilterValue(week int) map[string]any {
	y := strconv.Itoa(c.Season)
	ids := []string{"00" + y, "10" + y}
	for w := 1; w <= week; w++ {
		ids = append(ids, "11"+y+strconv.Itoa(w))
	}
	return map[string]any{"value": 20, "additionalValue": ids}
}

func (c *Client) enrichStats(ctx context.Context, l *League, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	filter := map[string]any{
		"players": map[string]any{
			"filterIds":                         map[string]any{"value": ids},
			"filterStatsForTopScoringPeriodIds": c.statsFilterValue(l.CurrentWeek),
		},
	}
	b, _ := json.Marshal(filter)
	var resp rawPlayersResponse
	u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(l.CurrentWeek, "kona_playercard")
	if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(b)}, &resp); err != nil {
		return err
	}
	byID := map[int][]StatLine{}
	for _, e := range resp.Players {
		byID[e.Player.ID] = e.Player.statLines()
	}
	for _, t := range l.Teams {
		for _, p := range t.Roster {
			if s, ok := byID[p.ID]; ok && len(s) > 0 {
				p.Stats = mergeStats(p.Stats, s)
			}
		}
	}
	return nil
}

func mergeStats(a, b []StatLine) []StatLine {
	type key struct {
		season, week int
		proj, weekly bool
	}
	seen := map[key]int{}
	out := append([]StatLine(nil), a...)
	for i, s := range out {
		seen[key{s.Season, s.Week, s.Projected, s.Weekly}] = i
	}
	for _, s := range b {
		k := key{s.Season, s.Week, s.Projected, s.Weekly}
		if i, ok := seen[k]; ok {
			out[i] = s
			continue
		}
		seen[k] = len(out)
		out = append(out, s)
	}
	return out
}

// FreeAgents returns available players (free agents and players on waivers)
// at the given position, sorted by ownership. Pass position "" for all, and
// proTeams to limit the search to specific NFL teams (e.g. to find the
// backup behind a newly injured starter).
func (c *Client) FreeAgents(ctx context.Context, l *League, position string, proTeams []int, limit int) ([]*Player, error) {
	players := map[string]any{
		"filterStatus":                      map[string]any{"value": []string{StatusFreeAgent, StatusWaivers}},
		"limit":                             limit,
		"sortPercOwned":                     map[string]any{"sortPriority": 1, "sortAsc": false},
		"sortDraftRanks":                    map[string]any{"sortPriority": 100, "sortAsc": true, "value": "STANDARD"},
		"filterStatsForTopScoringPeriodIds": c.statsFilterValue(l.CurrentWeek),
	}
	if slot, ok := PositionSlot[position]; ok {
		players["filterSlotIds"] = map[string]any{"value": []int{slot}}
	}
	if len(proTeams) > 0 {
		players["filterProTeamIds"] = map[string]any{"value": proTeams}
	}
	b, _ := json.Marshal(map[string]any{"players": players})
	var resp rawPlayersResponse
	u := c.leaguePath(c.ReadBase) + "?" + viewsQuery(l.CurrentWeek, "kona_player_info")
	if err := c.do(ctx, http.MethodGet, u, nil, map[string]string{"x-fantasy-filter": string(b)}, &resp); err != nil {
		return nil, err
	}
	now := c.Now()
	out := make([]*Player, 0, len(resp.Players))
	for _, e := range resp.Players {
		p := e.toPlayer(SlotUnknown, "")
		p.ProTeam = l.ProTeamAbbrev(p.ProTeamID)
		p.Locked = l.GameStarted(p.ProTeamID, l.CurrentWeek, now)
		if p.Locked {
			p.GameLeft = l.GameLeft(p.ProTeamID, l.CurrentWeek, now)
		}
		out = append(out, p)
	}
	_ = c.addReference(ctx, l, out) // optional: without it, no track record
	return out, nil
}

// --- raw API types ---

type rawLeague struct {
	ID              int `json:"id"`
	SeasonID        int `json:"seasonId"`
	ScoringPeriodID int `json:"scoringPeriodId"`
	Status          struct {
		CurrentMatchupPeriod int `json:"currentMatchupPeriod"`
		FinalScoringPeriod   int `json:"finalScoringPeriod"`
		LatestScoringPeriod  int `json:"latestScoringPeriod"`
	} `json:"status"`
	Settings struct {
		Name           string `json:"name"`
		RosterSettings struct {
			LineupSlotCounts map[string]int `json:"lineupSlotCounts"`
		} `json:"rosterSettings"`
		AcquisitionSettings struct {
			AcquisitionBudget        int  `json:"acquisitionBudget"`
			IsUsingAcquisitionBudget bool `json:"isUsingAcquisitionBudget"`
		} `json:"acquisitionSettings"`
		ScheduleSettings struct {
			MatchupPeriods map[string][]int `json:"matchupPeriods"`
		} `json:"scheduleSettings"`
	} `json:"settings"`
	Members []struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	} `json:"members"`
	Teams    []rawTeam `json:"teams"`
	Schedule []struct {
		MatchupPeriodID int `json:"matchupPeriodId"`
		Home            struct {
			TeamID      int     `json:"teamId"`
			TotalPoints float64 `json:"totalPoints"`
		} `json:"home"`
		Away struct {
			TeamID      int     `json:"teamId"`
			TotalPoints float64 `json:"totalPoints"`
		} `json:"away"`
	} `json:"schedule"`
}

type rawTeam struct {
	ID           int      `json:"id"`
	Abbrev       string   `json:"abbrev"`
	Name         string   `json:"name"`
	Location     string   `json:"location"`
	Nickname     string   `json:"nickname"`
	Owners       []string `json:"owners"`
	PrimaryOwner string   `json:"primaryOwner"`
	WaiverRank   int      `json:"waiverRank"`
	Record       struct {
		Overall struct {
			Wins      int     `json:"wins"`
			Losses    int     `json:"losses"`
			Ties      int     `json:"ties"`
			PointsFor float64 `json:"pointsFor"`
		} `json:"overall"`
	} `json:"record"`
	TransactionCounter struct {
		AcquisitionBudgetSpent int `json:"acquisitionBudgetSpent"`
	} `json:"transactionCounter"`
	Roster struct {
		Entries []struct {
			PlayerID        int          `json:"playerId"`
			LineupSlotID    int          `json:"lineupSlotId"`
			InjuryStatus    string       `json:"injuryStatus"`
			PlayerPoolEntry rawPoolEntry `json:"playerPoolEntry"`
		} `json:"entries"`
	} `json:"roster"`
}

type rawPoolEntry struct {
	ID           int       `json:"id"`
	OnTeamID     int       `json:"onTeamId"`
	Status       string    `json:"status"`
	LineupLocked bool      `json:"lineupLocked"`
	Player       rawPlayer `json:"player"`
}

type rawPlayer struct {
	ID                int       `json:"id"`
	FullName          string    `json:"fullName"`
	DefaultPositionID int       `json:"defaultPositionId"`
	EligibleSlots     []int     `json:"eligibleSlots"`
	ProTeamID         int       `json:"proTeamId"`
	Injured           bool      `json:"injured"`
	InjuryStatus      string    `json:"injuryStatus"`
	Stats             []rawStat `json:"stats"`
	Ownership         struct {
		PercentOwned  float64 `json:"percentOwned"`
		PercentChange float64 `json:"percentChange"`
	} `json:"ownership"`
}

type rawStat struct {
	SeasonID        int     `json:"seasonId"`
	ScoringPeriodID int     `json:"scoringPeriodId"`
	StatSourceID    int     `json:"statSourceId"`
	StatSplitTypeID int     `json:"statSplitTypeId"`
	AppliedTotal    float64 `json:"appliedTotal"`
	AppliedAverage  float64 `json:"appliedAverage"`
}

type rawPlayersResponse struct {
	Players []rawPoolEntry `json:"players"`
}

type rawProSchedule struct {
	Settings struct {
		ProTeams []struct {
			ID                      int    `json:"id"`
			Abbrev                  string `json:"abbrev"`
			ByeWeek                 int    `json:"byeWeek"`
			ProGamesByScoringPeriod map[string][]struct {
				Date          int64 `json:"date"`
				HomeProTeamID int   `json:"homeProTeamId"`
				AwayProTeamID int   `json:"awayProTeamId"`
			} `json:"proGamesByScoringPeriod"`
		} `json:"proTeams"`
	} `json:"settings"`
}

func (rp rawPlayer) statLines() []StatLine {
	out := make([]StatLine, 0, len(rp.Stats))
	for _, s := range rp.Stats {
		if s.StatSplitTypeID != statSplitSeason && s.StatSplitTypeID != statSplitWeek {
			continue
		}
		if s.StatSourceID != statSourceActual && s.StatSourceID != statSourceProjected {
			continue
		}
		weekly := s.StatSplitTypeID == statSplitWeek
		wk := 0
		if weekly {
			wk = s.ScoringPeriodID
		}
		out = append(out, StatLine{
			Season:    s.SeasonID,
			Week:      wk,
			Projected: s.StatSourceID == statSourceProjected,
			Weekly:    weekly,
			Points:    s.AppliedTotal,
			Average:   s.AppliedAverage,
		})
	}
	return out
}

func (e rawPoolEntry) toPlayer(slot int, injury string) *Player {
	rp := e.Player
	status := rp.InjuryStatus
	if status == "" {
		status = injury
	}
	if status == "" || status == "NORMAL" {
		status = InjuryActive
	}
	ps := e.Status
	if ps == "" && e.OnTeamID > 0 {
		ps = StatusOnTeam
	}
	return &Player{
		ID:            rp.ID,
		Name:          rp.FullName,
		Position:      PositionName(rp.DefaultPositionID),
		ProTeamID:     rp.ProTeamID,
		ProTeam:       proTeamAbbrevs[rp.ProTeamID],
		EligibleSlots: rp.EligibleSlots,
		InjuryStatus:  status,
		Injured:       rp.Injured,
		PercentOwned:  rp.Ownership.PercentOwned,
		PercentChange: rp.Ownership.PercentChange,
		OnTeamID:      e.OnTeamID,
		Status:        ps,
		LineupSlot:    slot,
		Locked:        e.LineupLocked,
		Stats:         rp.statLines(),
	}
}

func (r rawLeague) toLeague() *League {
	l := &League{
		ID:                   r.ID,
		Name:                 r.Settings.Name,
		Season:               r.SeasonID,
		CurrentWeek:          r.ScoringPeriodID,
		CurrentMatchupPeriod: r.Status.CurrentMatchupPeriod,
		FinalWeek:            r.Status.FinalScoringPeriod,
		LineupSlotCounts:     map[int]int{},
		MatchupPeriods:       map[int][]int{},
		UsesFAAB:             r.Settings.AcquisitionSettings.IsUsingAcquisitionBudget,
		FAABBudget:           r.Settings.AcquisitionSettings.AcquisitionBudget,
		Members:              map[string]string{},
		ByeWeeks:             map[int]int{},
		ProGames:             map[int]map[int]ProGame{},
	}
	if l.FinalWeek == 0 {
		l.FinalWeek = 18
	}
	if l.CurrentMatchupPeriod == 0 {
		l.CurrentMatchupPeriod = l.CurrentWeek
	}
	for k, v := range r.Settings.RosterSettings.LineupSlotCounts {
		id, err := strconv.Atoi(k)
		if err == nil && v > 0 {
			l.LineupSlotCounts[id] = v
		}
	}
	for k, v := range r.Settings.ScheduleSettings.MatchupPeriods {
		if id, err := strconv.Atoi(k); err == nil {
			l.MatchupPeriods[id] = v
		}
	}
	for _, m := range r.Members {
		l.Members[NormalizeSWID(m.ID)] = m.DisplayName
	}
	for _, rt := range r.Teams {
		name := rt.Name
		if name == "" {
			name = strings.TrimSpace(rt.Location + " " + rt.Nickname)
		}
		t := &Team{
			ID:         rt.ID,
			Abbrev:     rt.Abbrev,
			Name:       name,
			WaiverRank: rt.WaiverRank,
			Wins:       rt.Record.Overall.Wins,
			Losses:     rt.Record.Overall.Losses,
			Ties:       rt.Record.Overall.Ties,
			PointsFor:  rt.Record.Overall.PointsFor,
		}
		for _, o := range rt.Owners {
			t.Owners = append(t.Owners, NormalizeSWID(o))
		}
		if l.UsesFAAB {
			t.FAABRemaining = l.FAABBudget - rt.TransactionCounter.AcquisitionBudgetSpent
		}
		for _, e := range rt.Roster.Entries {
			pe := e.PlayerPoolEntry
			if pe.Player.ID == 0 {
				pe.Player.ID = e.PlayerID
			}
			if pe.OnTeamID == 0 {
				pe.OnTeamID = rt.ID
			}
			p := pe.toPlayer(e.LineupSlotID, e.InjuryStatus)
			p.Status = StatusOnTeam
			t.Roster = append(t.Roster, p)
		}
		l.Teams = append(l.Teams, t)
	}
	for _, m := range r.Schedule {
		l.Schedule = append(l.Schedule, Matchup{
			Period:     m.MatchupPeriodID,
			HomeTeamID: m.Home.TeamID,
			AwayTeamID: m.Away.TeamID,
			HomePoints: m.Home.TotalPoints,
			AwayPoints: m.Away.TotalPoints,
		})
	}
	return l
}
