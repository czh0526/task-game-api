package shard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"taskgame/internal/model"
	"taskgame/internal/store"

	_ "modernc.org/sqlite"
)

// Each shard is its own SQLite file with its own chat_seq sequence. Message
// order only has to be total inside one chat, so a global sequence is not
// needed (and chat_reads stores this per-chat value as the cursor).
const shardSchema = `
CREATE TABLE IF NOT EXISTS messages (
    chat_seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    type TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    attachment_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_chat ON messages (chat_id, chat_seq);
`

// Set owns one SQLite database per shard.
type Set struct {
	Router Router
	dbs    []*sql.DB
}

// Open creates N shard files from a DSN template containing %d.
func Open(router Router, dsnTemplate string) (*Set, error) {
	if !router.Enabled() {
		return nil, fmt.Errorf("shard: at least 2 shards required")
	}
	if !strings.Contains(dsnTemplate, "%d") {
		return nil, fmt.Errorf("shard: ShardDSN must contain %%d")
	}
	s := &Set{Router: router, dbs: make([]*sql.DB, router.N)}
	for i := 0; i < router.N; i++ {
		dsn := fmt.Sprintf(dsnTemplate, i)
		if err := mkdirForDSN(dsn); err != nil {
			_ = s.Close()
			return nil, err
		}
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("shard %d open: %w", i, err)
		}
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		ctx := context.Background()
		for _, stmt := range strings.Split(shardSchema, ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, stmt); err != nil {
				_ = db.Close()
				_ = s.Close()
				return nil, fmt.Errorf("shard %d schema: %w", i, err)
			}
		}
		for _, pragma := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`} {
			if _, err := db.ExecContext(ctx, pragma); err != nil {
				_ = db.Close()
				_ = s.Close()
				return nil, fmt.Errorf("shard %d %s: %w", i, pragma, err)
			}
		}
		s.dbs[i] = db
	}
	return s, nil
}

func (s *Set) Len() int { return len(s.dbs) }

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
	_, err := s.dbs[i].ExecContext(ctx, `
		INSERT OR IGNORE INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
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
