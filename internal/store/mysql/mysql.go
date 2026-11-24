// Package mysql is the shared-database Store. Several API processes can open
// the same MySQL 8.0+ database. Schema changes take a named lock so only one
// process migrates.
package mysql

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store/sqlstore"

	mysqldriver "github.com/go-sql-driver/mysql"
)

//go:embed schema.sql
var schemaFS embed.FS

const schemaLock = "taskgame_schema"

// Open applies the locked migration and returns a pooled Store.
// The DSN is a go-sql-driver/mysql DSN. clientFoundRows and utf8mb4 are set
// when the DSN does not already choose them.
func Open(dsn string, initialBalance int64) (*sqlstore.DB, error) {
	normalized, err := normalizeDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", normalized)
	if err != nil {
		return nil, fmt.Errorf("mysql open: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysql ping: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return sqlstore.New(db, initialBalance, dialect{}), nil
}

func normalizeDSN(dsn string) (string, error) {
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

func migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 30)`, schemaLock).Scan(&got); err != nil {
		return fmt.Errorf("mysql schema lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		return fmt.Errorf("mysql schema lock was not acquired")
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(?)`, schemaLock)
	}()
	if _, err := conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version BIGINT NOT NULL,
			applied_at VARCHAR(40) NOT NULL,
			PRIMARY KEY (version)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`); err != nil {
		return fmt.Errorf("mysql schema_migrations: %w", err)
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 1`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		script, err := schemaFS.ReadFile("schema.sql")
		if err != nil {
			return err
		}
		if err := execScript(ctx, conn, string(script)); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (1, ?)`, model.FormatTime(time.Now())); err != nil {
			return fmt.Errorf("mysql schema version: %w", err)
		}
	}
	// Databases migrated before avatar columns existed still need them.
	if err := ensureUserAvatarColumns(ctx, conn); err != nil {
		return err
	}
	// Older databases need the de-amplified delivery body reference column.
	if err := ensureDeliveryBodyRef(ctx, conn); err != nil {
		return err
	}
	// Large submission payloads may live in the blob tier. An upgraded v1
	// database may not have submissions at all, so skip when absent.
	if err := ensureSubmissionPayloadRef(ctx, conn); err != nil {
		return err
	}
	// Version 1 only runs once, so tables appended to schema.sql after a
	// database was stamped never reach an upgraded database. The shard
	// directory and outbox are additive, so repair them on every startup.
	return ensureShardTables(ctx, conn)
}

// ensureDeliveryBodyRef adds the de-amplified body reference column to databases
// that predate webhook body externalization. An upgraded v1 database may not
// have the webhook_deliveries table at all (only users/schema_migrations), so
// check for the table before altering it and skip when it is absent.
func ensureDeliveryBodyRef(ctx context.Context, conn *sql.Conn) error {
	var hasTable int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_deliveries'`).Scan(&hasTable); err != nil {
		return err
	}
	if hasTable == 0 {
		return nil
	}
	var n int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'webhook_deliveries'
		  AND COLUMN_NAME = 'body_ref'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx,
		`ALTER TABLE webhook_deliveries ADD COLUMN body_ref VARCHAR(128) NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("mysql deliveries.body_ref: %w", err)
	}
	return nil
}

// ensureSubmissionPayloadRef adds the large-payload blob reference column to
// databases that predate payload externalization. An upgraded v1 database may
// not have the submissions table, so check for it before altering.
func ensureSubmissionPayloadRef(ctx context.Context, conn *sql.Conn) error {
	var hasTable int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'submissions'`).Scan(&hasTable); err != nil {
		return err
	}
	if hasTable == 0 {
		return nil
	}
	var n int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'submissions'
		  AND COLUMN_NAME = 'payload_ref'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := conn.ExecContext(ctx,
		`ALTER TABLE submissions ADD COLUMN payload_ref VARCHAR(128) NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("mysql submissions.payload_ref: %w", err)
	}
	return nil
}

// ensureShardTables repairs MySQL databases created before message sharding
// existed. SQLite re-runs its whole schema on every Open and needs no repair.
func ensureShardTables(ctx context.Context, conn *sql.Conn) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS chat_shards (
    chat_id VARCHAR(64) NOT NULL,
    shard INT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (chat_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
CREATE TABLE IF NOT EXISTS message_outbox (
    id VARCHAR(64) NOT NULL,
    chat_id VARCHAR(64) NOT NULL,
    shard INT NOT NULL,
    sender_id VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,
    text MEDIUMTEXT NOT NULL,
    attachment_ref TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    projected TINYINT NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    KEY idx_outbox_projected (projected)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;`
	return execScript(ctx, conn, ddl)
}

func ensureUserAvatarColumns(ctx context.Context, conn *sql.Conn) error {
	var hasUsers int
	if err := conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users'`).Scan(&hasUsers); err != nil {
		return err
	}
	if hasUsers == 0 {
		return nil
	}
	columns := []struct {
		name string
		def  string
	}{
		{name: "avatar", def: "MEDIUMBLOB NULL"},
		{name: "avatar_updated_at", def: "VARCHAR(40) NOT NULL DEFAULT ''"},
		{name: "avatar_ref", def: "VARCHAR(128) NOT NULL DEFAULT ''"},
	}
	for _, col := range columns {
		var n int
		if err := conn.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'users' AND COLUMN_NAME = ?`, col.name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE users ADD COLUMN %s %s", col.name, col.def)
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("mysql users.%s: %w", col.name, err)
		}
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func execScript(ctx context.Context, db execer, script string) error {
	for _, stmt := range strings.Split(script, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		// Drop leading comment lines so a block that starts with a comment still runs.
		lines := strings.Split(stmt, "\n")
		var kept []string
		for _, line := range lines {
			trim := strings.TrimSpace(line)
			if trim == "" || strings.HasPrefix(trim, "--") {
				continue
			}
			kept = append(kept, line)
		}
		stmt = strings.TrimSpace(strings.Join(kept, "\n"))
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("mysql schema: %w", err)
		}
	}
	return nil
}
