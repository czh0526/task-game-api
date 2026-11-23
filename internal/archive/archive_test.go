package archive

import (
	"context"
	"testing"
	"time"
)

func TestFileBlobStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	b, err := NewFileBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ref, err := b.Put(ctx, "avatar/usr_a", []byte("jpeg-bytes"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, ref)
	if err != nil || string(got) != "jpeg-bytes" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := b.Get(ctx, "sha256:../etc/passwd"); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if err := b.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, ref); err == nil {
		t.Fatal("blob still readable after delete")
	}
}

func TestArchiverPlanIsPureAndCoversPolicies(t *testing.T) {
	b, err := NewFileBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := NewArchiver(b, nil)
	now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	batches, err := a.Plan(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != len(DefaultPolicies) {
		t.Fatalf("got %d batches, want %d", len(batches), len(DefaultPolicies))
	}
	for i, batch := range batches {
		want := now.Add(-DefaultPolicies[i].HotWindow)
		if !batch.Before.Equal(want) {
			t.Fatalf("%s: before %v, want %v", batch.Table, batch.Before, want)
		}
		if batch.SelectSQL == "" || batch.DeleteSQL == "" || !batch.RequiresACK {
			t.Fatalf("incomplete batch %+v", batch)
		}
	}
}

func TestArchiverRejectsBadPolicy(t *testing.T) {
	a := NewArchiver(nil, []RetentionPolicy{{Table: "t"}})
	if _, err := a.Plan(context.Background(), time.Now()); err == nil {
		t.Fatal("invalid policy was accepted")
	}
}

func TestLargeColumnsCoverKnownBlobs(t *testing.T) {
	if len(LargeColumns) < 3 {
		t.Fatal("expected avatar, payload and delivery body policies")
	}
	for _, c := range LargeColumns {
		if c.Table == "" || c.Column == "" || c.RefColumn == "" || c.MaxBytes <= 0 {
			t.Fatalf("incomplete large column %+v", c)
		}
	}
}
