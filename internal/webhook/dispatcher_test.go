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

	"taskgame/internal/engine"
	"taskgame/internal/model"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/types"
)

func TestDeliverSignsBody(t *testing.T) {
	var (
		mu   sync.Mutex
		got  []byte
		sig  string
		evID string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = body
		sig = r.Header.Get(HeaderSignature)
		evID = r.Header.Get(HeaderEventID)
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
	t.Cleanup(func() { _ = st.Close() })
	eng := engine.New(st, "credits", engine.WithClock(func() time.Time { return clk }))
	ctx := context.Background()
	secret := "webhook-secret-16"
	if _, err := eng.CreateWebhook(ctx, "usr_publisher", &types.CreateWebhookReq{
		Url: srv.URL, Secret: secret, Events: []string{model.EventTaskFunded, model.EventEscrowHold},
	}); err != nil {
		t.Fatal(err)
	}
	created, err := eng.CreateTask(ctx, "usr_publisher", &types.CreateTaskReq{
		Kind: model.KindExclusive, Title: "Ship the notes", Description: "Write the release notes.",
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
	d := NewDispatcher(st, Config{Mode: "post", Timeout: 2 * time.Second, MaxAttempts: 3})
	if err := d.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if evID == "" || sig == "" || sig != Sign(secret, got) {
		t.Fatalf("signature mismatch sig=%s id=%s body=%s", sig, evID, got)
	}
	if err := d.DeliverPending(ctx); err != nil {
		t.Fatal(err)
	}
}
