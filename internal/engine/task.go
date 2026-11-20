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
	t, err := r.Tasks().Lock(ctx, taskID)
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
	filter, err := taskFilterFromReq(req)
	if err != nil {
		return nil, err
	}
	if trim(req.Assignable) == "me" {
		return e.listAssignableWith(ctx, userID, filter, limit, offset)
	}
	filter.Limit = limit + 1
	filter.Offset = offset
	total, err := e.store.Tasks().Count(ctx, filter)
	if err != nil {
		return nil, err
	}
	rows, err := e.store.Tasks().List(ctx, filter)
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
	return &types.TaskList{Items: items, Total: total, NextCursor: next}, nil
}

// taskFilterFromReq validates the search parameters carried by a list request
// and maps them onto a store filter. Distance filtering requires both a center
// coordinate and a positive radius.
func taskFilterFromReq(req *types.ListTasksReq) (model.TaskFilter, error) {
	f := model.TaskFilter{
		Q:                   trim(req.Q),
		Status:              trim(req.Status),
		Kind:                trim(req.Kind),
		Keyword:             trim(req.Keyword),
		Urgency:             trim(req.Urgency),
		Recurrence:          trim(req.Recurrence),
		ExecutorRequirement: trim(req.ExecutorRequirement),
		MinAmount:           req.MinAmount,
		MaxAmount:           req.MaxAmount,
		Sort:                trim(req.Sort),
		Latitude:            req.Latitude,
		Longitude:           req.Longitude,
		RadiusKm:            req.RadiusKm,
	}
	if f.MinAmount < 0 || f.MaxAmount < 0 || (f.MaxAmount > 0 && f.MinAmount > f.MaxAmount) {
		return model.TaskFilter{}, biz.Invalid("invalid amount range")
	}
	switch f.Sort {
	case "", "recent", "amount_asc", "amount_desc", "deadline_asc", "distance_asc":
	default:
		return model.TaskFilter{}, biz.Invalid("unknown sort")
	}
	if (req.Latitude == nil) != (req.Longitude == nil) {
		return model.TaskFilter{}, biz.Invalid("latitude and longitude must be set together")
	}
	if req.Latitude != nil {
		if *req.Latitude < -90 || *req.Latitude > 90 || *req.Longitude < -180 || *req.Longitude > 180 {
			return model.TaskFilter{}, biz.Invalid("invalid coordinates")
		}
		if req.RadiusKm <= 0 || req.RadiusKm > 20015 {
			return model.TaskFilter{}, biz.Invalid("radius_km must be between 0 and 20015")
		}
	}
	return f, nil
}

func (e *Engine) listAssignableWith(ctx context.Context, userID string, base model.TaskFilter, limit, offset int) (*types.TaskList, error) {
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
	// Search filters run in SQL; canAcceptNow is the remaining per-user check.
	f := base
	f.Status = ""
	f.Statuses = []string{model.StatusOpen, model.StatusActive}
	f.Limit = 100
	var hits []model.Task
	var rawAt []int
	raw := 0
	for {
		f.Offset = raw
		rows, err := e.store.Tasks().List(ctx, f)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			break
		}
		for i, row := range rows {
			if base.Status != "" && row.Status != base.Status {
				continue
			}
			if !e.canAcceptNow(ctx, row, userID, ability, byTask) {
				continue
			}
			hits = append(hits, row)
			rawAt = append(rawAt, raw+i)
		}
		raw += len(rows)
		if len(rows) < 100 {
			break
		}
	}
	total := int64(len(hits))
	// Slice the page that starts at the requested offset.
	start := 0
	for start < len(hits) && rawAt[start] < offset {
		start++
	}
	end := start + limit
	if end > len(hits) {
		end = len(hits)
	}
	page := hits[start:end]
	var next *string
	if end < len(hits) {
		next = encodeCursor(rawAt[end])
	}
	items := make([]types.Task, 0, len(page))
	for _, t := range page {
		dto, err := e.dto(ctx, e.store, t)
		if err != nil {
			return nil, err
		}
		items = append(items, *dto)
	}
	return &types.TaskList{Items: items, Total: total, NextCursor: next}, nil
}
