package domain_test

import (
	"context"
	"testing"
	"time"

	"taskgame/internal/account"
	"taskgame/internal/engine"
	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store/domain"
	"taskgame/internal/types"

	"golang.org/x/crypto/bcrypt"
)

// TestCoLocatedEngineFlows proves stage 2 can carry the real API traffic while
// all three domains still point at the same three databases: registration,
// task creation, direct chat and message posting all stay inside one physical
// store, so no behaviour changes even though routing now goes through the
// control-plane directory.
func TestCoLocatedEngineFlows(t *testing.T) {
	st, _ := openDomain(t, nil)
	ctx := context.Background()
	accounts := account.New(st, "test-jwt-secret-0123456789", 24*time.Hour, account.WithBcryptCost(bcrypt.MinCost))
	eng := engine.New(st, "credits")

	a, err := accounts.Register(ctx, "domain-a@example.com", "password-123", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := accounts.Register(ctx, "domain-b@example.com", "password-123", nil)
	if err != nil {
		t.Fatal(err)
	}

	taskCtx := withRequest(ctx, "POST", "/v1/tasks", a.UserID)
	task, err := eng.CreateTask(taskCtx, a.UserID, &types.CreateTaskReq{
		Kind:        model.KindExclusive,
		Title:       "Domain mode task",
		Description: "created through the per-domain store",
		Payment:     types.Payment{Mode: model.PayOneShot, Amount: 100},
		Acceptance: types.Acceptance{
			Deadline:    time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339),
			Quality:     "done",
			ReviewHours: 72,
		},
		AssigneePolicy: types.AssigneePolicy{Type: model.PolicyOpen},
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if got, err := eng.GetTask(withRequest(ctx, "GET", "/v1/tasks/"+task.Id, a.UserID), task.Id); err != nil || got.Id != task.Id {
		t.Fatalf("read task back: %+v err=%v", got, err)
	}

	chatCtx := withRequest(ctx, "POST", "/v1/chats", a.UserID)
	chat, err := eng.CreateChat(chatCtx, a.UserID, &types.CreateChatReq{Type: model.ChatDirect, UserId: b.UserID})
	if err != nil {
		t.Fatalf("create direct chat: %v", err)
	}
	msgCtx := withRequest(ctx, "POST", "/v1/chats/"+chat.Id+"/messages", a.UserID)
	posted, err := eng.PostMessage(msgCtx, a.UserID, &types.PostMessageReq{ChatId: chat.Id, Type: "text", Text: "hello from domain mode"})
	if err != nil {
		t.Fatalf("post message: %v", err)
	}
	msgs, err := st.Chats().ListMessages(withRequest(ctx, "GET", "/v1/chats/"+chat.Id+"/messages", b.UserID), chat.Id, 10, 0)
	if err != nil || len(msgs) != 1 || msgs[0].ID != posted.Id {
		t.Fatalf("peer read messages: %d rows err=%v", len(msgs), err)
	}

	// The user, their task and the chat all resolve to one physical database.
	up, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, a.UserID)
	if err != nil {
		t.Fatal(err)
	}
	tp, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainTask, task.Id)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainMessage, chat.Id)
	if err != nil {
		t.Fatal(err)
	}
	if up.DSN != tp.DSN || up.DSN != cp.DSN {
		t.Fatalf("co-location broken: user=%s task=%s chat=%s", up.DSN, tp.DSN, cp.DSN)
	}
}

func withRequest(ctx context.Context, method, path, userID string) context.Context {
	route, ok := domain.RouteForRequest(method, path, userID)
	if !ok {
		return ctx
	}
	return domain.WithRoute(ctx, route.Domain, route.Key, route.Create)
}

func TestRouteForRequest(t *testing.T) {
	cases := []struct {
		method, path, user string
		domain             plane.Domain
		key                string
	}{
		{"POST", "/v1/tasks", "usr_a", plane.DomainTask, "usr_a"},
		{"GET", "/v1/tasks/tsk_1", "", plane.DomainTask, "tsk_1"},
		{"POST", "/v1/chats/cht_1/messages", "usr_a", plane.DomainMessage, "cht_1"},
		{"GET", "/v1/users/usr_b/scores", "", plane.DomainUser, "usr_b"},
		{"GET", "/v1/tasks", "", "", ""},
	}
	for i, tc := range cases {
		r, ok := domain.RouteForRequest(tc.method, tc.path, tc.user)
		if ok != (tc.domain != "") || string(r.Domain) != string(tc.domain) || r.Key != tc.key {
			t.Fatalf("case %d (%s %s): got %+v ok=%v want %s/%s", i, tc.method, tc.path, r, ok, tc.domain, tc.key)
		}
	}
}
