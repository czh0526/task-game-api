package projection

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestMemoryLeaseMutualExclusion(t *testing.T) {
	l := NewMemoryLease()
	defer l.Close()
	ctx := context.Background()
	ok, err := l.Acquire(ctx, "row-1", "api-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first acquire: %v %v", ok, err)
	}
	ok, err = l.Acquire(ctx, "row-1", "api-b", time.Minute)
	if err != nil || ok {
		t.Fatalf("second process must not acquire: %v %v", ok, err)
	}
	// Re-entrant acquire by the same owner refreshes the TTL.
	ok, err = l.Acquire(ctx, "row-1", "api-a", time.Minute)
	if err != nil || !ok {
		t.Fatalf("owner re-acquire: %v %v", ok, err)
	}
	// A foreign release must not free the lease.
	if err := l.Release(ctx, "row-1", "api-b"); err != nil {
		t.Fatal(err)
	}
	ok, _ = l.Acquire(ctx, "row-1", "api-b", time.Minute)
	if ok {
		t.Fatal("foreign release freed someone else's lease")
	}
	if err := l.Release(ctx, "row-1", "api-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = l.Acquire(ctx, "row-1", "api-b", time.Minute)
	if err != nil || !ok {
		t.Fatalf("release did not free the lease: %v %v", ok, err)
	}
}

func TestMemoryLeaseExpires(t *testing.T) {
	l := NewMemoryLease()
	defer l.Close()
	ctx := context.Background()
	if ok, _ := l.Acquire(ctx, "row-1", "api-a", 20*time.Millisecond); !ok {
		t.Fatal("first acquire failed")
	}
	time.Sleep(35 * time.Millisecond)
	if ok, _ := l.Acquire(ctx, "row-1", "api-b", time.Minute); !ok {
		t.Fatal("expired lease was not reclaimable (crash takeover)")
	}
}

func TestRedisLeaseAcrossProcesses(t *testing.T) {
	mr := miniredis.RunT(t)
	e := Endpoint{Addr: mr.Addr(), Timeout: 500 * time.Millisecond}
	a, b := NewRedisLease(e), NewRedisLease(e)
	defer a.Close()
	defer b.Close()
	ctx := context.Background()
	ttl := time.Minute
	ok, err := a.Acquire(ctx, "row-1", "api-a", ttl)
	if err != nil || !ok {
		t.Fatalf("A acquire: %v %v", ok, err)
	}
	ok, err = b.Acquire(ctx, "row-1", "api-b", ttl)
	if err != nil || ok {
		t.Fatalf("B must not acquire a live lease: %v %v", ok, err)
	}
	// After A releases, B can take over.
	if err := a.Release(ctx, "row-1", "api-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = b.Acquire(ctx, "row-1", "api-b", ttl)
	if err != nil || !ok {
		t.Fatalf("B acquire after release: %v %v", ok, err)
	}
	// A stale release must NOT delete B's lease (Lua compare-and-delete).
	if err := a.Release(ctx, "row-1", "api-a"); err != nil {
		t.Fatal(err)
	}
	ok, err = a.Acquire(ctx, "row-1", "api-a", ttl)
	if err != nil || ok {
		t.Fatalf("stale release deleted the new holder's lease: %v %v", ok, err)
	}
	// TTL expiry is the crash-takeover path.
	mr.FastForward(ttl)
	ok, err = a.Acquire(ctx, "row-1", "api-a", ttl)
	if err != nil || !ok {
		t.Fatalf("expired Redis lease was not reclaimable: %v %v", ok, err)
	}
}

func TestMemoryBusDeliverAndStop(t *testing.T) {
	b := NewMemoryBus()
	defer b.Close()
	ctx := context.Background()
	var got atomic.Int64
	stop1, err := b.Subscribe(ctx, func(string) { got.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	stop2, err := b.Subscribe(ctx, func(string) { got.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, "msg_1"); err != nil {
		t.Fatal(err)
	}
	if got.Load() != 2 {
		t.Fatalf("both subscribers should see the hint, got %d", got.Load())
	}
	stop1()
	stop1() // idempotent
	_ = b.Publish(ctx, "msg_2")
	if got.Load() != 3 {
		t.Fatalf("stopped subscriber still received, got %d", got.Load())
	}
	stop2()
}

func TestRedisBusAcrossProcesses(t *testing.T) {
	mr := miniredis.RunT(t)
	e := Endpoint{Addr: mr.Addr(), Channel: "taskgame.test.outbox", Timeout: 500 * time.Millisecond}
	a, b := NewRedisBus(e), NewRedisBus(e)
	defer a.Close()
	defer b.Close()
	ctx := context.Background()
	got := make(chan string, 4)
	stop, err := b.Subscribe(ctx, func(id string) { got <- id })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if err := a.Publish(ctx, "msg_42"); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-got:
		if id != "msg_42" {
			t.Fatalf("got %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("other process did not receive the outbox hint")
	}
}

func TestRedisBusDefaultChannelAndClose(t *testing.T) {
	mr := miniredis.RunT(t)
	b := NewRedisBus(Endpoint{Addr: mr.Addr(), Timeout: 500 * time.Millisecond})
	if b.Channel() != DefaultChannel {
		t.Fatalf("channel = %q", b.Channel())
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), func(string) {}); err == nil {
		t.Fatal("subscribe after close must fail")
	}
}

type fakeProjector struct {
	calls atomic.Int64
	err   error
}

func (f *fakeProjector) Recover(context.Context) error {
	f.calls.Add(1)
	return f.err
}

func TestWorkerWakesOnBusEvent(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()
	p := &fakeProjector{}
	w := NewWorker(p, bus, time.Hour) // long ticker: only the event triggers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	// The subscription is set up inside Run, so retry the hint until it lands.
	eventually(t, 2*time.Second, func() bool {
		_ = bus.Publish(context.Background(), "msg_1")
		return w.Runs() >= 1
	})
	if p.calls.Load() < 1 {
		t.Fatal("worker did not call Recover")
	}
	cancel()
}

func TestWorkerPeriodicFallbackWithoutBus(t *testing.T) {
	p := &fakeProjector{}
	w := NewWorker(p, nil, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	eventually(t, 2*time.Second, func() bool { return w.Runs() >= 2 })
	if p.calls.Load() < 2 {
		t.Fatalf("periodic scans did not run: %d", p.calls.Load())
	}
}

func TestWorkerCountsProjectionErrors(t *testing.T) {
	bus := NewMemoryBus()
	defer bus.Close()
	p := &fakeProjector{err: context.DeadlineExceeded}
	w := NewWorker(p, bus, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	w.Wake()
	eventually(t, 2*time.Second, func() bool { return w.Errors() >= 1 })
}
