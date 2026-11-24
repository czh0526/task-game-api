package plane

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func uniqueDSN(t *testing.T) string {
	t.Helper()
	return "file:" + filepath.Join(t.TempDir(), "unique.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
}

func TestSQLUniqueReserveOwnerAndIdempotency(t *testing.T) {
	ctx := context.Background()
	u, err := OpenSQLUnique(ctx, "sqlite", uniqueDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	if err := u.Reserve(ctx, "email", "a@example.com", "usr_1"); err != nil {
		t.Fatal(err)
	}
	// Same owner replay is a no-op.
	if err := u.Reserve(ctx, "email", "a@example.com", "usr_1"); err != nil {
		t.Fatalf("idempotent reserve: %v", err)
	}
	// Different owner conflicts.
	if err := u.Reserve(ctx, "email", "a@example.com", "usr_2"); err != ErrConflict {
		t.Fatalf("conflict: got %v, want ErrConflict", err)
	}
	owner, ok, err := u.Owner(ctx, "email", "a@example.com")
	if err != nil || !ok || owner != "usr_1" {
		t.Fatalf("owner: got (%q,%v,%v)", owner, ok, err)
	}
	owner, ok, err = u.Owner(ctx, "email", "missing@example.com")
	if err != nil || ok || owner != "" {
		t.Fatalf("missing owner: got (%q,%v,%v)", owner, ok, err)
	}
	// Namespaces are independent.
	if err := u.Reserve(ctx, "phone", "a@example.com", "usr_2"); err != nil {
		t.Fatalf("other namespace: %v", err)
	}
	// Invalid input is rejected.
	if err := u.Reserve(ctx, "", "v", "o"); err == nil {
		t.Fatal("empty namespace accepted")
	}
	if err := u.Reserve(ctx, "n", "", "o"); err == nil {
		t.Fatal("empty value accepted")
	}
	if err := u.Reserve(ctx, "n", "v", ""); err == nil {
		t.Fatal("empty owner accepted")
	}
}

func TestSQLUniqueCacheSeesAnotherProcessRow(t *testing.T) {
	ctx := context.Background()
	dsn := uniqueDSN(t)
	a, err := OpenSQLUnique(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenSQLUnique(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// a reserves; b's cold cache must see it via a lookup.
	if err := a.Reserve(ctx, "email", "cross@example.com", "usr_9"); err != nil {
		t.Fatal(err)
	}
	owner, ok, err := b.Owner(ctx, "email", "cross@example.com")
	if err != nil || !ok || owner != "usr_9" {
		t.Fatalf("cross-process owner: got (%q,%v,%v)", owner, ok, err)
	}
	if err := b.Reserve(ctx, "email", "cross@example.com", "usr_other"); err != ErrConflict {
		t.Fatalf("cross-process conflict: got %v", err)
	}
	// Invalidate then re-read after a repair delete.
	if _, err := a.DB().ExecContext(ctx, `DELETE FROM global_unique WHERE namespace='email' AND value='cross@example.com'`); err != nil {
		t.Fatal(err)
	}
	b.Invalidate("email", "cross@example.com")
	if err := b.Reserve(ctx, "email", "cross@example.com", "usr_10"); err != nil {
		t.Fatalf("reserve after invalidate: %v", err)
	}
}

func TestSQLUniqueConcurrentReserveSingleWinner(t *testing.T) {
	ctx := context.Background()
	u, err := OpenSQLUnique(ctx, "sqlite", uniqueDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()

	const n = 24
	var wg sync.WaitGroup
	var winners atomic.Int64
	var conflicts atomic.Int64
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := u.Reserve(ctx, "email", "race@example.com", fmt.Sprintf("usr_%d", i))
			switch {
			case err == nil:
				winners.Add(1)
			case err == ErrConflict:
				conflicts.Add(1)
			default:
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if winners.Load() != 1 {
		t.Fatalf("winners = %d, want 1", winners.Load())
	}
	if conflicts.Load() != n-1 {
		t.Fatalf("conflicts = %d, want %d", conflicts.Load(), n-1)
	}
	owner, ok, _ := u.Owner(ctx, "email", "race@example.com")
	if !ok || owner == "" {
		t.Fatalf("no final owner: %q %v", owner, ok)
	}
}
