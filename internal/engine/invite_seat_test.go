package engine

import (
	"context"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// newSeatInviteTask creates a funded collaborative task with two open seats:
// "design" (60%) and "copy" (40%). Invitations can bind to either seat.
func newSeatInviteTask(t *testing.T) (*Engine, store.Store, *clock, string) {
	t.Helper()
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
	created, err := eng.CreateTask(context.Background(), "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(context.Background(), "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	return eng, st, clk, created.Id
}

func seatOf(task *types.Task, id string) *types.CollaborativeRole {
	for i := range task.Collaborative.Roles {
		if task.Collaborative.Roles[i].Id == id {
			return &task.Collaborative.Roles[i]
		}
	}
	return nil
}

// A seat-bound invitation fills exactly that seat when the invitee accepts
// without sending a role_id. The stored invite and the task both reflect it.
func TestSeatInviteAutoFillsBoundSeat(t *testing.T) {
	ctx := context.Background()
	eng, st, _, taskID := newSeatInviteTask(t)

	list, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_designer"}, RoleId: "design",
	})
	if err != nil || len(list.Items) != 1 || list.Items[0].RoleId != "design" {
		t.Fatalf("invite %+v err %v", list, err)
	}

	persisted, err := st.Invites().Find(ctx, taskID, "usr_designer")
	if err != nil || persisted.RoleID != "design" || persisted.Status != model.InvitePending {
		t.Fatalf("persisted invite %+v err %v", persisted, err)
	}

	// Accepting with no explicit role_id must inherit "design" from the invite.
	accepted, err := eng.AcceptTask(ctx, "usr_designer", &types.AcceptTaskReq{TaskId: taskID})
	if err != nil {
		t.Fatal(err)
	}
	design := seatOf(accepted, "design")
	copySeat := seatOf(accepted, "copy")
	if design == nil || design.Status != model.RoleFilled || design.AssigneeUserId == nil || *design.AssigneeUserId != "usr_designer" {
		t.Fatalf("design seat %+v", design)
	}
	if copySeat == nil || copySeat.Status != model.RoleOpen || copySeat.AssigneeUserId != nil {
		t.Fatalf("copy seat must stay open: %+v", copySeat)
	}
	if len(accepted.ExecutorIds) != 1 || accepted.ExecutorIds[0] != "usr_designer" {
		t.Fatalf("executors %v", accepted.ExecutorIds)
	}

	done, err := st.Invites().Find(ctx, taskID, "usr_designer")
	if err != nil || done.Status != model.InviteAccepted || done.RoleID != "design" {
		t.Fatalf("accepted invite %+v err %v", done, err)
	}
}

// A seat-bound invitation cannot be used to claim a different seat.
func TestSeatInviteRejectsDifferentSeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)

	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_designer"}, RoleId: "design",
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := eng.AcceptTask(ctx, "usr_designer", &types.AcceptTaskReq{TaskId: taskID, RoleId: "copy"})
	if codeOf(err) != "invalid_argument" {
		t.Fatalf("expected seat mismatch, got %v", err)
	}
	if accepted != nil {
		t.Fatalf("no task should be returned on mismatch: %+v", accepted)
	}

	// The failed accept must not change the task or consume the invitation.
	task, err := eng.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(task.ExecutorIds) != 0 {
		t.Fatalf("executors %v", task.ExecutorIds)
	}
	for _, role := range task.Collaborative.Roles {
		if role.Status != model.RoleOpen || role.AssigneeUserId != nil {
			t.Fatalf("seat %s changed: %+v", role.Id, role)
		}
	}
	mine, err := eng.ListMyInvites(ctx, "usr_designer", model.InvitePending)
	if err != nil || len(mine.Items) != 1 || mine.Items[0].RoleId != "design" {
		t.Fatalf("invite consumed by failed accept: %+v err %v", mine, err)
	}
}

// A matching explicit role_id is still accepted (the UI path from Invites.tsx).
func TestSeatInviteAcceptsMatchingSeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)

	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_copywriter"}, RoleId: "copy",
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := eng.AcceptTask(ctx, "usr_copywriter", &types.AcceptTaskReq{TaskId: taskID, RoleId: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	copySeat := seatOf(accepted, "copy")
	if copySeat == nil || copySeat.Status != model.RoleFilled || copySeat.AssigneeUserId == nil || *copySeat.AssigneeUserId != "usr_copywriter" {
		t.Fatalf("copy seat %+v", copySeat)
	}
}

// A legacy task-level invite (no role_id) still requires the acceptor to pick
// an open seat explicitly. This keeps old invites and assignee-policy invites
// working after the schema change.
func TestTaskLevelInviteStillNeedsExplicitSeat(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)

	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_legacy"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_legacy", &types.AcceptTaskReq{TaskId: taskID}); codeOf(err) != "invalid_argument" {
		t.Fatalf("expected missing role_id, got %v", err)
	}
	accepted, err := eng.AcceptTask(ctx, "usr_legacy", &types.AcceptTaskReq{TaskId: taskID, RoleId: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if seat := seatOf(accepted, "copy"); seat == nil || seat.Status != model.RoleFilled {
		t.Fatalf("legacy accept seat %+v", seat)
	}
}

// If someone else takes the bound seat first, the invitation fails with a
// conflict instead of silently placing the acceptor in a different seat.
func TestSeatInviteFailsWhenBoundSeatTaken(t *testing.T) {
	ctx := context.Background()
	eng, _, _, taskID := newSeatInviteTask(t)

	if _, err := eng.InviteTask(ctx, "usr_publisher", &types.InviteUsersReq{
		TaskId: taskID, UserIds: []string{"usr_invited"}, RoleId: "design",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_walkin", &types.AcceptTaskReq{TaskId: taskID, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_invited", &types.AcceptTaskReq{TaskId: taskID}); codeOf(err) != "conflict" {
		t.Fatalf("expected occupied seat conflict, got %v", err)
	}
	task, err := eng.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if seat := seatOf(task, "design"); seat == nil || seat.AssigneeUserId == nil || *seat.AssigneeUserId != "usr_walkin" {
		t.Fatalf("design seat stolen: %+v", seat)
	}
}

func TestValidateInviteSeat(t *testing.T) {
	collab := model.Task{
		Kind: model.KindCollaborative,
		Collaborative: &model.Collaborative{Roles: []model.Role{
			{ID: "design", Status: model.RoleOpen},
			{ID: "copy", Status: model.RoleFilled, AssigneeUserID: "usr_other"},
		}},
	}
	cases := []struct {
		name    string
		task    model.Task
		roleID  string
		want    string
		wantErr string
	}{
		{name: "legacy empty", task: collab, roleID: "", want: ""},
		{name: "open seat", task: collab, roleID: " design ", want: "design"},
		{name: "non collaborative", task: model.Task{Kind: model.KindExclusive}, roleID: "design", wantErr: "invalid_argument"},
		{name: "malformed id", task: collab, roleID: "bad id!", wantErr: "invalid_argument"},
		{name: "unknown seat", task: collab, roleID: "missing", wantErr: "invalid_argument"},
		{name: "occupied seat", task: collab, roleID: "copy", wantErr: "conflict"},
		{name: "no collaborative settings", task: model.Task{Kind: model.KindCollaborative}, roleID: "design", wantErr: "conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateInviteSeat(tc.task, tc.roleID)
			if tc.wantErr != "" {
				if codeOf(err) != tc.wantErr {
					t.Fatalf("got %q err %v, want code %s", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q err %v, want %q", got, err, tc.want)
			}
		})
	}
}
