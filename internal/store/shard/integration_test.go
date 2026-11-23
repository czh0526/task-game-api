package shard_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/engine"
	"taskgame/internal/model"
	"taskgame/internal/notify"
	"taskgame/internal/store"
	"taskgame/internal/store/shard"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"

	_ "modernc.org/sqlite"
)

func pickTwoChats(t *testing.T, r shard.Router) (model.Chat, model.Chat) {
	t.Helper()
	const low = "usr_A"
	var c0, c1 model.Chat
	for i := 0; i < 500 && (c0.ID == "" || c1.ID == ""); i++ {
		high := fmt.Sprintf("usr_B%03d", i)
		c := model.Chat{
			ID:        fmt.Sprintf("cht_%03d", i),
			Type:      model.ChatDirect,
			PairLow:   low,
			PairHigh:  high,
			MemberIDs: []string{low, high},
			CreatedAt: time.Now().UTC(),
		}
		switch r.ShardFor(c) {
		case 0:
			if c0.ID == "" {
				c0 = c
			}
		case 1:
			if c1.ID == "" {
				c1 = c
			}
		}
	}
	if c0.ID == "" || c1.ID == "" {
		t.Fatal("could not find two direct chats on different shards")
	}
	return c0, c1
}

func countMessages(t *testing.T, db *sql.DB, chatID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_id = ?`, chatID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShardedMessagesEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mainDSN := "file:" + filepath.Join(dir, "main.db")
	shardDSN := "file:" + filepath.Join(dir, "shard-%d.db")

	st, err := sqlite.Open(mainDSN, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set, err := shard.Open(shard.NewRouter(2), shardDSN)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnableShards(set); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, "credits", engine.WithSignalNotifier(notify.New()))
	router := shard.NewRouter(2)
	c0, c1 := pickTwoChats(t, router)
	if router.ShardFor(c0) == router.ShardFor(c1) {
		t.Fatal("test setup: chats must land on different shards")
	}
	for _, c := range []model.Chat{c0, c1} {
		if err := st.Chats().InsertChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	post := func(chat model.Chat, sender, text, key string) {
		t.Helper()
		if _, err := eng.PostMessage(ctx, sender, &types.PostMessageReq{
			ChatId: chat.ID, Type: "text", Text: text, IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("post to %s: %v", chat.ID, err)
		}
	}
	post(c0, c0.PairLow, "hello from A", "")
	post(c0, c0.PairHigh, "hi from B", "")
	post(c1, c1.PairHigh, "message on the other shard", "")

	// A -> B read path: both messages of chat c0 come back in order.
	msgs, err := st.Chats().ListMessages(ctx, c0.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || msgs[0].Text != "hello from A" || msgs[1].Text != "hi from B" {
		t.Fatalf("unexpected thread: %+v", msgs)
	}
	msgs1, err := st.Chats().ListMessages(ctx, c1.ID, 50, 0)
	if err != nil || len(msgs1) != 1 || msgs1[0].Text != "message on the other shard" {
		t.Fatalf("unexpected second thread: %+v, %v", msgs1, err)
	}

	// Physical isolation: each chat's rows live in exactly one shard file.
	s0, s1 := router.ShardFor(c0), router.ShardFor(c1)
	if got := countMessages(t, set.DB(s0), c0.ID); got != 2 {
		t.Fatalf("shard %d has %d rows for %s", s0, got, c0.ID)
	}
	if got := countMessages(t, set.DB(s1), c0.ID); got != 0 {
		t.Fatalf("c0 leaked into the other shard: %d rows", got)
	}
	if got := countMessages(t, set.DB(s1), c1.ID); got != 1 {
		t.Fatalf("shard %d has %d rows for %s", s1, got, c1.ID)
	}
	if got := countMessages(t, set.DB(s0), c1.ID); got != 0 {
		t.Fatalf("c1 leaked into the other shard: %d rows", got)
	}

	// Directory and outbox are recorded transactionally in the primary store.
	main, err := sql.Open("sqlite", mainDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	var dirRows, outboxRows, pending int
	if err := main.QueryRow(`SELECT COUNT(*) FROM chat_shards`).Scan(&dirRows); err != nil {
		t.Fatal(err)
	}
	if err := main.QueryRow(`SELECT COUNT(*), COALESCE(SUM(projected), 0) FROM message_outbox`).Scan(&outboxRows, &pending); err != nil {
		t.Fatal(err)
	}
	if dirRows != 2 || outboxRows != 3 || pending != 3 {
		t.Fatalf("directory=%d outbox=%d projected=%d", dirRows, outboxRows, pending)
	}
	var primaryMessages int
	if err := main.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&primaryMessages); err != nil {
		t.Fatal(err)
	}
	if primaryMessages != 0 {
		t.Fatalf("primary messages table should be empty, got %d", primaryMessages)
	}

	// Unread cursors are per-chat shard sequences, aggregated across shards.
	unreads, err := st.Chats().ListDirectUnreads(ctx, c0.PairLow)
	if err != nil {
		t.Fatal(err)
	}
	if len(unreads) != 2 {
		t.Fatalf("expected 2 direct threads, got %+v", unreads)
	}
	for _, u := range unreads {
		if u.UnreadCount != 1 {
			t.Fatalf("unread count for %s = %d", u.ChatID, u.UnreadCount)
		}
	}
	latest, messageID, ok, err := st.Chats().LatestMessageRow(ctx, c0.ID)
	if err != nil || !ok || messageID != msgs[1].ID {
		t.Fatalf("LatestMessageRow = %d, %s, %v, %v", latest, messageID, ok, err)
	}
	if err := st.Chats().AdvanceRead(ctx, c0.PairLow, c0.ID, latest, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	n, err := st.Chats().CountUnread(ctx, c0.PairLow, c0.ID)
	if err != nil || n != 0 {
		t.Fatalf("c0 unread after read = %d, %v", n, err)
	}
	unreads, err = st.Chats().ListDirectUnreads(ctx, c0.PairLow)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range unreads {
		if u.ChatID == c0.ID && u.UnreadCount != 0 {
			t.Fatal("c0 should be read")
		}
		if u.ChatID == c1.ID && u.UnreadCount != 1 {
			t.Fatal("c1 should still be unread")
		}
	}

	// Idempotent replay does not duplicate a message.
	before := len(msgs)
	post(c0, c0.PairLow, "dupe", "idem-key-1")
	post(c0, c0.PairLow, "dupe", "idem-key-1")
	after, err := st.Chats().ListMessages(ctx, c0.ID, 50, 0)
	if err != nil || len(after) != before+1 {
		t.Fatalf("idempotent replay duplicated a message: %d -> %d, %v", before, len(after), err)
	}

	// A rolled-back transaction must not project anything.
	err = st.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		if err := r.Chats().InsertMessage(ctx, model.Message{
			ID: "msg_rollback", ChatID: c0.ID, SenderID: c0.PairLow,
			Type: "text", Text: "never", CreatedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}
	if got := countMessages(t, set.DB(s0), c0.ID); got != before+1 {
		t.Fatalf("rollback left a shard row: %d", got)
	}

	// Crash recovery replays unprojected outbox rows.
	recoverID := "msg_recovered"
	_, err = main.Exec(`
		INSERT INTO message_outbox (id, chat_id, shard, sender_id, type, text, attachment_ref, created_at, projected)
		VALUES (?, ?, ?, ?, 'text', 'recovered', '', ?, 0)`,
		recoverID, c0.ID, s0, c0.PairLow, model.FormatTime(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMessages(t, set.DB(s0), c0.ID); got != before+2 {
		t.Fatalf("Recover did not project the pending row: %d", got)
	}
	var projected int
	if err := main.QueryRow(`SELECT projected FROM message_outbox WHERE id = ?`, recoverID).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 1 {
		t.Fatal("outbox row was not marked projected")
	}
}

func TestUnshardedStoreKeepsMessagesInPrimary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	mainDSN := "file:" + filepath.Join(dir, "main.db")
	st, err := sqlite.Open(mainDSN, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Chats().InsertChat(ctx, model.Chat{
		ID: "cht_plain", Type: model.ChatGroup, MemberIDs: []string{"usr_a", "usr_b"},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Chats().InsertMessage(ctx, model.Message{
		ID: "msg_plain", ChatID: "cht_plain", SenderID: "usr_a",
		Type: "text", Text: "plain", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.Chats().ListMessages(ctx, "cht_plain", 50, 0)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "plain" {
		t.Fatalf("default path regressed: %+v, %v", msgs, err)
	}
	main, err := sql.Open("sqlite", mainDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	var n, outbox, dirRows int
	_ = main.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	_ = main.QueryRow(`SELECT COUNT(*) FROM message_outbox`).Scan(&outbox)
	_ = main.QueryRow(`SELECT COUNT(*) FROM chat_shards`).Scan(&dirRows)
	if n != 1 || outbox != 0 || dirRows != 0 {
		t.Fatalf("default path wrote shard tables: messages=%d outbox=%d dir=%d", n, outbox, dirRows)
	}
}
