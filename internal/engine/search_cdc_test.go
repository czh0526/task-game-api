package engine

import (
	"context"
	"testing"

	"taskgame/internal/search"
	"taskgame/internal/types"
)

// recordingSink is a TaskChangeRecorder that keeps the docs it was given.
type recordingSink struct {
	docs []search.TaskDoc
}

func (s *recordingSink) RecordTask(_ context.Context, doc search.TaskDoc) error {
	s.docs = append(s.docs, doc)
	return nil
}

func TestTaskWriteRecordsCDC(t *testing.T) {
	sink := &recordingSink{}
	e, _, clk := newEngineWith(t, 1_000_000, WithTaskChangeRecorder(sink))
	ctx := context.Background()

	task, err := e.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.docs) != 1 {
		t.Fatalf("recorded docs: %d, want 1", len(sink.docs))
	}
	doc := sink.docs[0]
	if doc.ID != task.Id || doc.Title != task.Title || doc.Status != task.Status {
		t.Fatalf("recorded doc mismatch: %+v vs task %+v", doc, task)
	}
	if doc.CreatedAt == "" {
		t.Fatal("recorded doc has no created_at")
	}

	// A state transition (fund) records another upsert with the new status.
	_, err = e.FundTask(ctx, "usr_publisher", &types.FundTaskReq{
		TaskId: task.Id, IdempotencyKey: "fund-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.docs) != 2 {
		t.Fatalf("recorded docs after fund: %d, want 2", len(sink.docs))
	}
	if sink.docs[1].Status != "open" {
		t.Fatalf("fund doc status: %q, want open", sink.docs[1].Status)
	}
}

func TestTaskWriteWithoutRecorderIsNoop(t *testing.T) {
	e, _, clk := newEngineWith(t, 1_000_000)
	if _, err := e.CreateTask(context.Background(), "usr_publisher", exclusiveReq(clk)); err != nil {
		t.Fatal(err)
	}
	// No panic / no error when no recorder is attached (legacy path).
}
