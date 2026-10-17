package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/espn"
)

// methodology tells Claude how the agent reasons, so it can explain the
// "why" behind numbers without inventing a method.
const methodology = `HOW THE AGENT DECIDES
- Projections start from ESPN's, then are scaled by each player's track record against ESPN's projections (shrunk toward 100% until there are several games, and capped at 80%-120%), by injury status (Questionable ~85%, Doubtful ~20%, Out/IR 0%), and by byes.
- When a starter is newly ruled OUT or to IR (report under 48 hours old), his healthiest same-team backup gets a share of the lost production: QB 90%, RB 55%, TE 45%, WR 30% per game.
- "Team value" = (best lineup's projected points this week x 2) + (best rest-of-season lineup x remaining weeks) + 15% credit for bench depth. A pickup or trade is proposed only if it raises team value by enough (default 5 points for pickups, 8 for trades).
- The lineup is chosen to maximize win probability against the opponent's best lineup, not just projected points: as an underdog it favors high-variance (boom/bust) players, as a favorite it favors steady ones.
- Players picked up in the last 7 days are never dropped, at most 3 pickups a day, and protected players are never dropped or traded.
- Every approved move is re-checked against fresh league data before it runs.`

// explainData renders what Claude may use to answer questions about a batch.
func (w *Watcher) explainData(b *approval.Batch, focus *approval.Item) string {
	var out strings.Builder
	st := w.approvals
	p := w.latest
	now := w.Agent.Now()

	if p != nil {
		fmt.Fprintf(&out, "LEAGUE: %s, week %d. As of %s.\n", p.League.Name, p.League.CurrentWeek, w.latestAt.Format(time.RFC1123))
		fmt.Fprintf(&out, "YOUR TEAM: %s (%s)\n", p.Me.Name, recordString(p.Me))
		if m := p.Matchup; m != nil && m.Opponent != nil {
			fmt.Fprintf(&out, "THIS WEEK: vs %s. Projected %.1f to %.1f. Win probability %.0f%% with the proposed lineup, %.0f%% with the lineup as currently set. Strategy: %s.\n",
				m.Opponent.Name, m.MyProjected, m.OppProjected, m.WinProb*100, p.CurrentWinProb*100, m.Strategy)
		}
	} else {
		out.WriteString("LEAGUE: the agent hasn't completed a check since it started, so detailed numbers aren't available.\n")
	}

	out.WriteString("\nPROPOSED MOVES (this email/batch)\n")
	ids := map[int]bool{}
	for _, it := range st.BatchItems(b) {
		fmt.Fprintf(&out, "#%d [%s] %s\n   status: %s", it.Number, kindLabel(it.Kind), it.Summary, it.Status)
		if it.Result != "" {
			fmt.Fprintf(&out, " (%s)", it.Result)
		}
		if !it.Expires.IsZero() && it.Status == approval.Pending {
			fmt.Fprintf(&out, "; expires in %s", it.Expires.Sub(now).Round(time.Minute))
		}
		fmt.Fprintf(&out, "\n   agent's rationale: %s\n", it.Reason)
		for _, d := range it.Depends {
			if dep := st.Item(d); dep != nil {
				fmt.Fprintf(&out, "   only runs if #%d runs\n", dep.Number)
			}
		}
		for _, id := range it.Players {
			ids[id] = true
		}
	}
	if focus != nil {
		fmt.Fprintf(&out, "The question is about move #%d.\n", focus.Number)
	}

	if p != nil {
		out.WriteString("\nPLAYERS IN THESE MOVES\n")
		positions := map[string]bool{}
		for _, id := range sortedIDs(ids) {
			v := p.Values[id]
			if v == nil {
				fmt.Fprintf(&out, "- player %d: no current data\n", id)
				continue
			}
			positions[v.Player.Position] = true
			w.describePlayer(&out, p, v)
		}

		out.WriteString("\nYOUR ROSTER (slot, player, this week's adjusted projection, rest-of-season points)\n")
		roster := append([]*espn.Player(nil), p.Me.Roster...)
		sort.SliceStable(roster, func(i, j int) bool { return slotOrder(roster[i].LineupSlot) < slotOrder(roster[j].LineupSlot) })
		for _, pl := range roster {
			if v := p.Values[pl.ID]; v != nil {
				fmt.Fprintf(&out, "- %s %s (%s %s): %.1f this week, %.0f ROS%s\n", espn.SlotName(pl.LineupSlot), pl.Name, pl.Position, pl.ProTeam,
					v.WeekProjection, v.ROSValue, injuryText(pl))
			}
		}

		out.WriteString("\nBEST AVAILABLE FREE AGENTS / WAIVER PLAYERS AT THOSE POSITIONS (by rest-of-season points)\n")
		for _, pos := range sortedKeys(positions) {
			var avail []*analysis.Valuation
			for _, v := range p.Values {
				if v.Player.OnTeamID == 0 && v.Player.Position == pos {
					avail = append(avail, v)
				}
			}
			analysis.SortByROS(avail)
			if len(avail) > 6 {
				avail = avail[:6]
			}
			for _, v := range avail {
				fmt.Fprintf(&out, "- %s %s (%s, %s): %.1f this week, %.0f ROS, %.1f per game%s%s\n", pos, v.Player.Name, v.Player.ProTeam, v.Player.Status,
					v.WeekProjection, v.ROSValue, v.PerGame, injuryText(v.Player), notesText(v))
			}
		}
	}

	if len(b.Thread) > 0 {
		out.WriteString("\nEARLIER QUESTIONS IN THIS CONVERSATION\n")
		for _, qa := range b.Thread {
			fmt.Fprintf(&out, "Q: %s\nA: %s\n", qa.Question, qa.Answer)
		}
	}
	out.WriteString("\n" + methodology + "\n")
	return out.String()
}

func (w *Watcher) describePlayer(out *strings.Builder, p *Plan, v *analysis.Valuation) {
	pl := v.Player
	owner := "free agent"
	switch {
	case pl.Status == espn.StatusWaivers:
		owner = "on waivers"
	case p.Me.Player(pl.ID) != nil:
		owner = "on your roster (" + espn.SlotName(p.Me.Player(pl.ID).LineupSlot) + ")"
	case pl.OnTeamID != 0:
		if t := p.League.Team(pl.OnTeamID); t != nil {
			owner = "on " + t.Name
		}
	}
	fmt.Fprintf(out, "- %s, %s %s, %s%s\n", pl.Name, pl.Position, pl.ProTeam, owner, injuryText(pl))
	fmt.Fprintf(out, "  this week: ESPN projects %.1f; agent's adjusted projection %.1f (±%.1f)", v.ESPNWeekProjection, v.WeekProjection, v.WeekStdDev)
	if v.OnBye {
		out.WriteString("; ON BYE")
	}
	out.WriteString("\n")
	fmt.Fprintf(out, "  season: %d games, %.1f avg, %.1f over the last 3", v.WeeksPlayed, v.SeasonAvg, v.RecentAvg)
	if v.ProjectedTotal > 0 {
		fmt.Fprintf(out, "; scored %.0f%% of ESPN's projections (%.1f actual vs %.1f projected; best week %+.1f, worst %+.1f vs projection)",
			v.Accuracy*100, v.ActualTotal, v.ProjectedTotal, v.BiggestBeat, v.BiggestMiss)
	}
	if len(v.WeeklyActuals) > 0 {
		var ws []string
		for _, x := range v.WeeklyActuals {
			ws = append(ws, fmt.Sprintf("%.1f", x))
		}
		fmt.Fprintf(out, "; weekly: %s", strings.Join(ws, ", "))
	}
	out.WriteString("\n")
	fmt.Fprintf(out, "  rest of season: %.1f per game, %d games left, %.0f points%s\n", v.PerGame, v.RemainingGames, v.ROSValue, notesText(v))
	if inj, ok := w.Agent.Injuries[pl.ID]; ok && inj.Note != "" {
		fmt.Fprintf(out, "  injury report (%s, %s): %s\n", inj.Status, inj.Date.Format("Jan 2 15:04 MST"), inj.Note)
	}
}

func injuryText(pl *espn.Player) string {
	if st := pl.EffectiveInjury(); st != espn.InjuryActive {
		return ", " + st
	}
	return ""
}

func notesText(v *analysis.Valuation) string {
	if len(v.Notes) == 0 {
		return ""
	}
	return "; notes: " + strings.Join(v.Notes, ", ")
}

func sortedIDs(m map[int]bool) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// errAskOff is returned when follow-up questions aren't set up.
var errAskOff = errors.New("follow-up questions aren't turned on (see \"ask\" in the config and `ffagent secrets set anthropic`)")

// Ask answers a follow-up question about a proposed move from the phone app.
// The Claude call runs without holding the watcher's lock.
func (w *Watcher) Ask(ctx context.Context, itemID int, question string) (approval.QA, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return approval.QA{}, errors.New("type a question first")
	}
	if len(question) > approval.MaxQuestionLen {
		question = question[:approval.MaxQuestionLen]
	}
	w.mu.Lock()
	if w.Asker == nil {
		w.mu.Unlock()
		return approval.QA{}, errAskOff
	}
	if w.approvals == nil {
		w.mu.Unlock()
		return approval.QA{}, errors.New("the agent isn't ready yet")
	}
	it := w.approvals.Item(itemID)
	if it == nil {
		w.mu.Unlock()
		return approval.QA{}, errors.New("no such move")
	}
	b := w.approvals.BatchOf(it)
	if err := w.askAllowed(); err != nil {
		w.mu.Unlock()
		return approval.QA{}, err
	}
	data := w.explainData(b, it)
	asker := w.Asker
	w.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	answer, err := asker.Answer(ctx, data, question)
	if err != nil {
		return approval.QA{}, err
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	qa := approval.QA{Item: it.Number, Question: question, Answer: answer, Via: "app", At: w.Agent.Now()}
	b.Thread = append(b.Thread, qa)
	return qa, w.approvals.Save(w.Agent.Now())
}

// askAllowed enforces the daily question cap. Call with w.mu held.
func (w *Watcher) askAllowed() error {
	limit := w.Agent.Cfg.Ask.MaxPerDay
	if limit > 0 && w.approvals.QuestionsSince(w.Agent.Now().Add(-24*time.Hour)) >= limit {
		return fmt.Errorf("you've reached today's limit of %d questions (ask.max_per_day)", limit)
	}
	return nil
}

// answerByEmail answers a question asked in an email reply. Call with w.mu held.
func (w *Watcher) answerByEmail(ctx context.Context, b *approval.Batch, question string) {
	now := w.Agent.Now()
	reply := func(body string) {
		_ = w.send(ctx, b, "Re: "+b.Subject, body+"\n\nRef: "+b.Token+"\n", b.MessageID)
	}
	if w.Asker == nil {
		reply("I can't answer questions by email yet: follow-up questions aren't turned on.\nTo turn them on, set \"ask\": {\"enabled\": true} in the config and run `ffagent secrets set anthropic`.")
		return
	}
	if err := w.askAllowed(); err != nil {
		reply("I didn't answer your question: " + err.Error() + ".")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	answer, err := w.Asker.Answer(ctx, w.explainData(b, nil), question)
	if err != nil {
		fmt.Fprintf(w.Out, "%s  ASK     couldn't answer an emailed question: %v\n", stamp(now), err)
		reply("I couldn't answer your question just now: " + err.Error() + ".")
		return
	}
	b.Thread = append(b.Thread, approval.QA{Question: question, Answer: answer, Via: "email", At: now})
	reply("You asked: " + question + "\n\n" + answer + "\n\n" +
		"(Answered by Claude from the agent's data. Nothing has been approved or changed: reply \"approve 1\", \"decline 2\", etc. to decide.)")
	fmt.Fprintf(w.Out, "%s  ASK     answered a question about #%s by email\n", stamp(now), b.Token)
}
