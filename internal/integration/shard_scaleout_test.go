package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/config"
	"taskgame/internal/model"
	"taskgame/internal/svc"

	"github.com/zeromicro/go-zero/core/conf"
	_ "modernc.org/sqlite"
)

// TestShardScaleOutPreservesExistingChats documents the resize rule of the
// sharding prototype: chat_shards is the authoritative route, so appending a
// shard only affects chats created after the change. Existing chats keep their
// original shard and need no data migration. Shrinking, on the other hand,
// leaves directory rows pointing at a shard the Set no longer owns.
func TestShardScaleOutPreservesExistingChats(t *testing.T) {
	dir := t.TempDir()
	primary := "file:" + filepath.Join(dir, "primary.db") +
		"?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	shardDSN := func(i int) string {
		return "file:" + filepath.Join(dir, fmt.Sprintf("shard-%d.db", i))
	}

	writeConfig := func(n int) string {
		t.Helper()
		path := filepath.Join(dir, fmt.Sprintf("scale-%d.yaml", n))
		list := ""
		for i := 0; i < n; i++ {
			list += fmt.Sprintf("    - %q\n", shardDSN(i))
		}
		body := fmt.Sprintf(`
Name: taskgame-api
Host: 127.0.0.1
Port: 8891
Mode: test
Timeout: 10000
CpuThreshold: 0
Log:
  Mode: console
  Encoding: plain
  Level: error
  Stat: false
Auth:
  JwtSecret: dev-jwt-secret-change-me-32b
  TokenTTL: 24h
Store:
  Driver: sqlite
  DSN: %q
  Shards: %d
  ShardDSNs:
%s
Escrow:
  InitialBalance: 1000000
  Currency: credits
Webhook:
  Mode: log
  Interval: 1s
  Timeout: 1s
  MaxAttempts: 2
`, primary, n, list)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	open := func(n int) *svc.ServiceContext {
		t.Helper()
		var c config.Config
		if err := conf.Load(writeConfig(n), &c); err != nil {
			t.Fatal(err)
		}
		if c.Store.Shards != n || len(c.Store.ShardDSNs) != n {
			t.Fatalf("config for N=%d not loaded: %+v", n, c.Store)
		}
		sc, err := svc.NewServiceContext(c)
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}

	directory := func() map[string]int {
		t.Helper()
		db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "primary.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`SELECT chat_id, shard FROM chat_shards`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var id string
			var idx int
			if err := rows.Scan(&id, &idx); err != nil {
				t.Fatal(err)
			}
			out[id] = idx
		}
		return out
	}

	ctx := context.Background()
	now := time.Now().UTC()

	makeChat := func(sc *svc.ServiceContext, prefix string, i int) (string, string) {
		t.Helper()
		a := fmt.Sprintf("usr_%s_a_%02d", prefix, i)
		b := fmt.Sprintf("usr_%s_b_%02d", prefix, i)
		for _, u := range []string{a, b} {
			if _, err := sc.Store.Users().Ensure(ctx, u, now); err != nil {
				t.Fatal(err)
			}
		}
		low, high := model.OrderedPair(a, b)
		cid := fmt.Sprintf("chat_%s_%02d", prefix, i)
		if err := sc.Store.Chats().InsertChat(ctx, model.Chat{
			ID: cid, Type: model.ChatDirect, PairLow: low, PairHigh: high,
			MemberIDs: []string{a, b}, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("%s-%d", prefix, i)
		if err := sc.Store.Chats().InsertMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_%s_%02d", prefix, i), ChatID: cid, SenderID: a,
			Type: "text", Text: text, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		return cid, text
	}

	// Phase 1: two shards, create chats and messages.
	sc := open(2)
	type oldChat struct{ id, text string }
	old := []oldChat{}
	for i := 0; i < 8; i++ {
		cid, text := makeChat(sc, "before", i)
		old = append(old, oldChat{cid, text})
	}
	before := directory()
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: append shard 2 at the END of the DSN list. No data is moved.
	sc = open(3)
	t.Cleanup(func() { _ = sc.Close() })

	after := directory()
	for id, idx := range before {
		if after[id] != idx {
			t.Fatalf("chat %s moved from shard %d to %d after scale-out", id, idx, after[id])
		}
	}
	for _, o := range old {
		msgs, err := sc.Store.Chats().ListMessages(ctx, o.id, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Text != o.text {
			t.Fatalf("chat %s lost its message after scale-out: %+v", o.id, msgs)
		}
	}

	// New chats may now land on shard 2. Keep creating until one does.
	var landedID string
	for i := 0; i < 32 && landedID == ""; i++ {
		cid, _ := makeChat(sc, "after", i)
		if directory()[cid] == 2 {
			landedID = cid
		}
	}
	if landedID == "" {
		t.Fatal("no new chat landed on shard 2; scale-out did not take effect")
	}
	msgs, err := sc.Store.Chats().ListMessages(ctx, landedID, 10, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Text == "" {
		t.Fatalf("message on the new shard unreadable: %+v %v", msgs, err)
	}

	// shard-2.db must contain only post-scaleout messages.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "shard-2.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var total, leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_id LIKE 'chat_before_%'`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if total == 0 || leaked != 0 {
		t.Fatalf("new shard holds %d rows, %d of which are pre-scaleout chats", total, leaked)
	}

	// Phase 3: shrinking back to 2 without migrating data breaks the chat that
	// lives on shard 2. This is why shrink needs a data migration step.
	if err := sc.Close(); err != nil {
		t.Fatal(err)
	}
	sc = open(2)
	if _, err := sc.Store.Chats().ListMessages(ctx, landedID, 10, 0); err == nil {
		t.Fatal("shrinking without migration unexpectedly succeeded")
	}
}
