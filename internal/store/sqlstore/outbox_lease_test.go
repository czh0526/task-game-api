package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/projection"
	"taskgame/internal/store/shard"

	_ "modernc.org/sqlite"
)

// These are white-box tests: they replace DB.project with a counter so no real
// shard database is needed, while still exercising the real Recover SQL and
// the real lease claim path.

func newOutboxTestDB(t *testing.T) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`
CREATE TABLE message_outbox (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    shard INTEGER NOT NULL,
    sender_id TEXT NOT NULL,
    type TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    attachment_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    projected INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return raw
}

func insertPending(t *testing.T, raw *sql.DB, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := raw.Exec(`
INSERT INTO message_outbox
 (id, chat_id, shard, sender_id, type, text, attachment_ref, created_at, projected)
 VALUES (?, ?, 0, 'usr_a', 'text', 'hello', '', ?, 0)`,
			id, "cht_"+id, model.FormatTime(time.Now())); err != nil {
			t.Fatal(err)
		}
	}
}

type projectCalls struct {
	mu  sync.Mutex
	ids []string
}

func (c *projectCalls) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.ids)
}

// installProject replaces the shard insert with a counter that still marks the
// row projected, mirroring the real projectOne contract.
func installProject(d *DB, raw *sql.DB, calls *projectCalls, failOn string) {
	d.project = func(ctx context.Context, it pendingMessage) error {
		if it.msg.ID == failOn {
			return errors.New("simulated shard failure")
		}
		calls.mu.Lock()
		calls.ids = append(calls.ids, it.msg.ID)
		calls.mu.Unlock()
		_, err := raw.ExecContext(ctx, `UPDATE message_outbox SET projected = 1 WHERE id = ?`, it.msg.ID)
		return err
	}
}

func countProjected(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM message_outbox WHERE projected = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRecoverWithoutCoordinatorProjectsEveryRowOnce(t *testing.T) {
	raw := newOutboxTestDB(t)
	insertPending(t, raw, "m1", "m2", "m3")
	d := &DB{db: raw, shards: &shard.Set{}}
	calls := &projectCalls{}
	installProject(d, raw, calls, "")
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.count() != 3 || countProjected(t, raw) != 3 {
		t.Fatalf("first Recover: calls=%d projected=%d", calls.count(), countProjected(t, raw))
	}
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.count() != 3 {
		t.Fatalf("second Recover repeated work: %d", calls.count())
	}
}

// Two API processes share one primary database and one lease. Each holds its
// own pending snapshot, but every row must be projected exactly once.
func TestRecoverSharedLeaseAvoidsDuplicateWork(t *testing.T) {
	raw := newOutboxTestDB(t)
	insertPending(t, raw, "m1", "m2", "m3", "m4", "m5")
	lease := projection.NewMemoryLease()
	defer lease.Close()
	calls := &projectCalls{}

	newView := func(owner string) *DB {
		d := &DB{
			db: raw, shards: &shard.Set{},
			outboxOwner: owner, outboxLease: lease, outboxTTL: time.Minute,
		}
		installProject(d, raw, calls, "")
		return d
	}
	a, b := newView("api-a"), newView("api-b")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = a.Recover(context.Background()) }()
	go func() { defer wg.Done(); _ = b.Recover(context.Background()) }()
	wg.Wait()
	if calls.count() != 5 {
		t.Fatalf("duplicate projection across processes: %d calls for 5 rows", calls.count())
	}
	if countProjected(t, raw) != 5 {
		t.Fatalf("not all rows projected: %d", countProjected(t, raw))
	}
}

func TestRecoverSkipsRowsHeldByAnotherProcess(t *testing.T) {
	raw := newOutboxTestDB(t)
	insertPending(t, raw, "m1", "m2")
	lease := projection.NewMemoryLease()
	defer lease.Close()
	if ok, _ := lease.Acquire(context.Background(), "outbox:m1", "api-other", time.Minute); !ok {
		t.Fatal("setup: could not acquire lease")
	}
	d := &DB{db: raw, shards: &shard.Set{}, outboxOwner: "api-a", outboxLease: lease, outboxTTL: time.Minute}
	calls := &projectCalls{}
	installProject(d, raw, calls, "")
	if err := d.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.count() != 1 || countProjected(t, raw) != 1 {
		t.Fatalf("held row should be skipped: calls=%d projected=%d", calls.count(), countProjected(t, raw))
	}
}

// A failed projection releases the lease so another process can retry, while
// the row stays pending for the next scan.
func TestRecoverReleasesLeaseOnFailure(t *testing.T) {
	raw := newOutboxTestDB(t)
	insertPending(t, raw, "m1")
	lease := projection.NewMemoryLease()
	defer lease.Close()
	d := &DB{db: raw, shards: &shard.Set{}, outboxOwner: "api-a", outboxLease: lease, outboxTTL: time.Minute}
	calls := &projectCalls{}
	installProject(d, raw, calls, "m1")
	if err := d.Recover(context.Background()); err == nil {
		t.Fatal("expected the simulated shard failure to surface")
	}
	if ok, _ := lease.Acquire(context.Background(), "outbox:m1", "api-b", time.Minute); !ok {
		t.Fatal("failed projection must free the lease for a retry")
	}
	if countProjected(t, raw) != 0 {
		t.Fatal("failed row must stay pending")
	}
}
