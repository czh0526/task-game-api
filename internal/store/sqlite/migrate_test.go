package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/store"

	_ "modernc.org/sqlite"
)

func TestOpenMigratesExistingUsersTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		balance INTEGER NOT NULL,
		integrity INTEGER NOT NULL,
		ability INTEGER NOT NULL,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO users (id, balance, integrity, ability, created_at)
		VALUES ('usr_publisher', 10, 1, 2, '2026-11-04T12:00:00.000000000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)

	old, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil {
		t.Fatal(err)
	}
	if old.Email != "" || old.Balance != 10 || old.PasswordHash != "" {
		t.Fatalf("migrated dev user %+v", old)
	}
	if _, err := st.Users().Ensure(ctx, "usr_executor", now); err != nil {
		t.Fatal(err)
	}

	created, err := st.Users().InsertRegistered(ctx, "usr_newuser", "a@example.com", "$2a$hashed", now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Email != "a@example.com" || created.Balance != 1000 || created.PasswordHash != "" {
		t.Fatalf("registered %+v", created)
	}
	again, err := st.Users().Ensure(ctx, "usr_newuser", now)
	if err != nil || again.Email != "a@example.com" || again.Balance != 1000 {
		t.Fatalf("ensure overwrote account %+v %v", again, err)
	}
	full, err := st.Users().GetByEmail(ctx, "a@example.com")
	if err != nil || full.PasswordHash != "$2a$hashed" {
		t.Fatalf("by email %+v %v", full, err)
	}
	if _, err := st.Users().InsertRegistered(ctx, "usr_other", "a@example.com", "$2a$other", now); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate email err %v", err)
	}
}
