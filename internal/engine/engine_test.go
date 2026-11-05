package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func newEngine(t *testing.T, initial int64) (*Engine, store.Store, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	st, err := sqlite.Open(dsn, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, "credits", WithClock(clk.Now)), st, clk
}

func deadline(clk *clock) string {
	return clk.t.Add(7 * 24 * time.Hour).Format(time.RFC3339)
}

func exclusiveReq(clk *clock) *types.CreateTaskReq {
	return &types.CreateTaskReq{
		Kind:           model.KindExclusive,
		Title:          "Translate release notes",
		Description:    "Translate the v0.1 notes from Chinese to English.",
		Payment:        types.Payment{Mode: model.PayOneShot, Amount: 500},
		Acceptance:     types.Acceptance{Deadline: deadline(clk), Quality: "Complete translation", ReviewHours: 72},
		AssigneePolicy: types.AssigneePolicy{Type: model.PolicyOpen},
	}
}

func codeOf(err error) string {
	var be *biz.Error
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}

func TestExclusiveHappyPath(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.IdempotencyKey = "create-1"
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != model.StatusDraft || created.Id == "" {
		t.Fatalf("created: %+v", created)
	}
	again, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil || again.Id != created.Id {
		t.Fatalf("idempotent create id=%v err=%v", again, err)
	}
	req.Title = "Different title"
	if _, err := eng.CreateTask(ctx, "usr_publisher", req); codeOf(err) != "idempotency_conflict" {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}

	funded, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id, IdempotencyKey: "fund-1"})
	if err != nil {
		t.Fatal(err)
	}
	if funded.Status != model.StatusOpen || funded.Escrow == nil || funded.Escrow.Held != 500 {
		t.Fatalf("funded: %+v", funded.Escrow)
	}
	replay, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id, IdempotencyKey: "fund-1"})
	if err != nil || replay.Status != model.StatusOpen {
		t.Fatal(err)
	}
	pub, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil || pub.Balance != 999_500 {
		t.Fatalf("publisher balance %v err %v", pub.Balance, err)
	}

	accepted, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != model.StatusActive || len(accepted.ExecutorIds) != 1 {
		t.Fatalf("accepted %+v", accepted)
	}
	submitted, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{
		TaskId: created.Id, Payload: map[string]any{"body": "translated"}, Note: "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Status != model.StatusInReview || submitted.ReviewDeadline == nil {
		t.Fatalf("submitted %+v", submitted.Status)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 || settled.WinningSubmissionId == nil {
		t.Fatalf("settled status=%s held=%d win=%v", settled.Status, settled.Escrow.Held, settled.WinningSubmissionId)
	}
	exec, err := st.Users().Get(ctx, "usr_executor")
	if err != nil {
		t.Fatal(err)
	}
	if exec.Balance != 1_000_500 || exec.Ability != 10 || exec.Integrity != 1 {
		t.Fatalf("executor %+v", exec)
	}
	pub, _ = st.Users().Get(ctx, "usr_publisher")
	if pub.Integrity != 1 || pub.Ability != 0 {
		t.Fatalf("publisher scores %+v", pub)
	}
	scores, err := eng.GetUserScores(ctx, "usr_executor")
	if err != nil || scores.Ability != 10 || len(scores.Recent) != 1 {
		t.Fatalf("scores %+v err %v", scores, err)
	}

	page, err := eng.ListTasks(ctx, &types.ListTasksReq{Kind: model.KindExclusive, Status: model.StatusSettled, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("list %+v err %v", page, err)
	}
}

func TestDisputeFreezesEscrow(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"x": 1}}); err != nil {
		t.Fatal(err)
	}
	disputed, err := eng.DisputeTask(ctx, "usr_publisher", &types.DisputeTaskReq{TaskId: created.Id, Reason: "incomplete"})
	if err != nil {
		t.Fatal(err)
	}
	if disputed.Status != model.StatusDisputed || disputed.Escrow.Held != 500 {
		t.Fatalf("disputed %+v", disputed)
	}
	frozen := false
	for _, e := range disputed.Escrow.Entries {
		if e.Type == model.EscrowFreeze && e.Amount == 500 {
			frozen = true
		}
	}
	if !frozen {
		t.Fatalf("missing freeze entry: %+v", disputed.Escrow.Entries)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("confirm after dispute: %v", err)
	}
}

func TestCollaborativeDropoutAndSettle(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{
		Roles: []types.CollaborativeRole{
			{Id: "design", Name: "Design", ShareBps: 6000},
			{Id: "copy", Name: "Copy", ShareBps: 4000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Collaborative.OnDropout != model.DropoutReplace {
		t.Fatalf("dropout rule %s", created.Collaborative.OnDropout)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	dropped, err := eng.DropoutTask(ctx, "usr_executor", &types.DropoutTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Status != model.StatusOpen || len(dropped.ExecutorIds) != 0 || dropped.Collaborative.Roles[0].Status != model.RoleOpen {
		t.Fatalf("dropout %+v executors %v", dropped.Status, dropped.ExecutorIds)
	}
	sc, err := eng.GetUserScores(ctx, "usr_executor")
	if err != nil || sc.Ability != 0 || sc.Recent[0].DeltaAbility != 0 || sc.Recent[0].Reason != scoreReasonDropout {
		t.Fatalf("dropout scores %+v err %v", sc, err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor_2", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("settled %+v", settled.Escrow)
	}
	a, _ := st.Users().Get(ctx, "usr_executor_2")
	b, _ := st.Users().Get(ctx, "usr_executor")
	if a.Balance != 1_000_300 || b.Balance != 1_000_200 {
		t.Fatalf("shares a=%d b=%d", a.Balance, b.Balance)
	}
}

func TestCompetitiveWinnerTakeAll(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCompetitive
	req.Competitive = &types.Competitive{Slots: 2}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Competitive.SettlementRule != model.SettlementWinnerTakeAll {
		t.Fatal(created.Competitive)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor_2", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "invalid_argument" {
		t.Fatalf("missing winner: %v", err)
	}
	subs, err := st.Submissions().ListByTask(ctx, created.Id)
	if err != nil || len(subs) != 2 {
		t.Fatal(err)
	}
	winner := subs[1].ID
	if subs[0].ExecutorID == "usr_executor_2" {
		winner = subs[0].ID
	} else {
		for _, s := range subs {
			if s.ExecutorID == "usr_executor_2" {
				winner = s.ID
			}
		}
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, SubmissionId: winner})
	if err != nil {
		t.Fatal(err)
	}
	if settled.WinningSubmissionId == nil || *settled.WinningSubmissionId != winner {
		t.Fatalf("winner %v", settled.WinningSubmissionId)
	}
	w, _ := st.Users().Get(ctx, "usr_executor_2")
	l, _ := st.Users().Get(ctx, "usr_executor")
	if w.Balance != 1_000_500 || l.Balance != 1_000_000 || l.Ability != 0 || w.Ability != 10 {
		t.Fatalf("winner %+v loser %+v", w, l)
	}
}

// TestAutoSettleCompetitiveDoesNotPickWinner advances the clock past review_deadline.
// review_hours is 1 so the test does not sleep the default 72h. The rule is the same
// one the background worker uses: winner_take_all with more than one submission stays
// in_review until the publisher confirms a submission_id.
func TestAutoSettleCompetitiveDoesNotPickWinner(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCompetitive
	req.Acceptance.ReviewHours = 1
	req.Competitive = &types.Competitive{Slots: 2}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor_2", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 2}}); err != nil {
		t.Fatal(err)
	}
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("before deadline n=%d err=%v", n, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	n, err := eng.AutoSettleDue(ctx)
	if err != nil || n != 0 {
		t.Fatalf("auto n=%d err=%v", n, err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusInReview || got.WinningSubmissionId != nil || got.Escrow == nil || got.Escrow.Held != 500 {
		t.Fatalf("auto changed the task: status=%s win=%v escrow=%+v", got.Status, got.WinningSubmissionId, got.Escrow)
	}
	for _, entry := range got.Escrow.Entries {
		if entry.Type == model.EscrowRelease || entry.Type == model.EscrowRefund {
			t.Fatalf("auto moved escrow: %+v", entry)
		}
	}
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("second auto n=%d err=%v", n, err)
	}
	first, _ := st.Users().Get(ctx, "usr_executor")
	second, _ := st.Users().Get(ctx, "usr_executor_2")
	if first.Balance != 1_000_000 || second.Balance != 1_000_000 || first.Ability != 0 || second.Ability != 0 {
		t.Fatalf("auto paid someone: %+v %+v", first, second)
	}

	subs, err := st.Submissions().ListByTask(ctx, created.Id)
	if err != nil || len(subs) != 2 {
		t.Fatal(err)
	}
	winner := ""
	for _, s := range subs {
		if s.ExecutorID == "usr_executor_2" {
			winner = s.ID
		}
	}
	if winner == "" {
		t.Fatal("missing executor 2 submission")
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, SubmissionId: winner})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.WinningSubmissionId == nil || *settled.WinningSubmissionId != winner || settled.Escrow.Held != 0 {
		t.Fatalf("explicit confirm %+v", settled)
	}
	second, _ = st.Users().Get(ctx, "usr_executor_2")
	first, _ = st.Users().Get(ctx, "usr_executor")
	if second.Balance != 1_000_500 || second.Ability != 10 || first.Balance != 1_000_000 || first.Ability != 0 {
		t.Fatalf("after confirm winner %+v loser %+v", second, first)
	}
}

func TestAutoSettleExclusive(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Acceptance.ReviewHours = 1
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 1}}); err != nil {
		t.Fatal(err)
	}
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("early auto n=%d err=%v", n, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	n, err := eng.AutoSettleDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("auto n=%d err=%v", n, err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusSettled {
		t.Fatalf("status %+v err %v", got, err)
	}
}

func TestMilestoneReleases(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Payment = types.Payment{
		Mode:   model.PayMilestone,
		Amount: 500,
		Milestones: []types.Milestone{
			{Id: "m1", Amount: 200, Condition: "draft"},
			{Id: "m2", Amount: 300, Condition: "final"},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, MilestoneId: "m1", Payload: map[string]any{"step": 1}}); err != nil {
		t.Fatal(err)
	}
	mid, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, MilestoneId: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	if mid.Status != model.StatusActive || mid.Escrow.Held != 300 {
		t.Fatalf("partial %+v held %d", mid.Status, mid.Escrow.Held)
	}
	exec, _ := st.Users().Get(ctx, "usr_executor")
	if exec.Ability != 0 || exec.Balance != 1_000_200 {
		t.Fatalf("partial scores %+v", exec)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, MilestoneId: "m2", Payload: map[string]any{"step": 2}}); err != nil {
		t.Fatal(err)
	}
	done, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, MilestoneId: "m2"})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != model.StatusSettled || done.Escrow.Held != 0 {
		t.Fatalf("final %+v", done)
	}
	exec, _ = st.Users().Get(ctx, "usr_executor")
	if exec.Balance != 1_000_500 || exec.Ability != 10 || exec.Integrity != 1 {
		t.Fatalf("final executor %+v", exec)
	}
}

func TestInsufficientCredits(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 100)
	req := exclusiveReq(clk)
	req.Payment.Amount = 500
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); codeOf(err) != "insufficient_credits" {
		t.Fatalf("fund: %v", err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusDraft {
		t.Fatalf("still draft? %+v %v", got, err)
	}
}
