package engine

import (
	"context"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/search"
	"taskgame/internal/types"
)

func TestListTasksViaIndex(t *testing.T) {
	ctx := context.Background()
	idx := search.NewMemoryTaskIndex()
	log := search.NewMemoryChangeLog()
	recorder := search.NewLogRecorder(log)

	e, _, clk := newEngineWith(t, 1_000_000,
		WithTaskIndex(idx), WithTaskChangeRecorder(recorder))

	task, err := e.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	// Drain the CDC stream into the index.
	cdc, err := search.NewCDCProjector(log, idx, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := cdc.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	// A simple listing is answered from the index and returns the full DTO
	// (re-read by primary key), including fields not stored in the index doc.
	list, err := e.ListTasks(ctx, "usr_reader", &types.ListTasksReq{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Id != task.Id {
		t.Fatalf("indexed list: %+v", list.Items)
	}
	if list.Total != 1 {
		t.Fatalf("indexed total: %d", list.Total)
	}
	if list.Items[0].Description != task.Description {
		t.Fatalf("DTO not re-read from store: %+v", list.Items[0])
	}

	// A keyword that misses the index returns empty (still from the index).
	list, err = e.ListTasks(ctx, "usr_reader", &types.ListTasksReq{Q: "nonexistent-xyz", Limit: 20})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("keyword miss: %+v err=%v", list, err)
	}

	// A complex filter (amount range) is not index-expressible and falls back
	// to the store; the task still shows up because its amount is in range.
	list, err = e.ListTasks(ctx, "usr_reader", &types.ListTasksReq{MinAmount: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Id != task.Id {
		t.Fatalf("fallback list: %+v", list.Items)
	}
}

func TestIndexQueryExpressibility(t *testing.T) {
	if _, ok := indexQuery(model.TaskFilter{Sort: "amount_desc"}); ok {
		t.Fatal("non-recent sort must not be index-expressible")
	}
	if _, ok := indexQuery(model.TaskFilter{MinAmount: 5}); ok {
		t.Fatal("amount filter must not be index-expressible")
	}
	lat := 1.0
	if _, ok := indexQuery(model.TaskFilter{Latitude: &lat}); ok {
		t.Fatal("distance filter must not be index-expressible")
	}
	if q, ok := indexQuery(model.TaskFilter{Status: "open", Keyword: "piano"}); !ok ||
		q.Keyword != "piano" || len(q.Statuses) != 1 || q.Statuses[0] != "open" {
		t.Fatalf("simple filter mapping: %+v ok=%v", q, ok)
	}
}
