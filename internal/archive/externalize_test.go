package archive

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestExternalizeDeliveryBodiesDedupAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "d.db")+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE webhook_deliveries (
		id TEXT PRIMARY KEY, body TEXT NOT NULL, body_ref TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	// Two events: event-1 delivered to two subscribers with identical body,
	// event-2 to one subscriber with a different body.
	for _, r := range [][2]string{
		{"d1", "same-body"}, {"d2", "same-body"}, {"d3", "other-body"},
	} {
		if _, err := db.Exec(`INSERT INTO webhook_deliveries (id, body) VALUES (?,?)`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
	blobs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	res, err := ExternalizeDeliveryBodies(ctx, db, blobs, 100)
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 3 || res.Blobs != 2 {
		t.Fatalf("first pass: %+v", res)
	}
	// d1 and d2 share the same ref; d3 has a different one.
	var r1, r2, r3 string
	_ = db.QueryRow(`SELECT body_ref FROM webhook_deliveries WHERE id='d1'`).Scan(&r1)
	_ = db.QueryRow(`SELECT body_ref FROM webhook_deliveries WHERE id='d2'`).Scan(&r2)
	_ = db.QueryRow(`SELECT body_ref FROM webhook_deliveries WHERE id='d3'`).Scan(&r3)
	if r1 == "" || r1 != r2 || r3 == "" || r3 == r1 {
		t.Fatalf("refs: d1=%s d2=%s d3=%s", r1, r2, r3)
	}
	// Bodies are now empty online.
	var inline int
	_ = db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries WHERE body <> ''`).Scan(&inline)
	if inline != 0 {
		t.Fatalf("inline bodies left: %d", inline)
	}
	// A rerun is a no-op.
	res2, err := ExternalizeDeliveryBodies(ctx, db, blobs, 100)
	if err != nil || res2.Moved != 0 || res2.Blobs != 0 {
		t.Fatalf("second pass: %+v err=%v", res2, err)
	}
	// The blob content matches the original body.
	got, err := blobs.Get(ctx, r1)
	if err != nil || string(got) != "same-body" {
		t.Fatalf("blob content: %q err=%v", got, err)
	}
}
