package engine

import (
	"context"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/types"
)

// Removing an empty seat refunds its share to the publisher, keeps the
// tombstone in roles for payout math, hides it from the DTO, and drops invites
// bound to that seat.
func TestRemoveSeatRefundsAndHidesSeat(t *testing.T) {
	ctx := context.Background()
	eng, st, _, taskID := newSeatInviteTask(t) // design 60% / copy 40%, pool 500

	// Bind an invite to the copy seat so we can verify it is deleted.
	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_writer"}, RoleId: "copy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Invites().Find(ctx, taskID, "usr_writer"); err != nil {
		t.Fatalf("invite should exist before removal: %v", err)
	}

	pubBefore, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil {
		t.Fatal(err)
	}
	escrowBefore, err := st.Escrow().LockByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	heldBefore := escrowBefore.Held

	removed, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "copy"})
	if err != nil {
		t.Fatal(err)
	}

	// DTO hides the removed seat entirely.
	if seatOf(removed, "copy") != nil {
		t.Fatalf("removed seat still visible in DTO: %+v", removed.Collaborative.Roles)
	}
	if len(removed.Collaborative.Roles) != 1 || removed.Collaborative.Roles[0].Id != "design" {
		t.Fatalf("expected only design seat, got %+v", removed.Collaborative.Roles)
	}

	// Money: publisher gets the copy share (40% of 500 = 200) back, held drops by 200.
	pubAfter, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil {
		t.Fatal(err)
	}
	if got := pubAfter.Balance - pubBefore.Balance; got != 200 {
		t.Fatalf("publisher refund %d, want 200", got)
	}
	escrowAfter, err := st.Escrow().LockByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got := heldBefore - escrowAfter.Held; got != 200 {
		t.Fatalf("escrow held decreased by %d, want 200", got)
	}

	// Invite bound to the removed seat is gone.
	if _, err := st.Invites().Find(ctx, taskID, "usr_writer"); err == nil {
		t.Fatal("invite bound to removed seat should be deleted")
	}

	// The tombstone stays in the persisted task with share_bps intact.
	persisted, err := loadTask(ctx, st, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Collaborative.Roles) != 2 {
		t.Fatalf("tombstone must stay in roles, got %d", len(persisted.Collaborative.Roles))
	}
	var tomb *model.Role
	for i := range persisted.Collaborative.Roles {
		if persisted.Collaborative.Roles[i].ID == "copy" {
			tomb = &persisted.Collaborative.Roles[i]
		}
	}
	if tomb == nil || tomb.Status != model.RoleRemoved || tomb.ShareBps != 4000 {
		t.Fatalf("bad tombstone: %+v", tomb)
	}
}

// Only the publisher can remove a seat.
func TestRemoveSeatRequiresPublisher(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)
	if _, err := eng.RemoveSeat(ctx, "usr_other", &types.RemoveSeatReq{TaskId: taskID, RoleId: "copy"}); err == nil {
		t.Fatal("non-publisher should not be able to remove a seat")
	}
}

// A filled seat cannot be removed.
func TestRemoveSeatRejectsNonEmptySeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)
	if _, err := eng.AcceptTask(ctx, "usr_designer", &types.AcceptTaskReq{TaskId: taskID, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "design"}); err == nil {
		t.Fatal("filled seat should not be removable")
	}
}

// Unknown role ids are rejected.
func TestRemoveSeatRejectsUnknownRole(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)
	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "nope"}); err == nil {
		t.Fatal("unknown role should be rejected")
	}
	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: ""}); err == nil {
		t.Fatal("empty role should be rejected")
	}
}

// The last live seat cannot be removed; the task must be cancelled instead.
func TestRemoveSeatRejectsLastSeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t) // two seats
	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "design"}); err == nil {
		t.Fatal("last live seat should not be removable")
	}
}

// After removing an empty seat, the remaining seat can still be accepted and
// the task still settles — proving payout math was not broken by the tombstone.
func TestRemoveSeatThenSettleRemainingSeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t) // pool 500, design 60% copy 40%

	if _, err := eng.RemoveSeat(ctx, "usr_publisher", &types.RemoveSeatReq{TaskId: taskID, RoleId: "copy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_designer", &types.AcceptTaskReq{TaskId: taskID, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	// Unified settlement needs the publisher to start before work can be submitted.
	if _, err := eng.StartTask(ctx, "usr_publisher", &types.StartTaskReq{TaskId: taskID}); err != nil {
		t.Fatalf("start after removal: %v", err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_designer", &types.SubmitTaskReq{TaskId: taskID, Payload: map[string]interface{}{"result": "done"}}); err != nil {
		t.Fatal(err)
	}
	settled, err := eng.ConfirmTask(ctx, "usr_publisher", &types.ConfirmTaskReq{TaskId: taskID})
	if err != nil {
		t.Fatalf("settle after removal: %v", err)
	}
	if settled.Status != model.StatusSettled {
		t.Fatalf("task status %s, want settled", settled.Status)
	}
}
