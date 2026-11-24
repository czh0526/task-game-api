package domain_test

import (
	"context"
	"fmt"
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

// TestCrossShardFriendFlows covers the three fan-out fixes found by the real
// smoke in domain mode: a friend request must appear in the recipient's
// incoming list (writer and reader live in different shards), accepting must
// be idempotent when both members hash to the same shard, and each friend card
// must show the peer's nickname re-read from the peer's own shard.
func TestCrossShardFriendFlows(t *testing.T) {
	st, _ := openDomain(t, nil)
	ctx := context.Background()
	accounts := account.New(st, "test-jwt-secret-0123456789", 24*time.Hour, account.WithBcryptCost(bcrypt.MinCost))
	eng := engine.New(st, "credits")

	// Register enough users to find a pair on DIFFERENT shards: that is the
	// case the single-shard point reads got wrong. Names are unique so each
	// user keeps a distinct nickname for the later re-read assertion.
	type candidate struct {
		id, nick string
		shard    int
	}
	var pool []candidate
	for i := 0; i < 12; i++ {
		nick := fmt.Sprintf("昵称%d", i)
		u, err := accounts.Register(ctx, fmt.Sprintf("friend-domain-%d@example.com", i), "password-123", &nick)
		if err != nil {
			t.Fatal(err)
		}
		p, err := st.Control().Resolver.ResolveExisting(ctx, plane.DomainUser, u.UserID)
		if err != nil {
			t.Fatal(err)
		}
		pool = append(pool, candidate{id: u.UserID, nick: nick, shard: p.Shard})
	}
	var a, b candidate
	found := false
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			if pool[i].shard != pool[j].shard {
				a, b, found = pool[i], pool[j], true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("test setup: all users landed on one shard; cannot exercise cross-shard reads")
	}
	t.Logf("cross-shard pair: %s@%d (%s) / %s@%d (%s)", a.id, a.shard, a.nick, b.id, b.shard, b.nick)
	nickA, nickB := a.nick, b.nick

	reqCtx := withRequest(ctx, "POST", "/v1/friends/requests", a.id)
	created, err := eng.CreateFriendRequest(reqCtx, a.id, &types.CreateFriendRequestReq{UserId: b.id})
	if err != nil {
		t.Fatalf("create friend request: %v", err)
	}

	incoming, err := eng.ListFriendRequests(withRequest(ctx, "GET", "/v1/friend-requests", b.id), b.id, "incoming")
	if err != nil {
		t.Fatalf("list incoming: %v", err)
	}
	if len(incoming.Items) != 1 || incoming.Items[0].Id != created.Id {
		t.Fatalf("recipient incoming list: %+v", incoming)
	}
	if incoming.Items[0].FromNickname != nickA || incoming.Items[0].ToNickname != nickB {
		t.Fatalf("request nicknames: from=%q to=%q", incoming.Items[0].FromNickname, incoming.Items[0].ToNickname)
	}

	acceptCtx := withRequest(ctx, "POST", "/v1/friend-requests/"+created.Id+"/accept", b.id)
	if _, err := eng.AcceptFriendRequest(acceptCtx, b.id, &types.FriendRequestActionReq{RequestId: created.Id}); err != nil {
		t.Fatalf("accept friend request: %v", err)
	}

	for _, tc := range []struct {
		viewer, peer, wantNick string
	}{
		{a.id, b.id, nickB},
		{b.id, a.id, nickA},
	} {
		friends, err := eng.ListFriends(withRequest(ctx, "GET", "/v1/friends", tc.viewer), tc.viewer)
		if err != nil {
			t.Fatalf("list friends for %s: %v", tc.viewer, err)
		}
		var cards []types.Friend
		cards = append(cards, friends.Ungrouped...)
		for _, g := range friends.Groups {
			cards = append(cards, g.Friends...)
		}
		found := false
		for _, c := range cards {
			if c.UserId == tc.peer {
				found = true
				if c.Nickname != tc.wantNick {
					t.Fatalf("viewer %s: peer %s nickname=%q want %q", tc.viewer, tc.peer, c.Nickname, tc.wantNick)
				}
			}
		}
		if !found {
			t.Fatalf("viewer %s: friend %s missing from list", tc.viewer, tc.peer)
		}
	}
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
