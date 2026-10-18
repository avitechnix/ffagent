package agent

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/espn"
)

type offerESPN struct {
	*fakeESPN
	offers []espn.TradeOffer
}

func (f *offerESPN) TradeOffers(context.Context, *espn.League, int) ([]espn.TradeOffer, error) {
	return f.offers, nil
}

// offerAgent is the test league with these offers from Rival: their
// players for mine.
func offerAgent(t *testing.T, offers map[string][2][]string) (*Agent, *offerESPN) {
	t.Helper()
	f, _ := scenario()
	l := f.league()
	ids := func(team int, names []string) []int {
		var out []int
		for _, n := range names {
			p := findByName(l.Teams[team].Roster, n)
			if p == nil {
				t.Fatalf("no %s", n)
			}
			out = append(out, p.ID)
		}
		return out
	}
	of := &offerESPN{fakeESPN: f}
	for id, o := range offers {
		of.offers = append(of.offers, espn.TradeOffer{ID: id, FromTeam: 2, Give: ids(1, o[0]), Get: ids(0, o[1])})
	}
	a := testAgent(f)
	a.ESPN = of
	return a, of
}

func reviews(t *testing.T, a *Agent) map[string]OfferReview {
	t.Helper()
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]OfferReview{}
	for _, r := range p.Offers {
		out[r.Offer.ID] = r
	}
	return out
}

func TestOffersAreGraded(t *testing.T) {
	a, of := offerAgent(t, map[string][2][]string{
		"good": {{"Opp WR1"}, {"QB Bench"}}, // a starting WR for my backup QB
		"bad":  {{"Opp K"}, {"WR One"}},     // a kicker for my best WR
		"even": {{"Opp WR4"}, {"WR Two"}},   // like for like
	})
	of.offers = append(of.offers, espn.TradeOffer{ID: "stale", FromTeam: 2, Give: of.offers[0].Give, Get: []int{999999}})
	r := reviews(t, a)
	if g := r["good"]; g.Grade != "A" || g.Verdict != "Accept" || !strings.Contains(g.Reason, "you'd get Opp WR1") {
		t.Errorf("good offer: %+v", g)
	}
	if b := r["bad"]; b.Grade != "F" || b.Verdict != "Decline" || !strings.Contains(b.Reason, "costs you") {
		t.Errorf("bad offer: %+v", b)
	}
	if e := r["even"]; e.Grade != "C" || e.Verdict != "Your call" {
		t.Errorf("even offer: %+v", e)
	}
	if _, ok := r["stale"]; ok {
		t.Error("graded an offer for a player who isn't on my roster any more")
	}
}

func TestCounterOffer(t *testing.T) {
	// They want my injured starter plus my second RB for their RB1: a bit
	// short for me. Sending a lesser RB instead works for both.
	a, _ := offerAgent(t, map[string][2][]string{"short": {{"Opp RB1"}, {"Hurt Starter", "RB Two"}}})
	r := reviews(t, a)["short"]
	if r.Verdict != "Counter" || r.Counter == nil {
		t.Fatalf("expected a counter: %+v", r)
	}
	c := r.Counter
	if c.MyGain < a.Cfg.MinTradeGain || c.TheirGain < -a.Cfg.TradeTolerance || c.oddsWeight < 0.5 {
		t.Errorf("counter doesn't work for both: %+v", c)
	}
	if !strings.HasPrefix(c.Action.Summary, "Counter Rival:") || c.Action.Kind != ActTrade || !strings.Contains(c.Action.Reason, "their offer costs you") {
		t.Errorf("counter action: %+v", c.Action)
	}
	for _, p := range c.Give {
		if p.Name == "RB Two" {
			t.Errorf("counter still sends RB Two: %s", c.Action.Summary)
		}
	}
}

func TestNewOfferIsReviewedOnce(t *testing.T) {
	a, _ := offerAgent(t, map[string][2][]string{"short": {{"Opp RB1"}, {"Hurt Starter", "RB Two"}}})
	a.Cfg.Email.Address, a.Cfg.Email.NotifyTo = "bot@example.com", owner
	fm, fn := &fakeMail{}, &fakeNotifier{}
	w := &Watcher{Agent: a, News: &fakeNews{}, Apply: true, Out: io.Discard, Mailer: fm, Notifier: fn, StatePath: filepath.Join(t.TempDir(), "state.json")}
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var review string
	for _, m := range fm.sent {
		if strings.Contains(m.Subject, "Trade offer from Rival") {
			review = m.Subject + "\n" + m.Body
		}
	}
	for _, want := range []string{"grade D (counter)", "You get:  Opp RB1", "fantasy.espn.com/football/team?leagueId=1&seasonId=2026&teamId=1", "never accepts trades", "approve 1", "Counter Rival:"} {
		if !strings.Contains(review, want) {
			t.Errorf("review email missing %q:\n%s", want, review)
		}
	}
	var counter *approval.Item
	for _, it := range w.approvals.Pending() {
		if it.Kind == string(ActTrade) && strings.HasPrefix(it.Summary, "Counter") {
			counter = it
		}
	}
	if counter == nil {
		t.Fatal("the counter isn't waiting for approval")
	}
	w.pushes.Wait()
	if !strings.Contains(fn.titles(), "Trade offer from Rival: counter") {
		t.Errorf("notifications: %s", fn.titles())
	}

	// The same offer on the next check isn't reviewed again.
	sent := len(fm.sent)
	w.lastFull = time.Time{}
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range fm.sent[sent:] {
		if strings.Contains(m.Subject, "Trade offer") {
			t.Errorf("reviewed the same offer twice")
		}
	}

	// The phone app shows it with a link to ESPN.
	snap := w.Snapshot()
	if len(snap.Offers) != 1 || snap.Offers[0].Grade != "D" || !strings.HasPrefix(snap.Offers[0].Link, "https://fantasy.espn.com/") || snap.Offers[0].Counter == "" || len(snap.Offers[0].Get) != 2 {
		t.Errorf("app offers: %+v", snap.Offers)
	}
}

// Trade ideas skip offers the other manager would almost surely refuse.
func TestTradeIdeasAreRealistic(t *testing.T) {
	f, _ := scenario()
	a := testAgent(f)
	p, err := a.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range p.Trades {
		if tr.oddsWeight < 0.5 || strings.Contains(tr.Odds, "long shot") || !strings.Contains(tr.Action.Reason, tr.Odds) {
			t.Errorf("unrealistic or unexplained: %s — %s", tr.Action.Summary, tr.Action.Reason)
		}
	}
	s := a.newState(f.league(), f.league().Teams[0])
	l := s.l
	star, scrub := findByName(l.Teams[1].Roster, "Opp WR1"), findByName(l.Teams[0].Roster, "WR Scrub")
	if odds, w := s.acceptance([]*espn.Player{scrub}, []*espn.Player{star}, 20); w >= 0.5 {
		t.Errorf("a scrub for their best WR: %s", odds)
	}
}
