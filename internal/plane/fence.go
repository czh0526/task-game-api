package plane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrFenced is returned when a write targets a key frozen for maintenance.
// It is a hard, retryable error: the caller must not write to a fenced key,
// because a physical migration is copying or moving it.
var ErrFenced = errors.New("plane: key is fenced for maintenance")

// fenceSchema is added to the control-plane database. A row with shard_key =
// "*" fences the whole domain. An optional expires_at lets a crashed operator
// tool's fence auto-expire, so a forgotten fence cannot freeze production
// forever.
const fenceSchema = `
CREATE TABLE IF NOT EXISTS shard_fence (
    domain      VARCHAR(16)  NOT NULL,
    shard_key   VARCHAR(191) NOT NULL,
    reason      VARCHAR(255) NOT NULL DEFAULT '',
    owner       VARCHAR(191) NOT NULL DEFAULT '',
    expires_at  DATETIME     NOT NULL,
    created_at  DATETIME     NOT NULL,
    PRIMARY KEY (domain, shard_key)
);
`

// Fence is the maintenance write guard. Online write paths call IsFenced
// before mutating an aggregate root; maintenance tools call Lock/Unlock.
type Fence interface {
	IsFenced(ctx context.Context, domain Domain, key string) (bool, error)
}

// FenceStore is the full maintenance-facing interface.
type FenceStore interface {
	Fence
	Lock(ctx context.Context, domain Domain, key, reason, owner string, ttl time.Duration) error
	Unlock(ctx context.Context, domain Domain, key string) error
	List(ctx context.Context) ([]FenceRow, error)
}

type FenceRow struct {
	Domain    Domain
	Key       string
	Reason    string
	Owner     string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// SQLFence persists fences in the control-plane database. A short-lived
// in-process cache keeps the hot IsFenced path off the control plane: a newly
// acquired fence waits at most cacheTTL to be visible on this process, and
// Lock refreshes the local cache immediately.
type SQLFence struct {
	db       *sql.DB
	driver   string
	now      func() time.Time
	cacheTTL time.Duration
	ownsDB   bool

	mu    sync.RWMutex
	cache map[string]fenceEntry

	closeOnce sync.Once
	closeErr  error
}

// Close releases the pool when this fence owns it.
func (f *SQLFence) Close() error {
	f.closeOnce.Do(func() {
		if f.ownsDB && f.db != nil {
			f.closeErr = f.db.Close()
		}
	})
	return f.closeErr
}

type fenceEntry struct {
	fenced   bool
	loadedAt time.Time
}

var _ FenceStore = (*SQLFence)(nil)

// OpenSQLFence connects to the control-plane database and returns a fence
// that owns its pool. Call Close on shutdown.
func OpenSQLFence(ctx context.Context, driverName, dsn string) (*SQLFence, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, fmt.Errorf("plane: fence DSN is empty")
	}
	if driver == "sqlite" {
		if err := ensureSQLiteDir(dsn); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("plane: ping fence db: %w", err)
	}
	f, err := NewSQLFence(ctx, db, driver)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	f.ownsDB = true
	return f, nil
}

func NewSQLFence(ctx context.Context, db *sql.DB, driverName string) (*SQLFence, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	f := &SQLFence{
		db: db, driver: driver, now: time.Now,
		cacheTTL: 500 * time.Millisecond,
		cache:    make(map[string]fenceEntry),
	}
	if _, err := db.ExecContext(ctx, fenceSchema); err != nil {
		return nil, fmt.Errorf("plane: migrate fence: %w", err)
	}
	return f, nil
}

func (f *SQLFence) Lock(ctx context.Context, domain Domain, key, reason, owner string, ttl time.Duration) error {
	if !domain.Valid() || key == "" {
		return fmt.Errorf("plane: invalid fence lock (%q, %q)", domain, key)
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	now := f.now().UTC()
	var query string
	if f.driver == "mysql" {
		query = `INSERT INTO shard_fence (domain, shard_key, reason, owner, expires_at, created_at)
			VALUES (?,?,?,?,?,?)
			ON DUPLICATE KEY UPDATE reason = VALUES(reason), owner = VALUES(owner), expires_at = VALUES(expires_at)`
	} else {
		query = `INSERT OR REPLACE INTO shard_fence (domain, shard_key, reason, owner, expires_at, created_at)
			VALUES (?,?,?,?,?,?)`
	}
	_, err := f.db.ExecContext(ctx, query,
		string(domain), key, reason, owner, formatPlaneTime(now.Add(ttl)), formatPlaneTime(now))
	if err != nil {
		return fmt.Errorf("plane: lock fence %s/%s: %w", domain, key, err)
	}
	f.setCache(domain, key, true)
	return nil
}

func (f *SQLFence) Unlock(ctx context.Context, domain Domain, key string) error {
	_, err := f.db.ExecContext(ctx,
		`DELETE FROM shard_fence WHERE domain = ? AND shard_key = ?`, string(domain), key)
	if err != nil {
		return fmt.Errorf("plane: unlock fence %s/%s: %w", domain, key, err)
	}
	f.setCache(domain, key, false)
	return nil
}

// IsFenced checks a specific key and the domain-wide wildcard. Expired rows
// are treated as absent and are hidden from the cache.
func (f *SQLFence) IsFenced(ctx context.Context, domain Domain, key string) (bool, error) {
	if !domain.Valid() || key == "" {
		return false, nil
	}
	if f.cached(domain, key) {
		return true, nil
	}
	if f.cached(domain, "*") {
		return true, nil
	}
	for _, k := range []string{key, "*"} {
		var expires string
		err := f.db.QueryRowContext(ctx,
			`SELECT expires_at FROM shard_fence WHERE domain = ? AND shard_key = ?`,
			string(domain), k).Scan(&expires)
		if errors.Is(err, sql.ErrNoRows) {
			f.setCache(domain, k, false)
			continue
		}
		if err != nil {
			return false, fmt.Errorf("plane: check fence %s/%s: %w", domain, k, err)
		}
		exp, perr := parseFenceTime(expires)
		active := perr == nil && f.now().UTC().Before(exp)
		f.setCache(domain, k, active)
		if active {
			return true, nil
		}
	}
	return false, nil
}

func (f *SQLFence) List(ctx context.Context) ([]FenceRow, error) {
	rows, err := f.db.QueryContext(ctx,
		`SELECT domain, shard_key, reason, owner, expires_at, created_at FROM shard_fence ORDER BY domain, shard_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FenceRow
	for rows.Next() {
		var r FenceRow
		var d, exp, created string
		if err := rows.Scan(&d, &r.Key, &r.Reason, &r.Owner, &exp, &created); err != nil {
			return nil, err
		}
		r.Domain = Domain(d)
		r.ExpiresAt, _ = parseFenceTime(exp)
		r.CreatedAt, _ = parseFenceTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (f *SQLFence) cached(domain Domain, key string) bool {
	k := fenceCacheKey(domain, key)
	f.mu.RLock()
	entry, ok := f.cache[k]
	f.mu.RUnlock()
	return ok && entry.fenced && f.now().Sub(entry.loadedAt) < f.cacheTTL
}

func (f *SQLFence) setCache(domain Domain, key string, fenced bool) {
	f.mu.Lock()
	f.cache[fenceCacheKey(domain, key)] = fenceEntry{fenced: fenced, loadedAt: f.now()}
	f.mu.Unlock()
}

func fenceCacheKey(domain Domain, key string) string {
	return string(domain) + "\x00" + key
}

// parseFenceTime reads DATETIME values written by formatPlaneTime, plus the
// RFC3339 form some MySQL drivers return for DATETIME columns.
func parseFenceTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("plane: parse fence time %q", s)
}
