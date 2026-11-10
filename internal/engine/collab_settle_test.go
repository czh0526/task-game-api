package engine

import (
	"context"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/types"
)

func TestUnifiedDropoutAfterStartBlocksConfirm(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "design", Name: "Design", ShareBps: 6000},
		{Id: "copy", Name: "Copy", ShareBps: 4000},
	}}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("start before full: %v", err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_copy", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	started, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id})
	if err != nil || started.Status != model.StatusActive {
		t.Fatalf("start %+v %v", started.Status, err)
	}
	dropped, err := eng.DropoutTask(ctx, "usr_design", &types.DropoutTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Status != model.StatusActive || dropped.Collaborative.Roles[0].Status != model.RoleOpen || dropped.Collaborative.Roles[1].Status != model.RoleFilled {
		t.Fatalf("after start dropout stays active: %+v", dropped.Collaborative.Roles)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("confirm with an open seat: %v", err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design_2", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_copy", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"copy": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "copy"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("role_id on unified: %v", err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design_2", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"design": true}}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("settled %+v", settled.Escrow)
	}
	design, _ := st.Users().Get(ctx, "usr_design_2")
	copy, _ := st.Users().Get(ctx, "usr_copy")
	exitee, _ := st.Users().Get(ctx, "usr_design")
	if design.Balance != 1_000_300 || copy.Balance != 1_000_200 || exitee.Balance != 1_000_000 || exitee.Ability != 0 {
		t.Fatalf("design %d copy %d exitee %+v", design.Balance, copy.Balance, exitee)
	}
	if design.Ability != 10 || copy.Ability != 10 {
		t.Fatalf("batch scores design %+v copy %+v", design, copy)
	}
}

func TestPerSeatSettlesIndependently(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{
		SettlementMode: model.SettlementPerSeat,
		Roles: []types.CollaborativeRole{
			{Id: "design", Name: "Design", ShareBps: 6000},
			{Id: "copy", Name: "Copy", ShareBps: 4000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Collaborative.SettlementMode != model.SettlementPerSeat {
		t.Fatalf("mode %s", created.Collaborative.SettlementMode)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("per_seat start: %v", err)
	}
	accepted, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != model.StatusActive || accepted.Collaborative.Roles[1].Status != model.RoleOpen {
		t.Fatalf("one seat can work: %+v", accepted.Collaborative.Roles)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"design": true}}); err != nil {
		t.Fatal(err)
	}
	partial, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "design"})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != model.StatusActive || partial.Escrow.Held != 200 {
		t.Fatalf("partial status %s held %d", partial.Status, partial.Escrow.Held)
	}
	if partial.Collaborative.Roles[0].Status != model.RoleSettled || partial.Collaborative.Roles[1].Status != model.RoleOpen {
		t.Fatalf("seats %+v", partial.Collaborative.Roles)
	}
	var releaseN, refundN int
	for _, ent := range partial.Escrow.Entries {
		if ent.Type == model.EscrowRelease && ent.ToUserId == "usr_design" && ent.Amount == 300 {
			releaseN++
		}
		if ent.Type == model.EscrowRefund {
			refundN++
		}
	}
	if releaseN != 1 || refundN != 0 {
		t.Fatalf("entries %+v", partial.Escrow.Entries)
	}
	design, _ := st.Users().Get(ctx, "usr_design")
	publisher, _ := st.Users().Get(ctx, "usr_publisher")
	if design.Balance != 1_000_300 || design.Ability != 10 || design.Integrity != 1 {
		t.Fatalf("design after own seat %+v", design)
	}
	if publisher.Integrity != 0 || publisher.Balance != 999_500 {
		t.Fatalf("publisher should wait for the task to settle %+v", publisher)
	}

	if _, err := eng.AcceptTask(ctx, "usr_copy", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "copy"}); codeOf(err) != "conflict" {
		t.Fatalf("confirm before copy submits: %v", err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_copy", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"copy": true}}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 {
		t.Fatalf("final %+v held %d", settled.Status, settled.Escrow.Held)
	}
	copy, _ := st.Users().Get(ctx, "usr_copy")
	publisher, _ = st.Users().Get(ctx, "usr_publisher")
	if copy.Balance != 1_000_200 || copy.Ability != 10 || publisher.Integrity != 1 || publisher.Balance != 999_500 {
		t.Fatalf("copy %+v publisher %+v", copy, publisher)
	}
}

func TestPerSeatRefundsOpenSeat(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{
		SettlementMode: model.SettlementPerSeat,
		Roles: []types.CollaborativeRole{
			{Id: "design", Name: "Design", ShareBps: 6000},
			{Id: "copy", Name: "Copy", ShareBps: 4000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != model.StatusSettled || settled.Escrow.Held != 0 || settled.Collaborative.Roles[1].Status != model.RoleRefunded {
		t.Fatalf("refund close %+v seats %+v", settled.Status, settled.Collaborative.Roles)
	}
	var refund int64
	for _, ent := range settled.Escrow.Entries {
		if ent.Type == model.EscrowRefund {
			refund += ent.Amount
			if ent.ToUserId != "usr_publisher" {
				t.Fatalf("refund target %+v", ent)
			}
		}
	}
	if refund != 200 {
		t.Fatalf("refund %d", refund)
	}
	publisher, _ := st.Users().Get(ctx, "usr_publisher")
	if publisher.Balance != 999_700 {
		t.Fatalf("publisher balance %d", publisher.Balance)
	}
	copy, err := st.Users().Get(ctx, "usr_copy")
	if err == nil && (copy.Balance != 1_000_000 || copy.Ability != 0) {
		t.Fatalf("empty seat was paid %+v", copy)
	}
}

func TestPerSeatAutoSettleWaitsForEverySubmission(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Acceptance.ReviewHours = 1
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{
		SettlementMode: model.SettlementPerSeat,
		Roles: []types.CollaborativeRole{
			{Id: "design", Name: "Design", ShareBps: 6000},
			{Id: "copy", Name: "Copy", ShareBps: 4000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
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
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"a": 1}}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("partial auto n=%d err=%v", n, err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusActive || got.Escrow.Held != 500 {
		t.Fatalf("partial auto moved the task %+v err %v", got.Status, err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_copy", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"b": 1}}); err != nil {
		t.Fatal(err)
	}
	if n, err := eng.AutoSettleDue(ctx); err != nil || n != 0 {
		t.Fatalf("before new deadline n=%d err=%v", n, err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	n, err := eng.AutoSettleDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("auto n=%d err=%v", n, err)
	}
	got, err = eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusSettled || got.Escrow.Held != 0 {
		t.Fatalf("auto settle %+v err %v", got.Status, err)
	}
}

func TestUnifiedAutoSettle(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Acceptance.ReviewHours = 1
	req.Payment.Amount = 500
	req.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "design", Name: "Design", ShareBps: 6000},
		{Id: "copy", Name: "Copy", ShareBps: 4000},
	}}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
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
	if _, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_design", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"a": 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_copy", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"b": 1}}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	n, err := eng.AutoSettleDue(ctx)
	if err != nil || n != 1 {
		t.Fatalf("auto n=%d err=%v", n, err)
	}
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusSettled || got.Escrow.Held != 0 {
		t.Fatalf("unified auto %+v err %v", got.Status, err)
	}
}

func TestPerSeatRoundingSumsToPool(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Kind = model.KindCollaborative
	req.Payment.Amount = 100
	req.Collaborative = &types.Collaborative{
		SettlementMode: model.SettlementPerSeat,
		Roles: []types.CollaborativeRole{
			{Id: "writer", Name: "Writer", Quantity: qty(3), ShareBps: 10000},
		},
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	users := []string{"usr_a", "usr_b", "usr_c"}
	for i, user := range users {
		roleID := created.Collaborative.Roles[i].Id
		if _, err := eng.AcceptTask(ctx, user, &types.AcceptTaskReq{TaskId: created.Id, RoleId: roleID}); err != nil {
			t.Fatal(err)
		}
		if _, err := eng.SubmitTask(ctx, user, &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"n": i}}); err != nil {
			t.Fatal(err)
		}
		got, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: created.Id, RoleId: roleID})
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && got.Status == model.StatusSettled {
			t.Fatalf("settled before the last seat")
		}
	}
	final, err := eng.GetTask(ctx, created.Id)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != model.StatusSettled || final.Escrow.Held != 0 {
		t.Fatalf("final %+v held %d", final.Status, final.Escrow.Held)
	}
	var paid int64
	for _, ent := range final.Escrow.Entries {
		if ent.Type == model.EscrowRelease {
			paid += ent.Amount
		}
	}
	if paid != 100 {
		t.Fatalf("releases sum %d", paid)
	}
}

func TestCollaborativeSettlementModeValidation(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	bad := exclusiveReq(clk)
	bad.Kind = model.KindCollaborative
	bad.Collaborative = &types.Collaborative{
		SettlementMode: "split",
		Roles:          []types.CollaborativeRole{{Id: "a", Name: "A", ShareBps: 10000}},
	}
	if _, err := eng.CreateTask(ctx, "usr_publisher", bad); codeOf(err) != "invalid_argument" {
		t.Fatalf("bad mode: %v", err)
	}
	miles := exclusiveReq(clk)
	miles.Kind = model.KindCollaborative
	miles.Payment = types.Payment{
		Mode:   model.PayMilestone,
		Amount: 500,
		Milestones: []types.Milestone{
			{Id: "m1", Amount: 500, Condition: "done"},
		},
	}
	miles.Collaborative = &types.Collaborative{Roles: []types.CollaborativeRole{
		{Id: "a", Name: "A", ShareBps: 10000},
	}}
	if _, err := eng.CreateTask(ctx, "usr_publisher", miles); codeOf(err) != "invalid_argument" {
		t.Fatalf("milestone collaborative: %v", err)
	}
}
