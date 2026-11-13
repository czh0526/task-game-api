package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

func becomeFriends(t *testing.T, eng *Engine, from, to string) {
	t.Helper()
	ctx := context.Background()
	req, err := eng.CreateFriendRequest(ctx, from, &types.CreateFriendRequestReq{UserId: to})
	if err != nil {
		t.Fatal(err)
	}
	if req.Status == model.FriendAccepted {
		return
	}
	if _, err := eng.AcceptFriendRequest(ctx, to, &types.FriendRequestActionReq{RequestId: req.Id}); err != nil {
		t.Fatal(err)
	}
}

func TestDirectChat(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000_000)
	ensureUsers(t, st, clk, "usr_a", "usr_b", "usr_c")

	pending, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b"}); codeOf(err) != "forbidden" {
		t.Fatalf("pending request is not a friend: %v", err)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: pending.Id}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_a"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("self: %v", err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_c"}); codeOf(err) != "forbidden" {
		t.Fatalf("non-friend: %v", err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "bad id"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b", MemberIds: []string{"usr_b"}}); codeOf(err) != "invalid_argument" {
		t.Fatalf("member_ids on direct: %v", err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: "room", UserId: "usr_b"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("type: %v", err)
	}

	hook, err := eng.CreateWebhook(ctx, "usr_b", &types.CreateWebhookReq{
		Url: "https://example.com/hooks", Secret: "webhook-secret-16", Events: []string{model.EventChatCreated, model.EventChatMessageCreated},
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b", IdempotencyKey: "dc-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != model.ChatDirect || first.TaskId != nil || len(first.MemberIds) != 2 {
		t.Fatalf("created %+v", first)
	}
	if first.MemberIds[0] != "usr_a" || first.MemberIds[1] != "usr_b" {
		t.Fatalf("pair order %+v", first.MemberIds)
	}
	replay, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b", IdempotencyKey: "dc-1"})
	if err != nil || replay.Id != first.Id {
		t.Fatalf("replay %+v %v", replay, err)
	}
	again, err := eng.CreateChat(ctx, "usr_b", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_a"})
	if err != nil || again.Id != first.Id {
		t.Fatalf("second call %+v %v", again, err)
	}
	stored, err := st.Chats().FindDirect(ctx, "usr_b", "usr_a")
	if err != nil || stored.ID != first.Id || stored.PairLow != "usr_a" || stored.PairHigh != "usr_b" {
		t.Fatalf("stored %+v %v", stored, err)
	}
	if n := countEvent(t, st, hook.Id, model.EventChatCreated); n != 1 {
		t.Fatalf("chat.created count %d", n)
	}

	msg, err := eng.PostMessage(ctx, "usr_a", &types.PostMessageReq{ChatId: first.Id, Type: "text", Text: "你好"})
	if err != nil || msg.ChatId != first.Id {
		t.Fatalf("message %+v %v", msg, err)
	}
	page, err := eng.ListMessages(ctx, "usr_b", &types.ListMessagesReq{ChatId: first.Id})
	if err != nil || len(page.Items) != 1 || page.Items[0].Text != "你好" {
		t.Fatalf("history %+v %v", page, err)
	}
	if _, err := eng.GetChat(ctx, "usr_c", first.Id); codeOf(err) != "forbidden" {
		t.Fatalf("stranger read %v", err)
	}
	if n := countEvent(t, st, hook.Id, model.EventChatMessageCreated); n != 1 {
		t.Fatalf("chat.message.created count %d", n)
	}

	group, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{MemberIds: []string{"usr_b"}})
	if err != nil || group.Type != model.ChatGroup || group.Id == first.Id {
		t.Fatalf("group %+v %v", group, err)
	}
	group2, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{MemberIds: []string{"usr_b"}})
	if err != nil || group2.Id == group.Id {
		t.Fatalf("group chats are not deduped %+v %v", group2, err)
	}

	task, err := eng.CreateTask(ctx, "usr_a", exclusiveReq(clk))
	if err != nil {
		t.Fatal(err)
	}
	funded, err := eng.FundTask(ctx, "usr_a", &types.FundTaskReq{TaskId: task.Id})
	if err != nil || funded.ChatId == nil {
		t.Fatalf("fund %+v %v", funded, err)
	}
	taskChat, err := eng.GetChat(ctx, "usr_a", *funded.ChatId)
	if err != nil || taskChat.Type != model.ChatTask || taskChat.TaskId == nil || *taskChat.TaskId != task.Id {
		t.Fatalf("task chat %+v %v", taskChat, err)
	}
	if taskChat.Id == first.Id {
		t.Fatal("task chat reused the direct chat")
	}
	listed, err := eng.ListChats(ctx, "usr_a", task.Id)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Type != model.ChatTask {
		t.Fatalf("list %+v %v", listed, err)
	}
	if _, err := eng.PostMessage(ctx, "usr_a", &types.PostMessageReq{ChatId: taskChat.Id, Type: "text", Text: "task"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Unfriend(ctx, "usr_a", &types.UnfriendReq{UserId: "usr_b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b"}); codeOf(err) != "forbidden" {
		t.Fatalf("unfriended create: %v", err)
	}
	still, err := eng.GetChat(ctx, "usr_b", first.Id)
	if err != nil || still.Id != first.Id {
		t.Fatalf("history after unfriend %+v %v", still, err)
	}
	page, err = eng.ListMessages(ctx, "usr_a", &types.ListMessagesReq{ChatId: first.Id})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("messages after unfriend %+v %v", page, err)
	}
	if _, err := eng.PostMessage(ctx, "usr_b", &types.PostMessageReq{ChatId: first.Id, Type: "text", Text: "还在"}); err != nil {
		t.Fatalf("send after unfriend: %v", err)
	}

	becomeFriends(t, eng, "usr_b", "usr_a")
	resumed, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b"})
	if err != nil || resumed.Id != first.Id {
		t.Fatalf("resume %+v %v", resumed, err)
	}
	if n := countEvent(t, st, hook.Id, model.EventChatCreated); n != 1 {
		t.Fatalf("resume emitted another chat.created: %d", n)
	}
}

func countEvent(t *testing.T, st store.Store, webhookID, eventType string) int {
	t.Helper()
	return len(eventsOf(t, st, webhookID, eventType))
}

func eventsOf(t *testing.T, st store.Store, webhookID, eventType string) []model.Delivery {
	t.Helper()
	rows, err := st.Deliveries().ListPending(context.Background(), 5, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Delivery
	for _, row := range rows {
		if row.WebhookID == webhookID && row.EventType == eventType {
			out = append(out, row)
		}
	}
	return out
}

func TestDirectChatWebhookBody(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000)
	ensureUsers(t, st, clk, "usr_a", "usr_b")
	becomeFriends(t, eng, "usr_a", "usr_b")
	hook, err := eng.CreateWebhook(ctx, "usr_a", &types.CreateWebhookReq{
		Url: "https://example.com/hooks", Secret: "webhook-secret-16", Events: []string{model.EventChatCreated},
	})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := eng.CreateChat(ctx, "usr_a", &types.CreateChatReq{Type: model.ChatDirect, UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	rows := eventsOf(t, st, hook.Id, model.EventChatCreated)
	if len(rows) != 1 {
		t.Fatalf("events %d", len(rows))
	}
	if strings.Contains(rows[0].Body, "email") || strings.Contains(rows[0].Body, "password") {
		t.Fatalf("webhook leaked a secret: %s", rows[0].Body)
	}
	var env struct {
		Type string `json:"type"`
		Data struct {
			Chat struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(rows[0].Body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != model.EventChatCreated || env.Data.Chat.ID != chat.Id || env.Data.Chat.Type != model.ChatDirect {
		t.Fatalf("payload %+v", env)
	}
}
