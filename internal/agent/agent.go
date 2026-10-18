// Package agent decides what to do with a fantasy team: who to start, who
// to stash on IR, who to pick up, and which trades to offer.
package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/config"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
	"github.com/avitechnix/ffagent/internal/secrets"
)

// ESPN is the subset of the ESPN client the agent uses.
type ESPN interface {
	FetchLeague(ctx context.Context) (*espn.League, error)
	FreeAgents(ctx context.Context, l *espn.League, position string, proTeams []int, limit int) ([]*espn.Player, error)
	Submit(ctx context.Context, t *espn.Transaction) (*espn.TransactionResult, error)
}

// Agent manages one fantasy team.
type Agent struct {
	ESPN ESPN
	Cfg  config.Config
	Opts analysis.Options
	Now  func() time.Time

	// Injuries is the latest injury news (see the news package), keyed by
	// player ID. It overrides ESPN fantasy statuses, which can lag, and
	// drives opportunity boosts for the next player up.
	Injuries map[int]analysis.Injury
	// RecentlyAcquired holds when the agent picked up each player; players
	// acquired within KeepNewPickupsDays are never dropped (no churn).
	RecentlyAcquired map[int]time.Time
	// OnSubmitted, when set, is called after each transaction ESPN accepts.
	OnSubmitted func(Action)
	// Confirm, when set, is asked before each move in Execute; a move only
	// runs if it returns true.
	Confirm func(Action) bool

	// The league's waiver claims this season (see loadBids).
	bids     []espn.WaiverBid
	bidsAt   time.Time
	bidsWeek int
}

// New returns an agent with default valuation options.
func New(c ESPN, cfg config.Config) *Agent {
	opts := analysis.DefaultOptions().WithTrackRecord(min(max(cfg.TrackRecordAdjustment, 0), 0.5))
	return &Agent{ESPN: c, Cfg: cfg, Opts: opts, Now: time.Now}
}

// ActionKind classifies a planned action.
type ActionKind string

const (
	ActIRActivate ActionKind = "IR-ACTIVATE"
	ActIRStash    ActionKind = "IR-STASH"
	ActDrop       ActionKind = "DROP"
	ActAdd        ActionKind = "ADD"
	ActClaim      ActionKind = "WAIVER-CLAIM"
	ActLineup     ActionKind = "LINEUP"
	ActTrade      ActionKind = "TRADE-OFFER"
)

// Action is one change the agent wants to make.
type Action struct {
	Kind    ActionKind
	Summary string
	Reason  string
	Txn     *espn.Transaction
	// Depends lists indexes (in Plan.Actions) of actions this one needs,
	// e.g. an add into a roster spot freed by an IR move.
	Depends []int
}

// Players returns the player IDs a transaction touches.
func (a Action) Players() []int {
	var ids []int
	for _, it := range a.Txn.Items {
		ids = append(ids, it.PlayerID)
	}
	return ids
}

// Signature identifies an action's effect, to recognize repeat proposals.
func (a Action) Signature() string {
	parts := []string{a.Txn.Type}
	for _, it := range a.Txn.Items {
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%s:%s:%s", it.Type, it.PlayerID,
			optInt(it.FromLineupSlotID), optInt(it.ToLineupSlotID), optInt(it.FromTeamID), optInt(it.ToTeamID)))
	}
	sort.Strings(parts[1:])
	return strings.Join(parts, "|")
}

func optInt(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

// Plan is everything the agent decided for this run.
type Plan struct {
	League   *espn.League
	Me       *espn.Team
	Roster   *espn.Team // my roster after planned IR moves and free-agent adds
	Values   map[int]*analysis.Valuation
	Matchup  *MatchupReport
	Lineup   lineup.Assignment
	Moves    []lineup.Move
	Actions  []Action // roster actions (IR, drops, adds, claims), in order
	Trades   []TradeIdea
	Warnings []string

	// The lineup as currently set on ESPN, for before/after comparisons.
	CurrentProjected float64
	CurrentWinProb   float64
}

// state is the working context for building a plan.
type state struct {
	a      *Agent
	l      *espn.League
	me     *espn.Team
	values map[int]*analysis.Valuation
	pool   []*espn.Player // available free agents / waiver players
	lambda float64        // risk preference for this week's lineup

	added          map[int]bool   // players acquired this run (never dropped again)
	irReplacements []*espn.Player // players moved to IR who need replacing
	freedBy        []int          // indexes of IR moves that opened a roster spot
	warnings       []string
	future         []futureWeek       // weeks after this one (see futureWeeks)
	weekly         map[[2]int]float64 // {player, week} -> FutureWeek
}

func (s *state) warn(msg string) { s.warnings = append(s.warnings, msg) }

func (a *Agent) myTeam(l *espn.League) (*espn.Team, error) {
	if a.Cfg.TeamID != 0 {
		if t := l.Team(a.Cfg.TeamID); t != nil {
			return t, nil
		}
		return nil, fmt.Errorf("team %d not found in league %d", a.Cfg.TeamID, l.ID)
	}
	if t := l.TeamByOwner(espn.NormalizeSWID(a.Cfg.SWID.Reveal())); t != nil {
		return t, nil
	}
	return nil, fmt.Errorf("couldn't find your team: set team_id (ESPN_TEAM_ID) or a valid SWID")
}

// Plan builds a plan without changing anything on ESPN.
func (a *Agent) Plan(ctx context.Context) (*Plan, error) {
	l, err := a.ESPN.FetchLeague(ctx)
	if err != nil && l == nil {
		return nil, err
	}
	p := &Plan{League: l}
	if err != nil {
		p.Warnings = append(p.Warnings, err.Error())
	}
	me, err := a.myTeam(l)
	if err != nil {
		return nil, err
	}
	p.Me = me

	s := a.newState(l, me)
	a.loadBids(ctx, l)
	pool, err := a.loadPool(ctx, l)
	if err != nil {
		p.Warnings = append(p.Warnings, "free agents: "+err.Error())
	}
	s.setPool(pool)

	p.CurrentProjected, p.CurrentWinProb = s.currentOutlook(me)
	work := me.Clone()
	// Set the risk preference from the current roster first so roster moves
	// are judged the same way the lineup will be.
	s.lambda = s.analyzeMatchup(work).Lambda
	p.Actions = append(p.Actions, s.planIR(work)...)
	p.Actions = append(p.Actions, s.planPickups(work)...)
	p.Roster = work

	p.Matchup = s.analyzeMatchup(work)
	s.lambda = p.Matchup.Lambda
	p.Lineup = lineup.Optimize(l, startable(work.Roster), s.weekScorer(s.lambda))
	p.Moves = lineup.Moves(work.Roster, p.Lineup)

	p.Trades = s.findTrades(work)
	p.Values = s.values
	p.Warnings = append(p.Warnings, s.warnings...)
	return p, nil
}

// newState values every rostered player, with injury news applied.
func (a *Agent) newState(l *espn.League, me *espn.Team) *state {
	s := &state{a: a, l: l, me: me, values: map[int]*analysis.Valuation{}, added: map[int]bool{}, weekly: map[[2]int]float64{}}
	for _, t := range l.Teams {
		for _, pl := range t.Roster {
			a.applyNews(pl)
			s.values[pl.ID] = analysis.Value(l, pl, a.Opts)
		}
	}
	s.applyOpportunity()
	return s
}

// setPool adds available players and re-runs opportunity boosts so backups
// in the pool are credited too.
func (s *state) setPool(pool []*espn.Player) {
	s.pool = pool
	for _, pl := range pool {
		s.a.applyNews(pl)
		s.values[pl.ID] = analysis.Value(s.l, pl, s.a.Opts)
	}
	// Recompute rostered values so boosts aren't applied twice.
	for _, t := range s.l.Teams {
		for _, pl := range t.Roster {
			s.values[pl.ID] = analysis.Value(s.l, pl, s.a.Opts)
		}
	}
	s.applyOpportunity()
}

func (s *state) applyOpportunity() {
	if len(s.a.Injuries) == 0 {
		return
	}
	var all []*espn.Player
	for _, t := range s.l.Teams {
		all = append(all, t.Roster...)
	}
	all = append(all, s.pool...)
	fresh := time.Duration(s.a.Cfg.NewsFreshHours) * time.Hour
	analysis.ApplyOpportunity(s.l, all, s.values, s.a.Injuries, s.a.Now(), fresh)
}

// applyNews records the injury report's status for valuation. ESPN
// Fantasy's own status is left alone because it governs IR eligibility, and
// players designated to return are left to ESPN Fantasy's status.
func (a *Agent) applyNews(p *espn.Player) {
	if inj, ok := a.Injuries[p.ID]; ok && inj.Status != "" && !inj.Returning {
		p.NewsStatus = inj.Status
	}
}

// freshInjuryTeams returns NFL teams with a fresh, significant injury.
func (a *Agent) freshInjuryTeams(l *espn.League) []int {
	fresh := time.Duration(a.Cfg.NewsFreshHours) * time.Hour
	seen := map[int]bool{}
	var teams []int
	for _, inj := range a.Injuries {
		if inj.ProTeamID == 0 || inj.Returning || seen[inj.ProTeamID] || a.Now().Sub(inj.Date) > fresh {
			continue
		}
		switch inj.Status {
		case espn.InjuryOut, espn.InjuryDoubtful, espn.InjuryReserve, espn.InjurySuspension:
			seen[inj.ProTeamID] = true
			teams = append(teams, inj.ProTeamID)
		}
	}
	sort.Ints(teams)
	return teams
}

func (a *Agent) loadPool(ctx context.Context, l *espn.League) ([]*espn.Player, error) {
	seen := map[int]bool{}
	var out []*espn.Player
	var firstErr error
	add := func(ps []*espn.Player, err error) {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return
		}
		for _, p := range ps {
			if !seen[p.ID] && p.OnTeamID == 0 {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
	}
	for pos := range espn.PositionSlot {
		if l.LineupSlotCounts[espn.PositionSlot[pos]] == 0 && !flexEligible(l, pos) {
			continue // league doesn't start this position
		}
		add(a.ESPN.FreeAgents(ctx, l, pos, nil, a.Cfg.FreeAgentsPerPosition))
	}
	// Backups behind freshly injured starters may not be widely owned yet,
	// which is exactly when they're worth grabbing.
	if teams := a.freshInjuryTeams(l); len(teams) > 0 {
		add(a.ESPN.FreeAgents(ctx, l, "", teams, 50))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID }) // deterministic ties
	return out, firstErr
}

func flexEligible(l *espn.League, pos string) bool {
	switch pos {
	case "RB", "WR", "TE":
		return l.LineupSlotCounts[espn.SlotFlex]+l.LineupSlotCounts[espn.SlotOP]+
			l.LineupSlotCounts[espn.SlotRBWR]+l.LineupSlotCounts[espn.SlotWRTE] > 0
	case "QB":
		return l.LineupSlotCounts[espn.SlotOP] > 0
	}
	return false
}

// startable excludes players who can't help this week's lineup because
// they are pending (e.g., a waiver claim that hasn't processed).
func startable(roster []*espn.Player) []*espn.Player {
	out := make([]*espn.Player, 0, len(roster))
	for _, p := range roster {
		if p.Status != pendingClaim {
			out = append(out, p)
		}
	}
	return out
}

// Local-only statuses for waiver claims that haven't processed yet: the
// claimed player can't start this week, and the player we'd drop still can.
const (
	pendingClaim = "PENDING_CLAIM"
	pendingDrop  = "PENDING_DROP"
)

func (s *state) val(p *espn.Player) *analysis.Valuation {
	if v, ok := s.values[p.ID]; ok {
		return v
	}
	v := analysis.Value(s.l, p, s.a.Opts)
	s.values[p.ID] = v
	return v
}

// maxRiskShift caps the boom/bust adjustment at this fraction of a player's
// projection, so it only settles close calls and can't start a player who
// projects far fewer points.
const maxRiskShift = 0.15

// weekScorer scores players for this week's lineup: expected points plus
// lambda standard deviations (lambda > 0 favors boom/bust upside).
func (s *state) weekScorer(lambda float64) lineup.Scorer {
	return func(p *espn.Player) float64 {
		v := s.val(p)
		limit := maxRiskShift * v.WeekProjection
		return v.WeekProjection + math.Max(-limit, math.Min(limit, lambda*v.WeekStdDev))
	}
}

// futureWeek is one later week and how much it counts.
type futureWeek struct {
	week   int
	weight float64
}

// futureWeeks are the weeks after this one, with fantasy-playoff weeks
// weighted by playoff_week_weight.
func (s *state) futureWeeks() []futureWeek {
	if s.future == nil {
		s.future = []futureWeek{}
		for w := s.l.CurrentWeek + 1; w <= s.l.FinalWeek; w++ {
			fw := futureWeek{week: w, weight: 1}
			if s.l.PlayoffWeek(w) {
				fw.weight = s.a.Cfg.PlayoffWeekWeight
			}
			s.future = append(s.future, fw)
		}
	}
	return s.future
}

// teamValue scores a roster: this week's best lineup (weighted), the best
// lineup in each later week (players on bye or still out score nothing
// that week, so depth that covers them counts), and a little credit for
// the bench.
func (s *state) teamValue(roster []*espn.Player, weekWeight float64) float64 {
	week := 0.0
	if weekWeight > 0 {
		// Roster moves are judged on expected points. Boom/bust upside only
		// matters for who starts, which the lineup optimizer handles.
		week = lineup.Optimize(s.l, startable(roster), s.weekScorer(0)).Total
	}
	future, irStash := s.futureRoster(roster)
	perWeek, starts := s.futureLineups(future)
	total := 0.0
	for i, fw := range s.futureWeeks() {
		total += fw.weight * perWeek[i]
	}
	var bench float64
	for _, p := range future {
		if !starts[p.ID] {
			bench += s.val(p).ROSValue
		}
	}
	return weekWeight*week + total + s.a.Cfg.BenchDepthWeight*(bench+irStash)
}

// futureRoster is the roster as it stands after this week: pending drops
// gone, this week's locks irrelevant. Players in IR slots count only as a
// stash.
func (s *state) futureRoster(roster []*espn.Player) (future []*espn.Player, irStash float64) {
	future = make([]*espn.Player, 0, len(roster))
	for _, p := range roster {
		if p.Status == pendingDrop {
			continue
		}
		if p.LineupSlot == espn.SlotIR {
			irStash += s.val(p).ROSValue
			continue
		}
		c := *p
		c.Locked = false
		future = append(future, &c)
	}
	return future, irStash
}

// futureLineups returns the best lineup's points in each later week (in
// futureWeeks order) and who starts in any of them.
//
// Most weeks don't need their own optimization. If a week's values are at
// most another solved week's, and equal for everyone who started in it
// (say, only bench players are on bye), the same lineup is still best.
// Weeks with the fewest players missing are solved first, so later weeks
// can reuse them.
func (s *state) futureLineups(future []*espn.Player) ([]float64, map[int]bool) {
	type solved struct {
		vals   []float64
		starts []bool
		total  float64
	}
	weeks := s.futureWeeks()
	vals := make([][]float64, len(weeks))
	missing := make([]int, len(weeks))
	for i, fw := range weeks {
		vals[i] = make([]float64, len(future))
		for j, p := range future {
			vals[i][j] = s.futureValue(p, fw.week)
			if vals[i][j] == 0 {
				missing[i]++
			}
		}
	}
	order := make([]int, len(weeks))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return missing[order[a]] < missing[order[b]] })

	var done []solved
	starts := map[int]bool{}
	perWeek := make([]float64, len(weeks))
	for _, i := range order {
		v := vals[i]
		pts, found := 0.0, false
		for _, d := range done {
			if reusable(v, d.vals, d.starts) {
				pts, found = d.total, true
				break
			}
		}
		if !found {
			idx := map[int]int{}
			for j, p := range future {
				idx[p.ID] = j
			}
			a := lineup.Optimize(s.l, future, func(p *espn.Player) float64 { return v[idx[p.ID]] })
			st := make([]bool, len(future))
			for j, p := range future {
				if espn.IsStartingSlot(a.Slots[p.ID]) {
					st[j] = true
					starts[p.ID] = true
				}
			}
			pts = a.Total
			done = append(done, solved{vals: v, starts: st, total: pts})
		}
		perWeek[i] = pts
	}
	return perWeek, starts
}

// reusable reports whether a solved lineup is still best for values v.
func reusable(v, solvedVals []float64, starts []bool) bool {
	for j := range v {
		if v[j] > solvedVals[j] || (starts[j] && v[j] != solvedVals[j]) {
			return false
		}
	}
	return true
}

// futureValue is FutureWeek, remembered for the plan.
func (s *state) futureValue(p *espn.Player, week int) float64 {
	k := [2]int{p.ID, week}
	if v, ok := s.weekly[k]; ok {
		return v
	}
	v := s.val(p).FutureWeek(s.l, week)
	s.weekly[k] = v
	return v
}

// quickValue is a fast estimate of teamValue without this week: one
// average-week lineup for the rest of the season. Trades are screened
// with it before being valued week by week.
func (s *state) quickValue(roster []*espn.Player) float64 {
	future, irStash := s.futureRoster(roster)
	weeks := float64(max(1, len(s.l.RemainingWeeks())))
	ros := lineup.Optimize(s.l, future, func(p *espn.Player) float64 { return s.val(p).ROSValue / weeks })
	var bench float64
	for _, p := range future {
		if !espn.IsStartingSlot(ros.Slots[p.ID]) {
			bench += s.val(p).ROSValue
		}
	}
	return float64(len(s.futureWeeks()))*ros.Total + s.a.Cfg.BenchDepthWeight*(bench+irStash)
}

// protected reports whether the user asked us never to move this player.
func (s *state) protected(p *espn.Player) bool {
	for _, x := range s.a.Cfg.ProtectedPlayers {
		x = strings.TrimSpace(x)
		if strings.EqualFold(x, p.Name) || x == strconv.Itoa(p.ID) {
			return true
		}
	}
	return false
}

func (s *state) irEligible(p *espn.Player) bool {
	for _, st := range s.a.Cfg.IREligibleStatuses {
		if strings.EqualFold(st, p.InjuryStatus) {
			return true
		}
	}
	return false
}

func activeCount(t *espn.Team) int {
	n := 0
	for _, p := range t.Roster {
		if p.LineupSlot != espn.SlotIR && p.Status != pendingDrop {
			n++
		}
	}
	return n
}

func (s *state) member() secrets.Secret { return espn.NormalizeSWIDSecret(s.a.Cfg.SWID) }
