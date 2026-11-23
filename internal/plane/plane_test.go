package plane

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func newTestPlane(t *testing.T, dsns ...string) (*ControlPlane, *Topology, *InMemoryDirectory) {
	t.Helper()
	dir := NewInMemoryDirectory()
	topo := NewTopology()
	if err := topo.Register(DomainMessage, dsns); err != nil {
		t.Fatal(err)
	}
	cp, err := NewControlPlane(dir, topo)
	if err != nil {
		t.Fatal(err)
	}
	return cp, topo, dir
}

func TestResolveIsStableAndUsesDirectory(t *testing.T) {
	cp, _, _ := newTestPlane(t, "db0", "db1")
	ctx := context.Background()
	first, err := cp.Resolver.Resolve(ctx, DomainMessage, "cht_1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := cp.Resolver.Resolve(ctx, DomainMessage, "cht_1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Shard != second.Shard || !second.FromDirectory {
		t.Fatalf("unstable placement: %+v then %+v", first, second)
	}
	if first.DSN == "" {
		t.Fatal("placement has no DSN")
	}
}

func TestScaleOutDoesNotMoveExistingKeys(t *testing.T) {
	cp, topo, dir := newTestPlane(t, "db0", "db1")
	ctx := context.Background()
	before := map[int]string{}
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("cht_%d", i)
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, key)
		if err != nil {
			t.Fatal(err)
		}
		before[p.Shard] = key
	}
	if len(before) != 2 {
		t.Fatalf("expected both shards to be used, got %v", before)
	}
	plan, err := topo.AddShard(ctx, DomainMessage, "db2", dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MigratedKeys != 0 || plan.To != 3 {
		t.Fatalf("scale-out must not migrate: %+v", plan)
	}
	for shard, key := range before {
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, key)
		if err != nil {
			t.Fatal(err)
		}
		if p.Shard != shard || !p.FromDirectory {
			t.Fatalf("key %q moved from %d to %d", key, shard, p.Shard)
		}
	}
}

func TestNewKeysCanLandOnNewShard(t *testing.T) {
	cp, topo, dir := newTestPlane(t, "db0", "db1")
	ctx := context.Background()
	if _, err := topo.AddShard(ctx, DomainMessage, "db2", dir); err != nil {
		t.Fatal(err)
	}
	landed := 0
	for i := 0; i < 100; i++ {
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, fmt.Sprintf("new_%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if p.Shard == 2 {
			landed++
		}
	}
	if landed == 0 {
		t.Fatal("no new key landed on the appended shard")
	}
}

func TestResolveExistingPinsLegacyModulus(t *testing.T) {
	cp, topo, dir := newTestPlane(t, "db0", "db1")
	ctx := context.Background()
	want := hashShard("legacy_chat", 2)
	legacy, err := cp.Resolver.ResolveExisting(ctx, DomainMessage, "legacy_chat")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Shard != want {
		t.Fatalf("legacy shard %d, want %d", legacy.Shard, want)
	}
	if _, err := topo.AddShard(ctx, DomainMessage, "db2", dir); err != nil {
		t.Fatal(err)
	}
	again, err := cp.Resolver.ResolveExisting(ctx, DomainMessage, "legacy_chat")
	if err != nil {
		t.Fatal(err)
	}
	if again.Shard != want {
		t.Fatalf("scale-out changed legacy fallback: %d -> %d", want, again.Shard)
	}
	if _, ok, err := dir.Lookup(ctx, DomainMessage, "legacy_chat"); err != nil || ok {
		t.Fatal("legacy fallback must not write a directory row")
	}
}

func TestPlanShrinkListsKeysNeedingMigration(t *testing.T) {
	cp, topo, dir := newTestPlane(t, "db0", "db1")
	ctx := context.Background()
	var target string
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("cht_%d", i)
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, key)
		if err != nil {
			t.Fatal(err)
		}
		if p.Shard == 0 {
			target = key
			break
		}
	}
	if target == "" {
		t.Fatal("no key on shard 0")
	}
	plan, err := topo.PlanShrink(ctx, dir, DomainMessage, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresRewrite || len(plan.Keys) == 0 {
		t.Fatalf("shrink must require migration: %+v", plan)
	}
	found := false
	for _, k := range plan.Keys {
		if k == target {
			found = true
		}
	}
	if !found {
		t.Fatalf("shrink plan omitted %q", target)
	}
}

func TestUniqueIndexAndIDGenerator(t *testing.T) {
	ctx := context.Background()
	u := NewInMemoryUnique()
	if err := u.Reserve(ctx, "email", "a@example.com", "usr_a"); err != nil {
		t.Fatal(err)
	}
	if err := u.Reserve(ctx, "email", "a@example.com", "usr_a"); err != nil {
		t.Fatalf("idempotent retry should pass: %v", err)
	}
	if err := u.Reserve(ctx, "email", "a@example.com", "usr_b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	owner, ok, err := u.Owner(ctx, "email", "a@example.com")
	if err != nil || !ok || owner != "usr_a" {
		t.Fatalf("owner = %q, %v, %v", owner, ok, err)
	}
	g := NewLocalIDGenerator("api-1")
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := g.New("tsk_")
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
