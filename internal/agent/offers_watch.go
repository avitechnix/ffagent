package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
	"github.com/avitechnix/ffagent/internal/mail"
	"github.com/avitechnix/ffagent/internal/push"
)

// espnTeamURL is your team page on ESPN, where pending offers are accepted
// or declined. The agent only recommends; it never accepts a trade.
func (w *Watcher) espnTeamURL(p *Plan) string {
	return fmt.Sprintf("https://fantasy.espn.com/football/team?leagueId=%d&seasonId=%d&teamId=%d", w.Agent.Cfg.LeagueID, p.League.Season, p.Me.ID)
}

// reviewNewOffers tells you about each trade offer the first time it's
// seen: its grade and why, by email and on the phone, with a counter you
// can approve when theirs isn't good enough. Call with w.mu held.
func (w *Watcher) reviewNewOffers(ctx context.Context, p *Plan, now time.Time) {
	if w.state.SeenOffers == nil {
		w.state.SeenOffers = map[string]time.Time{}
	}
	pending := map[string]bool{}
	for _, r := range p.Offers {
		pending[r.Offer.ID] = true
		if _, seen := w.state.SeenOffers[r.Offer.ID]; seen {
			continue
		}
		w.state.SeenOffers[r.Offer.ID] = now
		fmt.Fprintf(w.Out, "%s  OFFER   from %s: %s (%s) — %s\n", stamp(now), r.Partner.Name, r.Verdict, r.Grade, r.Reason)
		w.notify(push.Notification{
			Title: fmt.Sprintf("Trade offer from %s: %s", r.Partner.Name, strings.ToLower(r.Verdict)),
			Body:  fmt.Sprintf("Grade %s. You'd get %s for %s.", r.Grade, names(r.Give), names(r.Get)),
			URL:   "./#moves", Tag: "ffagent-offer-" + r.Offer.ID,
		})
		if w.Mailer == nil || w.Agent.Cfg.Email.NotifyTo == "" {
			continue
		}
		if err := w.mailOffer(ctx, p, r, now); err != nil {
			fmt.Fprintf(w.Out, "%s  EMAIL   couldn't send the trade review: %v\n", stamp(now), err)
		}
	}
	for id := range w.state.SeenOffers {
		if !pending[id] && now.Sub(w.state.SeenOffers[id]) > 14*24*time.Hour {
			delete(w.state.SeenOffers, id)
		}
	}
}

func (w *Watcher) mailOffer(ctx context.Context, p *Plan, r OfferReview, now time.Time) error {
	cfg := w.Agent.Cfg
	loc := cfg.Email.Location()
	var body strings.Builder
	fmt.Fprintf(&body, "%s%s offered you a trade.\n\n", leaguePrefix(p), r.Partner.Name)
	fmt.Fprintf(&body, "  You get:  %s\n  You give: %s\n\n", rosList(p.Values, r.Give), rosList(p.Values, r.Get))
	fmt.Fprintf(&body, "Grade %s: %s. Why: %s.\n", r.Grade, r.Verdict, r.Reason)
	if !r.Offer.Expires.IsZero() {
		fmt.Fprintf(&body, "The offer expires %s.\n", when(r.Offer.Expires, loc))
	}
	fmt.Fprintf(&body, "\nTo accept or decline it, open your team on ESPN (the agent never accepts trades):\n%s\n", w.espnTeamURL(p))

	subject := fmt.Sprintf("ffagent: trade offer from %s, grade %s (%s)", r.Partner.Name, r.Grade, strings.ToLower(r.Verdict))
	if r.Counter == nil || !w.emailApprovals() || !w.Apply {
		_, err := w.Mailer.Send(ctx, mail.Message{To: cfg.Email.NotifyTo, Subject: subject, Body: body.String(), MessageID: w.Mailer.NewMessageID()})
		return err
	}
	c := r.Counter
	it := &approval.Item{
		Kind: string(ActTrade), Summary: c.Action.Summary, Reason: c.Action.Reason, Txn: c.Action.Txn,
		Signature: c.Action.Signature(), Players: c.Action.Players(), Expires: now.Add(time.Duration(cfg.TradeExpiryHours) * time.Hour),
	}
	b := w.approvals.AddBatch("offer", []*approval.Item{it}, now)
	fmt.Fprintf(&body, "\nOR SEND A COUNTER (reply \"approve 1\" to send it as a new offer):\n\n")
	itemBlock(&body, &approval.Store{Items: []*approval.Item{it}}, it, loc)
	body.WriteString(replyHelp)
	fmt.Fprintf(&body, "\n\nRef: %s\n", b.Token)
	if err := w.send(ctx, b, fmt.Sprintf("[ffagent #%s] Trade offer from %s, grade %s (%s)", b.Token, r.Partner.Name, r.Grade, strings.ToLower(r.Verdict)), body.String(), ""); err != nil {
		it.Status, it.Done, it.Result = approval.Failed, now, "couldn't email the counter"
		return err
	}
	return nil
}
