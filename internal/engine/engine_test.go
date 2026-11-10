package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	if created.Collaborative.SettlementMode != model.SettlementUnified {
		t.Fatalf("default settlement %s", created.Collaborative.SettlementMode)
	}
	if len(created.Collaborative.Roles) != 2 {
		t.Fatalf("roles %+v", created.Collaborative.Roles)
	}
	design := created.Collaborative.Roles[0]
	copySeat := created.Collaborative.Roles[1]
	if design.Id != "design" || design.TemplateId != "design" || design.ShareBps != 6000 || design.Quantity != nil {
		t.Fatalf("flat design seat %+v", design)
	}
	if copySeat.Id != "copy" || copySeat.TemplateId != "copy" || copySeat.ShareBps != 4000 || copySeat.Quantity != nil {
		t.Fatalf("flat copy seat %+v", copySeat)
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
	open, err := eng.GetTask(ctx, created.Id)
	if err != nil || open.Status != model.StatusOpen {
		t.Fatalf("unified stays open until start: %+v %v", open.Status, err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"ok": true}}); codeOf(err) != "conflict" {
		t.Fatalf("submit before start: %v", err)
	}
	started, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id})
	if err != nil || started.Status != model.StatusActive {
		t.Fatalf("start %+v %v", started.Status, err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("confirm before every seat submits: %v", err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"copy": true}}); err != nil {
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

func TestCollaborativeQuantityMaterializesSeats(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{
		Roles: []types.CollaborativeRole{
			{Id: "writer", Name: "Writer", Quantity: qty(4), ShareBps: 10000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	roles := created.Collaborative.Roles
	if len(roles) != 4 {
		t.Fatalf("seats %+v", roles)
	}
	var shareSum int64
	for i, role := range roles {
		shareSum += role.ShareBps
		wantID := fmt.Sprintf("writer_%d", i+1)
		if role.Id != wantID || role.Name != "Writer" || role.ShareBps != 2500 || role.TemplateId != "writer" || role.Status != model.RoleOpen || role.Quantity != nil || role.AssigneeUserId != nil {
			t.Fatalf("seat %d %+v", i, role)
		}
	}
	if shareSum != 10000 {
		t.Fatalf("seat shares sum %d", shareSum)
	}
	raw, err := json.Marshal(roles[0])
	if err != nil || bytes.Contains(raw, []byte("quantity")) {
		t.Fatalf("quantity is create-input only: %s %v", raw, err)
	}
	stored, err := eng.GetTask(ctx, created.Id)
	if err != nil || stored.Collaborative.Roles[3].Id != "writer_4" || stored.Collaborative.Roles[3].TemplateId != "writer" {
		t.Fatalf("stored %+v %v", stored.Collaborative, err)
	}

	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	users := []string{"usr_w1", "usr_w2", "usr_w3", "usr_w4"}
	for i, user := range users {
		if _, err := eng.AcceptTask(ctx, user, &types.AcceptTaskReq{TaskId: created.Id, RoleId: fmt.Sprintf("writer_%d", i+1)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, users[0], &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"page": "final"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("quantity confirm before every seat submits: %v", err)
	}
	for _, user := range users[1:] {
		if _, err := eng.SubmitTask(ctx, user, &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"page": user}}); err != nil {
			t.Fatal(err)
		}
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("settled %+v", settled.Escrow)
	}
	for _, user := range users {
		got, err := st.Users().Get(ctx, user)
		if err != nil || got.Balance != 1_000_125 {
			t.Fatalf("%s balance %+v err %v", user, got, err)
		}
	}
}

func TestCollaborativeTemplateSplit(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)

	one := exclusiveReq(clk)
	one.Kind = model.KindCollaborative
	one.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "design", Name: "Design", Quantity: qty(1), ShareBps: 6000},
		{Id: "copy", Name: "Copy", ShareBps: 4000},
	}}
	created, err := eng.CreateTask(ctx, "usr_publisher", one)
	if err != nil {
		t.Fatal(err)
	}
	if created.Collaborative.Roles[0].Id != "design" || created.Collaborative.Roles[1].Id != "copy" {
		t.Fatalf("quantity 1 must keep the template id: %+v", created.Collaborative.Roles)
	}

	three := exclusiveReq(clk)
	three.Kind = model.KindCollaborative
	three.Title = "Three writers"
	three.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "writer", Name: "Writer", Quantity: qty(3), ShareBps: 10000},
	}}
	created, err = eng.CreateTask(ctx, "usr_publisher", three)
	if err != nil {
		t.Fatal(err)
	}
	got := sharesOf(created)
	if !equalInts(got, []int64{3333, 3333, 3334}) || created.Collaborative.Roles[2].Id != "writer_3" {
		t.Fatalf("remainder seats %+v", created.Collaborative.Roles)
	}

	weighted := exclusiveReq(clk)
	weighted.Kind = model.KindCollaborative
	weighted.Title = "Weighted templates"
	weighted.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "design", Name: "Design", Quantity: qty(4), ShareBps: 6000},
		{Id: "copy", Name: "Copy", Quantity: qty(2), ShareBps: 4000},
	}}
	created, err = eng.CreateTask(ctx, "usr_publisher", weighted)
	if err != nil {
		t.Fatal(err)
	}
	if !equalInts(sharesOf(created), []int64{1500, 1500, 1500, 1500, 2000, 2000}) {
		t.Fatalf("weighted %+v", created.Collaborative.Roles)
	}
	if created.Collaborative.Roles[0].Id != "design_1" || created.Collaborative.Roles[4].Id != "copy_1" || created.Collaborative.Roles[4].TemplateId != "copy" {
		t.Fatalf("ids %+v", created.Collaborative.Roles)
	}
}

func TestCollaborativeQuantityRejects(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	longID := strings.Repeat("a", 63)
	cases := []struct {
		name  string
		roles []types.CollaborativeRole
		want  string
	}{
		{
			name:  "per-seat number is not the template total",
			roles: []types.CollaborativeRole{{Id: "writer", Name: "Writer", Quantity: qty(4), ShareBps: 2500}},
			want:  "share_bps must sum to 10000",
		},
		{
			name:  "quantity zero",
			roles: []types.CollaborativeRole{{Id: "writer", Name: "Writer", Quantity: qty(0), ShareBps: 10000}},
			want:  "quantity must be between 1 and 20",
		},
		{
			name: "too many seats",
			roles: []types.CollaborativeRole{
				{Id: "a", Name: "A", Quantity: qty(11), ShareBps: 5000},
				{Id: "b", Name: "B", Quantity: qty(11), ShareBps: 5000},
			},
			want: "at most 20 seats",
		},
		{
			name: "expanded ids collide",
			roles: []types.CollaborativeRole{
				{Id: "writer", Name: "Writer", Quantity: qty(2), ShareBps: 6000},
				{Id: "writer_1", Name: "Overlap", ShareBps: 4000},
			},
			want: "expanded seat ids must be unique",
		},
		{
			name:  "seat id longer than 64",
			roles: []types.CollaborativeRole{{Id: longID, Name: "Writer", Quantity: qty(2), ShareBps: 10000}},
			want:  "too long to expand",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := exclusiveReq(clk)
			req.Kind = model.KindCollaborative
			req.Collaborative = &types.Collaborative{Roles: tc.roles}
			_, err := eng.CreateTask(ctx, "usr_publisher", req)
			if codeOf(err) != "invalid_argument" || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func qty(n int64) *int64 { return &n }

func sharesOf(task *types.Task) []int64 {
	out := make([]int64, len(task.Collaborative.Roles))
	for i, role := range task.Collaborative.Roles {
		out[i] = role.ShareBps
	}
	return out
}

func equalInts(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
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
