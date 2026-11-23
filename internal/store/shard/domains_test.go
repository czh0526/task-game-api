package shard

import (
	"context"
	"errors"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/plane"
)

func newDomainRouter(t *testing.T, n int) *DomainRouter {
	t.Helper()
	dir := plane.NewInMemoryDirectory()
	topo := plane.NewTopology()
	dsns := make([]string, n)
	for i := range dsns {
		dsns[i] = "db" + string(rune('0'+i))
	}
	if err := topo.Register(plane.DomainMessage, dsns); err != nil {
		t.Fatal(err)
	}
	cp, err := plane.NewControlPlane(dir, topo)
	if err != nil {
		t.Fatal(err)
	}
	dr, err := NewDomainRouter(cp, NewRouter(n))
	if err != nil {
		t.Fatal(err)
	}
	return dr
}

func TestChatKeyIsSymmetricForDirectChats(t *testing.T) {
	a := model.Chat{ID: "cht_a", Type: model.ChatDirect, PairLow: "usr_a", PairHigh: "usr_b"}
	b := model.Chat{ID: "cht_b", Type: model.ChatDirect, PairLow: "usr_b", PairHigh: "usr_a"}
	if ChatKey(a) != ChatKey(b) {
		t.Fatalf("direct chats disagree: %q vs %q", ChatKey(a), ChatKey(b))
	}
}

func TestResolveChatMatchesLegacyRouter(t *testing.T) {
	dr := newDomainRouter(t, 3)
	ctx := context.Background()
	c := model.Chat{ID: "cht_1", Type: model.ChatTask, MemberIDs: []string{"usr_a"}}
	p, err := dr.ResolveChat(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Shard != NewRouter(3).ShardFor(c) {
		t.Fatalf("directory placement %d != legacy hash", p.Shard)
	}
	if !p.FromDirectory {
		t.Fatal("ResolveChat must pin the directory row")
	}
	again, err := dr.Resolve(ctx, plane.DomainMessage, ChatKey(c))
	if err != nil {
		t.Fatal(err)
	}
	if again.Shard != p.Shard {
		t.Fatal("pinned chat moved")
	}
}

func TestDomainScaleOutKeepsChatsPut(t *testing.T) {
	dr := newDomainRouter(t, 2)
	ctx := context.Background()
	c := model.Chat{ID: "cht_1", Type: model.ChatTask}
	before, err := dr.ResolveChat(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := dr.ScaleOut(ctx, plane.DomainMessage, "db2")
	if err != nil {
		t.Fatal(err)
	}
	if plan.MigratedKeys != 0 {
		t.Fatal("scale-out migrated data")
	}
	after, err := dr.Resolve(ctx, plane.DomainMessage, ChatKey(c))
	if err != nil {
		t.Fatal(err)
	}
	if after.Shard != before.Shard {
		t.Fatalf("chat moved %d -> %d", before.Shard, after.Shard)
	}
}

func TestTxScopeRejectsCrossShard(t *testing.T) {
	s := NewTxScope()
	if err := s.Pin(plane.Placement{Domain: plane.DomainUser, Shard: 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Pin(plane.Placement{Domain: plane.DomainUser, Shard: 1}); !errors.Is(err, ErrCrossShard) {
		t.Fatalf("want ErrCrossShard, got %v", err)
	}
	if err := s.Pin(plane.Placement{Domain: plane.DomainUser, Shard: 0}); err != nil {
		t.Fatalf("same shard should be allowed: %v", err)
	}
	if err := s.Pin(plane.Placement{Domain: plane.DomainTask, Shard: 1}); err != nil {
		t.Fatalf("different domain should be allowed: %v", err)
	}
	if shard, ok := s.Shard(plane.DomainUser); !ok || shard != 0 {
		t.Fatalf("user shard = %d, %v", shard, ok)
	}
}
