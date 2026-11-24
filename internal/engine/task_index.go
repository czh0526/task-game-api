package engine

import (
	"context"

	"taskgame/internal/model"
	"taskgame/internal/search"
	"taskgame/internal/types"
)

// indexQuery maps the store filter onto an index query. It returns ok=false
// when the filter uses an attribute the denormalized index cannot express
// (distance, amount range, urgency/recurrence/executor requirements), so the
// caller falls back to the authoritative store scan.
func indexQuery(f model.TaskFilter) (search.Query, bool) {
	if f.Latitude != nil || f.Longitude != nil || f.RadiusKm > 0 ||
		f.MinAmount > 0 || f.MaxAmount > 0 ||
		f.Urgency != "" || f.Recurrence != "" || f.ExecutorRequirement != "" {
		return search.Query{}, false
	}
	switch f.Sort {
	case "", "recent":
	default:
		return search.Query{}, false
	}
	q := search.Query{Keyword: f.Q, Limit: f.Limit + 1}
	if f.Keyword != "" {
		// Q and keyword both narrow by text; keep the more specific one.
		if q.Keyword == "" {
			q.Keyword = f.Keyword
		} else {
			q.Keyword += " " + f.Keyword
		}
	}
	if f.Status != "" {
		q.Statuses = []string{f.Status}
	} else if len(f.Statuses) > 0 {
		q.Statuses = append([]string{}, f.Statuses...)
	}
	// The denormalized doc has no category/kind separate filter surface beyond
	// keyword; a Kind filter is expressed as a keyword to narrow safely.
	if f.Kind != "" {
		q.Keyword = appendKeyword(q.Keyword, f.Kind)
	}
	q.Limit = f.Limit
	return q, true
}

func appendKeyword(base, extra string) string {
	if base == "" {
		return extra
	}
	return base + " " + extra
}

// listViaIndex answers a listing from the index. It returns ok=false to fall
// back to the store when the filter is not index-expressible. Docs give IDs in
// rank order; details are re-read by primary key (no fan-out) so the response
// keeps the full, authoritative task shape.
func (e *Engine) listViaIndex(ctx context.Context, f model.TaskFilter, limit, offset int) (*types.TaskList, bool, error) {
	q, ok := indexQuery(f)
	if !ok {
		return nil, false, nil
	}
	// Cursor in the public API is an offset. Ask the index for the whole window
	// in one query; ES caps at 100 so large offsets fall back to the store.
	if offset+limit > 100 {
		return nil, false, nil
	}
	q.Limit = offset + limit
	docs, err := e.taskIndex.Query(ctx, q)
	if err != nil {
		// An index error must not break the API: degrade to the store.
		return nil, false, nil
	}
	total, err := e.taskIndex.Count(ctx, q)
	if err != nil {
		return nil, false, nil
	}
	start := offset
	if start > len(docs) {
		start = len(docs)
	}
	page := docs[start:]
	more := len(page) > limit
	if more {
		page = page[:limit]
	}
	items := make([]types.Task, 0, len(page))
	for _, doc := range page {
		t, err := e.store.Tasks().Get(ctx, doc.ID)
		if err != nil {
			// A stale index doc (task deleted) is skipped, not fatal.
			continue
		}
		dto, err := e.dto(ctx, e.store, t)
		if err != nil {
			return nil, true, err
		}
		items = append(items, *dto)
	}
	var next *string
	if more {
		next = encodeCursor(offset + limit)
	}
	return &types.TaskList{Items: items, Total: total, NextCursor: next}, true, nil
}
