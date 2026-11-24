package archive

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"

	_ "modernc.org/sqlite"
)

// TestArchiveMessagesKeepsNewestPage proves that the messages policy preserves
// each chat's newest N rows online while archiving older ones.
func TestArchiveMessagesKeepsNewestPage(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "msgs.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messages (
		id TEXT PRIMARY KEY, chat_id TEXT, sender_id TEXT, type TEXT,
		text TEXT, attachment_ref TEXT, created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	veryOld := now.Add(-200 * 24 * time.Hour)
	// chat A: 3 cold rows + 2 hot rows; chat B: 2 cold rows only.
	type row struct{ id, chat, created string }
	rows := []row{
		{"m_a1", "cht_a", model.FormatTime(veryOld)},
		{"m_a2", "cht_a", model.FormatTime(veryOld.Add(time.Second))},
		{"m_a3", "cht_a", model.FormatTime(veryOld.Add(2 * time.Second))},
		{"m_a_hot1", "cht_a", model.FormatTime(now.Add(-time.Hour))},
		{"m_a_hot2", "cht_a", model.FormatTime(now.Add(-30 * time.Minute))},
		{"m_b1", "cht_b", model.FormatTime(veryOld.Add(3 * time.Second))},
		{"m_b2", "cht_b", model.FormatTime(veryOld.Add(4 * time.Second))},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO messages (id,chat_id,sender_id,type,text,attachment_ref,created_at)
			VALUES (?,?,?,?,?,?,?)`, r.id, r.chat, "usr_x", "text", r.id, "", r.created); err != nil {
			t.Fatal(err)
		}
	}

	blobs, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewArchiver(blobs, []RetentionPolicy{{
		Table: "messages", TimeColumn: "created_at", HotWindow: 180 * 24 * time.Hour,
		GroupColumn: "chat_id", KeepPerGroup: 2,
	}})
	batches, err := a.Plan(ctx, now)
	if err != nil || len(batches) != 1 {
		t.Fatalf("plan: %+v err=%v", batches, err)
	}
	res, err := a.ExecuteBatch(ctx, db, "sqlite", batches[0], 100)
	if err != nil {
		t.Fatal(err)
	}
	// Cold rows: cht_a has 3 old but only 2 newer siblings (the hot rows) —
	// with keep=2, a1 has 4 newer, a2 has 3, a3 has 2 -> all three qualify.
	// cht_b has 2 old with 1 newer sibling each -> neither has 2 newer, so
	// BOTH are protected as the newest page of that chat.
	if res.Rows != 3 || res.Deleted != 3 {
		t.Fatalf("archive result: %+v", res)
	}

	var remaining int
	_ = db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&remaining)
	if remaining != 4 {
		t.Fatalf("remaining messages: %d, want 4 (2 hot from A + 2 from B)", remaining)
	}
	for _, id := range []string{"m_a_hot1", "m_a_hot2", "m_b1", "m_b2"} {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ?`, id).Scan(&n)
		if n != 1 {
			t.Fatalf("expected %s to stay online", id)
		}
	}
	for i := 1; i <= 3; i++ {
		var n int
		_ = db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM messages WHERE id = 'm_a%d'`, i)).Scan(&n)
		if n != 0 {
			t.Fatalf("expected m_a%d to be archived", i)
		}
	}
}
