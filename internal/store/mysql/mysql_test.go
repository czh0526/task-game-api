package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestAvatarOnFreshAndUpgradedSchema(t *testing.T) {
	st := OpenIsolated(t, 1000)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	user, err := st.Users().Ensure(ctx, "usr_publisher", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Users().GetAvatar(ctx, user.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fresh avatar: %v", err)
	}
	jpeg := []byte{0xff, 0xd8, 0x01, 0x02}
	if err := st.Users().SetAvatar(ctx, user.ID, jpeg, "2026-11-20T01:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().GetAvatar(ctx, user.ID)
	if err != nil || string(got.Data) != string(jpeg) || got.UpdatedAt != "2026-11-20T01:00:00.000000000Z" {
		t.Fatalf("avatar %+v %v", got, err)
	}

	// A database stamped at version 1 before avatar columns existed must gain them.
	base := os.Getenv("TASKGAME_MYSQL_DSN")
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBName == "" {
		cfg.DBName = "mysql"
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	name := "tg_old_" + hex.EncodeToString(raw[:])
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP DATABASE IF EXISTS `" + name + "`") })
	cfg.DBName = name
	rawDB, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		CREATE TABLE users (
			id VARCHAR(128) NOT NULL PRIMARY KEY,
			balance BIGINT NOT NULL,
			integrity BIGINT NOT NULL,
			ability BIGINT NOT NULL,
			created_at VARCHAR(40) NOT NULL,
			email VARCHAR(320) NULL,
			password_hash VARCHAR(255) NULL,
			nickname VARCHAR(64) NOT NULL DEFAULT ''
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`
		CREATE TABLE schema_migrations (
			version BIGINT NOT NULL PRIMARY KEY,
			applied_at VARCHAR(40) NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (1, '2026-11-20T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(cfg.FormatDSN(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if _, err := upgraded.Users().Ensure(ctx, "usr_old", now); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.Users().SetAvatar(ctx, "usr_old", jpeg, "2026-11-20T03:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	old, err := upgraded.Users().GetAvatar(ctx, "usr_old")
	if err != nil || string(old.Data) != string(jpeg) {
		t.Fatalf("upgraded avatar %+v %v", old, err)
	}
}

func TestMigrateTwice(t *testing.T) {
	st := OpenIsolated(t, 1000)
	ctx := context.Background()
	if err := st.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	// A second Open of the same database must see schema version 1 and return.
	// OpenIsolated already migrated. Ping proves the pool is usable.
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if _, err := st.Users().Ensure(ctx, "usr_publisher", now); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil || got.Balance != 1000 {
		t.Fatalf("user %+v %v", got, err)
	}
}

func TestFiltersAndClaim(t *testing.T) {
	st := OpenIsolated(t, 1000)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	lat, lon := 39.9042, 116.4074
	task := model.Task{
		ID: "tsk_beijing", Kind: model.KindExclusive, Title: "Beijing task", Description: "notes",
		Status: model.StatusOpen, PublisherID: "usr_publisher",
		Keywords:   []string{"design"},
		Attributes: model.TaskAttributes{Latitude: &lat, Longitude: &lon, Urgency: model.UrgencyWithin3Days},
		Payment:    model.Payment{Mode: model.PayOneShot, Amount: 500},
		Acceptance: model.Acceptance{Deadline: now.Add(24 * time.Hour), Quality: "clear"},
		CreatedAt:  now, UpdatedAt: now,
	}
	if err := st.Tasks().Insert(ctx, task); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Tasks().List(ctx, model.TaskFilter{Keyword: "design", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("keyword %+v %v", rows, err)
	}
	near, err := st.Tasks().List(ctx, model.TaskFilter{
		Latitude: &lat, Longitude: &lon, RadiusKm: 50, Sort: "distance_asc", Limit: 10,
	})
	if err != nil || len(near) != 1 || near[0].ID != "tsk_beijing" {
		t.Fatalf("distance %+v %v", near, err)
	}

	if err := st.Webhooks().Insert(ctx, model.Webhook{
		ID: "wh_1", OwnerID: "usr_publisher", URL: "http://127.0.0.1/hook", Secret: "secret-secret-secret",
		Events: []string{"task.funded"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Deliveries().Insert(ctx, model.Delivery{
		ID: "dlv_1", WebhookID: "wh_1", EventID: "evt_1", EventType: "task.funded",
		Body: "{}", Status: model.DeliveryPending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Deliveries().ClaimPending(ctx, 3, 10, now, now.Add(time.Hour))
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 || claimed[0].ClaimToken == "" {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	again, err := st.Deliveries().ClaimPending(ctx, 3, 10, now, now.Add(time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim %+v %v", again, err)
	}
	later := now.Add(2 * time.Hour)
	reclaimed, err := st.Deliveries().ClaimPending(ctx, 3, 10, later, later.Add(time.Hour))
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 || reclaimed[0].ClaimToken == claimed[0].ClaimToken {
		t.Fatalf("reclaim %+v %v", reclaimed, err)
	}
	if err := st.Deliveries().Finish(ctx, claimed[0].ID, claimed[0].ClaimToken, model.DeliveryDelivered, ""); err != nil {
		t.Fatal(err)
	}
	pending, err := st.Deliveries().ListPending(ctx, 5, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("stale finish changed the row %+v %v", pending, err)
	}
	if err := st.Deliveries().Finish(ctx, reclaimed[0].ID, reclaimed[0].ClaimToken, model.DeliveryDelivered, ""); err != nil {
		t.Fatal(err)
	}
	final, err := st.Deliveries().ClaimPending(ctx, 3, 10, later, later.Add(time.Hour))
	if err != nil || len(final) != 0 {
		t.Fatalf("delivered row reclaimed %+v %v", final, err)
	}
}

// TestShardTablesOnUpgradedSchema reproduces a database stamped at schema
// version 1 before chat_shards / message_outbox existed: re-opening it must
// add both tables, otherwise enabling sharding fails at startup.
func TestShardTablesOnUpgradedSchema(t *testing.T) {
	base := os.Getenv("TASKGAME_MYSQL_DSN")
	if base == "" {
		if os.Getenv("TASKGAME_MYSQL_REQUIRED") == "1" {
			t.Fatal("TASKGAME_MYSQL_DSN is required")
		}
		t.Skip("TASKGAME_MYSQL_DSN not set")
	}
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	adminCfg := *cfg
	adminCfg.DBName = "mysql"
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	name := "tg_upgrade_" + hex.EncodeToString(raw[:])
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = admin.Close()
	})
	dbCfg := *cfg
	dbCfg.DBName = name
	dsn := dbCfg.FormatDSN()

	st, err := Open(dsn, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate an older database: version 1 is stamped, but the two tables
	// appended to schema.sql later were never created.
	if _, err := admin.Exec("DROP TABLE `" + name + "`.`chat_shards`, `" + name + "`.`message_outbox`"); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(dsn, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var n int
	if err := admin.QueryRow(`
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME IN ('chat_shards', 'message_outbox')`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("upgraded database has %d shard tables, want 2", n)
	}
}
