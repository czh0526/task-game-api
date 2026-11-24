package plane_test

import (
	"context"
	"path/filepath"
	"testing"

	"taskgame/internal/plane"
)

func TestSQLDirectoryReassignBatchIsCASAndAtomic(t *testing.T) {
	ctx := context.Background()
	topo := plane.NewTopology()
	dsns := []string{"shard-0.db", "shard-1.db"}
	if err := topo.Register(plane.DomainMessage, dsns); err != nil {
		t.Fatal(err)
	}
	dir, err := plane.OpenSQLDirectory(ctx, "sqlite", filepath.Join(t.TempDir(), "control.db"), topo)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	for _, key := range []string{"chat_a", "chat_a#key", "chat_b"} {
		if err := dir.Assign(ctx, plane.DomainMessage, key, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := dir.ReassignBatch(ctx, plane.DomainMessage, 0, 1, []string{"chat_a", "chat_a#key"}); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	for key, want := range map[string]int{"chat_a": 1, "chat_a#key": 1, "chat_b": 0} {
		got, ok, err := dir.Lookup(ctx, plane.DomainMessage, key)
		if err != nil || !ok || got != want {
			t.Fatalf("lookup %s = %d/%v/%v, want %d", key, got, ok, err, want)
		}
	}

	// A stale retry contains one already-moved key; neither row may change.
	if err := dir.ReassignBatch(ctx, plane.DomainMessage, 0, 1, []string{"chat_b", "chat_a"}); err == nil {
		t.Fatal("reassign should reject an already-moved key")
	}
	got, _, _ := dir.Lookup(ctx, plane.DomainMessage, "chat_b")
	if got != 0 {
		t.Fatalf("failed batch moved chat_b to %d", got)
	}

	if err := dir.SyncTopology(ctx, plane.DomainMessage, 1, "new-dsn.db"); err != nil {
		t.Fatal(err)
	}
	var dsn string
	if err := dir.DB().QueryRow(`SELECT dsn FROM shard_topology WHERE domain = 'message' AND shard = 1`).Scan(&dsn); err != nil {
		t.Fatal(err)
	}
	if dsn != "new-dsn.db" {
		t.Fatalf("topology DSN = %q, want new-dsn.db", dsn)
	}
	n, err := dir.CountKeys(ctx, plane.DomainMessage)
	if err != nil || n != 3 {
		t.Fatalf("CountKeys = %d/%v, want 3", n, err)
	}
}
