// Package sqlite is the default Store. It uses modernc.org/sqlite (no cgo).
// One connection is used so SQLite writers cannot interleave.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"taskgame/internal/store"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

func schemaText() (string, error) {
	b, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DB is a SQLite Store.
type DB struct {
	db      *sql.DB
	initial int64
}

// Open creates the database file, applies schema, and returns a Store.
func Open(dsn string, initialBalance int64) (*DB, error) {
	if err := mkdirForDSN(dsn); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ctx := context.Background()
	script, err := schemaText()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := execScript(ctx, db, script); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite %s: %w", pragma, err)
		}
	}
	return &DB{db: db, initial: initialBalance}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *DB) Users() store.UserRepo             { return &userRepo{q: d.db, initial: d.initial} }
func (d *DB) Tasks() store.TaskRepo             { return &taskRepo{q: d.db} }
func (d *DB) Submissions() store.SubmissionRepo { return &submissionRepo{q: d.db} }
func (d *DB) Escrow() store.EscrowRepo          { return &escrowRepo{q: d.db} }
func (d *DB) Webhooks() store.WebhookRepo       { return &webhookRepo{q: d.db} }
func (d *DB) Deliveries() store.DeliveryRepo    { return &deliveryRepo{q: d.db} }
func (d *DB) Chats() store.ChatRepo             { return &chatRepo{q: d.db} }
func (d *DB) Invites() store.InviteRepo         { return &inviteRepo{q: d.db} }
func (d *DB) Friends() store.FriendRepo         { return &friendRepo{q: d.db} }
func (d *DB) Idem() store.IdemRepo              { return &idemRepo{q: d.db} }

func (d *DB) InTx(ctx context.Context, fn func(ctx context.Context, r store.Repos) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	r := &txRepos{q: tx, initial: d.initial}
	if err := fn(ctx, r); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// txRepos binds every repository to one transaction.
type txRepos struct {
	q       querier
	initial int64
}

func (t *txRepos) Users() store.UserRepo             { return &userRepo{q: t.q, initial: t.initial} }
func (t *txRepos) Tasks() store.TaskRepo             { return &taskRepo{q: t.q} }
func (t *txRepos) Submissions() store.SubmissionRepo { return &submissionRepo{q: t.q} }
func (t *txRepos) Escrow() store.EscrowRepo          { return &escrowRepo{q: t.q} }
func (t *txRepos) Webhooks() store.WebhookRepo       { return &webhookRepo{q: t.q} }
func (t *txRepos) Deliveries() store.DeliveryRepo    { return &deliveryRepo{q: t.q} }
func (t *txRepos) Chats() store.ChatRepo             { return &chatRepo{q: t.q} }
func (t *txRepos) Invites() store.InviteRepo         { return &inviteRepo{q: t.q} }
func (t *txRepos) Friends() store.FriendRepo         { return &friendRepo{q: t.q} }
func (t *txRepos) Idem() store.IdemRepo              { return &idemRepo{q: t.q} }

// migrate adds password-account columns on databases created before they existed.
// CREATE TABLE IF NOT EXISTS does not alter an existing users table.
func migrate(ctx context.Context, db *sql.DB) error {
	cols, err := userColumns(ctx, db)
	if err != nil {
		return err
	}
	if _, ok := cols["email"]; !ok {
		if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN email TEXT`); err != nil {
			return fmt.Errorf("migrate users.email: %w", err)
		}
	}
	if _, ok := cols["password_hash"]; !ok {
		if _, err := db.ExecContext(ctx, `ALTER TABLE users ADD COLUMN password_hash TEXT`); err != nil {
			return fmt.Errorf("migrate users.password_hash: %w", err)
		}
	}
	_, err = db.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email
		ON users (email) WHERE email IS NOT NULL AND email != ''`)
	if err != nil {
		return fmt.Errorf("migrate users email index: %w", err)
	}
	if err := addColumns(ctx, db, "tasks", map[string]string{
		"cancel_reason":          "TEXT NOT NULL DEFAULT ''",
		"cancel_code":            "TEXT NOT NULL DEFAULT ''",
		"cancelled_at":           "TEXT",
		"chat_id":                "TEXT NOT NULL DEFAULT ''",
		"funded_at":              "TEXT",
		"start_deadline":         "TEXT",
		"winner_select_deadline": "TEXT",
		"dispute_role_id":        "TEXT NOT NULL DEFAULT ''",
		"resolve_json":           "TEXT NOT NULL DEFAULT ''",
		"max_revisions":          "INTEGER NOT NULL DEFAULT 3",
	}); err != nil {
		return err
	}
	if err := addColumns(ctx, db, "submissions", map[string]string{
		"role_id": "TEXT NOT NULL DEFAULT ''",
		"status":  "TEXT NOT NULL DEFAULT 'current'",
	}); err != nil {
		return err
	}
	return nil
}

func addColumns(ctx context.Context, db *sql.DB, table string, cols map[string]string) error {
	have, err := tableColumns(ctx, db, table)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(cols))
	for name := range cols {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		if _, ok := have[name]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, cols[name])
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", table, name, err)
		}
	}
	return nil
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		j := i
		for j > 0 && ss[j] < ss[j-1] {
			ss[j], ss[j-1] = ss[j-1], ss[j]
			j--
		}
	}
}

func userColumns(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	return tableColumns(ctx, db, "users")
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("pragma table_info %s: %w", table, err)
	}
	defer rows.Close()
	cols := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("%s table is missing after schema apply", table)
	}
	return cols, nil
}

func execScript(ctx context.Context, db *sql.DB, script string) error {
	for _, stmt := range strings.Split(script, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	return nil
}

func mkdirForDSN(dsn string) error {
	path := sqliteFile(dsn)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func sqliteFile(dsn string) string {
	s := dsn
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "file:")
	if s == "" || strings.HasPrefix(s, ":memory:") {
		return ""
	}
	return s
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "[]", nil
	}
	return string(b), nil
}

func jsonOrNull(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return string(b), nil
}

func unmarshalSlice[T any](s string) ([]T, error) {
	if s == "" || s == "null" {
		return []T{}, nil
	}
	var out []T
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return []T{}, nil
	}
	return out, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
