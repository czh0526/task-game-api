package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"

	_ "modernc.org/sqlite"
)

func openArchiveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "db.sqlite") + "?mode=rwc&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE score_events (
		id TEXT PRIMARY KEY, user_id TEXT, delta INTEGER, reason TEXT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestArchiveExecuteVerifyDeleteAndRestore(t *testing.T) {
	ctx := context.Background()
	db := openArchiveDB(t)
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	old := now.Add(-200 * 24 * time.Hour)
	recent := now.Add(-10 * 24 * time.Hour)
	for _, r := range []struct {
		id, reason, created string
	}{
		{"evt_old1", "a", model.FormatTime(old)},
		{"evt_old2", "b", model.FormatTime(old.Add(time.Hour))},
		{"evt_hot", "c", model.FormatTime(recent)},
	} {
		if _, err := db.Exec(`INSERT INTO score_events (id,user_id,delta,reason,created_at) VALUES (?,?,?,?,?)`,
			r.id, "usr_x", 5, r.reason, r.created); err != nil {
			t.Fatal(err)
		}
	}

	blobs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewArchiver(blobs, []RetentionPolicy{
		{Table: "score_events", TimeColumn: "created_at", HotWindow: 90 * 24 * time.Hour},
	})
	batches, err := a.Plan(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.ExecuteBatch(ctx, db, "sqlite", batches[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 2 || res.Deleted != 2 || res.Ref == "" {
		t.Fatalf("result %+v", res)
	}
	// Only the two old rows are gone; the hot row remains.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM score_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("online rows = %d, want 1", count)
	}
	var hotID string
	if err := db.QueryRow(`SELECT id FROM score_events`).Scan(&hotID); err != nil || hotID != "evt_hot" {
		t.Fatalf("remaining row = %q err=%v", hotID, err)
	}

	// The blob is readable and restores the exact rows (idempotently).
	n, err := a.Restore(ctx, db, "sqlite", "score_events", res.Ref)
	if err != nil || n != 2 {
		t.Fatalf("restore: n=%d err=%v", n, err)
	}
	n, err = a.Restore(ctx, db, "sqlite", "score_events", res.Ref)
	if err != nil || n != 2 {
		t.Fatalf("idempotent restore: n=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM score_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("rows after restore = %d, want 3", count)
	}
}

// TestArchiveVerifyFailureKeepsRows proves a corrupted blob does not delete
// online rows: the checksum gate is the safety property of the whole design.
func TestArchiveVerifyFailureKeepsRows(t *testing.T) {
	ctx := context.Background()
	db := openArchiveDB(t)
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	old := now.Add(-200 * 24 * time.Hour)
	if _, err := db.Exec(`INSERT INTO score_events (id,user_id,delta,reason,created_at) VALUES (?,?,?,?,?)`,
		"evt_1", "usr_x", 5, "a", model.FormatTime(old)); err != nil {
		t.Fatal(err)
	}
	blobs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewArchiver(blobs, []RetentionPolicy{
		{Table: "score_events", TimeColumn: "created_at", HotWindow: 90 * 24 * time.Hour},
	})
	// A blob store that returns corrupt data on read must abort before delete.
	a.Blobs = &corruptReadStore{BlobStore: blobs}
	batches, _ := a.Plan(ctx, now)
	if _, err := a.ExecuteBatch(ctx, db, "sqlite", batches[0], 10); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM score_events`).Scan(&count)
	if count != 1 {
		t.Fatalf("online row deleted despite verify failure: %d", count)
	}
}

type corruptReadStore struct{ BlobStore }

func (c *corruptReadStore) Get(ctx context.Context, ref string) ([]byte, error) {
	return []byte("corrupted"), nil
}

func TestArchiveEmptyBatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	db := openArchiveDB(t)
	blobs, _ := NewFileBlobStore(t.TempDir())
	a := NewArchiver(blobs, []RetentionPolicy{
		{Table: "score_events", TimeColumn: "created_at", HotWindow: 90 * 24 * time.Hour},
	})
	batches, _ := a.Plan(ctx, time.Now())
	res, err := a.ExecuteBatch(ctx, db, "sqlite", batches[0], 10)
	if err != nil || res.Rows != 0 || res.Ref != "" {
		t.Fatalf("empty batch result %+v err=%v", res, err)
	}
}
