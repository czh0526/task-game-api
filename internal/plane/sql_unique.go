package plane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// SQLUnique is the persistent, cross-process UniqueIndex. It stores global
// constraints (email -> user_id) in the control-plane global_unique table
// (see Schema). The table is append-only: the primary key is (namespace,
// value) and rows are inserted with INSERT IGNORE, so an existing owner is
// NEVER replaced. That property makes an in-process positive cache safe:
// once a value has an owner, it can never acquire a different one.
//
// Reserve is therefore a single INSERT IGNORE:
//   - rows affected == 1  -> this caller won the reservation
//   - rows affected == 0  -> someone already owns it; read the owner and
//     return nil for an idempotent retry by the same owner, ErrConflict
//     otherwise.
//
// Owner is a primary-key point lookup, served from cache when warm.
type SQLUnique struct {
	db     *sql.DB
	driver string
	ownsDB bool

	mu    sync.RWMutex
	cache map[string]string // namespace\x00value -> ownerID

	now       func() time.Time
	closeOnce sync.Once
	closeErr  error
}

var _ UniqueIndex = (*SQLUnique)(nil)

// OpenSQLUnique connects to the control-plane database, ensures the
// global_unique table exists, and returns a SQLUnique that owns its pool.
// Call Close on shutdown. driver is "sqlite" or "mysql" (empty means sqlite).
func OpenSQLUnique(ctx context.Context, driverName, dsn string) (*SQLUnique, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, fmt.Errorf("plane: SQL unique DSN is empty")
	}
	if driver == "sqlite" {
		if err := ensureSQLiteDir(dsn); err != nil {
			return nil, fmt.Errorf("plane: prepare sqlite unique db: %w", err)
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("plane: open unique db: %w", err)
	}
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
		return nil, fmt.Errorf("plane: ping unique db: %w", err)
	}
	u, err := newSQLUnique(ctx, db, driver, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return u, nil
}

// NewSQLUnique wraps an existing *sql.DB (typically the same control-plane
// pool as SQLDirectory) and ensures the global_unique table exists. It does
// NOT own the pool: Close is a no-op.
func NewSQLUnique(ctx context.Context, db *sql.DB, driverName string) (*SQLUnique, error) {
	if db == nil {
		return nil, fmt.Errorf("plane: SQL unique needs a *sql.DB")
	}
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	return newSQLUnique(ctx, db, driver, false)
}

func newSQLUnique(ctx context.Context, db *sql.DB, driver string, owns bool) (*SQLUnique, error) {
	u := &SQLUnique{
		db:     db,
		driver: driver,
		ownsDB: owns,
		cache:  make(map[string]string),
		now:    time.Now,
	}
	// Migrate only the global_unique statement; callers may already share a
	// pool whose other control-plane tables are managed by SQLDirectory.
	stmt := `CREATE TABLE IF NOT EXISTS global_unique (
		namespace   VARCHAR(32)  NOT NULL,
		value       VARCHAR(255) NOT NULL,
		owner_id    VARCHAR(191) NOT NULL,
		PRIMARY KEY (namespace, value)
	)`
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return nil, fmt.Errorf("plane: migrate global_unique: %w", err)
	}
	return u, nil
}

// Reserve claims value for ownerID in namespace. A repeated reservation by
// the same owner is a no-op (idempotent retries); a different owner returns
// ErrConflict. Empty arguments are rejected.
func (u *SQLUnique) Reserve(ctx context.Context, namespace, value, ownerID string) error {
	if namespace == "" || value == "" || ownerID == "" {
		return fmt.Errorf("plane: invalid unique reservation")
	}
	key := uniqueKey(namespace, value)
	// Fast path: a warm, positive cache entry is authoritative because rows
	// are never overwritten.
	u.mu.RLock()
	owner, ok := u.cache[key]
	u.mu.RUnlock()
	if ok {
		if owner == ownerID {
			return nil
		}
		return ErrConflict
	}

	res, err := u.db.ExecContext(ctx,
		insertIgnoreSQL(u.driver)+` INTO global_unique (namespace, value, owner_id) VALUES (?, ?, ?)`,
		namespace, value, ownerID)
	if err != nil {
		return fmt.Errorf("plane: reserve %s/%s: %w", namespace, value, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("plane: reserve rows affected %s/%s: %w", namespace, value, err)
	}
	if affected == 1 {
		u.mu.Lock()
		u.cache[key] = ownerID
		u.mu.Unlock()
		return nil
	}
	// Lost the race (or a prior attempt committed). Read the winner; the
	// same owner means this is a replay, which is safe to treat as success.
	stored, found, err := u.lookup(ctx, namespace, value)
	if err != nil {
		return err
	}
	if !found {
		// INSERT IGNORE reported no insert but the row is gone: possible only
		// with an external DELETE. Fall back to a second attempt.
		return u.Reserve(ctx, namespace, value, ownerID)
	}
	u.mu.Lock()
	u.cache[key] = stored
	u.mu.Unlock()
	if stored == ownerID {
		return nil
	}
	return ErrConflict
}

// Owner returns the owner of a reserved value. A miss is not an error.
func (u *SQLUnique) Owner(ctx context.Context, namespace, value string) (string, bool, error) {
	if namespace == "" || value == "" {
		return "", false, fmt.Errorf("plane: invalid unique lookup")
	}
	key := uniqueKey(namespace, value)
	u.mu.RLock()
	owner, ok := u.cache[key]
	u.mu.RUnlock()
	if ok {
		return owner, true, nil
	}
	owner, found, err := u.lookup(ctx, namespace, value)
	if err != nil || !found {
		return owner, found, err
	}
	u.mu.Lock()
	u.cache[key] = owner
	u.mu.Unlock()
	return owner, true, nil
}

func (u *SQLUnique) lookup(ctx context.Context, namespace, value string) (string, bool, error) {
	var owner string
	err := u.db.QueryRowContext(ctx,
		`SELECT owner_id FROM global_unique WHERE namespace = ? AND value = ?`,
		namespace, value).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("plane: lookup unique %s/%s: %w", namespace, value, err)
	}
	return owner, true, nil
}

// Release deletes a reservation owned by ownerID, rolling back an email claim
// when the following user insert fails. The DELETE is guarded by owner_id so
// a failed registration can never free a value won by another process.
func (u *SQLUnique) Release(ctx context.Context, namespace, value, ownerID string) error {
	if namespace == "" || value == "" || ownerID == "" {
		return fmt.Errorf("plane: invalid unique release")
	}
	res, err := u.db.ExecContext(ctx,
		`DELETE FROM global_unique WHERE namespace = ? AND value = ? AND owner_id = ?`,
		namespace, value, ownerID)
	if err != nil {
		return fmt.Errorf("plane: release %s/%s: %w", namespace, value, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		u.Invalidate(namespace, value)
	}
	return nil
}

// Invalidate drops a cached value. Normally unnecessary (rows are immutable),
// but it is useful after an operator deletes a row during a data repair.
func (u *SQLUnique) Invalidate(namespace, value string) {
	u.mu.Lock()
	delete(u.cache, uniqueKey(namespace, value))
	u.mu.Unlock()
}

// DB exposes the shared control-plane pool.
func (u *SQLUnique) DB() *sql.DB { return u.db }

// Close releases the pool when this SQLUnique owns it.
func (u *SQLUnique) Close() error {
	u.closeOnce.Do(func() {
		if u.ownsDB && u.db != nil {
			u.closeErr = u.db.Close()
		}
	})
	return u.closeErr
}

func uniqueKey(namespace, value string) string {
	var b strings.Builder
	b.Grow(len(namespace) + len(value) + 1)
	b.WriteString(namespace)
	b.WriteByte(0)
	b.WriteString(value)
	return b.String()
}
