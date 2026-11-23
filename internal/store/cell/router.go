// Package cell implements horizontal, cell-based sharding (plan A).
//
// A deployment is N independent databases with identical schemas. There is no
// full primary database: every cell owns roughly 1/N of users, tasks, messages
// and every other business table. A request is routed to one cell by a routing
// key derived from the authenticated user id or from the aggregate-root id in
// the URL. All writes for one request run inside a single-cell transaction, so
// no distributed transaction (2PC) is needed.
//
// Reads that cannot be pinned to a key (market listing, login by email,
// webhook delivery scan) are fanned out across every cell and merged.
package cell

import (
	"context"
	"hash/fnv"
)

// Router maps a routing key to a cell using a stable FNV-1a hash. It is
// stateless: no directory table and no primary database are required, so any
// API process can compute the same answer. Resizing N is a data migration
// (see README); adding a cell never changes the key space of existing cells
// when a directory override is used.
type Router struct {
	N int
}

// New builds a router for n cells. n < 1 is treated as 1.
func New(n int) *Router {
	if n < 1 {
		n = 1
	}
	return &Router{N: n}
}

// CellFor returns the cell that owns key, in [0, N).
func (r *Router) CellFor(key string) int {
	if r == nil || r.N <= 1 || key == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(r.N))
}

// Len is the number of cells.
func (r *Router) Len() int {
	if r == nil {
		return 1
	}
	return r.N
}

type ctxKey int

const (
	keyCell ctxKey = iota
	keyHint
)

// Kind identifies which aggregate root a URL id refers to. The locator uses
// it to pick the right Get call when it has to search every cell.
const (
	KindUser = "user"
	KindTask = "task"
	KindChat = "chat"
)

type routeHint struct {
	Key  string
	Kind string
}

// WithCell pins a context to a specific cell. Background loops use this to
// sweep one cell at a time; the middleware uses it after resolving a hint.
func WithCell(ctx context.Context, i int) context.Context {
	return context.WithValue(ctx, keyCell, i)
}

// CellFrom returns an explicitly pinned cell index, if any.
func CellFrom(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	i, ok := ctx.Value(keyCell).(int)
	return i, ok
}

// WithHint attaches a routing key and its kind before a request is handled.
func WithHint(ctx context.Context, key, kind string) context.Context {
	return context.WithValue(ctx, keyHint, routeHint{Key: key, Kind: kind})
}

// HintFrom returns the routing hint attached by the middleware.
func HintFrom(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	h, ok := ctx.Value(keyHint).(routeHint)
	if !ok {
		return "", ""
	}
	return h.Key, h.Kind
}
