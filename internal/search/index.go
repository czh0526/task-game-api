// Package search is the read-side skeleton for P3.
//
// A sharded store must not answer "latest 100 tasks across all shards" by
// scattering the query to every shard and merging. That query belongs to a
// separate index. Only primary-key lookups should go to the shards.
//
// SKELETON BOUNDARY: MemoryTaskIndex is runnable and keeps queries off the
// primary store in tests. A production index (Elasticsearch / OpenSearch) and
// a real CDC projector are TODO; the TaskIndex interface is the seam.
package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// TaskDoc is the denormalized, wide row kept in the index. CreatedAt is a
// fixed-width RFC3339 string (see model.FormatTime), so lexical order is
// chronological order.
type TaskDoc struct {
	ID        string
	Title     string
	Keywords  []string
	Category  string
	Kind      string
	Status    string
	CreatedAt string
}

type Query struct {
	Keyword      string
	Categories   []string
	Statuses     []string
	BeforeCursor string
	Limit        int
}

type TaskIndex interface {
	Upsert(ctx context.Context, doc TaskDoc) error
	Delete(ctx context.Context, id string) error
	Query(ctx context.Context, q Query) ([]TaskDoc, error)
}

type MemoryTaskIndex struct {
	mu   sync.RWMutex
	docs map[string]TaskDoc
}

func NewMemoryTaskIndex() *MemoryTaskIndex {
	return &MemoryTaskIndex{docs: make(map[string]TaskDoc)}
}

func (m *MemoryTaskIndex) Upsert(_ context.Context, doc TaskDoc) error {
	if doc.ID == "" || doc.CreatedAt == "" {
		return fmt.Errorf("search: doc needs id and created_at")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[doc.ID] = doc
	return nil
}

func (m *MemoryTaskIndex) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, id)
	return nil
}

func (m *MemoryTaskIndex) Query(_ context.Context, q Query) ([]TaskDoc, error) {
	m.mu.RLock()
	out := make([]TaskDoc, 0, len(m.docs))
	for _, doc := range m.docs {
		if !matches(doc, q) {
			continue
		}
		out = append(out, doc)
	}
	m.mu.RUnlock()
	// Newest first; Cursor is (created_at, id) as in TaskRepo.ListSweep.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func matches(doc TaskDoc, q Query) bool {
	if q.BeforeCursor != "" && doc.CreatedAt >= q.BeforeCursor {
		return false
	}
	if q.Keyword != "" {
		hay := strings.ToLower(doc.Title + " " + doc.Category + " " + strings.Join(doc.Keywords, " "))
		if !strings.Contains(hay, strings.ToLower(q.Keyword)) {
			return false
		}
	}
	if len(q.Categories) > 0 && !contains(q.Categories, doc.Category) {
		return false
	}
	if len(q.Statuses) > 0 && !contains(q.Statuses, doc.Status) {
		return false
	}
	return true
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Change is one projection event. Kind is "upsert" or "delete".
type Change struct {
	Kind string
	Doc  TaskDoc
}

// Projector applies changes to the index.
//
// TODO: consume an outbox / CDC stream instead of synchronous double writes,
// so a task write and its index update are eventually consistent and can be
// replayed after a failure.
type Projector struct{ idx TaskIndex }

func NewProjector(idx TaskIndex) (*Projector, error) {
	if idx == nil {
		return nil, fmt.Errorf("search: projector needs an index")
	}
	return &Projector{idx: idx}, nil
}

func (p *Projector) Apply(ctx context.Context, c Change) error {
	switch c.Kind {
	case "upsert":
		return p.idx.Upsert(ctx, c.Doc)
	case "delete":
		return p.idx.Delete(ctx, c.Doc.ID)
	default:
		return fmt.Errorf("search: unknown change kind %q", c.Kind)
	}
}
