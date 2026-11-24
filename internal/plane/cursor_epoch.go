package plane

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A physical message-shard cutover rebuilds rowids/seq values. Any client that
// cached a rowid-based cursor would then skip or repeat messages. The API's
// cursor is offset-based, but we still keep an explicit per-chat generation so
// a cutover can invalidate outstanding cursors deterministically: cutover
// bumps the epoch, and ListMessages detects the mismatch and resets to the
// first page.
const cursorEpochSchema = `
CREATE TABLE IF NOT EXISTS chat_cursor_epoch (
    chat_id  VARCHAR(64) NOT NULL,
    epoch    BIGINT      NOT NULL DEFAULT 0,
    bumped_at VARCHAR(40) NOT NULL DEFAULT '',
    PRIMARY KEY (chat_id)
);
`

func cursorEpochSchemaFor(driver string) string {
	if strings.EqualFold(driver, "sqlite") {
		return strings.NewReplacer("VARCHAR(64)", "TEXT", "BIGINT", "INTEGER", "VARCHAR(40)", "TEXT").Replace(cursorEpochSchema)
	}
	return cursorEpochSchema
}

// CursorEpochStore is the maintenance-facing epoch registry.
type CursorEpochStore interface {
	// Epoch returns the current generation for a chat (0 when never bumped).
	Epoch(ctx context.Context, chatID string) (int64, error)
	// Bump increments the generation, invalidating all outstanding cursors.
	Bump(ctx context.Context, chatID string) (int64, error)
	Close() error
}

// OpenSQLCursorEpoch opens the epoch registry directly from a DSN. It owns the
// pool it creates and is the shape shardctl uses after a cutover.
func OpenSQLCursorEpoch(ctx context.Context, driverName, dsn string) (*SQLCursorEpoch, error) {
	driver, err := normalizeDriver(driverName)
	if err != nil {
		return nil, err
	}
	if dsn == "" {
		return nil, fmt.Errorf("plane: cursor epoch DSN is empty")
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
		return nil, fmt.Errorf("plane: ping cursor epoch db: %w", err)
	}
	s, err := NewSQLCursorEpoch(ctx, db, driver, true)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// MemoryCursorEpoch is the single-process implementation (default control plane).
type MemoryCursorEpoch struct {
	mu     sync.Mutex
	epochs map[string]int64
}

func NewMemoryCursorEpoch() *MemoryCursorEpoch {
	return &MemoryCursorEpoch{epochs: make(map[string]int64)}
}

func (m *MemoryCursorEpoch) Epoch(_ context.Context, chatID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epochs[chatID], nil
}

func (m *MemoryCursorEpoch) Bump(_ context.Context, chatID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epochs[chatID]++
	return m.epochs[chatID], nil
}

func (m *MemoryCursorEpoch) Close() error { return nil }

// SQLCursorEpoch persists generations in the control-plane database so every
// API process reads the same value and a shardctl bump is visible cluster-wide.
type SQLCursorEpoch struct {
	db     *sql.DB
	driver string
	ownsDB bool
}

func NewSQLCursorEpoch(ctx context.Context, db *sql.DB, driver string, ownsDB bool) (*SQLCursorEpoch, error) {
	if db == nil {
		return nil, fmt.Errorf("plane: cursor epoch needs a database")
	}
	for _, stmt := range strings.Split(cursorEpochSchemaFor(driver), ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return nil, fmt.Errorf("plane: cursor epoch schema: %w", err)
		}
	}
	return &SQLCursorEpoch{db: db, driver: driver, ownsDB: ownsDB}, nil
}

func (s *SQLCursorEpoch) Epoch(ctx context.Context, chatID string) (int64, error) {
	var epoch int64
	err := s.db.QueryRowContext(ctx,
		`SELECT epoch FROM chat_cursor_epoch WHERE chat_id = ?`, chatID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

func (s *SQLCursorEpoch) Bump(ctx context.Context, chatID string) (int64, error) {
	now := formatPlaneTime(time.Now().UTC())
	stmt := `INSERT INTO chat_cursor_epoch (chat_id, epoch, bumped_at) VALUES (?, 1, ?)
		ON DUPLICATE KEY UPDATE epoch = epoch + 1, bumped_at = VALUES(bumped_at)`
	if !strings.EqualFold(s.driver, "mysql") {
		stmt = `INSERT INTO chat_cursor_epoch (chat_id, epoch, bumped_at) VALUES (?, 1, ?)
		ON CONFLICT(chat_id) DO UPDATE SET epoch = epoch + 1, bumped_at = excluded.bumped_at`
	}
	if _, err := s.db.ExecContext(ctx, stmt, chatID, now); err != nil {
		return 0, err
	}
	return s.Epoch(ctx, chatID)
}

func (s *SQLCursorEpoch) Close() error {
	if s.ownsDB {
		return s.db.Close()
	}
	return nil
}

// ParseCursor decodes a message cursor into (epoch, offset). It accepts the
// legacy bare-offset form (treated as epoch 0) so existing clients keep working.
func ParseCursor(token string) (epoch, offset int64, err error) {
	if token == "" {
		return 0, 0, nil
	}
	decoded, dErr := base64.RawURLEncoding.DecodeString(token)
	if dErr != nil {
		return 0, 0, fmt.Errorf("plane: invalid cursor: %w", dErr)
	}
	raw := string(decoded)
	parts := strings.Split(raw, "|")
	switch len(parts) {
	case 1: // legacy: offset only
		n, pErr := strconv.ParseInt(parts[0], 10, 64)
		return 0, n, pErr
	case 3: // v2: "v2|<epoch>|<offset>"
		if parts[0] != "v2" {
			return 0, 0, fmt.Errorf("plane: unknown cursor version %q", parts[0])
		}
		e, pErr := strconv.ParseInt(parts[1], 10, 64)
		if pErr != nil {
			return 0, 0, pErr
		}
		n, pErr := strconv.ParseInt(parts[2], 10, 64)
		return e, n, pErr
	default:
		return 0, 0, fmt.Errorf("plane: malformed cursor")
	}
}
