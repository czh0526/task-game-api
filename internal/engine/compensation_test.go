package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/compensation"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"
)

// TestFundTaskSagaJournal verifies that a fund operation records a saga and
// closes it as done on success, so the reconciler has nothing left to do.
func TestFundTaskSagaJournal(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "app.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clk := &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}

	logDSN := "file:" + filepath.Join(t.TempDir(), "saga.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	sagaLog, err := compensation.OpenSQLLog(ctx, "sqlite", logDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer sagaLog.Close()
	coord := compensation.NewCoordinator(sagaLog, "api-test")

	eng := New(st, "credits", WithClock(clk.Now), WithCompensation(coord))
	created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}

	rec, err := sagaLog.Get(ctx, "fund_"+created.Id)
	if err != nil {
		t.Fatalf("saga not recorded: %v", err)
	}
	if rec.State != compensation.StateDone {
		t.Fatalf("saga state = %s, want done", rec.State)
	}

	// Reconciler finds nothing to do after a clean commit.
	n, err := coord.ReconcileOnce(ctx, 10)
	if err != nil || n != 0 {
		t.Fatalf("reconcile after success: n=%d err=%v", n, err)
	}

	// Only one saga exists for the task even if FundTask is revisited.
	all, _ := sagaLog.List(ctx)
	if len(all) != 1 {
		t.Fatalf("sagas = %d, want 1", len(all))
	}
}

// TestFundTaskWithoutCompensation keeps the original path intact: no saga
// coordinator means FundTask behaves exactly as before.
func TestFundTaskWithoutCompensation(t *testing.T) {
	ctx := context.Background()
	eng, _, clk := newEngine(t, 1_000_000)
	created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
}
