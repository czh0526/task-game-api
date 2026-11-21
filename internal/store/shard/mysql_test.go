package shard_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store/shard"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// mysqlShardDSNs creates one isolated MySQL database per shard on the server
// named by TASKGAME_MYSQL_DSN. The DSNs can point at different hosts in a real
// deployment; here they share one server so the test needs no extra services.
func mysqlShardDSNs(t *testing.T, n int) []string {
	t.Helper()
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
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	names := make([]string, 0, n)
	t.Cleanup(func() {
		for _, name := range names {
			_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		}
		_ = admin.Close()
	})
	dsns := make([]string, n)
	for i := 0; i < n; i++ {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		name := "tg_shard_" + hex.EncodeToString(raw[:])
		if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		shardCfg := *cfg
		shardCfg.DBName = name
		dsns[i] = shardCfg.FormatDSN()
	}
	return dsns
}

// TestMySQLShardSetEndToEnd drives the whole shard API against real network
// MySQL databases: dialect, schema, idempotent projection, per-chat ordering,
// read cursors, physical isolation and restart recovery.
func TestMySQLShardSetEndToEnd(t *testing.T) {
	dsns := mysqlShardDSNs(t, 2)
	ctx := context.Background()
	cfg := shard.Config{Driver: shard.DriverMySQL, DSNs: dsns}
	open := func() *shard.Set {
		t.Helper()
		s, err := shard.OpenConfig(shard.NewRouter(2), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	set := open()
	t.Cleanup(func() { _ = set.Close() })
	if set.Driver() != shard.DriverMySQL || set.Len() != 2 {
		t.Fatalf("driver=%s len=%d", set.Driver(), set.Len())
	}

	now := time.Now().UTC()
	m1 := model.Message{ID: "msg_m1", ChatID: "cht_a", SenderID: "usr_a", Type: "text", Text: "hello", CreatedAt: now}
	m2 := model.Message{ID: "msg_m2", ChatID: "cht_a", SenderID: "usr_b", Type: "text", Text: "hi", CreatedAt: now}
	m3 := model.Message{ID: "msg_m3", ChatID: "cht_b", SenderID: "usr_a", Type: "sticker", Text: "other shard", CreatedAt: now}
	for _, m := range []model.Message{m1, m2, m1} { // duplicate verifies INSERT IGNORE
		if err := set.Insert(ctx, 0, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Insert(ctx, 1, m3); err != nil {
		t.Fatal(err)
	}

	got, err := set.List(ctx, 0, "cht_a", 50, 0)
	if err != nil || len(got) != 2 || got[0].ID != "msg_m1" || got[1].ID != "msg_m2" {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[1].Text != "hi" || got[1].Type != "text" {
		t.Fatal("message fields were not round-tripped through MySQL")
	}
	seq, err := set.MessageSeq(ctx, 0, "cht_a", "msg_m2")
	if err != nil || seq <= 0 {
		t.Fatalf("MessageSeq = %d, %v", seq, err)
	}
	latest, id, ok, err := set.Latest(ctx, 0, "cht_a")
	if err != nil || !ok || id != "msg_m2" || latest != seq {
		t.Fatalf("Latest = %d, %s, %v, %v", latest, id, ok, err)
	}
	unread, err := set.CountUnread(ctx, 0, "cht_a", "usr_z", 0)
	if err != nil || unread != 2 {
		t.Fatalf("unread = %d, %v", unread, err)
	}
	unread, err = set.CountUnread(ctx, 0, "cht_a", "usr_z", seq)
	if err != nil || unread != 0 {
		t.Fatalf("unread after cursor = %d, %v", unread, err)
	}
	other, err := set.List(ctx, 1, "cht_a", 50, 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("cht_a leaked into shard 1: %d, %v", len(other), err)
	}
	var leaked int
	if err := set.DB(1).QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_id = ?`, "cht_a").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("physical isolation broken: %d rows, %v", leaked, err)
	}
	gotB, err := set.List(ctx, 1, "cht_b", 50, 0)
	if err != nil || len(gotB) != 1 || gotB[0].Text != "other shard" {
		t.Fatalf("second shard list = %+v, %v", gotB, err)
	}

	// Simulate a process restart: the schema is idempotent and replaying the
	// outbox rows must not duplicate anything (this is what Recover relies on).
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	set = open()
	for _, m := range []model.Message{m1, m2, m3} {
		i := 0
		if m.ChatID == "cht_b" {
			i = 1
		}
		if err := set.Insert(ctx, i, m); err != nil {
			t.Fatal(err)
		}
	}
	after, err := set.List(ctx, 0, "cht_a", 50, 0)
	if err != nil || len(after) != 2 {
		t.Fatalf("replay duplicated rows: %d, %v", len(after), err)
	}
	afterB, err := set.List(ctx, 1, "cht_b", 50, 0)
	if err != nil || len(afterB) != 1 {
		t.Fatalf("replay duplicated second-shard rows: %d, %v", len(afterB), err)
	}
}
