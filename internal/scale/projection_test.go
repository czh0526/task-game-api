package scale

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"taskgame/internal/config"
	"taskgame/internal/projection"
)

func TestProjectionWiresInProcessLeaseAndBus(t *testing.T) {
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "shared",
		InstanceID: "api-1",
		Projection: config.ProjectionScaleConf{
			Enabled: true, Interval: 50 * time.Millisecond, LeaseTTL: time.Minute,
		},
	}, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Lease == nil || rt.Bus == nil {
		t.Fatal("lease or bus missing")
	}
	if !rt.ProjectionEnabled() || rt.InstanceID() != "api-1" {
		t.Fatalf("projection flags wrong: enabled=%v id=%q", rt.ProjectionEnabled(), rt.InstanceID())
	}
	if rt.ProjectionInterval() != 50*time.Millisecond || rt.ProjectionLeaseTTL() != time.Minute {
		t.Fatalf("projection settings wrong: %v / %v", rt.ProjectionInterval(), rt.ProjectionLeaseTTL())
	}
	ctx := context.Background()
	if ok, _ := rt.Lease.Acquire(ctx, "row", "api-1", time.Minute); !ok {
		t.Fatal("first acquire failed")
	}
	if ok, _ := rt.Lease.Acquire(ctx, "row", "api-2", time.Minute); ok {
		t.Fatal("second owner acquired a live lease")
	}
	got := make(chan string, 1)
	stop, err := rt.Bus.Subscribe(ctx, func(id string) { got <- id })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := rt.Bus.Publish(ctx, "msg_1"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-got:
		if id != "msg_1" {
			t.Fatalf("got %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("bus did not deliver the hint")
	}
}

func TestProjectionDisabledBuildsNothing(t *testing.T) {
	rt, err := New(config.ScaleConf{Enabled: true, PubSub: "shared"}, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Lease != nil || rt.Bus != nil || rt.ProjectionEnabled() {
		t.Fatal("disabled projection must not build a lease or bus")
	}
	if rt.ProjectionInterval() != projection.DefaultInterval {
		t.Fatalf("default interval = %v", rt.ProjectionInterval())
	}
}

// Two runtimes sharing one Redis must share the lease: this is what makes
// outbox projection safe across real machines.
func TestProjectionUsesSharedRedisLease(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := config.ScaleConf{
		Enabled:    true,
		PubSub:     "redis",
		InstanceID: "api-1",
		Redis: config.RedisScaleConf{
			Addr: mr.Addr(), Channel: "taskgame.test.signals", Timeout: 500 * time.Millisecond,
		},
		Projection: config.ProjectionScaleConf{Enabled: true, Channel: "taskgame.test.outbox"},
	}
	rt, err := New(cfg, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, ok := rt.Lease.(*projection.RedisLease); !ok {
		t.Fatalf("lease is %T, want *projection.RedisLease", rt.Lease)
	}
	if _, ok := rt.Bus.(*projection.RedisBus); !ok {
		t.Fatalf("bus is %T, want *projection.RedisBus", rt.Bus)
	}
	cfg.InstanceID = "api-2"
	other, err := New(cfg, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	if ok, _ := rt.Lease.Acquire(ctx, "row", "api-1", time.Minute); !ok {
		t.Fatal("api-1 could not acquire")
	}
	if ok, _ := other.Lease.Acquire(ctx, "row", "api-2", time.Minute); ok {
		t.Fatal("api-2 acquired the same row through the shared Redis lease")
	}
}
