package compensation

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"
)

func testLog(t *testing.T) *SQLLog {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "saga.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	l, err := OpenSQLLog(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

type fakeHandler struct {
	forwardErr    error
	compensateErr error
	forwardN      atomic.Int64
	compensateN   atomic.Int64
	mu            sync.Mutex
}

func (f *fakeHandler) Forward(context.Context, Record) error {
	f.forwardN.Add(1)
	return f.forwardErr
}
func (f *fakeHandler) Compensate(context.Context, Record) error {
	f.compensateN.Add(1)
	return f.compensateErr
}

func TestCoordinatorExecuteDone(t *testing.T) {
	ctx := context.Background()
	l := testLog(t)
	h := &fakeHandler{}
	c := NewCoordinator(l, "api-1", WithHandler(KindFundHold, h))
	rec := Record{ID: "s1", Kind: KindFundHold, UserID: "usr_a", TaskID: "tsk_1"}
	if err := c.Execute(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := l.Get(ctx, "s1")
	if err != nil || got.State != StateDone {
		t.Fatalf("state: %+v err=%v", got, err)
	}
	// Replay is idempotent.
	if err := c.Execute(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if h.forwardN.Load() != 1 {
		t.Fatalf("forward replayed %d times", h.forwardN.Load())
	}
}

func TestCoordinatorExecuteCompensates(t *testing.T) {
	ctx := context.Background()
	l := testLog(t)
	h := &fakeHandler{forwardErr: errors.New("task shard down")}
	c := NewCoordinator(l, "api-1", WithHandler(KindFundHold, h))
	rec := Record{ID: "s2", Kind: KindFundHold}
	if err := c.Execute(ctx, rec); err == nil {
		t.Fatal("expected forward error")
	}
	got, _ := l.Get(ctx, "s2")
	if got.State != StateCompensated || h.compensateN.Load() != 1 {
		t.Fatalf("state=%s compensateN=%d", got.State, h.compensateN.Load())
	}
}

func TestCoordinatorReconcileLeaseAndRetry(t *testing.T) {
	ctx := context.Background()
	l := testLog(t)
	h := &fakeHandler{compensateErr: errors.New("refund failed")}
	c := NewCoordinator(l, "api-1", WithHandler(KindFundHold, h), WithMaxRetries(2))
	if err := c.Begin(ctx, Record{ID: "s3", Kind: KindFundHold, State: StateCompensating}); err != nil {
		t.Fatal(err)
	}
	n, err := c.ReconcileOnce(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("reconcile: n=%d err=%v", n, err)
	}
	got, _ := l.Get(ctx, "s3")
	if got.Attempts != 1 || got.State != StateCompensating {
		t.Fatalf("after retry: %+v", got)
	}
	// Second failure reaches max retries and leaves a visible failed record.
	if _, err := c.ReconcileOnce(ctx, 10); err != nil {
		t.Fatal(err)
	}
	got, _ = l.Get(ctx, "s3")
	if got.Attempts != 2 {
		t.Fatalf("attempts=%d", got.Attempts)
	}
	// Terminal states are never re-claimed.
	if err := l.MarkCompensated(ctx, "s3"); err != nil {
		t.Fatal(err)
	}
	n, _ = c.ReconcileOnce(ctx, 10)
	if n != 0 {
		t.Fatalf("reclaimed terminal saga: %d", n)
	}
}

func TestFundHoldHandlerForwardAndCompensate(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "app.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := NewFundHoldHandler(st)
	now := time.Now().UTC()
	rec := Record{
		ID: "fund-1", Kind: KindFundHold, UserID: "usr_pub", TaskID: "tsk_1",
		Payload: MarshalPayload(FundPayload{
			UserID: "usr_pub", TaskID: "tsk_1", Amount: 500, EscrowID: "esc_1", EntryID: "ent_1",
			Currency: "credits", CreatedAtUTC: now.Format(time.RFC3339Nano),
		}),
	}
	if err := h.Forward(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// Replay is a no-op: balance deducted once, one escrow account.
	if err := h.Forward(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		u, err := r.Users().Get(ctx, "usr_pub")
		if err != nil || u.Balance != 999_500 {
			t.Errorf("balance=%d err=%v", u.Balance, err)
		}
		acc, err := r.Escrow().GetByTask(ctx, "tsk_1")
		if err != nil || acc.Held != 500 {
			t.Errorf("escrow=%+v err=%v", acc, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Compensate restores the balance and zeroes the hold.
	if err := h.Compensate(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := h.Compensate(ctx, rec); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := st.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		u, _ := r.Users().Get(ctx, "usr_pub")
		if u.Balance != 1_000_000 {
			t.Errorf("refunded balance=%d", u.Balance)
		}
		acc, _ := r.Escrow().GetByTask(ctx, "tsk_1")
		if acc.Held != 0 {
			t.Errorf("hold not released: %d", acc.Held)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFundHoldHandlerCompensateNoEffect(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "app.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := NewFundHoldHandler(st)
	rec := Record{ID: "fund-x", Kind: KindFundHold, Payload: MarshalPayload(FundPayload{
		UserID: "usr_x", TaskID: "tsk_x", Amount: 100, EscrowID: "esc_x",
	})}
	if err := h.Compensate(ctx, rec); err != nil {
		t.Fatalf("compensate with no effect: %v", err)
	}
	var u model.User
	_ = u
}
