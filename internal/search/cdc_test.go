package search

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func sampleChange(id, status string) Change {
	return Change{Kind: "upsert", Doc: TaskDoc{
		ID: id, Title: "Task " + id, Status: status, CreatedAt: "2026-01-01T00:00:00Z",
	}}
}

func TestMemoryChangeLogIdempotentAndReplay(t *testing.T) {
	ctx := context.Background()
	log := NewMemoryChangeLog()
	if err := log.Append(ctx, sampleChange("tsk_1", "draft")); err != nil {
		t.Fatal(err)
	}
	// Same content again must not duplicate.
	_ = log.Append(ctx, sampleChange("tsk_1", "draft"))
	total, _, _ := log.Count(ctx)
	if total != 1 {
		t.Fatalf("total after duplicate append: %d", total)
	}

	idx := NewMemoryTaskIndex()
	p, err := NewCDCProjector(log, idx, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	docs, _ := idx.Query(ctx, Query{})
	if len(docs) != 1 {
		t.Fatalf("projected docs: %d", len(docs))
	}
	_, done, _ := log.Count(ctx)
	if done != 1 {
		t.Fatalf("projected flag: %d", done)
	}
	// Draining again is a no-op.
	if err := p.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// Replay resets flags; upserts are idempotent so no duplicates appear.
	n, _ := log.ReplayAll(ctx)
	if n != 1 {
		t.Fatalf("replay reset count: %d", n)
	}
	if err := p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	docs, _ = idx.Query(ctx, Query{})
	if len(docs) != 1 {
		t.Fatalf("docs after replay: %d", len(docs))
	}
}

func TestSQLChangeLogAcrossReopen(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "cdc.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	log, err := NewSQLChangeLog(ctx, db, "sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, sampleChange("tsk_2", "open")); err != nil {
		t.Fatal(err)
	}
	// Duplicate append is ignored by the PK.
	if err := log.Append(ctx, sampleChange("tsk_2", "open")); err != nil {
		t.Fatal(err)
	}
	entries, err := log.Pending(ctx, 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("pending: %+v err=%v", entries, err)
	}
	if err := log.MarkProjected(ctx, []string{entries[0].ID}); err != nil {
		t.Fatal(err)
	}
	entries, _ = log.Pending(ctx, 10)
	if len(entries) != 0 {
		t.Fatalf("pending after mark: %d", len(entries))
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the table and projected flag survive a process restart.
	db2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	log2, err := NewSQLChangeLog(ctx, db2, "sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	total, done, err := log2.Count(ctx)
	if err != nil || total != 1 || done != 1 {
		t.Fatalf("reopened count: total=%d done=%d err=%v", total, done, err)
	}
}

func TestCDCProjectorWithLease(t *testing.T) {
	ctx := context.Background()
	log := NewMemoryChangeLog()
	idx := NewMemoryTaskIndex()
	_ = log.Append(ctx, sampleChange("tsk_3", "open"))

	// A busy lease means this projector must skip; another owns the drain.
	busy := &stubLease{acquire: false}
	p, _ := NewCDCProjector(log, idx, busy, "a")
	if err := p.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	docs, _ := idx.Query(ctx, Query{})
	if len(docs) != 0 {
		t.Fatalf("projected while lease busy: %d", len(docs))
	}

	free := &stubLease{acquire: true}
	p2, _ := NewCDCProjector(log, idx, free, "b")
	if err := p2.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	docs, _ = idx.Query(ctx, Query{})
	if len(docs) != 1 {
		t.Fatalf("projected with free lease: %d", len(docs))
	}
	if free.released != 1 {
		t.Fatalf("lease releases: %d", free.released)
	}
}

type stubLease struct {
	acquire  bool
	released int
}

func (s *stubLease) Acquire(context.Context, string, string, time.Duration) (bool, error) {
	return s.acquire, nil
}
func (s *stubLease) Release(context.Context, string, string) error {
	s.released++
	return nil
}

func TestRebuildFromLister(t *testing.T) {
	ctx := context.Background()
	log := NewMemoryChangeLog()
	lister := stubLister{docs: []TaskDoc{
		{ID: "a", Status: "open", CreatedAt: "2026-01-01T00:00:01Z"},
		{ID: "b", Status: "open", CreatedAt: "2026-01-01T00:00:02Z"},
	}}
	n, err := Rebuild(ctx, lister, log)
	if err != nil || n != 2 {
		t.Fatalf("rebuild: n=%d err=%v", n, err)
	}
	total, _, _ := log.Count(ctx)
	if total != 2 {
		t.Fatalf("enqueued: %d", total)
	}
}

type stubLister struct{ docs []TaskDoc }

func (s stubLister) ListSweep(_ context.Context, afterCreated, afterID string, limit int) ([]TaskDoc, error) {
	if afterID == "" {
		if len(s.docs) == 0 {
			return nil, nil
		}
		return s.docs[:min(limit, len(s.docs))], nil
	}
	return nil, nil
}
