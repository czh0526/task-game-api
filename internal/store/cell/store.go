package cell

import (
	"context"
	"fmt"
	"sync"

	"taskgame/internal/auth"
	"taskgame/internal/store"
	"taskgame/internal/store/mysql"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/store/sqlstore"
)

// Config opens N independent, fully-featured stores. Every DSN gets the same
// full schema (users, tasks, messages, ledger, friends, webhooks, ...). There
// is deliberately no primary / meta database holding all rows.
type Config struct {
	// Driver is mysql or sqlite. sqlite is only for local single-process tests.
	Driver string
	// DSNs has one entry per cell, in cell order. They may point at different
	// MySQL hosts/ports (one database per instance) or at different schemas on
	// one server. Every API process must use the same ordered list.
	DSNs           []string
	InitialBalance int64
	// Blobs, when set, is injected into every cell so avatars and large
	// submission payloads live outside the online databases. Nil keeps the
	// legacy inline behavior.
	Blobs sqlstore.LargeValueStore
}

// Store is a store.Store spread over N cells. It delegates to one ordinary
// single-database Store per cell, so all existing repositories and the SQL
// migrations are reused unchanged.
type Store struct {
	router *Router
	cells  []store.Store
	loc    *Locator
}

// Open opens and migrates every cell. A failure closes everything opened so
// far, so a bad DSN never leaks a connection pool.
func Open(cfg Config) (*Store, error) {
	if len(cfg.DSNs) == 0 {
		return nil, fmt.Errorf("cell: at least one DSN is required")
	}
	s := &Store{router: New(len(cfg.DSNs))}
	for i, dsn := range cfg.DSNs {
		var st store.Store
		var err error
		switch cfg.Driver {
		case "mysql":
			st, err = mysql.Open(dsn, cfg.InitialBalance)
		case "", "sqlite":
			st, err = sqlite.Open(dsn, cfg.InitialBalance)
		default:
			err = fmt.Errorf("unsupported cell driver %q", cfg.Driver)
		}
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("cell %d: %w", i, err)
		}
		if cfg.Blobs != nil {
			if setter, ok := st.(interface {
				SetLargeValues(sqlstore.LargeValueStore)
			}); ok {
				setter.SetLargeValues(cfg.Blobs)
			}
		}
		s.cells = append(s.cells, st)
	}
	s.loc = &Locator{router: s.router, cells: s.cells}
	return s, nil
}

// Len is the number of cells.
func (s *Store) Len() int { return len(s.cells) }

// Cell exposes one cell's store. It is used by background sweeps and by tests
// that assert the data distribution across databases.
func (s *Store) Cell(i int) store.Store { return s.cells[i] }

// Router exposes the deterministic key -> cell mapping.
func (s *Store) Router() *Router { return s.router }

// Locator exposes the request-level resolver used by the HTTP middleware.
func (s *Store) Locator() *Locator { return s.loc }

func (s *Store) Ping(ctx context.Context) error {
	for i, c := range s.cells {
		if err := c.Ping(ctx); err != nil {
			return fmt.Errorf("cell %d: %w", i, err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	var first error
	for i, c := range s.cells {
		if err := c.Close(); err != nil && first == nil {
			first = fmt.Errorf("cell %d: %w", i, err)
		}
	}
	return first
}

// pick resolves the cell for one store call. An explicit WithCell always wins
// (background loops pin a cell). Otherwise the request hint set by the
// middleware, then the authenticated user id, then a deterministic hash of the
// key decide the cell.
func (s *Store) pick(ctx context.Context) int {
	i := s.loc.CellForContext(ctx)
	if i < 0 {
		i = 0
	}
	if i >= len(s.cells) {
		i = len(s.cells) - 1
	}
	return i
}

// InTx runs fn inside ONE cell's transaction. The context is pinned to that
// cell so any store call made from fn (outside the passed Repos) resolves to
// the same database. There is no cross-cell transaction.
func (s *Store) InTx(ctx context.Context, fn func(ctx context.Context, r store.Repos) error) error {
	i := s.pick(ctx)
	pinned := WithCell(ctx, i)
	return s.cells[i].InTx(pinned, func(txCtx context.Context, r store.Repos) error {
		return fn(txCtx, &overlayRepos{s: s, inner: r, cell: i})
	})
}

// Locator resolves a routing key to the cell that actually owns it. The hash
// is enough for new keys, but an aggregate root created by another user lives
// in that other user's cell. For those keys the locator searches every cell
// once and caches the answer; a cache miss (another API process, a restart) is
// harmless because the search is repeated. No directory table is required.
type Locator struct {
	router *Router
	cells  []store.Store
	cache  sync.Map // key -> cell index
}

// CellForContext resolves the cell carried by ctx. It returns the hash cell
// when no cell is pinned and the key has not been seen yet.
func (l *Locator) CellForContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	if i, ok := CellFrom(ctx); ok {
		return i
	}
	key, kind := HintFrom(ctx)
	if key == "" {
		if uid, ok := auth.UserID(ctx); ok {
			key, kind = uid, KindUser
		}
	}
	if key == "" {
		return 0
	}
	return l.CellFor(ctx, key, kind)
}

// CellFor resolves key. Known keys hit the cache; unknown ones are searched in
// every cell before falling back to the stable hash (used when the entity does
// not exist yet, e.g. registration or task creation).
func (l *Locator) CellFor(ctx context.Context, key, kind string) int {
	if key == "" {
		return 0
	}
	if v, ok := l.cache.Load(key); ok {
		return v.(int)
	}
	for i, c := range l.cells {
		if entityExists(ctx, c, kind, key) {
			l.cache.Store(key, i)
			return i
		}
	}
	return l.router.CellFor(key)
}

// Remember records a freshly-created entity so later requests do not need a
// scatter search. It is an optimisation only; correctness never depends on it.
func (l *Locator) Remember(key string, i int) {
	if key != "" && i >= 0 && i < len(l.cells) {
		l.cache.Store(key, i)
	}
}

func entityExists(ctx context.Context, s store.Store, kind, key string) bool {
	var err error
	switch kind {
	case KindTask:
		_, err = s.Tasks().Get(ctx, key)
	case KindChat:
		_, err = s.Chats().GetChat(ctx, key)
	case KindUser:
		_, err = s.Users().Get(ctx, key)
	default:
		return false
	}
	return err == nil
}
