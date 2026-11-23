package shardmigrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/shardmigrate"
	"taskgame/internal/store/domain"
)

// TestPhysicalCutoverDrill is the automated version of docs/physical-shard-drill.md.
// Four SQLite files stand in for three existing databases plus a new message
// shard. The drill covers legacy directory backfill, adding a shard, copying
// one chat, verifying it, flipping the directory, serving traffic from the new
// shard, and rolling the whole change back.
func TestPhysicalCutoverDrill(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsns := make([]string, 4)
	for i := range dsns {
		dsns[i] = filepath.Join(dir, fmt.Sprintf("shard-%d.db", i))
	}
	controlDSN := filepath.Join(dir, "control.db")
	base := dsns[:3]

	topo := plane.NewTopology()
	byDomain := map[plane.Domain][]string{
		plane.DomainUser:    base,
		plane.DomainTask:    base,
		plane.DomainMessage: base,
	}
	for _, d := range []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage} {
		if err := topo.Register(d, byDomain[d]); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := plane.OpenSQLDirectory(ctx, "sqlite", controlDSN, topo)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	cp, err := plane.NewControlPlane(directory, topo)
	if err != nil {
		t.Fatal(err)
	}

	open := func(messageDSNs []string) *domain.Store {
		t.Helper()
		cfg := domain.Config{
			Driver:         "sqlite",
			DSNs:           byDomain,
			InitialBalance: 1_000_000,
			Control:        cp,
		}
		cfg.DSNs[plane.DomainMessage] = messageDSNs
		st, err := domain.Open(cfg)
		if err != nil {
			t.Fatalf("open domain store: %v", err)
		}
		return st
	}

	// --- Stage 0: seed legacy data on the three original shards ------------
	st := open(base)
	a, b := "usr_a", "usr_b"
	for _, id := range []string{a, b} {
		if _, err := st.Users().Ensure(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Force this chat onto shard 0 so the drill has a known source.
	chat := model.Chat{ID: "chat_drill", Type: model.ChatDirect, PairLow: a, PairHigh: b,
		MemberIDs: []string{a, b}, CreatedAt: time.Now()}
	chatKey := a + "#" + b
	for _, key := range []string{chat.ID, chatKey} {
		if _, err := cp.Resolver.Place(ctx, plane.DomainMessage, key, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Chats().InsertChat(ctx, chat); err != nil {
		t.Fatal(err)
	}
	msgIDs := []string{"msg_1", "msg_2", "msg_3"}
	for i, id := range msgIDs {
		sender := a
		if i == 1 {
			sender = b
		}
		if err := st.Chats().InsertMessage(ctx, model.Message{
			ID: id, ChatID: chat.ID, SenderID: sender, Type: "text",
			Text: fmt.Sprintf("message %d", i+1), CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rowid, err := st.Chats().MessageRowID(ctx, chat.ID, msgIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Chats().AdvanceRead(ctx, b, chat.ID, rowid, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Legacy prototype directory row must travel with the chat.
	sourceDB, err := sql.Open("sqlite", dsns[0])
	if err != nil {
		t.Fatal(err)
	}
	sourceDB.SetMaxOpenConns(1)
	if _, err := sourceDB.ExecContext(ctx,
		`INSERT INTO chat_shards (chat_id, shard, created_at) VALUES (?, ?, ?)`, chat.ID, 0, "now"); err != nil {
		t.Fatal(err)
	}
	_ = sourceDB.Close()
	if unread, err := st.Chats().CountUnread(ctx, b, chat.ID); err != nil || unread != 1 {
		t.Fatalf("before cutover: unread = %d, err = %v, want 1", unread, err)
	}

	// --- Stage 1: simulate pre-directory data and backfill ------------------
	if _, err := directory.DB().ExecContext(ctx, `DELETE FROM shard_directory`); err != nil {
		t.Fatal(err)
	}
	report, err := shardmigrate.Backfill(ctx, "sqlite", controlDSN, base,
		[]plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if len(report.Mismatches) != 0 {
		t.Fatalf("backfill mismatches: %v", report.Mismatches)
	}
	for _, key := range []string{chat.ID, chatKey} {
		shard, ok, err := directory.Lookup(ctx, plane.DomainMessage, key)
		if err != nil || !ok || shard != 0 {
			t.Fatalf("backfilled key %s = %d/%v/%v, want shard 0", key, shard, ok, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// --- Stage 2: add a fourth physical database to the message domain ------
	if err := shardmigrate.EnsureSchema(ctx, "sqlite", dsns[3]); err != nil {
		t.Fatal(err)
	}
	plan, err := topo.AddShard(ctx, plane.DomainMessage, dsns[3], directory)
	if err != nil {
		t.Fatalf("AddShard: %v", err)
	}
	if plan.NewShard != 3 || plan.MigratedKeys != 0 {
		t.Fatalf("unexpected scale-out plan: %+v", plan)
	}

	chatPlans, err := shardmigrate.PlanChats(ctx, "sqlite", dsns[0], []string{chat.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(chatPlans) != 1 || chatPlans[0].Key != chatKey || chatPlans[0].Messages != 3 || chatPlans[0].Reads != 1 {
		t.Fatalf("plan = %+v", chatPlans)
	}

	// --- Stage 3: copy + verify while the old shard still serves traffic ----
	copyReport, err := shardmigrate.Copy(ctx, "sqlite", dsns[0], dsns[3], 3, []string{chat.ID})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copyReport.Messages != 3 || copyReport.Reads != 1 || copyReport.ShardRows != 1 {
		t.Fatalf("copy report = %+v", copyReport)
	}
	if err := shardmigrate.Verify(ctx, "sqlite", dsns[0], dsns[3], 3, []string{chat.ID}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// Copy is idempotent: a retry must not duplicate anything.
	if _, err := shardmigrate.Copy(ctx, "sqlite", dsns[0], dsns[3], 3, []string{chat.ID}); err != nil {
		t.Fatalf("copy retry: %v", err)
	}

	// --- Stage 4: flip the directory, then drop the source rows -------------
	if err := shardmigrate.Cutover(ctx, "sqlite", controlDSN, chatPlans, 0, 3, dsns[3]); err != nil {
		t.Fatalf("cutover: %v", err)
	}
	if removed, err := shardmigrate.Cleanup(ctx, "sqlite", dsns[0], []string{chat.ID}); err != nil || removed != 1 {
		t.Fatalf("cleanup: removed=%d err=%v", removed, err)
	}
	for _, key := range []string{chat.ID, chatKey} {
		shard, ok, err := directory.Lookup(ctx, plane.DomainMessage, key)
		if err != nil || !ok || shard != 3 {
			t.Fatalf("after cutover key %s = %d/%v/%v, want shard 3", key, shard, ok, err)
		}
	}

	// --- Stage 5: restart API with the new message DSN list and use it ------
	four := append(append([]string{}, base...), dsns[3])
	st2 := open(four)
	defer st2.Close()
	msgs, err := st2.Chats().ListMessages(ctx, chat.ID, 10, 0)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("messages after cutover: %d, err=%v", len(msgs), err)
	}
	if msgs[2].Text != "message 3" {
		t.Fatalf("message order changed: %+v", msgs)
	}
	if unread, err := st2.Chats().CountUnread(ctx, b, chat.ID); err != nil || unread != 1 {
		t.Fatalf("read cursor after cutover: unread=%d err=%v, want 1", unread, err)
	}
	if found, err := st2.Chats().FindDirect(ctx, b, a); err != nil || found.ID != chat.ID {
		t.Fatalf("direct chat lookup after cutover: %+v err=%v", found, err)
	}
	if err := st2.Chats().InsertMessage(ctx, model.Message{
		ID: "msg_after", ChatID: chat.ID, SenderID: a, Type: "text",
		Text: "after cutover", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("write after cutover: %v", err)
	}
	newShardStore, err := st2.StoreFor(plane.DomainMessage, 3)
	if err != nil {
		t.Fatal(err)
	}
	newMsgs, err := newShardStore.Chats().ListMessages(ctx, chat.ID, 10, 0)
	if err != nil || len(newMsgs) != 4 {
		t.Fatalf("new messages on shard 3: %d, err=%v", len(newMsgs), err)
	}

	// --- Stage 6: roll the cutover back ------------------------------------
	if err := shardmigrate.Rollback(ctx, "sqlite", controlDSN, dsns[0], dsns[3], 0, 3, []string{chat.ID}); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	for _, key := range []string{chat.ID, chatKey} {
		shard, ok, err := directory.Lookup(ctx, plane.DomainMessage, key)
		if err != nil || !ok || shard != 0 {
			t.Fatalf("after rollback key %s = %d/%v/%v, want shard 0", key, shard, ok, err)
		}
	}
	oldShard, err := st2.StoreFor(plane.DomainMessage, 0)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := oldShard.Chats().ListMessages(ctx, chat.ID, 10, 0)
	if err != nil || len(restored) != 4 {
		t.Fatalf("messages restored on shard 0: %d, err=%v", len(restored), err)
	}
	routed, err := st2.Chats().ListMessages(ctx, chat.ID, 10, 0)
	if err != nil || len(routed) != 4 {
		t.Fatalf("routed read after rollback: %d, err=%v", len(routed), err)
	}
	newDB, err := sql.Open("sqlite", dsns[3])
	if err != nil {
		t.Fatal(err)
	}
	defer newDB.Close()
	var leftovers int
	if err := newDB.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_id = ?`, chat.ID).Scan(&leftovers); err != nil {
		t.Fatal(err)
	}
	if leftovers != 0 {
		t.Fatalf("rollback left %d messages on the new shard", leftovers)
	}
}
