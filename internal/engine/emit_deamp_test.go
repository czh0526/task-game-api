package engine

import (
	"context"
	"sync"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/types"
)

type countingBodyStore struct {
	mu    sync.Mutex
	puts  map[string][]byte
	store map[string][]byte
	nGet  int
}

func newCountingBodyStore() *countingBodyStore {
	return &countingBodyStore{puts: make(map[string][]byte), store: make(map[string][]byte)}
}

func (c *countingBodyStore) Put(_ context.Context, key string, data []byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := "sha256:test" + string(rune('a'+len(c.puts)))
	c.puts[key] = data
	c.store[ref] = append([]byte{}, data...)
	return ref, nil
}

func (c *countingBodyStore) Get(_ context.Context, ref string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nGet++
	return c.store[ref], nil
}

// TestEmitDeamplifiesBodies proves one event with N subscribers stores ONE body
// blob and N delivery rows that carry only a reference.
func TestEmitDeamplifiesBodies(t *testing.T) {
	ctx := context.Background()
	bodies := newCountingBodyStore()
	e, st, clk := newEngineWith(t, 1_000_000, WithEventBodyStore(bodies))

	// Two webhooks for the request recipient, both subscribed to the same
	// event: one emit fans out to 2 deliveries but must store ONE body blob.
	ensureUsers(t, st, clk, "usr_a")
	ensureUsers(t, st, clk, "usr_b")
	for i := 0; i < 2; i++ {
		if _, err := e.CreateWebhook(ctx, "usr_b", &types.CreateWebhookReq{
			Url: "https://example.com/hook", Secret: "webhook-secret-16",
			Events: []string{model.EventFriendRequested},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"}); err != nil {
		t.Fatal(err)
	}

	rows, err := st.Deliveries().ListPending(ctx, 5, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("deliveries: %d, want 2", len(rows))
	}
	refs := make(map[string]int)
	for _, r := range rows {
		if r.Body != "" {
			t.Fatalf("delivery %s still has inline body: %q", r.ID, r.Body)
		}
		if r.BodyRef == "" {
			t.Fatalf("delivery %s has no body ref", r.ID)
		}
		refs[r.BodyRef]++
	}
	if len(refs) != 1 {
		t.Fatalf("distinct body refs: %d, want 1 (one body for the event)", len(refs))
	}
	if len(bodies.puts) != 1 {
		t.Fatalf("body blobs stored: %d, want 1", len(bodies.puts))
	}
}

func TestEmitWithoutBodyStoreKeepsInline(t *testing.T) {
	ctx := context.Background()
	e, st, clk := newEngineWith(t, 1_000_000)
	ensureUsers(t, st, clk, "usr_a")
	ensureUsers(t, st, clk, "usr_b")
	if _, err := e.CreateWebhook(ctx, "usr_b", &types.CreateWebhookReq{
		Url: "https://example.com/hook", Secret: "webhook-secret-16",
		Events: []string{model.EventFriendRequested},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"}); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Deliveries().ListPending(ctx, 5, 50)
	if len(rows) == 0 || rows[0].Body == "" || rows[0].BodyRef != "" {
		t.Fatalf("legacy inline delivery: %+v", rows)
	}
}
