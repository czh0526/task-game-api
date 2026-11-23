package compensation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

// Record is one saga's durable state. Payload is opaque application JSON that
// the compensating handler needs to undo the forward effect.
type Record struct {
	ID         string
	Kind       string
	UserID     string
	TaskID     string
	Payload    json.RawMessage
	State      string
	Attempts   int
	LeaseOwner string
	LeaseUntil time.Time
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Log is the durable saga store. Implementations must make Claim atomic so
// two reconcilers never drive the same saga at once.
type Log interface {
	Insert(ctx context.Context, rec Record) error
	Claim(ctx context.Context, now time.Time, owner string, ttl time.Duration, limit int) ([]Record, error)
	MarkDone(ctx context.Context, id string) error
	MarkCompensating(ctx context.Context, id string, errText string) error
	MarkCompensated(ctx context.Context, id string) error
	Release(ctx context.Context, id, owner string) error
	Fail(ctx context.Context, id, owner string, attempts int, errText string) error
	Get(ctx context.Context, id string) (Record, error)
	List(ctx context.Context) ([]Record, error)
	Close() error
}

var (
	ErrNotFound = errors.New("compensation: saga not found")
	ErrConflict = errors.New("compensation: saga already exists")
)

// SQLLog persists sagas in a control-plane database.
type SQLLog struct {
	db     *sql.DB
	driver string
	ownsDB bool
	now    func() time.Time

	closeOnce sync.Once
	closeErr  error
}

var _ Log = (*SQLLog)(nil)

func OpenSQLLog(ctx context.Context, driverName, dsn string) (*SQLLog, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, fmt.Errorf("compensation: DSN is empty")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("compensation: ping: %w", err)
	}
	l, err := NewSQLLog(ctx, db, driver)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	l.ownsDB = true
	return l, nil
}

func NewSQLLog(ctx context.Context, db *sql.DB, driverName string) (*SQLLog, error) {
	if db == nil {
		return nil, fmt.Errorf("compensation: needs a *sql.DB")
	}
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	l := &SQLLog{db: db, driver: driver, now: time.Now}
	for _, stmt := range strings.Split(Schema, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("compensation: migrate: %w", err)
		}
	}
	return l, nil
}

func (l *SQLLog) Insert(ctx context.Context, rec Record) error {
	if rec.ID == "" || rec.Kind == "" {
		return fmt.Errorf("compensation: invalid saga id/kind")
	}
	if rec.State == "" {
		rec.State = StatePending
	}
	if len(rec.Payload) == 0 {
		rec.Payload = json.RawMessage(`{}`)
	}
	now := l.now().UTC()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	res, err := l.db.ExecContext(ctx,
		insertIgnore(l.driver)+` INTO compensation_sagas
		(id, kind, user_id, task_id, payload, state, attempts, lease_owner, lease_until, last_error, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.ID, rec.Kind, rec.UserID, rec.TaskID, string(rec.Payload), rec.State, rec.Attempts,
		rec.LeaseOwner, formatTime(rec.LeaseUntil), rec.LastError, formatTime(rec.CreatedAt), formatTime(rec.UpdatedAt))
	if err != nil {
		return fmt.Errorf("compensation: insert %s: %w", rec.ID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrConflict
	}
	return nil
}

// Claim atomically takes a lease on up to limit unleased/expired non-terminal
// sagas. The UPDATE ... WHERE lease_owner=” OR lease_until<=now pattern is
// supported identically by sqlite and mysql.
func (l *SQLLog) Claim(ctx context.Context, now time.Time, owner string, ttl time.Duration, limit int) ([]Record, error) {
	if limit <= 0 {
		limit = 32
	}
	until := formatTime(now.Add(ttl))
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, kind, user_id, task_id, payload, state, attempts, lease_owner, lease_until, last_error, created_at, updated_at
		FROM compensation_sagas
		WHERE state IN (?, ?) AND (lease_owner = '' OR lease_until <= ?)
		ORDER BY created_at ASC LIMIT ?`,
		StatePending, StateCompensating, formatTime(now), limit)
	if err != nil {
		return nil, err
	}
	var recs []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		recs = append(recs, rec)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, rec := range recs {
		if _, err := tx.ExecContext(ctx, `
			UPDATE compensation_sagas SET lease_owner = ?, lease_until = ?, updated_at = ? WHERE id = ?`,
			owner, until, formatTime(now), rec.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return recs, nil
}

func (l *SQLLog) MarkDone(ctx context.Context, id string) error {
	return l.setState(ctx, id, StateDone, "")
}

func (l *SQLLog) MarkCompensating(ctx context.Context, id, errText string) error {
	return l.setState(ctx, id, StateCompensating, errText)
}

func (l *SQLLog) MarkCompensated(ctx context.Context, id string) error {
	return l.setState(ctx, id, StateCompensated, "")
}

func (l *SQLLog) setState(ctx context.Context, id, state, errText string) error {
	res, err := l.db.ExecContext(ctx, `
		UPDATE compensation_sagas SET state = ?, last_error = ?, lease_owner = '', updated_at = ? WHERE id = ?`,
		state, errText, formatTime(l.now().UTC()), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (l *SQLLog) Release(ctx context.Context, id, owner string) error {
	_, err := l.db.ExecContext(ctx, `
		UPDATE compensation_sagas SET lease_owner = '', lease_until = ? WHERE id = ? AND lease_owner = ?`,
		formatTime(time.Time{}), id, owner)
	return err
}

func (l *SQLLog) Fail(ctx context.Context, id, owner string, attempts int, errText string) error {
	_, err := l.db.ExecContext(ctx, `
		UPDATE compensation_sagas SET attempts = ?, last_error = ?, lease_owner = '', lease_until = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ?`,
		attempts, errText, formatTime(time.Time{}), formatTime(l.now().UTC()), id, owner)
	return err
}

func (l *SQLLog) Get(ctx context.Context, id string) (Record, error) {
	rec, err := scanRecord(l.db.QueryRowContext(ctx, `
		SELECT id, kind, user_id, task_id, payload, state, attempts, lease_owner, lease_until, last_error, created_at, updated_at
		FROM compensation_sagas WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return rec, err
}

func (l *SQLLog) List(ctx context.Context) ([]Record, error) {
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, kind, user_id, task_id, payload, state, attempts, lease_owner, lease_until, last_error, created_at, updated_at
		FROM compensation_sagas ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recs []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, rows.Err()
}

func (l *SQLLog) DB() *sql.DB { return l.db }

func (l *SQLLog) Close() error {
	l.closeOnce.Do(func() {
		if l.ownsDB && l.db != nil {
			l.closeErr = l.db.Close()
		}
	})
	return l.closeErr
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row scanner) (Record, error) {
	var rec Record
	var payload, leaseUntil, created, updated string
	var leaseOwner sql.NullString
	err := row.Scan(&rec.ID, &rec.Kind, &rec.UserID, &rec.TaskID, &payload, &rec.State,
		&rec.Attempts, &leaseOwner, &leaseUntil, &rec.LastError, &created, &updated)
	if err != nil {
		return Record{}, err
	}
	rec.Payload = json.RawMessage(payload)
	rec.LeaseOwner = leaseOwner.String
	rec.LeaseUntil, _ = parseTime(leaseUntil)
	rec.CreatedAt, _ = parseTime(created)
	rec.UpdatedAt, _ = parseTime(updated)
	return rec, nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("compensation: parse time %q", s)
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01 00:00:00"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func insertIgnore(driver string) string {
	if driver == "mysql" {
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
		return "", fmt.Errorf("compensation: unsupported driver %q", s)
	}
}
