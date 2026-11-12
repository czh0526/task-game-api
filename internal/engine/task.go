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

func loadTask(ctx context.Context, r store.Repos, taskID string) (model.Task, error) {
	if !id.ValidTaskID(taskID) {
		return model.Task{}, biz.Invalid("taskId must match ^tsk_")
	}
	t, err := r.Tasks().Get(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return model.Task{}, biz.NotFound("task not found")
	}
	return t, err
}

// CreateTask stores a draft. The publisher wallet is created with the configured opening balance.
func (e *Engine) CreateTask(ctx context.Context, userID string, req *types.CreateTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/tasks", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		if _, err := r.Users().Ensure(ctx, userID, e.now()); err != nil {
			return nil, err
		}
		t, err := e.buildTask(userID, req)
		if err != nil {
			return nil, err
		}
		t.ID = id.New("tsk_")
		if err := r.Tasks().Insert(ctx, t); err != nil {
			return nil, err
		}
		return e.dto(ctx, r, t)
	})
}

// GetTask returns one task.
func (e *Engine) GetTask(ctx context.Context, taskID string) (*types.Task, error) {
	t, err := loadTask(ctx, e.store, taskID)
	if err != nil {
		return nil, err
	}
	return e.dto(ctx, e.store, t)
}

// ListTasks filters the marketplace. Cursor is an opaque offset.
// assignable=me returns tasks the caller can accept right now.
func (e *Engine) ListTasks(ctx context.Context, userID string, req *types.ListTasksReq) (*types.TaskList, error) {
	if req.Limit < 0 || req.Limit > 100 {
		return nil, biz.Invalid("limit must be between 1 and 100")
	}
	limit := int(req.Limit)
	if limit == 0 {
		limit = 20
	}
	offset, err := decodeCursor(req.Cursor)
	if err != nil {
		return nil, err
	}
	switch trim(req.Assignable) {
	case "", "me":
	default:
		return nil, biz.Invalid("assignable must be me")
	}
	if trim(req.Assignable) == "me" {
		return e.listAssignable(ctx, userID, req, limit, offset)
	}
	rows, err := e.store.Tasks().List(ctx, model.TaskFilter{
		Status: req.Status,
		Kind:   req.Kind,
		Limit:  limit + 1,
		Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	var next *string
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(offset + limit)
	}
	items := make([]types.Task, 0, len(rows))
	for _, t := range rows {
		dto, err := e.dto(ctx, e.store, t)
		if err != nil {
			return nil, err
		}
		items = append(items, *dto)
	}
	return &types.TaskList{Items: items, NextCursor: next}, nil
}

func (e *Engine) listAssignable(ctx context.Context, userID string, req *types.ListTasksReq, limit, offset int) (*types.TaskList, error) {
	ability, err := e.abilityOf(ctx, e.store, userID)
	if err != nil {
		return nil, err
	}
	invites, err := e.store.Invites().ListByUser(ctx, userID, "")
	if err != nil {
		return nil, err
	}
	byTask := map[string]model.Invite{}
	for _, inv := range invites {
		byTask[inv.TaskID] = inv
	}
	var hits []model.Task
	var rawAt []int
	raw := offset
	for len(hits) <= limit {
		rows, err := e.store.Tasks().List(ctx, model.TaskFilter{
			Statuses: []string{model.StatusOpen, model.StatusActive},
			Kind:     req.Kind,
			Limit:    100,
			Offset:   raw,
		})
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for i, row := range rows {
			if req.Status != "" && row.Status != req.Status {
				continue
			}
			if !e.canAcceptNow(ctx, row, userID, ability, byTask) {
				continue
			}
			hits = append(hits, row)
			rawAt = append(rawAt, raw+i)
			if len(hits) > limit {
				break
			}
		}
		raw += len(rows)
		if len(hits) > limit || len(rows) < 100 {
			break
		}
	}
	var next *string
	if len(hits) > limit {
		hits = hits[:limit]
		next = encodeCursor(rawAt[limit])
	}
	items := make([]types.Task, 0, len(hits))
	for _, t := range hits {
		dto, err := e.dto(ctx, e.store, t)
		if err != nil {
			return nil, err
		}
		items = append(items, *dto)
	}
	return &types.TaskList{Items: items, NextCursor: next}, nil
}
