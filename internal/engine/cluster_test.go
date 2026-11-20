package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/mysql"
	"taskgame/internal/types"
)

func TestSweepProcessesMoreThanFifty(t *testing.T) {
	cfg := TimeoutConfig{OpenTimeout: time.Hour, StartTimeout: time.Hour, SeatTimeout: time.Hour, WinnerSelectHours: 1}
	t.Run("sqlite", func(t *testing.T) {
		eng, st, clk := newEngineWith(t, 1_000_000, WithTimeouts(cfg))
		assertSweepAll(t, eng, st, clk)
	})
	t.Run("mysql", func(t *testing.T) {
		st := mysql.OpenIsolated(t, 1_000_000)
		clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
		eng := New(st, "credits", WithClock(clk.Now), WithTimeouts(cfg))
		assertSweepAll(t, eng, st, clk)
	})
}

func assertSweepAll(t *testing.T, eng *Engine, st store.Store, clk *clock) {
	t.Helper()
	ctx := context.Background()
	const n = 51
	for i := 0; i < n; i++ {
		created, err := eng.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
			t.Fatal(err)
		}
	}
	clk.t = clk.t.Add(2 * time.Hour)
	got, err := eng.AutoSettleDue(ctx)
	if err != nil || got != n {
		t.Fatalf("sweep n=%d err=%v", got, err)
	}
	pub, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil || pub.Balance != 1_000_000 {
		t.Fatalf("balance %+v %v", pub, err)
	}
	open, err := st.Tasks().Count(ctx, model.TaskFilter{Statuses: []string{model.StatusOpen, model.StatusActive, model.StatusInReview}})
	if err != nil || open != 0 {
		t.Fatalf("still open %d %v", open, err)
	}
}

func TestConcurrentAutoSettle(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		eng, st, clk := newEngine(t, 1_000_000)
		eng2 := New(st, "credits", WithClock(clk.Now))
		assertOneAutoSettle(t, eng, eng2, st, clk)
	})
	t.Run("mysql", func(t *testing.T) {
		st := mysql.OpenIsolated(t, 1_000_000)
		clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
		eng := New(st, "credits", WithClock(clk.Now))
		eng2 := New(st, "credits", WithClock(clk.Now))
		assertOneAutoSettle(t, eng, eng2, st, clk)
	})
}

func assertOneAutoSettle(t *testing.T, eng, eng2 *Engine, st store.Store, clk *clock) {
	t.Helper()
	ctx := context.Background()
	req := exclusiveReq(clk)
	req.Acceptance.ReviewHours = 1
	created, err := eng.CreateTask(ctx, "usr_publisher", req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_executor", &types.AcceptTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SubmitTask(ctx, "usr_executor", &types.SubmitTaskReq{TaskId: created.Id, Payload: map[string]any{"v": 1}}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	runBoth(t, func() error { _, err := eng.AutoSettleDue(ctx); return err }, func() error { _, err := eng2.AutoSettleDue(ctx); return err })
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Status != model.StatusSettled || got.Escrow == nil || got.Escrow.Held != 0 {
		t.Fatalf("task %+v %v", got, err)
	}
	releases := 0
	for _, entry := range got.Escrow.Entries {
		if entry.Type == model.EscrowRelease {
			releases++
			if entry.Amount != 500 {
				t.Fatalf("release %+v", entry)
			}
		}
	}
	if releases != 1 {
		t.Fatalf("releases %d", releases)
	}
	exec, err := st.Users().Get(ctx, "usr_executor")
	if err != nil || exec.Balance != 1_000_500 || exec.Ability != 10 {
		t.Fatalf("executor %+v %v", exec, err)
	}
}

func TestConcurrentSeatTimeout(t *testing.T) {
	cfg := TimeoutConfig{OpenTimeout: time.Hour, StartTimeout: time.Hour, SeatTimeout: time.Hour, WinnerSelectHours: 1}
	t.Run("sqlite", func(t *testing.T) {
		eng, st, clk := newEngineWith(t, 1_000_000, WithTimeouts(cfg))
		eng2 := New(st, "credits", WithClock(clk.Now), WithTimeouts(cfg))
		assertOneSeatRefund(t, eng, eng2, st, clk)
	})
	t.Run("mysql", func(t *testing.T) {
		st := mysql.OpenIsolated(t, 1_000_000)
		clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
		eng := New(st, "credits", WithClock(clk.Now), WithTimeouts(cfg))
		eng2 := New(st, "credits", WithClock(clk.Now), WithTimeouts(cfg))
		assertOneSeatRefund(t, eng, eng2, st, clk)
	})
}

func assertOneSeatRefund(t *testing.T, eng, eng2 *Engine, st store.Store, clk *clock) {
	t.Helper()
	ctx := context.Background()
	created, err := eng.CreateTask(ctx, "usr_publisher", perSeatReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptTask(ctx, "usr_design", &types.AcceptTaskReq{TaskId: created.Id, RoleId: "design"}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(2 * time.Hour)
	runBoth(t, func() error { _, err := eng.AutoSettleDue(ctx); return err }, func() error { _, err := eng2.AutoSettleDue(ctx); return err })
	got, err := eng.GetTask(ctx, created.Id)
	if err != nil || got.Escrow == nil || got.Escrow.Held != 300 {
		t.Fatalf("held %+v %v", got, err)
	}
	refunds := 0
	for _, entry := range got.Escrow.Entries {
		if entry.Type == model.EscrowRefund {
			refunds++
			if entry.Amount != 200 {
				t.Fatalf("refund %+v", entry)
			}
		}
	}
	if refunds != 1 {
		t.Fatalf("refunds %d entries %+v", refunds, got.Escrow.Entries)
	}
	pub, err := st.Users().Get(ctx, "usr_publisher")
	if err != nil || pub.Balance != 999_700 {
		t.Fatalf("publisher %+v %v", pub, err)
	}
	for _, role := range got.Collaborative.Roles {
		if role.Id == "copy" && role.Status != model.RoleRefunded {
			t.Fatalf("copy %+v", role)
		}
		if role.Id == "design" && role.Status != model.RoleFilled {
			t.Fatalf("design %+v", role)
		}
	}
}

func TestConcurrentIdempotentCreate(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		eng, st, clk := newEngine(t, 1_000_000)
		eng2 := New(st, "credits", WithClock(clk.Now))
		assertOneIdempotentCreate(t, eng, eng2, st, clk)
	})
	t.Run("mysql", func(t *testing.T) {
		st := mysql.OpenIsolated(t, 1_000_000)
		clk := &clock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
		eng := New(st, "credits", WithClock(clk.Now))
		eng2 := New(st, "credits", WithClock(clk.Now))
		assertOneIdempotentCreate(t, eng, eng2, st, clk)
	})
}

func assertOneIdempotentCreate(t *testing.T, eng, eng2 *Engine, st store.Store, clk *clock) {
	t.Helper()
	ctx := context.Background()
	req := exclusiveReq(clk)
	req.IdempotencyKey = "same-create"
	var (
		wg   sync.WaitGroup
		ids  [2]string
		errs [2]error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		task, err := eng.CreateTask(ctx, "usr_publisher", req)
		errs[0] = err
		if task != nil {
			ids[0] = task.Id
		}
	}()
	go func() {
		defer wg.Done()
		task, err := eng2.CreateTask(ctx, "usr_publisher", req)
		errs[1] = err
		if task != nil {
			ids[1] = task.Id
		}
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errs %v %v", errs[0], errs[1])
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("ids %q %q", ids[0], ids[1])
	}
	n, err := st.Tasks().Count(ctx, model.TaskFilter{})
	if err != nil || n != 1 {
		t.Fatalf("tasks %d %v", n, err)
	}
}

func runBoth(t *testing.T, a, b func() error) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = a()
	}()
	go func() {
		defer wg.Done()
		errs[1] = b()
	}()
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("workers %v %v", errs[0], errs[1])
	}
}
