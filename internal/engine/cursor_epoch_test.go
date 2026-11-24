package engine

import (
	"context"
	"testing"

	"taskgame/internal/types"
)

type fixedEpoch struct{ value int64 }

func (f *fixedEpoch) Epoch(context.Context, string) (int64, error) { return f.value, nil }

func TestListMessagesEpochInvalidatesStaleCursor(t *testing.T) {
	ctx := context.Background()
	epoch := &fixedEpoch{value: 2}
	e, _, clk := newEngineWith(t, 1_000_000, WithCursorEpoch(epoch))

	created, err := e.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	funded, err := e.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id})
	if err != nil || funded.ChatId == nil {
		t.Fatalf("fund: %+v err=%v", funded, err)
	}
	chatID := *funded.ChatId
	for _, txt := range []string{"one", "two", "three", "four"} {
		if _, err := e.PostMessage(ctx, "usr_publisher", &types.PostMessageReq{
			ChatId: chatID, Type: "text", Text: txt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Page 1 with a cursor minted under epoch 2.
	page1, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2})
	if err != nil || len(page1.Items) != 2 || page1.NextCursor == nil {
		t.Fatalf("page1: %+v err=%v", page1, err)
	}
	if page1.Items[0].Text != "one" {
		t.Fatalf("page1 start: %+v", page1.Items[0])
	}
	// Page 2 with the same epoch continues correctly.
	page2, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2, Cursor: *page1.NextCursor})
	if err != nil || len(page2.Items) != 2 || page2.Items[0].Text != "three" {
		t.Fatalf("page2: %+v err=%v", page2, err)
	}

	// Simulate a cutover: epoch bumps to 3. The old cursor is now stale and
	// must restart from the first page (no skipped / duplicated messages).
	epoch.value = 3
	stale, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2, Cursor: *page1.NextCursor})
	if err != nil || len(stale.Items) != 2 || stale.Items[0].Text != "one" {
		t.Fatalf("stale cursor after bump: %+v err=%v", stale, err)
	}
	// The refreshed cursor carries the new epoch, so the next page works.
	page2b, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2, Cursor: *stale.NextCursor})
	if err != nil || len(page2b.Items) != 2 || page2b.Items[0].Text != "three" {
		t.Fatalf("page2 after bump: %+v err=%v", page2b, err)
	}
}

func TestListMessagesWithoutEpochUnchanged(t *testing.T) {
	ctx := context.Background()
	e, _, clk := newEngineWith(t, 1_000_000)
	created, _ := e.CreateTask(ctx, "usr_publisher", exclusiveReq(clk))
	funded, _ := e.FundTask(ctx, "usr_publisher", &types.FundTaskReq{TaskId: created.Id})
	chatID := *funded.ChatId
	for _, txt := range []string{"a", "b", "c"} {
		_, _ = e.PostMessage(ctx, "usr_publisher", &types.PostMessageReq{ChatId: chatID, Type: "text", Text: txt})
	}
	p1, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2})
	if err != nil || len(p1.Items) != 2 {
		t.Fatalf("page1: %+v err=%v", p1, err)
	}
	p2, err := e.ListMessages(ctx, "usr_publisher", &types.ListMessagesReq{ChatId: chatID, Limit: 2, Cursor: *p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.Items[0].Text != "c" {
		t.Fatalf("page2: %+v err=%v", p2, err)
	}
}
