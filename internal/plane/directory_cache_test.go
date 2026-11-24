package plane

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func dirDSN(t *testing.T, name string) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), name) + "?mode=rwc&_pragma=busy_timeout(5000)"
}

// TestSQLDirectoryCacheAndInvalidation verifies the read-through cache and
// that Assign invalidates the local entry immediately.
func TestSQLDirectoryCacheAndInvalidation(t *testing.T) {
	ctx := context.Background()
	dsn := dirDSN(t, "dir.db")
	d, err := OpenSQLDirectory(ctx, "sqlite", dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.cacheTTL = time.Hour // long TTL so only explicit invalidation changes a cached value

	if _, ok, _ := d.Lookup(ctx, DomainUser, "usr_1"); ok {
		t.Fatal("unexpected hit before assign")
	}
	if err := d.Assign(ctx, DomainUser, "usr_1", 2); err != nil {
		t.Fatal(err)
	}
	// Assign invalidated the cached miss, so this sees the row now.
	if shard, ok, _ := d.Lookup(ctx, DomainUser, "usr_1"); !ok || shard != 2 {
		t.Fatalf("after assign: shard=%d ok=%v", shard, ok)
	}

	// An external change is NOT visible while the cached value is fresh.
	if _, err := d.DB().ExecContext(ctx,
		`UPDATE shard_directory SET shard = 1 WHERE domain='user' AND shard_key='usr_1'`); err != nil {
		t.Fatal(err)
	}
	if shard, _, _ := d.Lookup(ctx, DomainUser, "usr_1"); shard != 2 {
		t.Fatalf("cache did not shield external change: shard=%d", shard)
	}
	// After invalidation the new value shows up.
	d.invalidateLookup(DomainUser, "usr_1")
	if shard, _, _ := d.Lookup(ctx, DomainUser, "usr_1"); shard != 1 {
		t.Fatalf("after invalidate: shard=%d", shard)
	}
}

// TestSQLDirectoryReplicaReadAndFallback verifies reads go to the replica,
// and a replica outage falls back to the primary instead of failing.
func TestSQLDirectoryReplicaReadAndFallback(t *testing.T) {
	ctx := context.Background()
	primaryDSN := dirDSN(t, "primary.db")
	replicaDSN := dirDSN(t, "replica.db")
	d, err := OpenSQLDirectoryWithReplica(ctx, "sqlite", primaryDSN, replicaDSN, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.cacheTTL = 0 // disable cache for this test

	// Seed only the replica: Lookup must read from it.
	replica, err := OpenSQLDirectory(ctx, "sqlite", replicaDSN, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	if err := replica.Assign(ctx, DomainTask, "tsk_1", 1); err != nil {
		t.Fatal(err)
	}
	if shard, ok, err := d.Lookup(ctx, DomainTask, "tsk_1"); err != nil || !ok || shard != 1 {
		t.Fatalf("replica read: shard=%d ok=%v err=%v", shard, ok, err)
	}

	// Kill the replica and confirm fallback to the primary (which has a
	// different value for the same key) keeps reads working.
	_ = replica.Close()
	if err := d.readDB.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Assign(ctx, DomainTask, "tsk_1", 2); err != nil { // primary write
		t.Fatal(err)
	}
	if shard, ok, err := d.Lookup(ctx, DomainTask, "tsk_1"); err != nil || !ok || shard != 2 {
		t.Fatalf("primary fallback: shard=%d ok=%v err=%v", shard, ok, err)
	}
}

// TestSQLDirectoryCacheConcurrent is a race-detector smoke test.
func TestSQLDirectoryCacheConcurrent(t *testing.T) {
	ctx := context.Background()
	d, err := OpenSQLDirectory(ctx, "sqlite", dirDSN(t, "dir.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var wg sync.WaitGroup
	var n atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("usr_%d", i)
			if err := d.Assign(ctx, DomainUser, key, i%3); err == nil {
				n.Add(1)
			}
			_, _, _ = d.Lookup(ctx, DomainUser, key)
		}(i)
	}
	wg.Wait()
	if n.Load() != 20 {
		t.Fatalf("assigns = %d", n.Load())
	}
}
