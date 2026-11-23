package plane

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testFence(t *testing.T) *SQLFence {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "fence.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	f, err := OpenSQLFence(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestSQLFenceLockUnlockAndWildcard(t *testing.T) {
	ctx := context.Background()
	f := testFence(t)

	if fenced, _ := f.IsFenced(ctx, DomainUser, "usr_1"); fenced {
		t.Fatal("key fenced before lock")
	}
	if err := f.Lock(ctx, DomainUser, "usr_1", "moving to DB4", "op-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if fenced, err := f.IsFenced(ctx, DomainUser, "usr_1"); err != nil || !fenced {
		t.Fatalf("after lock: fenced=%v err=%v", fenced, err)
	}
	// A different key and a different domain are unaffected.
	if fenced, _ := f.IsFenced(ctx, DomainUser, "usr_2"); fenced {
		t.Fatal("other key fenced")
	}
	if fenced, _ := f.IsFenced(ctx, DomainTask, "usr_1"); fenced {
		t.Fatal("other domain fenced")
	}

	// Domain-wide fence.
	if err := f.Lock(ctx, DomainMessage, "*", "full domain cutover", "op-2", time.Hour); err != nil {
		t.Fatal(err)
	}
	if fenced, _ := f.IsFenced(ctx, DomainMessage, "cht_any"); !fenced {
		t.Fatal("wildcard fence did not cover a key")
	}

	if err := f.Unlock(ctx, DomainUser, "usr_1"); err != nil {
		t.Fatal(err)
	}
	if fenced, _ := f.IsFenced(ctx, DomainUser, "usr_1"); fenced {
		t.Fatal("key still fenced after unlock")
	}

	rows, err := f.List(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %d rows err=%v", len(rows), err)
	}
	if rows[0].Key != "*" {
		t.Fatalf("remaining fence = %q", rows[0].Key)
	}
}

func TestSQLFenceExpiry(t *testing.T) {
	ctx := context.Background()
	f := testFence(t)
	// Use a fake clock so we can advance past expiry deterministically.
	now := time.Now()
	f.now = func() time.Time { return now }
	if err := f.Lock(ctx, DomainTask, "tsk_1", "short", "op", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if fenced, _ := f.IsFenced(ctx, DomainTask, "tsk_1"); !fenced {
		t.Fatal("not fenced before expiry")
	}
	now = now.Add(11 * time.Second)
	// The cache uses the same clock, so advancing it also expires the cache.
	if fenced, _ := f.IsFenced(ctx, DomainTask, "tsk_1"); fenced {
		t.Fatal("expired fence still active")
	}
}
