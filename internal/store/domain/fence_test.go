package domain_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/plane"
	"taskgame/internal/store"
	"taskgame/internal/store/domain"
)

// TestFenceBlocksWrites verifies the maintenance write guard: when a key is
// frozen, a write transaction targeting it fails with ErrFenced before any
// business row is written.
func TestFenceBlocksWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "d.db")
	fence, err := plane.OpenSQLFence(ctx, "sqlite", "file:"+dsn+"?mode=rwc&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()

	byDomain := map[plane.Domain][]string{
		plane.DomainUser:    {dsn},
		plane.DomainTask:    {dsn},
		plane.DomainMessage: {dsn},
	}
	st, err := domain.Open(domain.Config{
		Driver: "sqlite", DSNs: byDomain, InitialBalance: 1000, Fence: fence,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	key := "usr_fenced"
	// Freeze the key.
	if err := fence.Lock(ctx, plane.DomainUser, key, "migration", "op", 3600_000_000_000); err != nil {
		t.Fatal(err)
	}
	route := domain.WithRoute(ctx, plane.DomainUser, key, true)
	err = st.InTx(route, func(ctx context.Context, r store.Repos) error {
		_, err := r.Users().Ensure(ctx, key, time.Now())
		return err
	})
	if !errors.Is(err, plane.ErrFenced) {
		t.Fatalf("want ErrFenced, got %v", err)
	}

	// After unlock the write succeeds.
	if err := fence.Unlock(ctx, plane.DomainUser, key); err != nil {
		t.Fatal(err)
	}
	err = st.InTx(route, func(ctx context.Context, r store.Repos) error {
		_, err := r.Users().Ensure(ctx, key, time.Now())
		return err
	})
	if err != nil {
		t.Fatalf("write after unlock: %v", err)
	}
}
