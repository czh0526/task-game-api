// Package shard routes chat messages to independent message databases.
//
// This is a deliberately small prototype: only the messages table is sharded.
// Chats, chat_reads, idempotency keys and webhooks stay in the primary store,
// so the public API, the engine and the frontend are unchanged. A shard may be
// a local SQLite file or a network MySQL database; the primary store's driver
// is chosen independently.
package shard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"

	mysqldriver "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

const (
	DriverSQLite = "sqlite"
	DriverMySQL  = "mysql"
)

// Config describes one database per shard: DSNs[i] is the DSN of shard i.
// Driver selects the SQL dialect and is independent of the primary store's
// Store.Driver. A MySQL DSN list may point at different hosts, which is what
// makes the shard databases reachable from every API process.
type Config struct {
	Driver string
	DSNs   []string
}

// Validate checks the driver and that the DSN list matches the router exactly.
func (c Config) Validate(n int) error {
	switch c.Driver {
	case "", DriverSQLite, DriverMySQL:
	default:
		return fmt.Errorf("shard: unsupported driver %q (want %s or %s)", c.Driver, DriverSQLite, DriverMySQL)
	}
	if n < 2 {
		return fmt.Errorf("shard: at least 2 shards required")
	}
	if len(c.DSNs) != n {
		return fmt.Errorf("shard: need %d shard DSNs, got %d", n, len(c.DSNs))
	}
	for i, dsn := range c.DSNs {
		if strings.TrimSpace(dsn) == "" {
			return fmt.Errorf("shard: DSN %d is empty", i)
		}
	}
	return nil
}

func (c Config) driver() string {
	if c.Driver == "" {
		return DriverSQLite
	}
	return c.Driver
}

// ExpandTemplate turns a DSN template containing %d into a per-shard list.
// A list is preferred (it can point at different hosts); the template is kept
// for the simple SQLite case and for same-host MySQL shard databases.
func ExpandTemplate(template string, n int) ([]string, error) {
	if !strings.Contains(template, "%d") {
		return nil, fmt.Errorf("shard: ShardDSN must contain %%d")
	}
	if n < 2 {
		return nil, fmt.Errorf("shard: at least 2 shards required")
	}
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(template, i)
	}
	return out, nil
}

// Each shard has its own chat_seq sequence. Message order only has to be
// total inside one chat, so a global sequence is not needed (and chat_reads
// stores this per-chat value as the cursor).
var sqliteSchema = []string{
	`CREATE TABLE IF NOT EXISTS messages (
    chat_seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    type TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    attachment_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages (chat_id, chat_seq)`,
}

// MySQL has no CREATE INDEX IF NOT EXISTS, so the index is declared inline in
// CREATE TABLE and the whole statement stays idempotent on restart.
var mysqlSchema = []string{
	`CREATE TABLE IF NOT EXISTS messages (
    chat_seq BIGINT NOT NULL AUTO_INCREMENT,
    id VARCHAR(64) NOT NULL,
    chat_id VARCHAR(64) NOT NULL,
    sender_id VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,
    text MEDIUMTEXT NOT NULL,
    attachment_ref TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (chat_seq),
    UNIQUE KEY uk_messages_id (id),
    KEY idx_messages_chat (chat_id, chat_seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}

const (
	sqliteInsert = `INSERT OR IGNORE INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	mysqlInsert = `INSERT IGNORE INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
)

func schemaStatements(driver string) []string {
	if driver == DriverMySQL {
		return mysqlSchema
	}
	return sqliteSchema
}

// Set owns one database per shard. Every API process opens its own Set but
// derives the same routing from the primary store's chat_shards directory.
type Set struct {
	Router    Router
	dbs       []*sql.DB
	driver    string
	insertSQL string
}

// Open creates N SQLite shard files from a DSN template containing %d.
// It is kept for the default configuration and for tests.
func Open(router Router, dsnTemplate string) (*Set, error) {
	if !router.Enabled() {
		return nil, fmt.Errorf("shard: at least 2 shards required")
	}
	dsns, err := ExpandTemplate(dsnTemplate, router.N)
	if err != nil {
		return nil, err
	}
	return OpenConfig(router, Config{Driver: DriverSQLite, DSNs: dsns})
}

// OpenConfig opens one database per DSN. A MySQL DSN list lets the shards live
// on different hosts; all connections are pinged and migrated before returning.
func OpenConfig(router Router, cfg Config) (*Set, error) {
	if err := cfg.Validate(router.N); err != nil {
		return nil, err
	}
	driver := cfg.driver()
	s := &Set{
		Router:    router,
		dbs:       make([]*sql.DB, router.N),
		driver:    driver,
		insertSQL: sqliteInsert,
	}
	if driver == DriverMySQL {
		s.insertSQL = mysqlInsert
	}
	stmts := schemaStatements(driver)
	for i, dsn := range cfg.DSNs {
		normalized := dsn
		var err error
		switch driver {
		case DriverSQLite:
			if err = mkdirForDSN(dsn); err != nil {
				_ = s.Close()
				return nil, err
			}
		case DriverMySQL:
			normalized, err = normalizeMySQLDSN(dsn)
			if err != nil {
				_ = s.Close()
				return nil, fmt.Errorf("shard %d: %w", i, err)
			}
		}
		db, err := sql.Open(driver, normalized)
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("shard %d open: %w", i, err)
		}
		if driver == DriverSQLite {
			// One connection per file keeps SQLite's single-writer model simple.
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
		} else {
			db.SetMaxOpenConns(8)
			db.SetMaxIdleConns(8)
			db.SetConnMaxLifetime(5 * time.Minute)
			db.SetConnMaxIdleTime(time.Minute)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := db.PingContext(ctx); err != nil {
			cancel()
			_ = db.Close()
			_ = s.Close()
			return nil, fmt.Errorf("shard %d ping: %w", i, err)
		}
		for _, stmt := range stmts {
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				cancel()
				_ = db.Close()
				_ = s.Close()
				return nil, fmt.Errorf("shard %d schema: %w", i, err)
			}
		}
		if driver == DriverSQLite {
			for _, pragma := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
				if _, err := db.ExecContext(ctx, pragma); err != nil {
					cancel()
					_ = db.Close()
					_ = s.Close()
					return nil, fmt.Errorf("shard %d %s: %w", i, pragma, err)
				}
			}
		}
		cancel()
		s.dbs[i] = db
	}
	return s, nil
}

// normalizeMySQLDSN applies the same defaults as the primary MySQL store, so
// a shard DSN behaves consistently whether or not it spells out the charset.
func normalizeMySQLDSN(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("mysql dsn: %w", err)
	}
	cfg.ParseTime = false
	cfg.ClientFoundRows = true
	if cfg.Collation == "" {
		cfg.Collation = "utf8mb4_unicode_ci"
	}
	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	if _, ok := cfg.Params["charset"]; !ok {
		cfg.Params["charset"] = "utf8mb4"
	}
	return cfg.FormatDSN(), nil
}

func (s *Set) Len() int { return len(s.dbs) }

// Driver reports the SQL dialect used by the shard databases.
func (s *Set) Driver() string { return s.driver }

// DB exposes a shard handle for tests that assert physical isolation.
func (s *Set) DB(i int) *sql.DB { return s.dbs[i] }

func (s *Set) Close() error {
	var first error
	for _, db := range s.dbs {
		if db == nil {
			continue
		}
		if err := db.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Set) check(i int) error {
	if i < 0 || i >= len(s.dbs) {
		return fmt.Errorf("shard: index %d out of range", i)
	}
	return nil
}

// Insert is idempotent on message id, so replaying an outbox row is safe.
func (s *Set) Insert(ctx context.Context, i int, m model.Message) error {
	if err := s.check(i); err != nil {
		return err
	}
	_, err := s.dbs[i].ExecContext(ctx, s.insertSQL,
		m.ID, m.ChatID, m.SenderID, m.Type, m.Text, m.AttachmentRef, model.FormatTime(m.CreatedAt))
	return err
}

func (s *Set) List(ctx context.Context, i int, chatID string, limit, offset int) ([]model.Message, error) {
	if err := s.check(i); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.dbs[i].QueryContext(ctx, `
		SELECT id, chat_id, sender_id, type, text, attachment_ref, created_at
		FROM messages WHERE chat_id = ? ORDER BY created_at ASC, chat_seq ASC LIMIT ? OFFSET ?`,
		chatID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Message{}
	for rows.Next() {
		var m model.Message
		var created string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.Type, &m.Text, &m.AttachmentRef, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Set) MessageSeq(ctx context.Context, i int, chatID, messageID string) (int64, error) {
	if err := s.check(i); err != nil {
		return 0, err
	}
	var seq int64
	err := s.dbs[i].QueryRowContext(ctx, `
		SELECT chat_seq FROM messages WHERE chat_id = ? AND id = ?`, chatID, messageID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	return seq, err
}

func (s *Set) Latest(ctx context.Context, i int, chatID string) (int64, string, bool, error) {
	if err := s.check(i); err != nil {
		return 0, "", false, err
	}
	var seq int64
	var id string
	err := s.dbs[i].QueryRowContext(ctx, `
		SELECT chat_seq, id FROM messages WHERE chat_id = ? ORDER BY chat_seq DESC LIMIT 1`, chatID).Scan(&seq, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return seq, id, true, nil
}

// CountUnread counts peer messages after the caller's per-chat cursor.
func (s *Set) CountUnread(ctx context.Context, i int, chatID, userID string, cursor int64) (int64, error) {
	if err := s.check(i); err != nil {
		return 0, err
	}
	var n int64
	err := s.dbs[i].QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages
		WHERE chat_id = ? AND sender_id != ? AND chat_seq > ?`,
		chatID, userID, cursor).Scan(&n)
	return n, err
}

func mkdirForDSN(dsn string) error {
	p := dsn
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	p = strings.TrimPrefix(p, "file:")
	if p == "" || strings.HasPrefix(p, ":memory:") {
		return nil
	}
	dir := filepath.Dir(p)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
