package engine

import (
	"context"
	"errors"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

func (e *Engine) allowAccept(ctx context.Context, r store.Repos, t model.Task, userID string, ability int64) error {
	inv, err := r.Invites().Find(ctx, t.ID, userID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var invite *model.Invite
	if err == nil {
		invite = &inv
	}
	if t.AssigneePolicy.Type == model.PolicySpecific {
		if invite != nil && invite.Status == model.InviteDeclined {
			return biz.Forbidden("invite was declined; ask the publisher to invite you again")
		}
		if contains(t.AssigneePolicy.UserIDs, userID) {
			return nil
		}
		if invite != nil && (invite.Status == model.InvitePending || invite.Status == model.InviteAccepted) {
			return nil
		}
		return biz.Forbidden("you are not on the assignee list")
	}
	return checkPolicy(t, userID, ability)
}

func (e *Engine) markInviteAccepted(ctx context.Context, r store.Repos, taskID, userID string) error {
	inv, err := r.Invites().Find(ctx, taskID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if inv.Status == model.InviteDeclined {
		return nil
	}
	if inv.Status == model.InviteAccepted {
		return nil
	}
	inv.Status = model.InviteAccepted
	inv.UpdatedAt = e.now()
	return r.Invites().Update(ctx, inv)
}

func (e *Engine) inviteSpecificOnFund(ctx context.Context, r store.Repos, t model.Task) error {
	if t.AssigneePolicy.Type != model.PolicySpecific || len(t.AssigneePolicy.UserIDs) == 0 {
		return nil
	}
	return e.addInvites(ctx, r, t, t.AssigneePolicy.UserIDs)
}

func (e *Engine) addInvites(ctx context.Context, r store.Repos, t model.Task, userIDs []string) error {
	now := e.now()
	var created []string
	for _, uid := range dedupe(userIDs) {
		if uid == t.PublisherID {
			continue
		}
		existing, err := r.Invites().Find(ctx, t.ID, uid)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		inv := model.Invite{
			ID: id.New("inv_"), TaskID: t.ID, UserID: uid, Status: model.InvitePending,
			CreatedAt: now, UpdatedAt: now,
		}
		if err == nil {
			inv.ID = existing.ID
			inv.CreatedAt = existing.CreatedAt
		}
		if err := r.Invites().Upsert(ctx, inv); err != nil {
			return err
		}
		created = append(created, uid)
	}
	if len(created) == 0 {
		return nil
	}
	return e.emit(ctx, r, append(parties(t), created...), model.EventTaskInvited, map[string]any{
		"task_id": t.ID, "user_ids": created,
	})
}

// InviteTask adds pending invites while the task still has open capacity.
func (e *Engine) InviteTask(ctx context.Context, userID string, req *types.InviteUsersReq) (*types.InviteList, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "invites"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.InviteList, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.PublisherID != userID {
			return nil, biz.Forbidden("only the publisher can invite")
		}
		if !hasInviteCapacity(t) {
			return nil, biz.Conflict("task has no open capacity")
		}
		ids, err := cleanUserIDs(req.UserIds)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, biz.Invalid("user_ids is required")
		}
		if t.AssigneePolicy.Type == model.PolicySpecific {
			t.AssigneePolicy.UserIDs = dedupe(append(append([]string{}, t.AssigneePolicy.UserIDs...), ids...))
			t.UpdatedAt = e.now()
			if err := r.Tasks().Update(ctx, t); err != nil {
				return nil, err
			}
		}
		if err := e.addInvites(ctx, r, t, ids); err != nil {
			return nil, err
		}
		return e.inviteList(ctx, r, t.ID)
	})
}

// ListTaskInvites returns every invite for the publisher, or the caller's own invite.
func (e *Engine) ListTaskInvites(ctx context.Context, userID, taskID string) (*types.InviteList, error) {
	t, err := loadTask(ctx, e.store, taskID)
	if err != nil {
		return nil, err
	}
	rows, err := e.store.Invites().ListByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if userID != t.PublisherID {
		filtered := make([]model.Invite, 0, 1)
		for _, row := range rows {
			if row.UserID == userID {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return toInviteList(rows), nil
}

// ListMyInvites lists invitations for the caller.
func (e *Engine) ListMyInvites(ctx context.Context, userID, status string) (*types.InviteList, error) {
	switch status {
	case "", model.InvitePending, model.InviteAccepted, model.InviteDeclined:
	default:
		return nil, biz.Invalid("status must be pending, accepted, or declined")
	}
	rows, err := e.store.Invites().ListByUser(ctx, userID, status)
	if err != nil {
		return nil, err
	}
	return toInviteList(rows), nil
}

// DeclineInvite marks a pending invite declined.
func (e *Engine) DeclineInvite(ctx context.Context, userID string, req *types.DeclineInviteReq) (*types.Invite, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/invites/"+req.InviteId+"/decline", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Invite, error) {
		if !id.ValidInviteID(req.InviteId) {
			return nil, biz.Invalid("inviteId must match ^inv_")
		}
		inv, err := r.Invites().Get(ctx, req.InviteId)
		if errors.Is(err, store.ErrNotFound) {
			return nil, biz.NotFound("invite not found")
		}
		if err != nil {
			return nil, err
		}
		if inv.UserID != userID {
			return nil, biz.Forbidden("only the invitee can decline")
		}
		if inv.Status == model.InviteDeclined {
			out := toInvite(inv)
			return &out, nil
		}
		if inv.Status != model.InvitePending {
			return nil, biz.Conflict("invite is not pending")
		}
		inv.Status = model.InviteDeclined
		inv.UpdatedAt = e.now()
		if err := r.Invites().Update(ctx, inv); err != nil {
			return nil, err
		}
		t, err := loadTask(ctx, r, inv.TaskID)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t, userID), model.EventTaskInviteDeclined, map[string]any{
			"task_id": t.ID, "invite_id": inv.ID, "user_id": userID,
		}); err != nil {
			return nil, err
		}
		out := toInvite(inv)
		return &out, nil
	})
}

func (e *Engine) inviteList(ctx context.Context, r store.Repos, taskID string) (*types.InviteList, error) {
	rows, err := r.Invites().ListByTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return toInviteList(rows), nil
}

func hasInviteCapacity(t model.Task) bool {
	switch t.Kind {
	case model.KindExclusive:
		return t.Status == model.StatusOpen && len(t.ExecutorIDs) == 0
	case model.KindCompetitive:
		if t.Competitive == nil {
			return false
		}
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return false
		}
		return len(t.ExecutorIDs) < t.Competitive.Slots
	case model.KindCollaborative:
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return false
		}
		return hasOpenSeat(t)
	default:
		return false
	}
}

func (e *Engine) canAcceptNow(ctx context.Context, t model.Task, userID string, ability int64, invites map[string]model.Invite) bool {
	if userID == "" || userID == t.PublisherID {
		return false
	}
	if !hasAcceptCapacity(t, userID) {
		return false
	}
	inv, ok := invites[t.ID]
	var invite *model.Invite
	if ok {
		invite = &inv
	}
	switch t.AssigneePolicy.Type {
	case model.PolicySpecific:
		if invite != nil && invite.Status == model.InviteDeclined {
			return false
		}
		if contains(t.AssigneePolicy.UserIDs, userID) {
			return true
		}
		return invite != nil && (invite.Status == model.InvitePending || invite.Status == model.InviteAccepted)
	case model.PolicyCriteria:
		return checkPolicy(t, userID, ability) == nil
	case model.PolicyOpen:
		return true
	default:
		return false
	}
}

func hasAcceptCapacity(t model.Task, userID string) bool {
	switch t.Kind {
	case model.KindExclusive:
		return t.Status == model.StatusOpen && len(t.ExecutorIDs) == 0
	case model.KindCompetitive:
		if t.Competitive == nil || contains(t.ExecutorIDs, userID) {
			return false
		}
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return false
		}
		return len(t.ExecutorIDs) < t.Competitive.Slots
	case model.KindCollaborative:
		if t.Collaborative == nil || (t.Status != model.StatusOpen && t.Status != model.StatusActive) {
			return false
		}
		for _, role := range t.Collaborative.Roles {
			if role.AssigneeUserID == userID && (role.Status == model.RoleFilled || role.Status == model.RoleSubmitted) {
				return false
			}
		}
		return hasOpenSeat(t)
	default:
		return false
	}
}

func toInvite(inv model.Invite) types.Invite {
	return types.Invite{
		Id: inv.ID, TaskId: inv.TaskID, UserId: inv.UserID, Status: inv.Status,
		CreatedAt: model.FormatTime(inv.CreatedAt), UpdatedAt: model.FormatTime(inv.UpdatedAt),
	}
}

func toInviteList(rows []model.Invite) *types.InviteList {
	items := make([]types.Invite, 0, len(rows))
	for _, row := range rows {
		items = append(items, toInvite(row))
	}
	return &types.InviteList{Items: items}
}
