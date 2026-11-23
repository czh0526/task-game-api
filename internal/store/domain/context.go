// Package domain implements the per-aggregate-root store skeleton (P2).
//
// Unlike cell.Store, which routes every table by the same user/key hash, this
// store routes each aggregate-root family (user / task / message) through its
// own Resolver and its own ordered DSN list. A transaction is pinned to ONE
// domain/shard by shard.TxScope; touching a second shard of the same domain
// returns shard.ErrCrossShard instead of silently starting a 2PC.
//
// STAGE 1 BOUNDARY: this package is not wired into ServiceContext yet. The
// default runtime stays on cell.Store. It is deliberately self-contained so
// it can be reviewed and tested without changing API behaviour.
package domain

import (
	"context"

	"taskgame/internal/plane"
	"taskgame/internal/store"
	"taskgame/internal/store/shard"
)

type ctxKey int

const (
	keyRoute ctxKey = iota
	keyScope
)

// Route names the primary aggregate root of an operation. WithRoute is how a
// caller tells InTx which domain/shard to pin. Create=true asks Resolver.Resolve
// to assign a directory row; false uses ResolveExisting so legacy keys keep
// hashing over the pinned shard count.
type Route struct {
	Domain plane.Domain
	Key    string
	Create bool
}

func WithRoute(ctx context.Context, domain plane.Domain, key string, create bool) context.Context {
	return context.WithValue(ctx, keyRoute, Route{Domain: domain, Key: key, Create: create})
}

func RouteFrom(ctx context.Context) (Route, bool) {
	if ctx == nil {
		return Route{}, false
	}
	r, ok := ctx.Value(keyRoute).(Route)
	return r, ok
}

// scopeState is bound to one InTx callback. It records the pinned placements
// and the transaction-owned repos for the primary domain.
type scopeState struct {
	tx         *shard.TxScope
	mainDomain plane.Domain
	txStore    store.Store
	repos      map[plane.Domain]store.Repos
}

func withScope(ctx context.Context, s *scopeState) context.Context {
	return context.WithValue(ctx, keyScope, s)
}

func scopeFrom(ctx context.Context) *scopeState {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(keyScope).(*scopeState)
	return s
}
