package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/mysql"
	"taskgame/internal/store/sqlite"
)

func TestConcurrentDeliverySQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	assertOneDelivery(t, st)
}

func TestConcurrentDeliveryMySQL(t *testing.T) {
	st := mysql.OpenIsolated(t, 1000)
	assertOneDelivery(t, st)
}

func TestClaimLeaseSQLite(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if err := st.Webhooks().Insert(ctx, model.Webhook{
		ID: "wh_1", OwnerID: "usr_publisher", URL: "http://127.0.0.1/hook", Secret: "secret-secret-secret",
		Events: []string{"task.funded"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Deliveries().Insert(ctx, model.Delivery{
		ID: "dlv_1", WebhookID: "wh_1", EventID: "evt_1", EventType: "task.funded",
		Body: "{}", Status: model.DeliveryPending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.Deliveries().ClaimPending(ctx, 3, 10, now, now.Add(time.Hour))
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	again, err := st.Deliveries().ClaimPending(ctx, 3, 10, now, now.Add(time.Hour))
	if err != nil || len(again) != 0 {
		t.Fatalf("double claim %+v %v", again, err)
	}
	later := now.Add(2 * time.Hour)
	reclaimed, err := st.Deliveries().ClaimPending(ctx, 3, 10, later, later.Add(time.Hour))
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
		t.Fatalf("reclaim %+v %v", reclaimed, err)
	}
}

func assertOneDelivery(t *testing.T, st store.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	if err := st.Webhooks().Insert(ctx, model.Webhook{
		ID: "wh_1", OwnerID: "usr_publisher", URL: srv.URL, Secret: "secret-secret-secret",
		Events: []string{"task.funded"}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Deliveries().Insert(ctx, model.Delivery{
		ID: "dlv_1", WebhookID: "wh_1", EventID: "evt_1", EventType: "task.funded",
		Body: `{"id":"evt_1"}`, Status: model.DeliveryPending, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		d := NewDispatcher(st, Config{Mode: "post", Timeout: 2 * time.Second, MaxAttempts: 3})
		go func() {
			defer wg.Done()
			<-start
			if err := d.DeliverPending(ctx); err != nil {
				t.Errorf("deliver: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("http deliveries %d", hits.Load())
	}
	if err := NewDispatcher(st, Config{Mode: "post", Timeout: 2 * time.Second, MaxAttempts: 3}).DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("redelivered, hits %d", hits.Load())
	}
}
