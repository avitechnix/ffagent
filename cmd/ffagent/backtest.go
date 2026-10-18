package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/analysis"
	"github.com/avitechnix/ffagent/internal/espn"
)

// backtest checks whether Vegas lines or defense-vs-position would have
// made ESPN's projections more accurate this season. The agent uses ESPN's
// projections as they are; this is how to tell whether that should change.
func backtest(ctx context.Context, client *espn.Client, out io.Writer) error {
	sched, err := client.ProSchedule(ctx)
	if err != nil {
		return err
	}
	through := espn.CompletedWeeks(sched, time.Now())
	if through < 2 {
		return fmt.Errorf("only %d week(s) of %d have been played; a backtest needs at least 2", through, client.Season)
	}
	var ids []int
	seen := map[int]bool{}
	for _, games := range sched.ProGames {
		for w, g := range games {
			if w <= through && g.ID != 0 && !seen[g.ID] {
				seen[g.ID] = true
				ids = append(ids, g.ID)
			}
		}
	}
	sort.Ints(ids)
	fmt.Fprintf(out, "Reading %d games' betting lines and ESPN's projections for weeks 1-%d…\n", len(ids), through)
	lines, err := client.GameLines(ctx, ids)
	if err != nil {
		return err
	}
	pool, err := client.ReferencePool(ctx, through, 700)
	if err != nil {
		return err
	}

	var games []analysis.BTGame
	for _, team := range sched.ProGames {
		for w, g := range team {
			if ln, ok := lines[g.ID]; ok && w <= through && seen[g.ID] {
				games = append(games, analysis.BTGame{Week: w, Home: g.HomeID, Away: g.AwayID, OverUnder: ln.OverUnder, Spread: ln.Spread})
				delete(seen, g.ID) // each game once
			}
		}
	}
	var players []analysis.BTPlayer
	for _, p := range pool {
		bp := analysis.BTPlayer{Position: p.Position, Team: p.ProTeamID, Projected: map[int]float64{}, Actual: map[int]float64{}}
		for _, s := range p.RefStats {
			if s.Projected {
				bp.Projected[s.Week] = s.Points
			} else {
				bp.Actual[s.Week] = s.Points
			}
		}
		players = append(players, bp)
	}
	results := analysis.Backtest(players, games, through)
	printBacktest(out, results, len(games), len(ids))
	return nil
}

var signalLabels = map[string]string{
	analysis.SignalVegas:                            "Vegas implied team total",
	analysis.SignalDvP:                              "Defense vs position",
	analysis.SignalVegas + "+" + analysis.SignalDvP: "Both together",
}

func printBacktest(out io.Writer, results []analysis.SignalResult, withLines, games int) {
	fmt.Fprintf(out, "%d of %d games had a line. Error is ESPN's projection vs. actual PPR points, per player-week,\n", withLines, games)
	fmt.Fprintln(out, "each week scored with coefficients fitted on the other weeks.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%-26s %6s  %8s  %8s  %8s  %s\n", "Signal", "n", "ESPN", "With it", "Gain", "95% interval")
	var won, lost []string
	for _, r := range results {
		if r.N == 0 {
			continue
		}
		fmt.Fprintf(out, "%-26s %6d  %8.3f  %8.3f  %+8.3f  [%+.3f, %+.3f]\n", signalLabels[r.Signal], r.N, r.ESPN, r.Adjusted, r.Gain, r.Low, r.High)
		switch {
		case r.Beats():
			won = append(won, signalLabels[r.Signal])
		case r.High < 0:
			lost = append(lost, signalLabels[r.Signal])
		}
	}
	fmt.Fprintln(out)
	if len(lost) > 0 {
		fmt.Fprintf(out, "Worse than ESPN by more than chance: %s.\n", strings.Join(lost, ", "))
	}
	if len(won) == 0 {
		fmt.Fprintln(out, "Verdict: nothing here beats ESPN's projections by more than chance, so the agent")
		fmt.Fprintln(out, "keeps using ESPN's projections unadjusted. Re-run as the season goes on: more weeks")
		fmt.Fprintln(out, "make the test sharper.")
		return
	}
	fmt.Fprintf(out, "Verdict: %s beat ESPN's projections by more than chance this season.\n", strings.Join(won, " and "))
	fmt.Fprintln(out, "The agent doesn't apply them on its own; this is evidence for deciding whether it should.")
}
