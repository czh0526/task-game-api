package shard

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

func openTestSet(t *testing.T, n int) *Set {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "shard-%d.db")
	s, err := Open(NewRouter(n), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSetInsertListSeqAndIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openTestSet(t, 2)
	now := time.Now().UTC()
	m1 := model.Message{ID: "msg_1", ChatID: "cht_a", SenderID: "usr_a", Type: "text", Text: "one", CreatedAt: now}
	m2 := model.Message{ID: "msg_2", ChatID: "cht_a", SenderID: "usr_b", Type: "sticker", Text: "two", CreatedAt: now}
	for _, m := range []model.Message{m1, m2, m1} {
		if err := s.Insert(ctx, 0, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.List(ctx, 0, "cht_a", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("idempotent insert should leave 2 rows, got %d", len(got))
	}
	if got[0].ID != "msg_1" || got[1].ID != "msg_2" {
		t.Fatalf("wrong chat order: %s, %s", got[0].ID, got[1].ID)
	}
	if got[1].Text != "two" || got[1].Type != "sticker" {
		t.Fatal("message fields were not round-tripped")
	}
	seq, err := s.MessageSeq(ctx, 0, "cht_a", "msg_2")
	if err != nil || seq <= 0 {
		t.Fatalf("MessageSeq = %d, %v", seq, err)
	}
	if _, err := s.MessageSeq(ctx, 0, "cht_a", "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing message should be ErrNotFound, got %v", err)
	}
	latest, messageID, ok, err := s.Latest(ctx, 0, "cht_a")
	if err != nil || !ok || messageID != "msg_2" || latest != seq {
		t.Fatalf("Latest = %d, %s, %v, %v", latest, messageID, ok, err)
	}
	other, err := s.List(ctx, 1, "cht_a", 50, 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("shard 1 must not contain shard 0 messages: %d, %v", len(other), err)
	}
	n, err := s.CountUnread(ctx, 0, "cht_a", "usr_z", 0)
	if err != nil || n != 2 {
		t.Fatalf("unread from cursor 0 = %d, %v", n, err)
	}
	n, err = s.CountUnread(ctx, 0, "cht_a", "usr_z", seq)
	if err != nil || n != 0 {
		t.Fatalf("unread from latest cursor = %d, %v", n, err)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	if _, err := Open(NewRouter(1), "file:/tmp/x-%d.db"); err == nil {
		t.Fatal("single shard should be rejected")
	}
	if _, err := Open(NewRouter(2), "file:/tmp/x.db"); err == nil {
		t.Fatal("DSN without a shard index placeholder should be rejected")
	}
}
