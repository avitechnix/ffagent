package espn

import (
	"sort"
	"time"
)

// StatLine is one fantasy-point line for a player: a season total or a
// single week, either actual or projected. Points are already scored with
// the league's scoring settings.
type StatLine struct {
	Season    int
	Week      int // 0 for season-level lines
	Projected bool
	Weekly    bool
	Points    float64
	Average   float64
}

// Player is a player in the league player pool, possibly on a fantasy roster.
type Player struct {
	ID            int
	Name          string
	Position      string
	ProTeamID     int
	ProTeam       string
	EligibleSlots []int
	InjuryStatus  string // ESPN Fantasy's designation; governs IR eligibility
	NewsStatus    string // fresher designation from the injury report, if any
	Injured       bool
	PercentOwned  float64
	PercentChange float64

	// Roster context.
	OnTeamID   int    // 0 when unrostered
	Status     string // FREEAGENT, WAIVERS, ONTEAM
	LineupSlot int    // SlotUnknown when not on a roster
	Locked     bool   // game started; lineup slot can't change
	// GameLeft is the fraction of this week's game still to play once it
	// has started (Locked): 0 when it's over.
	GameLeft float64

	Stats []StatLine
	// RefStats are the same player's weekly actuals and projections under
	// ESPN's default PPR scoring, from its public player data. They're only
	// used to compare a player with his projections in weeks the league
	// didn't return a projection for (private leagues often return only the
	// latest week's).
	RefStats []StatLine
}

// EffectiveInjury is the best current view of a player's availability:
// the injury report when it has news, else ESPN Fantasy's status. Use it for
// projections and lineups; use InjuryStatus for IR eligibility, which ESPN
// enforces from its own status.
func (p *Player) EffectiveInjury() string {
	if p.NewsStatus != "" {
		return p.NewsStatus
	}
	return p.InjuryStatus
}

// CanPlay reports whether the player is eligible for the given lineup slot.
func (p *Player) CanPlay(slot int) bool {
	for _, s := range p.EligibleSlots {
		if s == slot {
			return true
		}
	}
	return false
}

func (p *Player) find(season, week int, projected, weekly bool) (StatLine, bool) {
	for _, s := range p.Stats {
		if s.Season == season && s.Projected == projected && s.Weekly == weekly && (!weekly || s.Week == week) {
			return s, true
		}
	}
	return StatLine{}, false
}

// RefWeek returns a week's reference actual and projection (see RefStats).
func (p *Player) RefWeek(season, week int) (actual, projected float64, ok bool) {
	var okA, okP bool
	for _, s := range p.RefStats {
		if s.Season != season || !s.Weekly || s.Week != week {
			continue
		}
		if s.Projected {
			projected, okP = s.Points, true
		} else {
			actual, okA = s.Points, true
		}
	}
	return actual, projected, okA && okP
}

// WeekActual returns actual points scored in a week.
func (p *Player) WeekActual(season, week int) (float64, bool) {
	s, ok := p.find(season, week, false, true)
	return s.Points, ok
}

// WeekProjection returns ESPN's projection for a week.
func (p *Player) WeekProjection(season, week int) (float64, bool) {
	s, ok := p.find(season, week, true, true)
	return s.Points, ok
}

// SeasonActual returns the season-to-date actual line.
func (p *Player) SeasonActual(season int) (StatLine, bool) {
	return p.find(season, 0, false, false)
}

// SeasonProjection returns ESPN's full-season projection line.
func (p *Player) SeasonProjection(season int) (StatLine, bool) {
	return p.find(season, 0, true, false)
}

// Team is a fantasy team.
type Team struct {
	ID            int
	Abbrev        string
	Name          string
	Logo          string // image URL the team's manager chose; may be on any host
	Owners        []string
	WaiverRank    int
	FAABRemaining int
	Wins          int
	Losses        int
	Ties          int
	PointsFor     float64
	Roster        []*Player
}

// Player returns the rostered player with the given ID.
func (t *Team) Player(id int) *Player {
	for _, p := range t.Roster {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Remove drops a player from the local roster model.
func (t *Team) Remove(id int) {
	out := t.Roster[:0]
	for _, p := range t.Roster {
		if p.ID != id {
			out = append(out, p)
		}
	}
	t.Roster = out
}

// Clone returns a copy of the team with a shallow-copied roster slice and
// copies of each player, so lineup slots can be changed independently.
func (t *Team) Clone() *Team {
	c := *t
	c.Roster = make([]*Player, len(t.Roster))
	for i, p := range t.Roster {
		pc := *p
		c.Roster[i] = &pc
	}
	return &c
}

// Matchup is one head-to-head game in the fantasy schedule.
type Matchup struct {
	Period     int
	HomeTeamID int
	AwayTeamID int
	HomePoints float64
	AwayPoints float64
}

// ProGame is one NFL game.
type ProGame struct {
	ID     int // ESPN game (event) ID
	Week   int
	Start  time.Time
	HomeID int
	AwayID int
}

// League is a snapshot of an ESPN fantasy football league.
type League struct {
	ID                   int
	Name                 string
	Season               int
	CurrentWeek          int // scoring period
	CurrentMatchupPeriod int
	FinalWeek            int
	LineupSlotCounts     map[int]int
	MatchupPeriods       map[int][]int // matchup period -> scoring periods
	RegularSeason        int           // matchup periods before the fantasy playoffs (0 = unknown)
	PlayoffTeams         int
	UsesFAAB             bool
	FAABBudget           int
	Teams                []*Team
	Members              map[string]string // SWID -> display name
	Schedule             []Matchup
	ByeWeeks             map[int]int // pro team -> bye week
	ProGames             map[int]map[int]ProGame
	ProTeamAbbrevs       map[int]string
	Live                 map[int]LiveGame // pro team -> this week's game progress, when known
}

// ProTeamAbbrev returns the NFL team abbreviation for a pro team ID.
func (l *League) ProTeamAbbrev(id int) string {
	if a, ok := l.ProTeamAbbrevs[id]; ok && a != "" {
		return a
	}
	return proTeamAbbrevs[id]
}

// Team returns the fantasy team with the given ID.
func (l *League) Team(id int) *Team {
	for _, t := range l.Teams {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// TeamByOwner returns the team owned by the given SWID.
func (l *League) TeamByOwner(swid string) *Team {
	for _, t := range l.Teams {
		for _, o := range t.Owners {
			if o == swid {
				return t
			}
		}
	}
	return nil
}

// Opponent returns the opposing team ID for a team in a matchup period.
func (l *League) Opponent(teamID, period int) (int, bool) {
	for _, m := range l.Schedule {
		if m.Period != period {
			continue
		}
		if m.HomeTeamID == teamID && m.AwayTeamID != 0 {
			return m.AwayTeamID, true
		}
		if m.AwayTeamID == teamID {
			return m.HomeTeamID, true
		}
	}
	return 0, false
}

// RosterSize is the maximum number of non-IR players on a roster.
func (l *League) RosterSize() int {
	n := 0
	for slot, c := range l.LineupSlotCounts {
		if slot != SlotIR {
			n += c
		}
	}
	return n
}

// StartingSlots returns the expanded list of starting slots, ordered by slot ID.
func (l *League) StartingSlots() []int {
	ids := make([]int, 0, len(l.LineupSlotCounts))
	for id := range l.LineupSlotCounts {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var slots []int
	for _, id := range ids {
		if !IsStartingSlot(id) {
			continue
		}
		for i := 0; i < l.LineupSlotCounts[id]; i++ {
			slots = append(slots, id)
		}
	}
	return slots
}

// OnBye reports whether a pro team is on bye in a given week.
func (l *League) OnBye(proTeamID, week int) bool {
	if proTeamID == 0 {
		return false
	}
	if bye, ok := l.ByeWeeks[proTeamID]; ok && bye == week {
		return true
	}
	if games, ok := l.ProGames[proTeamID]; ok && len(games) > 0 {
		_, plays := games[week]
		return !plays
	}
	return false
}

// LiveGame is an NFL game's progress this week.
type LiveGame struct {
	Left  float64 // fraction of the game still to play: 1 before kickoff, 0 when final
	Final bool
}

// GameLeft returns the fraction of a pro team's game this week still to
// play. Without live data it estimates from the kickoff time, assuming
// about 3 hours 10 minutes of real time per game.
func (l *League) GameLeft(proTeamID, week int, now time.Time) float64 {
	if g, ok := l.Live[proTeamID]; ok {
		return g.Left
	}
	g, ok := l.ProGames[proTeamID][week]
	if !ok || g.Start.IsZero() || now.Before(g.Start) {
		return 1
	}
	return max(0, 1-now.Sub(g.Start).Minutes()/190)
}

// AnyGameLive reports whether some NFL game this week is in progress.
func (l *League) AnyGameLive(now time.Time) bool {
	for team := range l.ProGames {
		if l.GameStarted(team, l.CurrentWeek, now) {
			if left := l.GameLeft(team, l.CurrentWeek, now); left > 0 {
				return true
			}
		}
	}
	return false
}

// GameStarted reports whether a pro team's game for the week has kicked off.
func (l *League) GameStarted(proTeamID, week int, now time.Time) bool {
	g, ok := l.ProGames[proTeamID][week]
	return ok && !g.Start.IsZero() && !now.Before(g.Start)
}

// PlayoffWeek reports whether a scoring period is in the fantasy playoffs.
func (l *League) PlayoffWeek(week int) bool {
	if l.RegularSeason <= 0 {
		return false
	}
	for mp, weeks := range l.MatchupPeriods {
		if mp > l.RegularSeason {
			for _, w := range weeks {
				if w == week {
					return true
				}
			}
		}
	}
	return false
}

// RemainingWeeks returns the regular and playoff scoring periods from the
// current week through the final week, inclusive.
func (l *League) RemainingWeeks() []int {
	var weeks []int
	for w := l.CurrentWeek; w <= l.FinalWeek; w++ {
		weeks = append(weeks, w)
	}
	return weeks
}
