package plane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Register both SQL drivers so OpenSQLDirectory works from any importer.
	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

// SQLDirectory is the persistent, cross-process implementation of Directory.
// It stores key -> shard mappings in shard_directory and the declared shard
// count in shard_topology (both from Schema), so every API process resolves a
// key to the same physical database across restarts and rolling upgrades.
//
// Two invariants, both inherited from the InMemoryDirectory contract:
//   - Assign is idempotent and NEVER overwrites an existing row. This is what
//     makes "add a shard without moving old data" possible.
//   - EnsureShards only raises the count. A smaller count is ignored, which is
//     important when one process has already scaled out and another is still
//     rolling with the older config.
//
// SKELETON BOUNDARY: OpenSQLDirectory owns a separate *sql.DB, so Assign
// cannot yet join the transaction that creates the aggregate root. AssignInTx
// is exported for the P2 repository split; until then the directory row and
// the business row are eventually consistent, which is safe precisely because
// Assign is idempotent. shard_topology.dsn is an informational snapshot of the
// DSN list at first registration; the authoritative DSN order is still the
// in-memory Topology built from config.
type SQLDirectory struct {
	db     *sql.DB
	driver string    // SQL dialect: "sqlite" | "mysql"
	topo   *Topology // optional; supplies DSNs for shard_topology rows
	now    func() time.Time
	ownsDB bool

	// readDB serves Lookup when a read replica is configured; writes always
	// go to db (the primary). A failed replica read falls back to the primary.
	readDB      *sql.DB
	ownsRead    bool
	cacheTTL    time.Duration
	lookupMu    sync.RWMutex
	lookupCache map[dirCacheKey]dirCacheEntry

	closeOnce sync.Once
	closeErr  error
}

type dirCacheKey struct {
	domain Domain
	key    string
}

type dirCacheEntry struct {
	shard     int
	ok        bool
	expiresAt time.Time
}

// OpenSQLDirectory connects to the control-plane database, applies Schema and
// returns a Directory that owns its connection pool. Call Close on shutdown.
// driverName is "sqlite" or "mysql"; an empty value means sqlite.
func OpenSQLDirectory(ctx context.Context, driverName, dsn string, topo *Topology) (*SQLDirectory, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, fmt.Errorf("plane: SQL directory DSN is empty")
	}
	if driver == "sqlite" {
		if err := ensureSQLiteDir(dsn); err != nil {
			return nil, fmt.Errorf("plane: prepare sqlite directory: %w", err)
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("plane: open control-plane db: %w", err)
	}
	// SQLite is a single-writer database; one connection avoids lock churn
	// between this pool and the primary store's pool on the same file.
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(10)
		db.SetMaxIdleConns(10)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("plane: ping control-plane db: %w", err)
	}
	d, err := newSQLDirectory(ctx, db, driver, topo, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return d, nil
}

// OpenSQLDirectoryWithReplica is OpenSQLDirectory plus an optional read
// replica. Lookup reads are served from the replica (with primary fallback on
// error); all writes still target the primary. This is the HA shape for the
// control plane: the directory survives a replica outage and reads are
// offloaded from the primary. Empty replicaDSN behaves like OpenSQLDirectory.
func OpenSQLDirectoryWithReplica(ctx context.Context, driverName, dsn, replicaDSN string, topo *Topology) (*SQLDirectory, error) {
	d, err := OpenSQLDirectory(ctx, driverName, dsn, topo)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(replicaDSN) == "" || replicaDSN == dsn {
		return d, nil
	}
	driver, _ := normalizeDriver(driverName)
	if driver == "sqlite" {
		if err := ensureSQLiteDir(replicaDSN); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("plane: prepare replica sqlite dir: %w", err)
		}
	}
	readDB, err := sql.Open(driver, replicaDSN)
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("plane: open directory replica: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := readDB.PingContext(pingCtx); err != nil {
		_ = readDB.Close()
		_ = d.Close()
		return nil, fmt.Errorf("plane: ping directory replica: %w", err)
	}
	d.readDB = readDB
	d.ownsRead = true
	return d, nil
}

// NewSQLDirectory wraps an existing *sql.DB (for example the primary store's
// pool) and applies Schema. It does NOT own the pool: Close is a no-op.
func NewSQLDirectory(ctx context.Context, db *sql.DB, driverName string, topo *Topology) (*SQLDirectory, error) {
	if db == nil {
		return nil, fmt.Errorf("plane: SQL directory needs a *sql.DB")
	}
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	return newSQLDirectory(ctx, db, driver, topo, false)
}

func newSQLDirectory(ctx context.Context, db *sql.DB, driver string, topo *Topology, owns bool) (*SQLDirectory, error) {
	d := &SQLDirectory{
		db:          db,
		driver:      driver,
		topo:        topo,
		now:         time.Now,
		ownsDB:      owns,
		cacheTTL:    2 * time.Second,
		lookupCache: make(map[dirCacheKey]dirCacheEntry),
	}
	if err := d.Migrate(ctx); err != nil {
		return nil, err
	}
	return d, nil
}

// Migrate applies plane.Schema. Every statement is CREATE TABLE IF NOT
// EXISTS, so it is safe to run on every process start and on an existing
// primary database that already has the 19 business tables.
func (d *SQLDirectory) Migrate(ctx context.Context) error {
	for _, stmt := range strings.Split(Schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("plane: migrate control plane: %w", err)
		}
	}
	return nil
}

// Lookup returns the authoritative shard for a key. A miss is not an error.
// Results (including misses) are cached briefly to keep the control plane off
// the hot path; Assign invalidates the entry so this process sees its own
// writes immediately. Other processes see a change within cacheTTL.
func (d *SQLDirectory) Lookup(ctx context.Context, domain Domain, key string) (int, bool, error) {
	if !domain.Valid() {
		return 0, false, fmt.Errorf("plane: invalid domain %q", domain)
	}
	ck := dirCacheKey{domain: domain, key: key}
	d.lookupMu.RLock()
	entry, cached := d.lookupCache[ck]
	d.lookupMu.RUnlock()
	if cached && d.now().Before(entry.expiresAt) {
		return entry.shard, entry.ok, nil
	}

	reader := d.db
	if d.readDB != nil {
		reader = d.readDB
	}
	var shard int
	err := reader.QueryRowContext(ctx,
		`SELECT shard FROM shard_directory WHERE domain = ? AND shard_key = ?`,
		string(domain), key).Scan(&shard)
	if err != nil && d.readDB != nil {
		// Replica failed: fall back to the primary rather than failing the
		// request (an HA control plane must survive a replica outage).
		err = d.db.QueryRowContext(ctx,
			`SELECT shard FROM shard_directory WHERE domain = ? AND shard_key = ?`,
			string(domain), key).Scan(&shard)
	}
	ok := true
	if errors.Is(err, sql.ErrNoRows) {
		shard, ok = 0, false
		err = nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("plane: lookup %s/%s: %w", domain, key, err)
	}
	d.lookupMu.Lock()
	d.lookupCache[ck] = dirCacheEntry{shard: shard, ok: ok, expiresAt: d.now().Add(d.cacheTTL)}
	d.lookupMu.Unlock()
	return shard, ok, nil
}

// Invalidate drops one cached lookup. A control-plane writer in another
// process (e.g. shardctl cutover) cannot push an invalidation here; callers
// that just performed an out-of-band directory change can use this to refresh
// immediately instead of waiting out cacheTTL.
func (d *SQLDirectory) Invalidate(domain Domain, key string) {
	d.invalidateLookup(domain, key)
}

// InvalidateAll clears the entire lookup cache.
func (d *SQLDirectory) InvalidateAll() {
	d.lookupMu.Lock()
	d.lookupCache = make(map[dirCacheKey]dirCacheEntry)
	d.lookupMu.Unlock()
}

// invalidateLookup drops a cached entry after a write.
func (d *SQLDirectory) invalidateLookup(domain Domain, key string) {
	d.lookupMu.Lock()
	delete(d.lookupCache, dirCacheKey{domain: domain, key: key})
	d.lookupMu.Unlock()
}

// Assign writes a directory row in a transaction. An existing row wins: the
// INSERT is ignored, so a concurrent or replayed assignment can never move a
// key to a different shard.
func (d *SQLDirectory) Assign(ctx context.Context, domain Domain, key string, shard int) error {
	if !domain.Valid() || key == "" || shard < 0 {
		return fmt.Errorf("plane: invalid assignment (%q, %q, %d)", domain, key, shard)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("plane: begin assign: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := AssignInTx(ctx, tx, d.driver, domain, key, shard, d.now()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("plane: commit assign: %w", err)
	}
	d.invalidateLookup(domain, key)
	return nil
}

// AssignInTx writes the directory row inside a CALLER-OWNED transaction.
// It is deliberately a package-level function with an explicit dialect: when
// the P2 User/Task repositories are split out, the store can create the
// aggregate root and pin its routing in ONE transaction, exactly as the
// Directory interface comment requires. SQLDirectory.Assign is just a thin
// wrapper that opens its own transaction.
func AssignInTx(ctx context.Context, tx *sql.Tx, dialect string, domain Domain, key string, shard int, now time.Time) error {
	if !domain.Valid() || key == "" || shard < 0 {
		return fmt.Errorf("plane: invalid assignment (%q, %q, %d)", domain, key, shard)
	}
	ignore := insertIgnoreSQL(dialect)
	if _, err := tx.ExecContext(ctx,
		ignore+` INTO shard_directory (domain, shard_key, shard, created_at) VALUES (?, ?, ?, ?)`,
		string(domain), key, shard, formatPlaneTime(now)); err != nil {
		return fmt.Errorf("plane: insert directory row: %w", err)
	}
	// Record that this shard exists. EnsureShards will fill in the real DSN
	// for declared shards; this row keeps ShardCount correct even if Assign
	// races ahead of the topology sync.
	if _, err := tx.ExecContext(ctx,
		ignore+` INTO shard_topology (domain, shard, dsn) VALUES (?, ?, ?)`,
		string(domain), shard, ""); err != nil {
		return fmt.Errorf("plane: record shard topology: %w", err)
	}
	return nil
}

// Keys lists the keys pinned to one shard. It is used by PlanShrink (a
// maintenance operation), not by an online request path: shard_directory is
// keyed by (domain, shard_key), so this query is a scan. Add a
// (domain, shard) index before using it on a hot path.
func (d *SQLDirectory) Keys(ctx context.Context, domain Domain, shard int) ([]string, error) {
	if !domain.Valid() || shard < 0 {
		return nil, fmt.Errorf("plane: invalid Keys(%q, %d)", domain, shard)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT shard_key FROM shard_directory WHERE domain = ? AND shard = ? ORDER BY shard_key`,
		string(domain), shard)
	if err != nil {
		return nil, fmt.Errorf("plane: list keys: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("plane: scan key: %w", err)
		}
		out = append(out, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("plane: iterate keys: %w", err)
	}
	return out, nil
}

// ShardCount is the number of declared shards, persisted in shard_topology.
// MAX(shard)+1 (rather than COUNT(*)) stays correct if a row for a high shard
// index was written by Assign before EnsureShards ran.
func (d *SQLDirectory) ShardCount(ctx context.Context, domain Domain) (int, error) {
	if !domain.Valid() {
		return 0, fmt.Errorf("plane: invalid domain %q", domain)
	}
	var n sql.NullInt64
	if err := d.db.QueryRowContext(ctx,
		`SELECT MAX(shard) + 1 FROM shard_topology WHERE domain = ?`,
		string(domain)).Scan(&n); err != nil {
		return 0, fmt.Errorf("plane: shard count: %w", err)
	}
	if !n.Valid {
		return 0, nil
	}
	return int(n.Int64), nil
}

// EnsureShards declares shards 0..count-1. It only ever inserts rows, so a
// process still running an older, smaller config cannot shrink the shared
// topology. DSNs come from the optional Topology and are informational.
func (d *SQLDirectory) EnsureShards(ctx context.Context, domain Domain, count int) error {
	if !domain.Valid() || count < 1 {
		return fmt.Errorf("plane: invalid shard count %d for %q", count, domain)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("plane: begin ensure shards: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt := insertIgnoreSQL(d.driver) + ` INTO shard_topology (domain, shard, dsn) VALUES (?, ?, ?)`
	for i := 0; i < count; i++ {
		dsn := ""
		if d.topo != nil {
			dsn, _ = d.topo.DSN(domain, i)
		}
		if _, err := tx.ExecContext(ctx, stmt, string(domain), i, dsn); err != nil {
			return fmt.Errorf("plane: ensure shard %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("plane: commit ensure shards: %w", err)
	}
	return nil
}

// Close releases the connection pool opened by OpenSQLDirectory. It is
// idempotent and a no-op for directories wrapping an externally owned DB.
func (d *SQLDirectory) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.ownsDB && d.db != nil {
			d.closeErr = d.db.Close()
		}
		if d.ownsRead && d.readDB != nil {
			if rerr := d.readDB.Close(); d.closeErr == nil {
				d.closeErr = rerr
			}
		}
	})
	return d.closeErr
}

// DB exposes the underlying pool for tests and for a future store that wants
// to place directory rows in its own transaction. Do not close it directly.
func (d *SQLDirectory) DB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func insertIgnoreSQL(dialect string) string {
	if dialect == "mysql" {
		return "INSERT IGNORE"
	}
	return "INSERT OR IGNORE"
}

func normalizeDriver(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "sqlite":
		return "sqlite", nil
	case "mysql":
		return "mysql", nil
	default:
		return "", fmt.Errorf("plane: unsupported SQL directory driver %q (want sqlite or mysql)", s)
	}
}

func formatPlaneTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

// ensureSQLiteDir mirrors the primary SQLite store's behaviour: sql.Open does
// not create parent directories, so the first Exec would otherwise fail on a
// fresh checkout where data/ does not exist yet.
func ensureSQLiteDir(dsn string) error {
	path := dsn
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimPrefix(path, "file:")
	if path == "" || strings.HasPrefix(path, ":memory:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
