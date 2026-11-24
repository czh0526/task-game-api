package archive

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "migrate.db")+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ddl := `
	CREATE TABLE users (id TEXT PRIMARY KEY, avatar BLOB, avatar_ref TEXT NOT NULL DEFAULT '');
	CREATE TABLE submissions (id TEXT PRIMARY KEY, payload TEXT NOT NULL, payload_ref TEXT NOT NULL DEFAULT '');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestExternalizeAvatars(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)
	blobs, err := NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	jpeg := bytes.Repeat([]byte{0xff, 0xd8, 0x01}, 100)
	if _, err := db.Exec(`INSERT INTO users (id, avatar) VALUES ('u1', ?), ('u2', ?)`, jpeg, jpeg); err != nil {
		t.Fatal(err)
	}

	res, err := ExternalizeAvatars(ctx, db, blobs, 10)
	// Avatars are keyed by user id, so two users with identical bytes still
	// produce two independent blobs (unlike deduplicated webhook bodies).
	if err != nil || res.Moved != 2 || res.Blobs != 2 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	var ref string
	var inlineLen int
	if err := db.QueryRow(`SELECT avatar_ref, length(COALESCE(avatar, X'')) FROM users WHERE id='u1'`).Scan(&ref, &inlineLen); err != nil {
		t.Fatal(err)
	}
	if ref == "" || inlineLen != 0 {
		t.Fatalf("expected ref set and inline cleared, got ref=%q len=%d", ref, inlineLen)
	}
	data, err := blobs.Get(ctx, ref)
	if err != nil || !bytes.Equal(data, jpeg) {
		t.Fatalf("blob content mismatch: %v", err)
	}
	// Idempotent: a second pass must move nothing.
	res, err = ExternalizeAvatars(ctx, db, blobs, 10)
	if err != nil || res.Moved != 0 {
		t.Fatalf("second pass should be a no-op: %+v %v", res, err)
	}
}

func TestExternalizeSubmissionPayloads(t *testing.T) {
	ctx := context.Background()
	db := openMigrationDB(t)
	blobs, err := NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	big := string(bytes.Repeat([]byte("z"), LargePayloadThreshold+100))
	small := `{"step":1}`
	if _, err := db.Exec(`INSERT INTO submissions (id, payload) VALUES ('big', ?), ('small', ?)`, big, small); err != nil {
		t.Fatal(err)
	}

	res, err := ExternalizeSubmissionPayloads(ctx, db, blobs, 10, LargePayloadThreshold)
	if err != nil || res.Moved != 1 {
		t.Fatalf("first pass: %+v %v", res, err)
	}
	var ref, inline string
	if err := db.QueryRow(`SELECT payload_ref, payload FROM submissions WHERE id='big'`).Scan(&ref, &inline); err != nil {
		t.Fatal(err)
	}
	if ref == "" || inline != "{}" {
		t.Fatalf("expected ref set and inline '{}', got ref=%q inline=%q", ref, inline)
	}
	data, err := blobs.Get(ctx, ref)
	if err != nil || string(data) != big {
		t.Fatalf("blob content mismatch: %v", err)
	}
	// The small payload must be untouched.
	var smallRef string
	if err := db.QueryRow(`SELECT payload_ref FROM submissions WHERE id='small'`).Scan(&smallRef); err != nil {
		t.Fatal(err)
	}
	if smallRef != "" {
		t.Fatalf("small payload should stay inline, got ref %q", smallRef)
	}
	res, err = ExternalizeSubmissionPayloads(ctx, db, blobs, 10, LargePayloadThreshold)
	if err != nil || res.Moved != 0 {
		t.Fatalf("second pass should be a no-op: %+v %v", res, err)
	}
}
