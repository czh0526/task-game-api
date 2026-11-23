package domain_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"
	"taskgame/internal/store/domain"
	"taskgame/internal/store/shard"
)

func openDomain(t *testing.T, control *plane.ControlPlane) (*domain.Store, []string) {
	t.Helper()
	dir := t.TempDir()
	dsns := make([]string, 3)
	for i := range dsns {
		dsns[i] = filepath.Join(dir, fmt.Sprintf("domain-%d.db", i))
	}
	byDomain := map[plane.Domain][]string{
		plane.DomainUser:    dsns,
		plane.DomainTask:    dsns,
		plane.DomainMessage: dsns,
	}
	st, err := domain.Open(domain.Config{
		Driver:         "sqlite",
		DSNs:           byDomain,
		InitialBalance: 1_000_000,
		Control:        control,
	})
	if err != nil {
		t.Fatalf("open domain store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dsns
}

func TestCoLocatedDomainsResolveToSameShard(t *testing.T) {
	st, _ := openDomain(t, nil)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		key := fmt.Sprintf("usr_%02d", i)
		u, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, key)
		if err != nil {
			t.Fatal(err)
		}
		task, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainTask, key)
		if err != nil {
			t.Fatal(err)
		}
		if u.Shard != task.Shard || u.DSN != task.DSN {
			t.Fatalf("co-located domains diverged for %s: user=%d/%s task=%d/%s", key, u.Shard, u.DSN, task.Shard, task.DSN)
		}
	}
}

func TestUsersDistributeAndDirectoryIsAuthoritative(t *testing.T) {
	st, _ := openDomain(t, nil)
	ctx := context.Background()
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = fmt.Sprintf("usr_%02d", i)
		if _, err := st.Users().Ensure(ctx, ids[i], time.Now()); err != nil {
			t.Fatalf("ensure %s: %v", ids[i], err)
		}
	}

	perShard := make([]int, st.Topology().Len(plane.DomainUser))
	for _, id := range ids {
		p, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.FromDirectory {
			t.Fatalf("directory row missing for %s", id)
		}
		perShard[p.Shard]++
		physical, err := st.StoreFor(plane.DomainUser, p.Shard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := physical.Users().Get(ctx, id); err != nil {
			t.Fatalf("user %s not on resolved shard %d: %v", id, p.Shard, err)
		}
	}
	total := 0
	for i, n := range perShard {
		if n < 5 || n > 15 {
			t.Fatalf("shard %d holds %d users, expected roughly 1/3 of 30: %v", i, n, perShard)
		}
		total += n
	}
	if total != len(ids) {
		t.Fatalf("counted %d users, want %d", total, len(ids))
	}
}

func TestInTxPinsAndRejectsSameDomainCrossShard(t *testing.T) {
	st, _ := openDomain(t, nil)
	ctx := context.Background()

	var first, other string
	for i := 0; i < 100 && other == ""; i++ {
		id := fmt.Sprintf("usr_%03d", i)
		p, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, id)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first, _ = id, p.Shard
			continue
		}
		fp, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, first)
		if err != nil {
			t.Fatal(err)
		}
		if p.Shard != fp.Shard {
			other = id
		}
	}
	if other == "" {
		t.Fatal("test needs two users on different shards")
	}
	for _, id := range []string{first, other} {
		if _, err := st.Users().Ensure(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	err := st.InTx(domain.WithRoute(ctx, plane.DomainUser, first, false), func(ctx context.Context, r store.Repos) error {
		if _, err := r.Users().Get(ctx, first); err != nil {
			return fmt.Errorf("read pinned user: %w", err)
		}
		if err := r.Users().SetNickname(ctx, first, "pinned"); err != nil {
			return fmt.Errorf("write pinned user: %w", err)
		}
		if _, err := r.Users().Get(ctx, other); !errors.Is(err, shard.ErrCrossShard) {
			return fmt.Errorf("same-domain cross-shard read: got %v, want ErrCrossShard", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	u, err := st.Users().Get(ctx, first)
	if err != nil || u.Nickname != "pinned" {
		t.Fatalf("tx write not visible: user=%+v err=%v", u, err)
	}
}

func TestDirectChatStaysOnOneMessageShardAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsns := make([]string, 3)
	for i := range dsns {
		dsns[i] = filepath.Join(dir, fmt.Sprintf("message-%d.db", i))
	}
	topo := plane.NewTopology()
	byDomain := map[plane.Domain][]string{
		plane.DomainUser:    dsns,
		plane.DomainTask:    dsns,
		plane.DomainMessage: dsns,
	}
	for _, d := range []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage} {
		if err := topo.Register(d, byDomain[d]); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := plane.OpenSQLDirectory(ctx, "sqlite", filepath.Join(dir, "control.db"), topo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	control, err := plane.NewControlPlane(directory, topo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := domain.Config{Driver: "sqlite", DSNs: byDomain, InitialBalance: 1_000_000, Control: control}

	st, err := domain.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, b := "usr_a", "usr_b"
	chatAB := model.Chat{ID: "chat_ab", Type: model.ChatDirect, PairLow: a, PairHigh: b, MemberIDs: []string{a, b}, CreatedAt: time.Now()}
	chatBA := model.Chat{ID: "chat_ba", Type: model.ChatDirect, PairLow: b, PairHigh: a, MemberIDs: []string{a, b}, CreatedAt: time.Now()}
	if err := st.Chats().InsertChat(ctx, chatAB); err != nil {
		t.Fatal(err)
	}
	if err := st.Chats().InsertChat(ctx, chatBA); err != nil {
		t.Fatal(err)
	}
	pAB, err := control.Resolver.ResolveExisting(ctx, plane.DomainMessage, chatAB.ID)
	if err != nil {
		t.Fatal(err)
	}
	pBA, err := control.Resolver.ResolveExisting(ctx, plane.DomainMessage, chatBA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pAB.Shard != pBA.Shard {
		t.Fatalf("direct chats for the same pair landed on %d and %d", pAB.Shard, pBA.Shard)
	}
	for _, chatID := range []string{chatAB.ID, chatBA.ID} {
		if err := st.Chats().InsertMessage(ctx, model.Message{ID: "msg_" + chatID, ChatID: chatID, SenderID: a, Type: "text", Text: "hi", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	physical, err := st.StoreFor(plane.DomainMessage, pAB.Shard)
	if err != nil {
		t.Fatal(err)
	}
	for _, chatID := range []string{chatAB.ID, chatBA.ID} {
		msgs, err := physical.Chats().ListMessages(ctx, chatID, 10, 0)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("chat %s on shard %d: %d messages, err=%v", chatID, pAB.Shard, len(msgs), err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// A fresh process with the same SQL directory must resolve the same shards.
	st2, err := domain.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, err := st2.Chats().GetChat(ctx, chatAB.ID)
	if err != nil || got.ID != chatAB.ID {
		t.Fatalf("chat not found after restart: %+v err=%v", got, err)
	}
	msgs, err := st2.Chats().ListMessages(ctx, chatBA.ID, 10, 0)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages not found after restart: %d, err=%v", len(msgs), err)
	}
}
