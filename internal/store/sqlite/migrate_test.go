package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
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
	if old.Email != "" || old.Nickname != "" || old.Balance != 10 || old.PasswordHash != "" {
		t.Fatalf("migrated dev user %+v", old)
	}
	if err := st.Users().SetNickname(ctx, "usr_publisher", "老用户"); err != nil {
		t.Fatal(err)
	}
	named, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil || named.Nickname != "老用户" || named.Balance != 10 {
		t.Fatalf("nickname after migrate %+v %v", named, err)
	}
	if err := st.Users().SetNickname(ctx, "usr_missing", "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing nickname update %v", err)
	}
	if _, err := st.Users().Ensure(ctx, "usr_executor", now); err != nil {
		t.Fatal(err)
	}

	created, err := st.Users().InsertRegistered(ctx, "usr_newuser", "a@example.com", "$2a$hashed", now)
	if err != nil {
		t.Fatal(err)
	}
	if created.Email != "a@example.com" || created.Nickname != "" || created.Balance != 1000 || created.PasswordHash != "" {
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

func TestOpenMigratesChatType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-chats.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			balance INTEGER NOT NULL,
			integrity INTEGER NOT NULL,
			ability INTEGER NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE chats (
			id TEXT PRIMARY KEY,
			task_id TEXT,
			member_ids TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		INSERT INTO chats (id, task_id, member_ids, created_at)
		VALUES ('cht_oldtask', 'tsk_1', '["usr_publisher"]', '2026-11-04T12:00:00.000000000Z');
		INSERT INTO chats (id, task_id, member_ids, created_at)
		VALUES ('cht_oldgroup', NULL, '["usr_a","usr_b"]', '2026-11-04T12:00:00.000000000Z');
	`)
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
	taskChat, err := st.Chats().GetChat(ctx, "cht_oldtask")
	if err != nil || taskChat.Type != "task" || taskChat.TaskID != "tsk_1" {
		t.Fatalf("task chat %+v %v", taskChat, err)
	}
	groupChat, err := st.Chats().GetChat(ctx, "cht_oldgroup")
	if err != nil || groupChat.Type != "group" || groupChat.TaskID != "" {
		t.Fatalf("group chat %+v %v", groupChat, err)
	}
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	direct := model.Chat{
		ID: "cht_direct", Type: model.ChatDirect, PairLow: "usr_a", PairHigh: "usr_b",
		MemberIDs: []string{"usr_a", "usr_b"}, CreatedAt: now,
	}
	if err := st.Chats().InsertChat(ctx, direct); err != nil {
		t.Fatal(err)
	}
	dup := direct
	dup.ID = "cht_direct2"
	if err := st.Chats().InsertChat(ctx, dup); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate direct pair err %v", err)
	}
	found, err := st.Chats().FindDirect(ctx, "usr_b", "usr_a")
	if err != nil || found.ID != "cht_direct" || found.Type != "direct" {
		t.Fatalf("found %+v %v", found, err)
	}
}

// TestOpenMigratesOldWebhookDeliveries covers a database created before
// delivery claims. schema.sql must not index claimed_until until migrate adds it.
func TestOpenMigratesOldWebhookDeliveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-deliveries.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE webhook_deliveries (
		id TEXT PRIMARY KEY,
		webhook_id TEXT NOT NULL,
		event_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		body TEXT NOT NULL,
		status TEXT NOT NULL,
		attempts INTEGER NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT INTO webhook_deliveries
		(id, webhook_id, event_id, event_type, body, status, attempts, last_error, created_at)
		VALUES ('dlv_old', 'wh_old', 'evt_old', 'task.funded', '{}', 'pending', 0, '', '2026-11-04T12:00:00.000000000Z')`)
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
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if err := st.Webhooks().Insert(ctx, model.Webhook{
		ID: "wh_old", OwnerID: "usr_publisher", URL: "http://127.0.0.1/hook", Secret: "secret-secret-secret",
		Events: []string{"task.funded"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Deliveries().ClaimPending(ctx, 3, 10, now, now.Add(time.Hour))
	if err != nil || len(claimed) != 1 || claimed[0].ID != "dlv_old" || claimed[0].Attempts != 1 {
		t.Fatalf("claim old delivery %+v %v", claimed, err)
	}

	check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = check.Close() })
	var indexName string
	if err := check.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_deliveries_claim'`).Scan(&indexName); err != nil {
		t.Fatal(err)
	}
	cols := map[string]bool{}
	rows, err := check.Query(`PRAGMA table_info(webhook_deliveries)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !cols["claim_token"] || !cols["claimed_until"] {
		t.Fatalf("delivery columns after open: %+v", cols)
	}
}
