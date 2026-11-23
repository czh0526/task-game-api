package domain

import (
	"context"
	"fmt"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"
	"taskgame/internal/store/mysql"
	"taskgame/internal/store/shard"
	"taskgame/internal/store/sqlite"
)

var allDomains = []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage}

// Config describes one physical database list per domain. Domains may share
// DSNs (logical split, co-located data) or point at completely different
// servers. Control is optional: when omitted an in-memory directory and a
// topology built from DSNs are used, which is enough for single-process tests.
type Config struct {
	Driver         string
	DSNs           map[plane.Domain][]string
	InitialBalance int64
	Control        *plane.ControlPlane
	// ChatRouter is the legacy FNV router used to pin a NEW chat before a
	// directory row exists. Zero value means "derive from the message DSNs".
	ChatRouter shard.Router
	// StrictTx makes TxScope reject every same-domain cross-shard access.
	// It is off by default: in a co-located deployment several domains share
	// one physical store, so a cross-domain call still uses the same SQL
	// transaction. Once domains point at different DSNs, non-strict mode uses
	// separate pools (eventual consistency) and strict mode fails loudly.
	StrictTx bool
}

// Store is a store.Store whose tables are split by aggregate-root domain.
// Every domain owns an ordered DSN list; a placement (not a global hash)
// decides which physical store serves a repository call.
type Store struct {
	control  *plane.ControlPlane
	topo     *plane.Topology
	chat     shard.Router
	domain   *shard.DomainRouter
	strictTx bool

	pools  map[plane.Domain][]store.Store
	byDSN  map[string]store.Store
	opened []store.Store
}

// Open creates one connection pool per distinct DSN. When several domains
// share a DSN they also share the same pool: that is what keeps a co-located
// deployment's writes inside one physical database (and one transaction).
func Open(cfg Config) (*Store, error) {
	if len(cfg.DSNs) == 0 {
		return nil, fmt.Errorf("domain: at least one domain DSN list is required")
	}
	for _, d := range allDomains {
		if len(cfg.DSNs[d]) == 0 {
			return nil, fmt.Errorf("domain: domain %q has no DSNs", d)
		}
		for i, dsn := range cfg.DSNs[d] {
			if dsn == "" {
				return nil, fmt.Errorf("domain: %s DSNs[%d] is empty", d, i)
			}
		}
	}

	s := &Store{
		strictTx: cfg.StrictTx,
		pools:    make(map[plane.Domain][]store.Store, len(allDomains)),
		byDSN:    make(map[string]store.Store),
	}
	if cfg.Control != nil {
		if cfg.Control.Resolver == nil || cfg.Control.Topology == nil {
			return nil, fmt.Errorf("domain: control plane is incomplete")
		}
		s.control = cfg.Control
		s.topo = cfg.Control.Topology
	} else {
		topo := plane.NewTopology()
		for _, d := range allDomains {
			if err := topo.Register(d, cfg.DSNs[d]); err != nil {
				return nil, err
			}
		}
		cp, err := plane.NewControlPlane(plane.NewInMemoryDirectory(), topo)
		if err != nil {
			return nil, err
		}
		s.control, s.topo = cp, topo
	}

	s.chat = cfg.ChatRouter
	if s.chat.Enabled() == false {
		s.chat = shard.NewRouter(s.topo.Len(plane.DomainMessage))
	}
	dr, err := shard.NewDomainRouter(s.control, s.chat)
	if err != nil {
		return nil, err
	}
	s.domain = dr

	for _, d := range allDomains {
		for _, dsn := range cfg.DSNs[d] {
			st, ok := s.byDSN[dsn]
			if !ok {
				switch cfg.Driver {
				case "mysql":
					st, err = mysql.Open(dsn, cfg.InitialBalance)
				case "", "sqlite":
					st, err = sqlite.Open(dsn, cfg.InitialBalance)
				default:
					err = fmt.Errorf("unsupported domain driver %q", cfg.Driver)
				}
				if err != nil {
					_ = s.Close()
					return nil, fmt.Errorf("domain %s DSN %q: %w", d, dsn, err)
				}
				s.byDSN[dsn] = st
				s.opened = append(s.opened, st)
			}
			s.pools[d] = append(s.pools[d], st)
		}
	}
	return s, nil
}

// Control exposes the control plane for tests and for the optional directory
// backfill tool. It is not part of store.Store.
func (s *Store) Control() *plane.ControlPlane { return s.control }

// Topology exposes the ordered DSN list per domain.
func (s *Store) Topology() *plane.Topology { return s.topo }

// StoreFor returns the physical store of one domain/shard. Tests use it to
// assert distribution without going through the routing repos.
func (s *Store) StoreFor(d plane.Domain, shardIndex int) (store.Store, error) {
	list := s.pools[d]
	if shardIndex < 0 || shardIndex >= len(list) {
		return nil, fmt.Errorf("domain: shard %d out of range for %q", shardIndex, d)
	}
	return list[shardIndex], nil
}

func (s *Store) Ping(ctx context.Context) error {
	for i, st := range s.opened {
		if err := st.Ping(ctx); err != nil {
			return fmt.Errorf("domain pool %d: %w", i, err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	var first error
	for i, st := range s.opened {
		if err := st.Close(); err != nil && first == nil {
			first = fmt.Errorf("domain pool %d: %w", i, err)
		}
	}
	return first
}

// place resolves a key to its physical shard. create=true assigns a directory
// row for a brand-new aggregate root; create=false uses ResolveExisting so a
// scale-out never moves legacy keys.
//
// SKELETON LIMIT: store.Store.InTx hands out store.Repos, not *sql.Tx, so the
// assignment cannot join the business transaction. Assign is idempotent, which
// makes the window safe but eventually consistent; plane.AssignInTx stays
// reserved until the store transaction interface exposes its *sql.Tx.
func (s *Store) place(ctx context.Context, d plane.Domain, key string, create bool) (plane.Placement, error) {
	if create {
		return s.control.Resolver.Resolve(ctx, d, key)
	}
	return s.control.Resolver.ResolveExisting(ctx, d, key)
}

// placeChat pins a chat by its ChatKey, keeping direct-chat symmetry. New
// chats use DomainRouter.ResolveChat so the directory row is committed with
// the same shard number the legacy FNV router would have chosen.
func (s *Store) placeChat(ctx context.Context, c model.Chat, create bool) (plane.Placement, error) {
	if create {
		return s.domain.ResolveChat(ctx, c)
	}
	return s.place(ctx, plane.DomainMessage, shard.ChatKey(c), false)
}

// pick returns the repos that should serve one domain/key call. Inside a
// transaction the primary domain uses the transaction's own repos; another
// domain gets its pool's repos (best effort, not covered by the transaction).
// A second shard of the SAME domain is always rejected via TxScope.
func (s *Store) pick(ctx context.Context, d plane.Domain, key string, create bool) (store.Repos, error) {
	p, err := s.place(ctx, d, key, create)
	if err != nil {
		return nil, err
	}
	return s.pickPlacement(ctx, p)
}

func (s *Store) pickPlacement(ctx context.Context, p plane.Placement) (store.Repos, error) {
	pool, err := s.StoreFor(p.Domain, p.Shard)
	if err != nil {
		return nil, err
	}
	scope := scopeFrom(ctx)
	if scope == nil || scope.txStore == nil {
		return pool, nil
	}
	// Co-located domains resolve to the SAME pool. Reuse the transaction's
	// connection, whatever table the caller touches: users + tasks in one
	// CreateTask really are one local transaction in this deployment.
	if pool == scope.txStore {
		for _, repos := range scope.repos {
			if repos != nil {
				return repos, nil
			}
		}
	}
	if s.strictTx {
		if err := scope.tx.Pin(p); err != nil {
			return nil, err
		}
		if p.Domain == scope.mainDomain {
			return nil, fmt.Errorf("%w: %s/%s resolves to shard %d while the transaction is pinned to another shard",
				shard.ErrCrossShard, p.Domain, p.Key, p.Shard)
		}
	}
	// Physically split domains use a separate pool, so the write is NOT part
	// of the surrounding transaction. Stage 4 replaces this with outbox
	// compensation for the operations that need atomicity.
	return pool, nil
}

// InTx opens the transaction on the shard named by the request route. Without
// a route (registration, background sweeps) it starts on the first user shard
// and lets each repository resolve its own placement; in a co-located
// deployment those calls still share the same physical pool and transaction.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, r store.Repos) error) error {
	var p plane.Placement
	route, ok := RouteFrom(ctx)
	if ok && route.Key != "" && route.Domain.Valid() {
		var err error
		p, err = s.place(ctx, route.Domain, route.Key, route.Create)
		if err != nil {
			return err
		}
	} else {
		dsn, err := s.topo.DSN(plane.DomainUser, 0)
		if err != nil {
			return err
		}
		p = plane.Placement{Domain: plane.DomainUser, Shard: 0, DSN: dsn}
	}
	pool, err := s.StoreFor(p.Domain, p.Shard)
	if err != nil {
		return err
	}
	scope := &scopeState{
		tx:         shard.NewTxScope(),
		mainDomain: p.Domain,
		txStore:    pool,
		repos:      make(map[plane.Domain]store.Repos),
	}
	if s.strictTx {
		if err := scope.tx.Pin(p); err != nil {
			return err
		}
	}
	txCtx := withScope(ctx, scope)
	return pool.InTx(txCtx, func(ctx context.Context, repos store.Repos) error {
		scope.repos[p.Domain] = repos
		return fn(ctx, &Repos{s: s, scope: scope})
	})
}

// allRepos runs fn on every physical store of a domain. Read callers merge
// results; write callers use it only for id-only operations where the owning
// shard is unknown (the other shards are no-ops).
func (s *Store) allRepos(ctx context.Context, d plane.Domain, fn func(store.Repos) error) error {
	scope := scopeFrom(ctx)
	for _, st := range s.pools[d] {
		var repos store.Repos = st
		if scope != nil && st == scope.txStore {
			// The transaction connection belongs to ONE physical store, but
			// its full schema serves every domain in a co-located deployment.
			for _, txRepos := range scope.repos {
				if txRepos != nil {
					repos = txRepos
					break
				}
			}
		}
		if err := fn(repos); err != nil {
			return err
		}
	}
	return nil
}

// byRouteOrAll prefers the route attached to ctx; without one it falls back
// to fan-out. It is used for id-only operations (MarkStatus, Finish, ...) that
// carry no aggregate-root key of their own.
func (s *Store) byRouteOrAll(ctx context.Context, d plane.Domain, id string) ([]store.Repos, error) {
	if route, ok := RouteFrom(ctx); ok && route.Domain == d && route.Key != "" {
		repos, err := s.pick(ctx, d, route.Key, false)
		if err != nil {
			return nil, err
		}
		return []store.Repos{repos}, nil
	}
	var out []store.Repos
	scope := scopeFrom(ctx)
	for _, st := range s.pools[d] {
		var repos store.Repos = st
		if scope != nil && st == scope.txStore {
			for _, txRepos := range scope.repos {
				if txRepos != nil {
					repos = txRepos
					break
				}
			}
		}
		out = append(out, repos)
	}
	return out, nil
}
