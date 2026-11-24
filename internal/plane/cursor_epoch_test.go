package plane

import (
	"context"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestParseCursorCompatibility(t *testing.T) {
	// Legacy bare-offset cursors still decode as epoch 0.
	legacy := base64.RawURLEncoding.EncodeToString([]byte("12"))
	e, off, err := ParseCursor(legacy)
	if err != nil || e != 0 || off != 12 {
		t.Fatalf("legacy cursor: e=%d off=%d err=%v", e, off, err)
	}
	// Empty cursor starts at 0.
	e, off, err = ParseCursor("")
	if err != nil || e != 0 || off != 0 {
		t.Fatalf("empty cursor: e=%d off=%d err=%v", e, off, err)
	}
	// v2 cursors carry an epoch.
	v2 := base64.RawURLEncoding.EncodeToString([]byte("v2|7|30"))
	e, off, err = ParseCursor(v2)
	if err != nil || e != 7 || off != 30 {
		t.Fatalf("v2 cursor: e=%d off=%d err=%v", e, off, err)
	}
	if _, _, err := ParseCursor("not-base64!"); err == nil {
		t.Fatal("bad cursor accepted")
	}
}

func TestSQLCursorEpochBumpAndReopen(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "epoch.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSQLCursorEpoch(ctx, db, "sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Epoch(ctx, "cht_1"); e != 0 {
		t.Fatalf("initial epoch: %d", e)
	}
	for i := int64(1); i <= 3; i++ {
		got, err := s.Bump(ctx, "cht_1")
		if err != nil || got != i {
			t.Fatalf("bump: got=%d want=%d err=%v", got, i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db2, _ := sql.Open("sqlite", dsn)
	s2, err := NewSQLCursorEpoch(ctx, db2, "sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if e, _ := s2.Epoch(ctx, "cht_1"); e != 3 {
		t.Fatalf("epoch after reopen: %d", e)
	}
	if e, _ := s2.Epoch(ctx, "cht_other"); e != 0 {
		t.Fatalf("other chat epoch: %d", e)
	}
}

func TestMemoryCursorEpoch(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryCursorEpoch()
	if e, _ := m.Epoch(ctx, "c"); e != 0 {
		t.Fatal("initial")
	}
	if e, _ := m.Bump(ctx, "c"); e != 1 {
		t.Fatalf("bump: %d", e)
	}
}
