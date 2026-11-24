package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/archive"
	"taskgame/internal/model"
)

// openRaw opens a second connection to the same file so the test can inspect
// inline columns without adding a production accessor to sqlstore.DB.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func queryScalar(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
		t.Fatalf("scalar %q: %v", query, err)
	}
	return s
}

func execRaw(ctx context.Context, t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestLargeValuesExternalized proves avatar JPEGs and large submission
// payloads round-trip through the blob tier, the inline columns are cleared,
// and reads still work after reopening (the ref, not process memory, is the
// source of truth).
func TestLargeValuesExternalized(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	blobs, err := archive.NewFileBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "large.db")
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLargeValues(blobs)
	t.Cleanup(func() { _ = st.Close })
	ctx := context.Background()
	raw := openRaw(t, path)

	u, err := st.Users().Ensure(ctx, "usr_av", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	jpeg := bytes.Repeat([]byte{0xff, 0xd8, 0x02}, 200)
	if err := st.Users().SetAvatar(ctx, u.ID, jpeg, "v1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().GetAvatar(ctx, u.ID)
	if err != nil || !bytes.Equal(got.Data, jpeg) {
		t.Fatalf("externalized avatar: %+v %v", got, err)
	}
	if ref := queryScalar(ctx, t, raw, `SELECT avatar_ref FROM users WHERE id = ?`, u.ID); ref == "" {
		t.Fatal("avatar_ref was not written")
	}
	if n := queryScalar(ctx, t, raw, `SELECT length(COALESCE(avatar, X'')) FROM users WHERE id = ?`, u.ID); n != "0" {
		t.Fatalf("inline avatar should be cleared, length=%s", n)
	}

	// Large payload is externalized; the inline column keeps valid JSON '{}'.
	big := map[string]any{"evidence": string(bytes.Repeat([]byte("x"), 8<<10))}
	payload, _ := json.Marshal(big)
	if err := st.Submissions().Insert(ctx, model.Submission{
		ID: "sub_big", TaskID: "tsk_1", ExecutorID: "usr_av", Payload: payload, Status: model.SubmissionCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	subs, err := st.Submissions().ListByTask(ctx, "tsk_1")
	if err != nil || len(subs) != 1 {
		t.Fatalf("list submissions: %+v %v", subs, err)
	}
	if !bytes.Equal([]byte(subs[0].Payload), payload) {
		t.Fatal("large payload did not round-trip through blob tier")
	}
	if ref := queryScalar(ctx, t, raw, `SELECT payload_ref FROM submissions WHERE id = ?`, "sub_big"); ref == "" {
		t.Fatal("payload_ref was not written")
	}
	if inline := queryScalar(ctx, t, raw, `SELECT payload FROM submissions WHERE id = ?`, "sub_big"); inline != "{}" {
		t.Fatalf("inline payload should be '{}', got %q", inline)
	}

	// Reopen with a fresh DB handle sharing the SAME blob dir: reads must
	// resolve the refs from storage, not from the old process's memory.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	reopened.SetLargeValues(blobs)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err = reopened.Users().GetAvatar(ctx, u.ID)
	if err != nil || !bytes.Equal(got.Data, jpeg) {
		t.Fatalf("avatar lost after reopen: %+v %v", got, err)
	}
	subs, err = reopened.Submissions().ListByTask(ctx, "tsk_1")
	if err != nil || len(subs) != 1 || !bytes.Equal([]byte(subs[0].Payload), payload) {
		t.Fatalf("payload lost after reopen: %+v %v", subs, err)
	}
}

// TestLargeValuesLegacyFallback proves rows with an empty ref still read their
// inline data, so enabling the blob tier never breaks pre-existing rows.
func TestLargeValuesLegacyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	blobs, err := archive.NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLargeValues(blobs)
	t.Cleanup(func() { _ = st.Close })
	ctx := context.Background()
	raw := openRaw(t, path)

	u, err := st.Users().Ensure(ctx, "usr_legacy", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	inline := bytes.Repeat([]byte{0xff, 0xd8, 0x03}, 10)
	// Simulate a legacy row by writing inline bytes with no ref via raw SQL
	// (SetAvatar now always uses the blob tier when configured).
	execRaw(ctx, t, raw, `UPDATE users SET avatar = ?, avatar_ref = '', avatar_updated_at = 'legacy' WHERE id = ?`, inline, u.ID)
	got, err := st.Users().GetAvatar(ctx, u.ID)
	if err != nil || !bytes.Equal(got.Data, inline) {
		t.Fatalf("legacy inline avatar fallback: %+v %v", got, err)
	}

	small := []byte(`{"step":1}`)
	execRaw(ctx, t, raw, `INSERT INTO submissions (id, task_id, executor_id, payload, payload_ref, status, created_at)
		VALUES ('sub_legacy','tsk_1','usr_legacy',?,'','current','2026-11-20T00:00:00.000000000Z')`, string(small))
	subs, err := st.Submissions().ListByTask(ctx, "tsk_1")
	if err != nil || len(subs) != 1 || !bytes.Equal([]byte(subs[0].Payload), small) {
		t.Fatalf("legacy inline payload fallback: %+v %v", subs, err)
	}
}

// TestSmallPayloadStaysInline proves payloads under the threshold are not
// sent to the blob tier at all (no read amplification for tiny JSON).
func TestSmallPayloadStaysInline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.db")
	blobs, err := archive.NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLargeValues(blobs)
	t.Cleanup(func() { _ = st.Close })
	ctx := context.Background()
	raw := openRaw(t, path)

	small := []byte(`{"step":1,"body":"short"}`)
	if err := st.Submissions().Insert(ctx, model.Submission{
		ID: "sub_small", TaskID: "tsk_1", ExecutorID: "usr_x", Payload: small, Status: model.SubmissionCurrent,
	}); err != nil {
		t.Fatal(err)
	}
	if ref := queryScalar(ctx, t, raw, `SELECT payload_ref FROM submissions WHERE id = ?`, "sub_small"); ref != "" {
		t.Fatalf("small payload should stay inline, got ref %q", ref)
	}
}
