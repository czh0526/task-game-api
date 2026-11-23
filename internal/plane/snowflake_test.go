package plane

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"taskgame/internal/id"
)

func TestSnowflakeWorkerAllocationAndRestart(t *testing.T) {
	ctx := context.Background()
	u := NewInMemoryUnique()

	g1, err := NewSnowflakeIDGenerator(ctx, u, "api-1")
	if err != nil {
		t.Fatal(err)
	}
	g2, err := NewSnowflakeIDGenerator(ctx, u, "api-2")
	if err != nil {
		t.Fatal(err)
	}
	g1again, err := NewSnowflakeIDGenerator(ctx, u, "api-1")
	if err != nil {
		t.Fatal(err)
	}
	if g1.WorkerID() == g2.WorkerID() {
		t.Fatalf("two live instances got the same worker id: %d", g1.WorkerID())
	}
	if g1.WorkerID() != g1again.WorkerID() {
		t.Fatalf("restart changed worker id: %d -> %d", g1.WorkerID(), g1again.WorkerID())
	}
}

func TestSnowflakeMonotonicUniqueAndShape(t *testing.T) {
	var clock time.Time
	var mu sync.Mutex
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	g := newSnowflake(7, SnowEpoch, now)

	// Same millisecond: sequence increments.
	clock = SnowEpoch.Add(100 * time.Millisecond)
	seen := make(map[string]bool)
	var prev uint64
	for i := 0; i < 10; i++ {
		v := g.New("usr_")
		if seen[v] {
			t.Fatalf("duplicate id %q", v)
		}
		seen[v] = true
		if !id.ValidUserID(v) {
			t.Fatalf("id fails shape check: %q", v)
		}
		n, err := strconv.ParseUint(v[len("usr_"):], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && n <= prev {
			t.Fatalf("not monotonic: %d then %d", prev, n)
		}
		prev = n
	}

	// Clock rollback: still monotonic, still unique.
	clock = clock.Add(-10 * time.Second)
	for i := 0; i < 5; i++ {
		v := g.New("tsk_")
		if seen[v] {
			t.Fatalf("duplicate after rollback %q", v)
		}
		seen[v] = true
		n, err := strconv.ParseUint(v[len("tsk_"):], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if n <= prev {
			t.Fatalf("rollback not monotonic: %d then %d", prev, n)
		}
		prev = n
	}

	// Different worker ids produce different ids at the same instant.
	a := newSnowflake(1, SnowEpoch, func() time.Time { return SnowEpoch.Add(time.Second) })
	b := newSnowflake(2, SnowEpoch, func() time.Time { return SnowEpoch.Add(time.Second) })
	if a.New("x") == b.New("x") {
		t.Fatal("different workers produced the same id")
	}
}

func TestSnowflakeConcurrentUnique(t *testing.T) {
	g := newSnowflake(3, SnowEpoch, time.Now)
	const n = 500
	var wg sync.WaitGroup
	out := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out <- g.New(fmt.Sprintf("usr_w%d_", i))
		}(i)
	}
	wg.Wait()
	close(out)
	seen := make(map[string]bool, n)
	for v := range out {
		if seen[v] {
			t.Fatalf("duplicate concurrent id %q", v)
		}
		seen[v] = true
	}
}
