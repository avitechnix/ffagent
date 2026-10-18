package agent

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/espn"
	"github.com/avitechnix/ffagent/internal/lineup"
)

// Snapshot is everything the phone app shows, copied out under the
// watcher's lock so web handlers never touch live state.
type Snapshot struct {
	Status  AppStatus  `json:"status"`
	Pending []AppItem  `json:"pending"`
	History []AppItem  `json:"history"`
	Roster  []AppRow   `json:"roster"`
	Lineup  []AppStart `json:"lineup"`
	// OppLineup is the opponent's lineup as set on ESPN.
	OppLineup []AppStart `json:"opp_lineup,omitempty"`
	// Chat is the Ask tab's recent questions and answers, oldest first.
	Chat []AppQA `json:"chat,omitempty"`
	// Offers are pending trade offers other teams made you, graded.
	Offers []AppOffer `json:"offers,omitempty"`
}

// AppOffer is a trade someone offered you, with the agent's read on it.
type AppOffer struct {
	ID        string      `json:"id"`
	Partner   string      `json:"partner"`
	PartnerID int         `json:"partner_id"`
	Give      []AppPlayer `json:"give"` // what they'd send you
	Get       []AppPlayer `json:"get"`  // what you'd send them
	Grade     string      `json:"grade"`
	Verdict   string      `json:"verdict"`
	Reason    string      `json:"reason"`
	Counter   string      `json:"counter,omitempty"` // a better version, sent if you approve it in Moves
	Expires   time.Time   `json:"expires,omitempty"`
	Link      string      `json:"link"` // your team on ESPN, to accept or decline
}

// AppPlayer is a player in a trade.
type AppPlayer struct {
	ID       int     `json:"id"`
	Name     string  `json:"name"`
	Position string  `json:"position"`
	Team     string  `json:"team"`
	ROS      float64 `json:"ros"`
}

// AppStatus is the header: team, matchup, and what the agent is doing.
type AppStatus struct {
	League       string    `json:"league"`
	Week         int       `json:"week"`
	Team         string    `json:"team"`
	Record       string    `json:"record"`
	Opponent     string    `json:"opponent,omitempty"`
	MyProjected  float64   `json:"my_projected"`
	OppProjected float64   `json:"opp_projected"`
	WinProb      float64   `json:"win_prob"`
	Strategy     string    `json:"strategy,omitempty"`
	Mode         string    `json:"mode"` // approve, auto, dry-run
	LastCheck    time.Time `json:"last_check"`
	PlannedAt    time.Time `json:"planned_at"`
	Ready        bool      `json:"ready"` // false until the first plan is made
	AskEnabled   bool      `json:"ask_enabled"`
	Health       []string  `json:"health,omitempty"` // problems the agent's data checks found
	MyTeamID     int       `json:"my_team_id"`
	OppTeamID    int       `json:"opp_team_id,omitempty"`
	OppRecord    string    `json:"opp_record,omitempty"`
	MyActual     float64   `json:"my_actual"`  // starters' points so far
	OppActual    float64   `json:"opp_actual"` // opponent starters' points so far
}

// AppItem is one proposed move and what happened to it.
type AppItem struct {
	ID      int       `json:"id"`
	Number  int       `json:"number"`
	Batch   string    `json:"batch"`
	Kind    string    `json:"kind"`
	Label   string    `json:"label"`
	Summary string    `json:"summary"`
	Reason  string    `json:"reason"`
	Needs   []string  `json:"needs,omitempty"` // summaries of moves it depends on
	Status  string    `json:"status"`
	Result  string    `json:"result,omitempty"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires,omitempty"`
	Decided time.Time `json:"decided,omitempty"`
	Via     string    `json:"via,omitempty"`
	Thread  []AppQA   `json:"thread,omitempty"`  // follow-up questions about this move
	Players []int     `json:"players,omitempty"` // ESPN IDs in the move, in transaction order (added before dropped)
}

// AppQA is one follow-up question and Claude's answer.
type AppQA struct {
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
	Via      string    `json:"via"`
	At       time.Time `json:"at"`
}

// AppRow is one roster player's analysis.
type AppRow struct {
	ID       int       `json:"id"` // ESPN player ID (negative for a D/ST)
	Slot     string    `json:"slot"`
	Name     string    `json:"name"`
	Position string    `json:"position"`
	Team     string    `json:"team"`
	Injury   string    `json:"injury,omitempty"`
	ESPN     float64   `json:"espn"`
	Proj     float64   `json:"proj"`
	StdDev   float64   `json:"sd"`
	Accuracy float64   `json:"accuracy,omitempty"`  // actual/projected; 0 when unknown
	AccWeeks int       `json:"acc_weeks,omitempty"` // weeks behind Accuracy
	Opp      string    `json:"opp,omitempty"`       // this week's NFL opponent: "NYJ" at home, "@NYJ" away, "BYE"
	Kickoff  time.Time `json:"kickoff,omitempty"`   // this week's game
	Avg      float64   `json:"avg"`
	Last3    float64   `json:"last3"`
	ROS      float64   `json:"ros"`
	Notes    []string  `json:"notes,omitempty"`
}

// AppStart is one starter in a lineup.
type AppStart struct {
	ID       int       `json:"id"`
	Slot     string    `json:"slot"`
	Name     string    `json:"name"`
	Position string    `json:"position"`
	Team     string    `json:"team"`
	Proj     float64   `json:"proj"`   // expected final points (live once his game starts)
	Actual   float64   `json:"actual"` // points so far this week
	Locked   bool      `json:"locked"` // game started
	Left     float64   `json:"left"`   // fraction of his game still to play
	Opp      string    `json:"opp,omitempty"`
	Kickoff  time.Time `json:"kickoff,omitempty"`
}

// Snapshot returns the current state for the phone app.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Agent.Now()
	var s Snapshot
	s.Status.Mode = "dry-run"
	if w.Apply {
		s.Status.Mode = "approve"
		if !w.emailApprovals() {
			s.Status.Mode = "auto"
		}
	}
	s.Status.LastCheck = w.lastTick
	s.Status.AskEnabled = w.Asker != nil
	s.Status.Health = w.health.Problems

	if st := w.approvals; st != nil {
		for _, it := range st.Pending() {
			s.Pending = append(s.Pending, appItem(st, it))
		}
		for _, it := range st.Approved() { // approved but waiting on a dependency
			s.Pending = append(s.Pending, appItem(st, it))
		}
		for _, it := range st.Since(now.Add(-7 * 24 * time.Hour)) {
			if it.Status != approval.Pending && it.Status != approval.Approved {
				s.History = append(s.History, appItem(st, it))
			}
		}
		chat := st.Chat
		if len(chat) > 30 {
			chat = chat[len(chat)-30:]
		}
		for _, qa := range chat {
			s.Chat = append(s.Chat, AppQA{Question: qa.Question, Answer: qa.Answer, Via: qa.Via, At: qa.At})
		}
		sort.Slice(s.Pending, func(i, j int) bool { return s.Pending[i].ID < s.Pending[j].ID })
		sort.Slice(s.History, func(i, j int) bool { return s.History[i].ID > s.History[j].ID })
	}

	p := w.latest
	if p == nil {
		return s
	}
	s.Status.Ready, s.Status.PlannedAt = true, w.latestAt
	s.Status.League, s.Status.Week, s.Status.Team = p.League.Name, p.League.CurrentWeek, p.Me.Name
	s.Status.Record, s.Status.MyTeamID = recordString(p.Me), p.Me.ID
	if m := p.Matchup; m != nil {
		s.Status.MyProjected, s.Status.WinProb, s.Status.Strategy = m.MyProjected, m.WinProb, m.Strategy
		if m.Opponent != nil {
			s.Status.Opponent, s.Status.OppProjected = m.Opponent.Name, m.OppProjected
			s.Status.OppTeamID, s.Status.OppRecord = m.Opponent.ID, recordString(m.Opponent)
		}
	}
	for _, r := range p.Offers {
		o := AppOffer{ID: r.Offer.ID, Partner: r.Partner.Name, PartnerID: r.Partner.ID, Grade: r.Grade, Verdict: r.Verdict,
			Reason: r.Reason, Expires: r.Offer.Expires, Link: w.espnTeamURL(p)}
		for _, pl := range r.Give {
			o.Give = append(o.Give, appPlayer(p, pl))
		}
		for _, pl := range r.Get {
			o.Get = append(o.Get, appPlayer(p, pl))
		}
		if r.Counter != nil {
			o.Counter = r.Counter.Action.Summary
		}
		s.Offers = append(s.Offers, o)
	}
	roster := append([]*espn.Player(nil), p.Me.Roster...)
	sort.SliceStable(roster, func(i, j int) bool { return slotOrder(roster[i].LineupSlot) < slotOrder(roster[j].LineupSlot) })
	for _, pl := range roster {
		v := p.Values[pl.ID]
		if v == nil {
			continue
		}
		row := AppRow{
			ID: pl.ID, Slot: espn.SlotName(pl.LineupSlot), Name: pl.Name, Position: pl.Position, Team: pl.ProTeam,
			ESPN: v.ESPNWeekProjection, Proj: v.WeekProjection, StdDev: v.WeekStdDev,
			Avg: v.SeasonAvg, Last3: v.RecentAvg, ROS: v.ROSValue, Notes: v.Notes,
		}
		row.Opp, row.Kickoff = gameInfo(p.League, pl)
		if st := pl.EffectiveInjury(); st != espn.InjuryActive {
			row.Injury = st
		}
		if v.ProjectedTotal > 0 {
			row.Accuracy, row.AccWeeks = v.Accuracy, v.ProjectedWeeks
		}
		s.Roster = append(s.Roster, row)
	}
	if p.Roster != nil {
		s.Lineup, s.Status.MyActual = starters(p, p.Roster.Roster, p.Lineup)
	}
	if m := p.Matchup; m != nil && m.Opponent != nil {
		s.OppLineup, s.Status.OppActual = starters(p, m.Opponent.Roster, m.OppLineup)
	}
	return s
}

// starters lists a lineup's starters in slot order, with their points so far.
func starters(p *Plan, roster []*espn.Player, a lineup.Assignment) ([]AppStart, float64) {
	var out []AppStart
	var actual float64
	for _, pl := range roster {
		slot, ok := a.Slots[pl.ID]
		if !ok || !espn.IsStartingSlot(slot) {
			continue
		}
		st := AppStart{ID: pl.ID, Slot: espn.SlotName(slot), Name: pl.Name, Position: pl.Position, Team: pl.ProTeam, Locked: pl.Locked, Left: 1}
		if v := p.Values[pl.ID]; v != nil {
			st.Proj, st.Actual = v.WeekProjection, v.WeekActual
		}
		if pl.Locked {
			st.Left = pl.GameLeft
		}
		st.Opp, st.Kickoff = gameInfo(p.League, pl)
		actual += st.Actual
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool { return lineupOrder(out[i].Slot) < lineupOrder(out[j].Slot) })
	return out, actual
}

// gameInfo returns a player's NFL opponent this week ("NYJ" at home,
// "@NYJ" away, "BYE") and the kickoff time.
func gameInfo(l *espn.League, pl *espn.Player) (string, time.Time) {
	if l == nil {
		return "", time.Time{}
	}
	g, ok := l.ProGames[pl.ProTeamID][l.CurrentWeek]
	if !ok {
		if l.OnBye(pl.ProTeamID, l.CurrentWeek) {
			return "BYE", time.Time{}
		}
		return "", time.Time{}
	}
	if g.HomeID == pl.ProTeamID {
		return l.ProTeamAbbrev(g.AwayID), g.Start
	}
	return "@" + l.ProTeamAbbrev(g.HomeID), g.Start
}

// TeamLogo returns the logo URL a fantasy team in the league chose, or "".
func (w *Watcher) TeamLogo(id int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.latest == nil || w.latest.League == nil {
		return ""
	}
	if t := w.latest.League.Team(id); t != nil {
		return t.Logo
	}
	return ""
}

// Decide records your decision on a move from the phone app; approved
// moves run on the watcher's next pass (prompted immediately), after the
// same fresh re-check as an email approval.
func (w *Watcher) Decide(id int, approve bool) (AppItem, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.approvals == nil {
		return AppItem{}, errors.New("the agent isn't ready yet")
	}
	if !w.Apply || !w.emailApprovals() {
		return AppItem{}, errors.New("the agent isn't in approve mode")
	}
	it, err := w.approvals.DecideItem(id, approve, "app", w.Agent.Now())
	if err != nil {
		return AppItem{}, err
	}
	if err := w.approvals.Save(w.Agent.Now()); err != nil {
		return AppItem{}, err
	}
	w.poke()
	return appItem(w.approvals, it), nil
}

func appPlayer(p *Plan, pl *espn.Player) AppPlayer {
	a := AppPlayer{ID: pl.ID, Name: pl.Name, Position: pl.Position, Team: pl.ProTeam}
	if v := p.Values[pl.ID]; v != nil {
		a.ROS = v.ROSValue
	}
	return a
}

func appItem(st *approval.Store, it *approval.Item) AppItem {
	a := AppItem{
		ID: it.ID, Number: it.Number, Batch: it.Batch, Kind: it.Kind, Label: kindLabel(it.Kind),
		Summary: it.Summary, Reason: it.Reason, Status: string(it.Status), Result: it.Result,
		Created: it.Created, Expires: it.Expires, Decided: it.Decided, Via: it.Via, Players: it.Players,
	}
	for _, id := range it.Depends {
		if d := st.Item(id); d != nil {
			a.Needs = append(a.Needs, d.Summary)
		}
	}
	// Questions asked about this move, plus email questions about its batch.
	if b := st.BatchOf(it); b != nil {
		for _, qa := range b.Thread {
			if qa.Item == it.Number || qa.Item == 0 {
				a.Thread = append(a.Thread, AppQA{Question: qa.Question, Answer: qa.Answer, Via: qa.Via, At: qa.At})
			}
		}
	}
	return a
}

func recordString(t *espn.Team) string {
	r := fmt.Sprintf("%d-%d", t.Wins, t.Losses)
	if t.Ties > 0 {
		r += fmt.Sprintf("-%d", t.Ties)
	}
	return r
}

// lineupOrder sorts starters the way ESPN lists them.
func lineupOrder(slot string) int {
	order := []string{"QB", "TQB", "OP", "RB", "RB/WR", "WR", "WR/TE", "TE", "FLEX", "D/ST", "K"}
	for i, s := range order {
		if s == slot {
			return i
		}
	}
	return len(order)
}
