package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// newSpecificTask creates a funded open exclusive task with two designated
// executors and their pending invites.
func newSpecificTask(t *testing.T) (*Engine, store.Store, string) {
	t.Helper()
	eng, st, clk := newEngine(t, 1_000_000)
	req := exclusiveReq(clk)
	req.Payment.Amount = 500
	req.AssigneePolicy = types.AssigneePolicy{
		Type:    model.PolicySpecific,
		UserIds: []string{"usr_a", "usr_b"},
	}
	created, err := eng.CreateTask(context.Background(), "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(context.Background(), "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	return eng, st, created.Id
}

// Replacing the list swaps the policy, deletes removed users' invites, and
// creates pending invites for newly added users.
func TestSetAssigneesReplacesListAndSyncsInvites(t *testing.T) {
	ctx := context.Background()
	eng, st, taskID := newSpecificTask(t)

	updated, err := eng.SetAssignees(ctx, "usr_publisher", &types.SetAssigneesReq{
		TaskId: taskID, UserIds: []string{"usr_b", "usr_c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.AssigneePolicy.UserIds; len(got) != 2 || got[0] != "usr_b" || got[1] != "usr_c" {
		t.Fatalf("policy user ids %v", got)
	}

	// usr_a removed: invite gone. usr_b kept: invite still pending. usr_c added: new invite.
	if _, err := st.Invites().Find(ctx, taskID, "usr_a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed user invite should be gone, got %v", err)
	}
	for _, uid := range []string{"usr_b", "usr_c"} {
		inv, err := st.Invites().Find(ctx, taskID, uid)
		if err != nil || inv.Status != model.InvitePending {
			t.Fatalf("invite for %s: %+v err %v", uid, inv, err)
		}
	}

	persisted, err := st.Tasks().Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.AssigneePolicy.UserIDs) != 2 || persisted.AssigneePolicy.UserIDs[0] != "usr_b" {
		t.Fatalf("persisted policy %+v", persisted.AssigneePolicy)
	}
}

// Only the publisher may change the list.
func TestSetAssigneesRejectsNonPublisher(t *testing.T) {
	ctx := context.Background()
	eng, _, taskID := newSpecificTask(t)
	_, err := eng.SetAssignees(ctx, "usr_other", &types.SetAssigneesReq{
		TaskId: taskID, UserIds: []string{"usr_x"},
	})
	if err == nil || err.(*biz.Error).Status != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

// Once accepted, the task is active and the list is locked.
func TestSetAssigneesRejectsNonOpenTask(t *testing.T) {
	ctx := context.Background()
	eng, _, taskID := newSpecificTask(t)
	if _, err := eng.AcceptTask(ctx, "usr_a", &types.AcceptTaskReq{TaskId: taskID}); err != nil {
		t.Fatal(err)
	}
	_, err := eng.SetAssignees(ctx, "usr_publisher", &types.SetAssigneesReq{
		TaskId: taskID, UserIds: []string{"usr_a", "usr_b"},
	})
	if err == nil || err.(*biz.Error).Status != http.StatusConflict {
		t.Fatalf("expected conflict on active task, got %v", err)
	}
}

// Saving the list re-invites someone who had declined, so the publisher can
// bring them back without a separate invite entry point.
func TestSetAssigneesReinvitesDeclinedUser(t *testing.T) {
	ctx := context.Background()
	eng, st, taskID := newSpecificTask(t)

	inv, err := st.Invites().Find(ctx, taskID, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DeclineInvite(ctx, "usr_a", &types.DeclineInviteReq{InviteId: inv.ID}); err != nil {
		t.Fatal(err)
	}
	declined, err := st.Invites().Find(ctx, taskID, "usr_a")
	if err != nil || declined.Status != model.InviteDeclined {
		t.Fatalf("expected declined, got %+v err %v", declined, err)
	}

	// Saving the unchanged list still re-invites the declined user.
	if _, err := eng.SetAssignees(ctx, "usr_publisher", &types.SetAssigneesReq{
		TaskId: taskID, UserIds: []string{"usr_a", "usr_b"},
	}); err != nil {
		t.Fatal(err)
	}
	reinvited, err := st.Invites().Find(ctx, taskID, "usr_a")
	if err != nil || reinvited.Status != model.InvitePending {
		t.Fatalf("expected pending after re-invite, got %+v err %v", reinvited, err)
	}
}
