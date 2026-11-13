package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"
)

func newEngineWith(t *testing.T, initial int64, opts ...Option) (*Engine, store.Store, *clock) {
	t.Helper()
	clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	st, err := sqlite.Open(dsn, initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	all := append([]Option{WithClock(clk.Now)}, opts...)
	return New(st, "credits", all...), st, clk
}

func perSeatReq(clk *clock) *types.CreateTaskReq {
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Collaborative = &types.Collaborative{
		SettlementMode: model.SettlementPerSeat,
		Roles: []types.CollaborativeRole{
			{Id: "design", Name: "Design", ShareBps: 6000},
			{Id: "copy", Name: "Copy", ShareBps: 4000},
		},
	}
	return req
}

func TestCancelOpenRefundsAndDraft(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CancelTask(ctx, "usr_executor", &types.CancelTaskReq{TaskId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("executor cancel: %v", err)
	}
	cancelled, err := eng.CancelTask(ctx, "usr_publisher", &types.CancelTaskReq{TaskId: created.Id, Reason: "changed my mind"})
	if err != nil || cancelled.Status != model.StatusCancelled || cancelled.CancelReason == nil || *cancelled.CancelReason != "changed my mind" || cancelled.CancelledAt == nil {
		t.Fatalf("draft cancel %+v err %v", cancelled, err)
	}
	if _, err := eng.CancelTask(ctx, "usr_publisher", &types.CancelTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("second cancel %v", err)
	}

	created, err = eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	funded, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id})
	if err != nil || funded.ChatId == nil || *funded.ChatId == "" || funded.OpenDeadline == nil {
		t.Fatalf("fund chat/deadline %+v err %v", funded, err)
	}
	chat, err := eng.GetChat(ctx, "usr_publisher", *funded.ChatId)
	if err != nil || chat.TaskId == nil || *chat.TaskId != created.Id || chat.Type != model.ChatTask {
		t.Fatalf("bound chat %+v err %v", chat, err)
	}
	listed, err := eng.ListChats(ctx, "usr_publisher", created.Id)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Type != model.ChatTask || listed.Items[0].Id != chat.Id {
		t.Fatalf("task chat list %+v err %v", listed, err)
	}
	if _, err := eng.GetChat(ctx, "usr_executor", *funded.ChatId); codeOf(err) != "forbidden" {
		t.Fatalf("stranger chat %v", err)
	}
	page, err := eng.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: *funded.ChatId, Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("empty history %+v %v", page, err)
	}
	if _, err := eng.PostMessage(ctx, "usr_publisher", &types.PostMessageReq{ChatId: *funded.ChatId, Type: "text", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.PostMessage(ctx, "usr_publisher", &types.PostMessageReq{ChatId: *funded.ChatId, Type: "text", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	page, err = eng.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: *funded.ChatId, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "hello" || page.NextCursor == nil {
		t.Fatalf("page %+v err %v", page, err)
	}
	cancelled, err = eng.CancelTask(ctx, "usr_publisher", &types.CancelTaskReq{TaskId: created.Id, IdempotencyKey: "cancel-open"})
	if err != nil || cancelled.Status != model.StatusCancelled || cancelled.Escrow == nil || cancelled.Escrow.Held != 0 {
		t.Fatalf("open cancel %+v err %v", cancelled, err)
	}
	replay, err := eng.CancelTask(ctx, "usr_publisher", &types.CancelTaskReq{TaskId: created.Id, IdempotencyKey: "cancel-open"})
	if err != nil || replay.Status != model.StatusCancelled {
		t.Fatal(err)
	}
	pub, _ := st.Users().Get(ctx, "usr_publisher")
	if pub.Balance != 1_000_000 {
		t.Fatalf("publisher balance %d", pub.Balance)
	}
}

func TestReviseThenConfirmAndLimit(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Acceptance.MaxRevisions = 1
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	accepted, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.GetChat(ctx, "usr_executor", *accepted.ChatId); err != nil {
		t.Fatalf("executor joined chat: %v", err)
	}
	submitted, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"n": 1}, Note: "first"})
	if err != nil || len(submitted.Submissions) != 1 || submitted.Submissions[0].Status != model.SubmissionCurrent {
		t.Fatalf("submission list %+v err %v", submitted.Submissions, err)
	}
	revised, err := eng.ReviseTask(ctx, "usr_publisher", &types.ReviseTaskReq{TaskId: created.Id, Reason: "please fix the intro"})
	if err != nil || revised.Status != model.StatusActive || revised.ReviewDeadline != nil {
		t.Fatalf("revise %+v err %v", revised.Status, err)
	}
	if revised.Submissions[0].Status != model.SubmissionSuperseded {
		t.Fatalf("history %+v", revised.Submissions)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"n": 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ReviseTask(ctx, "usr_publisher", &types.ReviseTaskReq{TaskId: created.Id, Reason: "again"}); codeOf(err) != "conflict" {
		t.Fatalf("limit %v", err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id})
	if err != nil || settled.Status != model.StatusSettled {
		t.Fatalf("confirm after revise %+v %v", settled, err)
	}
}

func TestMutualResolveAgreeDisagreeAndArbitrate(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngineWith(t, 1_000_000, WithArbiters([]string{"usr_arbiter"}))
	req := exclusiveReq(clk)
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
	disputed, err := eng.DisputeTask(ctx, "usr_publisher", &types.DisputeTaskReq{TaskId: created.Id, Reason: "not what I asked"})
	if err != nil || disputed.Status != model.StatusDisputed || disputed.Escrow.Frozen != 500 {
		t.Fatalf("dispute %+v err %v", disputed, err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("confirm while frozen %v", err)
	}
	proposed, err := eng.ResolveTask(ctx, "usr_publisher", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor})
	if err != nil || proposed.Status != model.StatusDisputed || len(proposed.ResolveProposals) != 1 {
		t.Fatalf("proposal %+v err %v", proposed, err)
	}
	counter, err := eng.ResolveTask(ctx, "usr_executor", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomeRefundPublisher})
	if err != nil || counter.Status != model.StatusDisputed || counter.Escrow.Held != 500 {
		t.Fatalf("disagreement should keep funds frozen %+v err %v", counter, err)
	}
	if len(counter.ResolveProposals) != 1 || counter.ResolveProposals[0].Outcome != model.OutcomeRefundPublisher {
		t.Fatalf("publisher proposal should clear %+v", counter.ResolveProposals)
	}
	agreed, err := eng.ResolveTask(ctx, "usr_publisher", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomeRefundPublisher})
	if err != nil || agreed.Status != model.StatusCancelled || agreed.Escrow.Held != 0 || agreed.Escrow.Frozen != 0 {
		t.Fatalf("agreed refund %+v err %v", agreed, err)
	}
	pub, _ := st.Users().Get(ctx, "usr_publisher")
	exec, _ := st.Users().Get(ctx, "usr_executor")
	if pub.Balance != 1_000_000 || exec.Balance != 1_000_000 || exec.Ability != 0 {
		t.Fatalf("balances pub %d exec %+v", pub.Balance, exec)
	}

	created, err = eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DisputeTask(ctx, "usr_executor", &types.DisputeTaskReq{TaskId: created.Id, Reason: "underpaid scope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ArbitrateTask(ctx, "usr_publisher", &types.ArbitrateTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor, Reason: "I say so"}); codeOf(err) != "forbidden" {
		t.Fatalf("publisher arbitrate %v", err)
	}
	paid, err := eng.ArbitrateTask(ctx, "usr_arbiter", &types.ArbitrateTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor, Reason: "delivery matches the brief"})
	if err != nil || paid.Status != model.StatusSettled || paid.Escrow.Held != 0 {
		t.Fatalf("arbitrate %+v err %v", paid, err)
	}
	exec, _ = st.Users().Get(ctx, "usr_executor")
	if exec.Balance != 1_000_500 || exec.Ability != 10 {
		t.Fatalf("executor after arbitrate %+v", exec)
	}
}

func TestPerSeatCancelRemainderAndSeatDispute(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngineWith(t, 1_000_000, WithArbiters([]string{"usr_arbiter"}))
	created, err := eng.CreateTask(ctx, "usr_publisher", perSeatReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"d": 1}}); err != nil {
		t.Fatal(err)
	}
	partial, err := eng.CancelTask(ctx, "usr_publisher", &types.CancelTaskReq{TaskId: created.Id, Reason: "drop the empty seat"})
	if err != nil || partial.Status != model.StatusInReview {
		t.Fatalf("partial cancel %+v err %v", partial, err)
	}
	var copyRefunded bool
	for _, role := range partial.Collaborative.Roles {
		if role.Id == "copy" && role.Status == model.RoleRefunded {
			copyRefunded = true
		}
		if role.Id == "design" && role.Status != model.RoleSubmitted {
			t.Fatalf("design seat %+v", role)
		}
	}
	if !copyRefunded || partial.Escrow.Held != 300 {
		t.Fatalf("copy refund held %d roles %+v", partial.Escrow.Held, partial.Collaborative.Roles)
	}
	if _, err := eng.AcceptTask(ctx, "usr_copy", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"}); codeOf(err) != "conflict" {
		t.Fatalf("refunded seat %v", err)
	}

	created, err = eng.CreateTask(ctx, "usr_publisher", perSeatReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_copy", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"d": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_copy", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"c": 1}}); err != nil {
		t.Fatal(err)
	}
	disputed, err := eng.DisputeTask(ctx, "usr_publisher", &types.DisputeTaskReq{TaskId: created.Id, Reason: "design is incomplete", RoleId: "design"})
	if err != nil || disputed.Escrow.Frozen != 300 || disputed.Escrow.Held != 500 {
		t.Fatalf("seat dispute frozen/held %+v err %v", disputed.Escrow, err)
	}
	paid, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "copy"})
	if err != nil || paid.Escrow.Held != 300 {
		t.Fatalf("other seat still payable %+v err %v", paid, err)
	}
	if _, err := eng.ResolveTask(ctx, "usr_copy", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor, RoleId: "design"}); codeOf(err) != "forbidden" {
		t.Fatalf("other executor resolve %v", err)
	}
	if _, err := eng.ResolveTask(ctx, "usr_design", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ResolveTask(ctx, "usr_publisher", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomePayExecutor})
	if err != nil || settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("seat resolve %+v err %v", settled, err)
	}
	design, _ := st.Users().Get(ctx, "usr_design")
	copy, _ := st.Users().Get(ctx, "usr_copy")
	if design.Balance != 1_000_300 || copy.Balance != 1_000_200 {
		t.Fatalf("design %d copy %d", design.Balance, copy.Balance)
	}
}

func TestTimeouts(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngineWith(t, 1_000_000, WithTimeouts(TimeoutConfig{
		OpenTimeout: time.Hour, StartTimeout: time.Hour, SeatTimeout: time.Hour, WinnerSelectHours: 1,
	}))

	created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(30 * time.Minute)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("early open timeout n=%d err=%v", n, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("open timeout n=%d err=%v", n, err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusCancelled || got.CancelCode == nil || *got.CancelCode != model.CancelOpenTimeout || got.Escrow.Held != 0 {
		t.Fatalf("open timeout result %+v err %v", got, err)
	}
	pub, _ := st.Users().Get(ctx, "usr_publisher")
	if pub.Balance != 1_000_000 {
		t.Fatalf("refund balance %d", pub.Balance)
	}

	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "design", Name: "Design", ShareBps: 6000},
		{Id: "copy", Name: "Copy", ShareBps: 4000},
	}}
	created, err = eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	filled, err := eng.AcceptTask(ctx, "usr_copy", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"})
	if err != nil || filled.Status != model.StatusOpen || filled.StartDeadline == nil {
		t.Fatalf("start deadline %+v err %v", filled, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("start timeout n=%d err=%v", n, err)
	}
	got, err = eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusCancelled || got.CancelCode == nil || *got.CancelCode != model.CancelStartTimeout {
		t.Fatalf("start timeout %+v err %v", got, err)
	}
	design, _ := st.Users().Get(ctx, "usr_design")
	if design.Balance != 1_000_000 || design.Ability != 0 {
		t.Fatalf("unstarted executor %+v", design)
	}

	created, err = eng.CreateTask(ctx, "usr_publisher", perSeatReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("seat timeout n=%d err=%v", n, err)
	}
	got, err = eng.GetTask(ctx, created.Id)
	if err != nil || got.Escrow.Held != 300 {
		t.Fatalf("only the open seat refunds %+v err %v", got, err)
	}
	for _, role := range got.Collaborative.Roles {
		if role.Id == "copy" && role.Status != model.RoleRefunded {
			t.Fatalf("copy %+v", role)
		}
		if role.Id == "design" && role.Status != model.RoleFilled {
			t.Fatalf("design %+v", role)
		}
	}

	comp := exclusiveReq(clk)
	comp.Kind = model.KindCompetitive
	comp.Acceptance.ReviewHours = 1
	comp.Competitive = &types.Competitive{Slots: 2}
	created, err = eng.CreateTask(ctx, "usr_publisher", comp)
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
	submitted, err := eng.SubmitTask(ctx, "usr_executor_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 2}})
	if err != nil || submitted.WinnerSelectDeadline == nil || len(submitted.Submissions) != 2 {
		t.Fatalf("winner deadline %+v subs %d err %v", submitted.WinnerSelectDeadline, len(submitted.Submissions), err)
	}
	clk.t = clk.t.Add(90 * time.Minute)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("before winner deadline n=%d err=%v", n, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("winner timeout n=%d err=%v", n, err)
	}
	got, err = eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusCancelled || got.CancelCode == nil || *got.CancelCode != model.CancelWinnerSelectTimeout || got.WinningSubmissionId != nil {
		t.Fatalf("winner timeout %+v err %v", got, err)
	}
	first, _ := st.Users().Get(ctx, "usr_executor")
	second, _ := st.Users().Get(ctx, "usr_executor_2")
	if first.Balance != 1_000_000 || second.Balance != 1_000_000 || first.Ability != 0 || second.Ability != 0 {
		t.Fatalf("winner timeout paid %+v %+v", first, second)
	}

	early := exclusiveReq(clk)
	early.Acceptance.Deadline = clk.t.Add(30 * time.Minute).Format(time.RFC3339)
	created, err = eng.CreateTask(ctx, "usr_publisher", early)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 1 {
		t.Fatalf("acceptance deadline n=%d err=%v", n, err)
	}
	got, err = eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusCancelled || got.Escrow.Held != 0 {
		t.Fatalf("no-submit deadline %+v err %v", got, err)
	}
}

func TestResolveSplit(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
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
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DisputeTask(ctx, "usr_publisher", &types.DisputeTaskReq{TaskId: created.Id, Reason: "split it"}); err != nil {
		t.Fatal(err)
	}
	pubBps := int64(4000)
	exeBps := int64(6000)
	splits := []types.SplitLine{
		{UserId: "usr_publisher", ShareBps: &pubBps},
		{UserId: "usr_executor", ShareBps: &exeBps},
	}
	if _, err := eng.ResolveTask(ctx, "usr_publisher", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomeSplit, Splits: splits}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ResolveTask(ctx, "usr_executor", &types.ResolveTaskReq{TaskId: created.Id, Outcome: model.OutcomeSplit, Splits: splits})
	if err != nil || settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("split %+v err %v", settled, err)
	}
	pub, _ := st.Users().Get(ctx, "usr_publisher")
	exec, _ := st.Users().Get(ctx, "usr_executor")
	if pub.Balance != 999_700 || exec.Balance != 1_000_300 || exec.Ability != 10 {
		t.Fatalf("split balances pub %d exec %+v", pub.Balance, exec)
	}
}

func TestPolicyInvitesAndAssignable(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.AssigneePolicy = types.AssigneePolicy{Type: model.PolicySpecific, UserIds: []string{"usr_executor"}}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	mine, err := eng.ListMyInvites(ctx, "usr_executor", model.InvitePending)
	if err != nil || len(mine.Items) != 1 || mine.Items[0].TaskId != created.Id {
		t.Fatalf("invites %+v err %v", mine, err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_other", &types.AcceptTaskReq{TaskId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("specific stranger %v", err)
	}
	assigned, err := eng.ListTasks(ctx, "usr_executor", &types.ListTasksReq{Assignable: "me", Limit: 20})
	if err != nil || len(assigned.Items) != 1 || assigned.Items[0].Id != created.Id {
		t.Fatalf("assignable executor %+v err %v", assigned, err)
	}
	publisherView, err := eng.ListTasks(ctx, "usr_publisher", &types.ListTasksReq{Assignable: "me", Limit: 20})
	if err != nil || len(publisherView.Items) != 0 {
		t.Fatalf("publisher is not assignable %+v err %v", publisherView, err)
	}
	stranger, err := eng.ListTasks(ctx, "usr_other", &types.ListTasksReq{Assignable: "me", Limit: 20})
	if err != nil || len(stranger.Items) != 0 {
		t.Fatalf("stranger assignable %+v err %v", stranger, err)
	}
	if _, err := eng.DeclineInvite(ctx, "usr_executor", &types.DeclineInviteReq{InviteId: mine.Items[0].Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("declined %v", err)
	}
	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{TaskId: created.Id, UserIds: []string{"usr_executor"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}

	minAbility := int64(10)
	criteria := exclusiveReq(clk)
	criteria.AssigneePolicy = types.AssigneePolicy{Type: model.PolicyCriteria, MinAbility: &minAbility}
	created, err = eng.CreateTask(ctx, "usr_publisher", criteria)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("criteria %v", err)
	}
	openReq := exclusiveReq(clk)
	created, err = eng.CreateTask(ctx, "usr_publisher", openReq)
	if err != nil {
		t.Fatal(err)
	}
	funded, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	openList, err := eng.ListTasks(ctx, "usr_new", &types.ListTasksReq{Assignable: "me", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range openList.Items {
		if item.Id == funded.Id {
			found = true
		}
	}
	if !found {
		t.Fatalf("open task missing from assignable=me: %+v", openList.Items)
	}
}
