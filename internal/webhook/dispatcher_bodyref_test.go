package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"taskgame/internal/archive"
	"taskgame/internal/engine"
	"taskgame/internal/model"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"
)

// TestDispatcherResolvesBodyRef proves the de-amplification round trip: emit()
// stores one body blob and the dispatcher resolves it from the delivery's ref.
func TestDispatcherResolvesBodyRef(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	clk := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	blobs, err := archive.NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, "credits",
		engine.WithClock(func() time.Time { return clk }),
		engine.WithEventBodyStore(blobs))
	ctx := context.Background()
	secret := "webhook-secret-16"
	if _, err := eng.CreateWebhook(ctx, "usr_publisher", &types.CreateWebhookReq{
		Url: srv.URL, Secret: secret, Events: []string{model.EventTaskFunded},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", &types.CreateTaskReq{
		Kind: model.KindExclusive, Title: "Ship it", Description: "Write notes.",
		Payment:        types.Payment{Mode: model.PayOneShot, Amount: 100},
		Acceptance:     types.Acceptance{Deadline: clk.Add(24 * time.Hour).Format(time.RFC3339), Quality: "clear", ReviewHours: 72},
		AssigneePolicy: types.AssigneePolicy{Type: model.PolicyOpen},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Deliveries().ListPending(ctx, 5, 50)
	if len(rows) == 0 || rows[0].BodyRef == "" || rows[0].Body != "" {
		t.Fatalf("emit did not de-amplify: %+v", rows)
	}

	d := NewDispatcher(st, Config{Mode: "post", Timeout: 2 * time.Second, MaxAttempts: 3, BodyStore: blobs})
	if err := d.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(gotBody) == 0 || gotBody[0] != '{' {
		t.Fatalf("posted body not resolved from ref: %q", gotBody)
	}
	// The resolved body must be a valid signed envelope containing the event.
	if !contains(string(gotBody), "task.funded") {
		t.Fatalf("unexpected body: %s", gotBody)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
