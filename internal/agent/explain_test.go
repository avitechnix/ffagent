package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/avitechnix/ffagent/internal/approval"
)

type fakeAsker struct {
	data, question []string
	err            error
}

func (f *fakeAsker) Answer(_ context.Context, data, question string) (string, error) {
	f.data = append(f.data, data)
	f.question = append(f.question, question)
	if f.err != nil {
		return "", f.err
	}
	return "Because the starter is out.", nil
}

func TestAskFromPhoneApp(t *testing.T) {
	w, _, _, _, star, backup := approvalWatcher(t)
	fa := &fakeAsker{}
	w.Asker = fa
	w.Agent.Cfg.Ask.MaxPerDay = 2
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	snap := w.Snapshot()
	if !snap.Status.AskEnabled {
		t.Error("ask_enabled not reported")
	}
	add := snap.Pending[0]
	qa, err := w.Ask(ctx, add.ID, "  Why him?  ")
	if err != nil {
		t.Fatal(err)
	}
	if qa.Question != "Why him?" || qa.Via != "app" || qa.Item != 1 {
		t.Errorf("qa: %+v", qa)
	}
	data := fa.data[0]
	for _, want := range []string{backup.Name, star.Name, "OUT", "HOW THE AGENT DECIDES", "question is about move #1"} {
		if !strings.Contains(data, want) {
			t.Errorf("explain data missing %q:\n%s", want, data)
		}
	}

	// The thread shows on the move it was about, and survives a restart.
	snap = w.Snapshot()
	if len(snap.Pending[0].Thread) != 1 || len(snap.Pending[1].Thread) != 0 {
		t.Errorf("threads: %+v / %+v", snap.Pending[0].Thread, snap.Pending[1].Thread)
	}
	if st, err := approval.Load(w.ApprovalsPath); err != nil || st.QuestionsSince(w.Agent.Now().Add(-time.Hour)) != 1 {
		t.Errorf("thread not saved: %v", err)
	}
	// Earlier answers are part of the next question's context.
	if _, err := w.Ask(ctx, add.ID, "And next week?"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fa.data[1], "Why him?") {
		t.Error("earlier question missing from context")
	}

	// Daily cap, unknown moves, empty questions, Claude errors.
	if _, err := w.Ask(ctx, add.ID, "One more?"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("cap not enforced: %v", err)
	}
	w.Agent.Cfg.Ask.MaxPerDay = 0
	if _, err := w.Ask(ctx, 9999, "?"); err == nil {
		t.Error("unknown move accepted")
	}
	if _, err := w.Ask(ctx, add.ID, "   "); err == nil {
		t.Error("empty question accepted")
	}
	fa.err = errors.New("rate-limited")
	if _, err := w.Ask(ctx, add.ID, "Hm?"); err == nil {
		t.Error("Claude error swallowed")
	}
	w.Asker = nil
	if _, err := w.Ask(ctx, add.ID, "Hm?"); !errors.Is(err, errAskOff) {
		t.Errorf("asking with questions off: %v", err)
	}
}

func TestQuestionInEmailReply(t *testing.T) {
	w, f, fm, _, _, backup := approvalWatcher(t)
	fa := &fakeAsker{}
	w.Asker = fa
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	proposal := fm.sent[0]

	// A question is answered in the thread, and "decline 2?" is not a decision.
	fm.reply(owner, proposal.Subject, "Why the backup? Should I decline 2?\n\nOn Fri, ffagent wrote:\n> approve all")
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fa.question) != 1 || !strings.Contains(fa.question[0], "Why the backup?") {
		t.Fatalf("questions asked: %q", fa.question)
	}
	ans := fm.sent[len(fm.sent)-1]
	if !strings.Contains(ans.Body, "Because the starter is out.") || !strings.Contains(ans.Body, "Nothing has been approved") ||
		ans.InReplyTo != proposal.MessageID || !strings.HasPrefix(ans.Subject, "Re: ") {
		t.Errorf("answer email: %+v", ans)
	}
	if strings.Contains(ans.Body, "couldn't find a decision") {
		t.Error("a question was treated as an unreadable reply")
	}
	for _, it := range w.approvals.Items {
		if it.Status != approval.Pending {
			t.Errorf("a question changed item %d to %s", it.Number, it.Status)
		}
	}

	// A decision and a question in one reply: both are handled.
	fm.reply(owner, proposal.Subject, "approve all\nWhat if he plays Sunday?")
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fa.question) != 2 || countAdds(f, backup.ID) != 1 {
		t.Errorf("questions %d, adds %d", len(fa.question), countAdds(f, backup.ID))
	}
	if snap := w.Snapshot(); len(snap.History) == 0 || snap.History[0].Status != "executed" {
		t.Errorf("history: %+v", snap.History)
	}
}

func TestEmailQuestionWithAskOff(t *testing.T) {
	w, _, fm, _, _, _ := approvalWatcher(t)
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	fm.reply(owner, fm.sent[0].Subject, "Why this guy?")
	if err := w.TickApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	if body := fm.sent[len(fm.sent)-1].Body; !strings.Contains(body, "aren't turned on") {
		t.Errorf("reply: %s", body)
	}
}

func TestAskAnything(t *testing.T) {
	w, _, _, _, star, backup := approvalWatcher(t)
	fa := &fakeAsker{}
	w.Asker = fa
	w.Agent.Cfg.Ask.MaxPerDay = 2
	ctx := context.Background()
	if err := w.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	qa, err := w.AskAnything(ctx, " Who should I start at RB? ")
	if err != nil {
		t.Fatal(err)
	}
	if qa.Question != "Who should I start at RB?" || qa.Via != "app" {
		t.Errorf("qa: %+v", qa)
	}
	data := fa.data[0]
	for _, want := range []string{"YOUR RECOMMENDED LINEUP", "OPPONENT'S LINEUP", "YOUR ROSTER", "MOVES WAITING FOR YOUR DECISION", backup.Name, star.Name, "BEST AVAILABLE", "HOW THE AGENT DECIDES"} {
		if !strings.Contains(data, want) {
			t.Errorf("general data missing %q:\n%s", want, data)
		}
	}
	if snap := w.Snapshot(); len(snap.Chat) != 1 || snap.Chat[0].Answer == "" {
		t.Errorf("chat in snapshot: %+v", snap.Chat)
	}
	// Earlier questions are context, saved, and count toward the daily cap
	// together with questions about moves.
	if _, err := w.Ask(ctx, w.Snapshot().Pending[0].ID, "And this move?"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AskAnything(ctx, "One more?"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("cap not shared: %v", err)
	}
	if st, err := approval.Load(w.ApprovalsPath); err != nil || len(st.Chat) != 1 {
		t.Errorf("chat not saved: %v", err)
	}
	w.Asker = nil
	if _, err := w.AskAnything(ctx, "Hm?"); !errors.Is(err, errAskOff) {
		t.Errorf("questions off: %v", err)
	}
}
