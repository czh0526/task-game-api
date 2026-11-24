package search

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"taskgame/internal/model"
)

func nowUTC() time.Time { return time.Now().UTC() }

// cdcSchema keeps the task-index change stream in the control-plane database,
// next to the directory and the compensation log. Every API process sees the
// same stream, so an index projection can be claimed, retried and replayed by
// any of them. It is intentionally small: one row per (kind, doc) with a
// projected flag, not a full event-sourcing log.
const cdcSchema = `
CREATE TABLE IF NOT EXISTS task_index_cdc (
    id          VARCHAR(64)  NOT NULL,
    kind        VARCHAR(16)  NOT NULL,
    doc         MEDIUMTEXT   NOT NULL,
    projected   TINYINT      NOT NULL DEFAULT 0,
    created_at  VARCHAR(40)  NOT NULL,
    PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_task_cdc_projected ON task_index_cdc (projected, created_at);
`

// cdcSchemaSQLite swaps MySQL-only column types for SQLite equivalents.
func cdcSchemaFor(driver string) string {
	if strings.EqualFold(driver, "sqlite") {
		return strings.NewReplacer(
			"VARCHAR(64)", "TEXT", "VARCHAR(16)", "TEXT",
			"VARCHAR(40)", "TEXT", "MEDIUMTEXT", "TEXT", "TINYINT", "INTEGER",
		).Replace(cdcSchema)
	}
	return cdcSchema
}

// LogEntry is one persisted CDC change.
type LogEntry struct {
	ID        string
	Kind      string
	Doc       TaskDoc
	CreatedAt string
}

// ChangeLog is the durable, replayable task change stream. Writes happen on
// the request path (best effort); a background projector drains Pending and
// marks entries projected. Replay resets the flags for a full re-index.
type ChangeLog interface {
	Append(ctx context.Context, c Change) error
	Pending(ctx context.Context, limit int) ([]LogEntry, error)
	MarkProjected(ctx context.Context, ids []string) error
	ReplayAll(ctx context.Context) (int, error)
	Count(ctx context.Context) (total, projected int64, err error)
	Close() error
}

// MemoryChangeLog is a single-process implementation for tests and the
// default (non-SQL) control plane. It is not shared between API processes.
type MemoryChangeLog struct {
	mu      sync.Mutex
	entries map[string]memoryLogEntry
	next    int64
}

type memoryLogEntry struct {
	entry     LogEntry
	projected bool
}

func NewMemoryChangeLog() *MemoryChangeLog {
	return &MemoryChangeLog{entries: make(map[string]memoryLogEntry)}
}

func (m *MemoryChangeLog) Append(_ context.Context, c Change) error {
	id := changeID(c)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; ok {
		return nil // idempotent
	}
	m.next++
	m.entries[id] = memoryLogEntry{entry: LogEntry{
		ID: id, Kind: c.Kind, Doc: c.Doc,
		CreatedAt: fmt.Sprintf("memory-%020d", m.next),
	}}
	return nil
}

func (m *MemoryChangeLog) Pending(_ context.Context, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 64
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make([]LogEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.projected {
			continue
		}
		entries = append(entries, e.entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt < entries[j].CreatedAt })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (m *MemoryChangeLog) MarkProjected(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if e, ok := m.entries[id]; ok {
			e.projected = true
			m.entries[id] = e
		}
	}
	return nil
}

func (m *MemoryChangeLog) ReplayAll(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, e := range m.entries {
		if e.projected {
			e.projected = false
			m.entries[id] = e
			n++
		}
	}
	return n, nil
}

func (m *MemoryChangeLog) Count(_ context.Context) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var done int64
	for _, e := range m.entries {
		if e.projected {
			done++
		}
	}
	return int64(len(m.entries)), done, nil
}

func (m *MemoryChangeLog) Close() error { return nil }

// SQLChangeLog persists the change stream in a shared database. Append is an
// INSERT IGNORE on a content-addressed id, so retries are harmless.
type SQLChangeLog struct {
	db     *sql.DB
	driver string
	ownsDB bool
}

// NewSQLChangeLog opens a change log on db. When ownsDB is true, Close closes
// the pool; pass false when it shares the control-plane pool.
func NewSQLChangeLog(ctx context.Context, db *sql.DB, driver string, ownsDB bool) (*SQLChangeLog, error) {
	if db == nil {
		return nil, fmt.Errorf("search: change log needs a database")
	}
	if driver == "" {
		driver = "sqlite"
	}
	for _, stmt := range strings.Split(cdcSchemaFor(driver), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("search: cdc schema: %w", err)
		}
	}
	return &SQLChangeLog{db: db, driver: driver, ownsDB: ownsDB}, nil
}

func (l *SQLChangeLog) Append(ctx context.Context, c Change) error {
	raw, err := json.Marshal(c.Doc)
	if err != nil {
		return err
	}
	id := changeID(c)
	stmt := `INSERT INTO task_index_cdc (id, kind, doc, projected, created_at) VALUES (?,?,?,0,?)`
	if strings.EqualFold(l.driver, "mysql") {
		stmt = `INSERT IGNORE INTO task_index_cdc (id, kind, doc, projected, created_at) VALUES (?,?,?,0,?)`
	} else {
		stmt = `INSERT OR IGNORE INTO task_index_cdc (id, kind, doc, projected, created_at) VALUES (?,?,?,0,?)`
	}
	_, err = l.db.ExecContext(ctx, stmt, id, c.Kind, string(raw), model.FormatTime(nowUTC()))
	return err
}

func (l *SQLChangeLog) Pending(ctx context.Context, limit int) ([]LogEntry, error) {
	if limit <= 0 {
		limit = 64
	}
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, kind, doc, created_at FROM task_index_cdc
		WHERE projected = 0 ORDER BY created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]LogEntry, 0, limit)
	for rows.Next() {
		var e LogEntry
		var raw string
		if err := rows.Scan(&e.ID, &e.Kind, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e.Doc); err != nil {
			return nil, fmt.Errorf("search: decode cdc doc %s: %w", e.ID, err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (l *SQLChangeLog) MarkProjected(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE task_index_cdc SET projected = 1 WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			return err
		}
	}
	_ = stmt.Close()
	return tx.Commit()
}

func (l *SQLChangeLog) ReplayAll(ctx context.Context) (int, error) {
	res, err := l.db.ExecContext(ctx, `UPDATE task_index_cdc SET projected = 0 WHERE projected = 1`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (l *SQLChangeLog) Count(ctx context.Context) (int64, int64, error) {
	var total, projected int64
	err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(projected), 0) FROM task_index_cdc`).Scan(&total, &projected)
	return total, projected, err
}

func (l *SQLChangeLog) Close() error {
	if l.ownsDB {
		return l.db.Close()
	}
	return nil
}

// LogRecorder adapts a ChangeLog into an engine-facing task change recorder.
// It lives here so the engine only needs a one-method interface.
type LogRecorder struct {
	log ChangeLog
}

func NewLogRecorder(log ChangeLog) *LogRecorder {
	return &LogRecorder{log: log}
}

func (r *LogRecorder) RecordTask(ctx context.Context, doc TaskDoc) error {
	if r == nil || r.log == nil {
		return nil
	}
	return r.log.Append(ctx, Change{Kind: "upsert", Doc: doc})
}

var (
	_ ChangeLog = (*MemoryChangeLog)(nil)
	_ ChangeLog = (*SQLChangeLog)(nil)
)

// ErrNoChanges is returned by a projector when there is nothing to apply.
var ErrNoChanges = errors.New("search: no pending changes")
