package engine

import (
	"context"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// SetAssignees replaces the whole specific-policy assignee list. The publisher
// may add or remove designated executors only while the task is still open,
// before anyone has accepted. Invites are kept in sync: removed users lose
// their invite, added users get a fresh pending invite, and everyone learns
// about the change through task.assignees_changed.
func (e *Engine) SetAssignees(ctx context.Context, userID string, req *types.SetAssigneesReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPut, taskPath(req.TaskId, "assignees"), req.IdempotencyKey, req,
		func(ctx context.Context, r store.Repos) (*types.Task, error) {
			t, err := loadTask(ctx, r, req.TaskId)
			if err != nil {
				return nil, err
			}
			if t.PublisherID != userID {
				return nil, biz.Forbidden("only the publisher can change assignees")
			}
			if t.Status != model.StatusOpen {
				return nil, biz.Conflict("assignees can only be changed while the task is open")
			}
			if t.AssigneePolicy.Type != model.PolicySpecific {
				return nil, biz.Conflict("assignees can only be changed on a specific-assignee task")
			}
			ids, err := cleanUserIDs(req.UserIds)
			if err != nil {
				return nil, err
			}
			if len(ids) == 0 {
				return nil, biz.Invalid("user_ids is required")
			}

			previous := append([]string{}, t.AssigneePolicy.UserIDs...)
			next := dedupe(ids)

			// Determine who was added and who was removed before mutating the task.
			var added, removed []string
			for _, id := range next {
				if !contains(previous, id) {
					added = append(added, id)
				}
			}
			for _, id := range previous {
				if !contains(next, id) {
					removed = append(removed, id)
				}
			}

			now := e.now()
			t.AssigneePolicy.UserIDs = next
			t.UpdatedAt = now
			if err := r.Tasks().Update(ctx, t); err != nil {
				return nil, err
			}
			// Drop invites for users no longer designated.
			for _, id := range removed {
				if err := r.Invites().DeleteByUser(ctx, t.ID, id); err != nil {
					return nil, err
				}
			}
			// Invite the newly designated users.
			if len(added) > 0 {
				if err := e.addInvites(ctx, r, t, added, ""); err != nil {
					return nil, err
				}
			}

			dto, err := e.dto(ctx, r, t)
			if err != nil {
				return nil, err
			}
			notify := append(append([]string{}, previous...), next...)
			if err := e.emit(ctx, r, parties(t, notify...), model.EventTaskAssigneesChanged, map[string]any{
				"task": dto, "added": added, "removed": removed,
			}); err != nil {
				return nil, err
			}
			return dto, nil
		})
}
