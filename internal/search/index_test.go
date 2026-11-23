package search

import (
	"context"
	"testing"
)

func TestMemoryTaskIndexFiltersAndOrders(t *testing.T) {
	idx := NewMemoryTaskIndex()
	ctx := context.Background()
	docs := []TaskDoc{
		{ID: "tsk_1", Title: "写周报", Category: "文案", Keywords: []string{"写作"}, Status: "open", CreatedAt: "2026-11-20T10:00:00Z"},
		{ID: "tsk_2", Title: "修 bug", Category: "开发", Keywords: []string{"golang"}, Status: "open", CreatedAt: "2026-11-21T10:00:00Z"},
		{ID: "tsk_3", Title: "设计 logo", Category: "设计", Status: "draft", CreatedAt: "2026-11-22T10:00:00Z"},
	}
	for _, d := range docs {
		if err := idx.Upsert(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	got, err := idx.Query(ctx, Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "tsk_3" {
		t.Fatalf("newest-first order broken: %+v", got)
	}
	got, err = idx.Query(ctx, Query{Keyword: "golang", Statuses: []string{"open"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "tsk_2" {
		t.Fatalf("keyword/status filter broken: %+v", got)
	}
	got, err = idx.Query(ctx, Query{Categories: []string{"文案"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "tsk_1" {
		t.Fatalf("category filter broken: %+v", got)
	}
}

func TestProjectorApplyAndCursor(t *testing.T) {
	idx := NewMemoryTaskIndex()
	p, err := NewProjector(idx)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := p.Apply(ctx, Change{Kind: "upsert", Doc: TaskDoc{ID: "tsk_1", CreatedAt: "2026-11-20T10:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(ctx, Change{Kind: "upsert", Doc: TaskDoc{ID: "tsk_2", CreatedAt: "2026-11-21T10:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	got, err := idx.Query(ctx, Query{BeforeCursor: "2026-11-21T10:00:00Z", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "tsk_1" {
		t.Fatalf("cursor filter broken: %+v", got)
	}
	if err := p.Apply(ctx, Change{Kind: "delete", Doc: TaskDoc{ID: "tsk_1"}}); err != nil {
		t.Fatal(err)
	}
	got, err = idx.Query(ctx, Query{Limit: 10})
	if err != nil || len(got) != 1 {
		t.Fatalf("delete not projected: %+v, %v", got, err)
	}
	if err := p.Apply(ctx, Change{Kind: "bogus"}); err == nil {
		t.Fatal("unknown change kind accepted")
	}
}
