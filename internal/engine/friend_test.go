package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

func ensureUsers(t *testing.T, st store.Store, clk *clock, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if _, err := st.Users().Ensure(ctx, id, clk.t); err != nil {
			t.Fatal(err)
		}
	}
}

func friendIDs(list *types.FriendList) (grouped map[string][]string, ungrouped []string) {
	grouped = map[string][]string{}
	for _, g := range list.Groups {
		ids := make([]string, 0, len(g.Friends))
		for _, f := range g.Friends {
			ids = append(ids, f.UserId)
		}
		grouped[g.Name] = ids
	}
	for _, f := range list.Ungrouped {
		ungrouped = append(ungrouped, f.UserId)
	}
	return grouped, ungrouped
}

func containsID(ids []string, id string) bool {
	for _, item := range ids {
		if item == id {
			return true
		}
	}
	return false
}

func TestFriendRequestLifecycle(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000)
	ensureUsers(t, st, clk, "usr_a", "usr_b", "usr_c")
	if _, err := st.Users().InsertRegistered(ctx, "usr_mail", "secret-friend@example.com", "hash", clk.t); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().ApplyScore(ctx, "usr_b", 3, 7); err != nil {
		t.Fatal(err)
	}
	mailReq, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_mail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_mail", &types.FriendRequestActionReq{RequestId: mailReq.Id}); err != nil {
		t.Fatal(err)
	}
	mailList, err := eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	mailRaw, _ := json.Marshal(mailList)
	if strings.Contains(string(mailRaw), "secret-friend@example.com") || strings.Contains(string(mailRaw), "email") || strings.Contains(string(mailRaw), "password") {
		t.Fatalf("friend list leaked private fields: %s", mailRaw)
	}
	if _, err := eng.Unfriend(ctx, "usr_a", &types.UnfriendReq{UserId: "usr_mail"}); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_a"}); codeOf(err) != "invalid_argument" {
		t.Fatalf("self friend: %v", err)
	}
	if _, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_missing"}); codeOf(err) != "not_found" {
		t.Fatalf("missing user: %v", err)
	}
	if _, err := eng.ListFriendRequests(ctx, "usr_a", "sideways"); codeOf(err) != "invalid_argument" {
		t.Fatalf("direction: %v", err)
	}

	req := &types.CreateFriendRequestReq{UserId: "usr_b", IdempotencyKey: "fr-1"}
	created, err := eng.CreateFriendRequest(ctx, "usr_a", req)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != model.FriendPending || created.FromUserId != "usr_a" || created.ToUserId != "usr_b" {
		t.Fatalf("created %+v", created)
	}
	again, err := eng.CreateFriendRequest(ctx, "usr_a", req)
	if err != nil || again.Id != created.Id {
		t.Fatalf("replay %+v %v", again, err)
	}
	req.UserId = "usr_c"
	if _, err := eng.CreateFriendRequest(ctx, "usr_a", req); codeOf(err) != "idempotency_conflict" {
		t.Fatalf("idempotency: %v", err)
	}
	if _, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"}); codeOf(err) != "conflict" {
		t.Fatalf("duplicate outgoing: %v", err)
	}

	incoming, err := eng.ListFriendRequests(ctx, "usr_b", model.FriendIncoming)
	if err != nil || len(incoming.Items) != 1 || incoming.Items[0].Id != created.Id {
		t.Fatalf("incoming %+v %v", incoming, err)
	}
	outgoing, err := eng.ListFriendRequests(ctx, "usr_a", model.FriendOutgoing)
	if err != nil || len(outgoing.Items) != 1 {
		t.Fatalf("outgoing %+v %v", outgoing, err)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_a", &types.FriendRequestActionReq{RequestId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("requester accept: %v", err)
	}
	if _, err := eng.CancelFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: created.Id}); codeOf(err) != "forbidden" {
		t.Fatalf("recipient cancel: %v", err)
	}

	accepted, err := eng.AcceptFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: created.Id})
	if err != nil || accepted.Status != model.FriendAccepted {
		t.Fatalf("accept %+v %v", accepted, err)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: created.Id}); codeOf(err) != "conflict" {
		t.Fatalf("second accept: %v", err)
	}
	empty, err := eng.ListFriendRequests(ctx, "usr_b", model.FriendIncoming)
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("pending after accept %+v %v", empty, err)
	}
	if _, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"}); codeOf(err) != "conflict" {
		t.Fatalf("already friends: %v", err)
	}

	listA, err := eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	_, ungrouped := friendIDs(listA)
	if !containsID(ungrouped, "usr_b") || len(listA.Groups) != 0 {
		t.Fatalf("A friends %+v", listA)
	}
	if listA.Ungrouped[0].Integrity != 3 || listA.Ungrouped[0].Ability != 7 {
		t.Fatalf("public scores %+v", listA.Ungrouped[0])
	}
	listB, err := eng.ListFriends(ctx, "usr_b")
	if err != nil {
		t.Fatal(err)
	}
	_, ungrouped = friendIDs(listB)
	if !containsID(ungrouped, "usr_a") {
		t.Fatalf("B friends %+v", listB)
	}

	group, err := eng.CreateFriendGroup(ctx, "usr_a", &types.CreateFriendGroupReq{Name: "  同事  "})
	if err != nil || group.Name != "同事" || group.SortOrder != 0 {
		t.Fatalf("group %+v %v", group, err)
	}
	if _, err := eng.CreateFriendGroup(ctx, "usr_a", &types.CreateFriendGroupReq{Name: "同事"}); codeOf(err) != "conflict" {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := eng.CreateFriendGroup(ctx, "usr_a", &types.CreateFriendGroupReq{Name: "   "}); codeOf(err) != "invalid_argument" {
		t.Fatalf("blank name: %v", err)
	}
	long := strings.Repeat("友", 32)
	second, err := eng.CreateFriendGroup(ctx, "usr_a", &types.CreateFriendGroupReq{Name: long})
	if err != nil || second.SortOrder != 1 {
		t.Fatalf("second group %+v %v", second, err)
	}
	if _, err := eng.CreateFriendGroup(ctx, "usr_a", &types.CreateFriendGroupReq{Name: strings.Repeat("友", 33)}); codeOf(err) != "invalid_argument" {
		t.Fatalf("long name: %v", err)
	}
	renamed, err := eng.UpdateFriendGroup(ctx, "usr_a", &types.PatchFriendGroupReq{GroupId: group.Id, SortOrder: int64Ptr(-1)})
	if err != nil || renamed.SortOrder != -1 || renamed.Name != "同事" {
		t.Fatalf("reorder %+v %v", renamed, err)
	}
	if _, err := eng.UpdateFriendGroup(ctx, "usr_a", &types.PatchFriendGroupReq{GroupId: group.Id}); codeOf(err) != "invalid_argument" {
		t.Fatalf("empty patch: %v", err)
	}
	groups, err := eng.ListFriendGroups(ctx, "usr_a")
	if err != nil || len(groups.Items) != 2 || groups.Items[0].Id != group.Id || groups.Items[1].Id != second.Id {
		t.Fatalf("group order %+v %v", groups, err)
	}
	if _, err := eng.UpdateFriendGroup(ctx, "usr_b", &types.PatchFriendGroupReq{GroupId: group.Id, Name: strPtr("偷")}); codeOf(err) != "not_found" {
		t.Fatalf("rename other group: %v", err)
	}

	placed, err := eng.SetFriendGroup(ctx, "usr_a", &types.SetFriendGroupReq{UserId: "usr_b", GroupId: &group.Id})
	if err != nil || placed.GroupId == nil || *placed.GroupId != group.Id {
		t.Fatalf("place %+v %v", placed, err)
	}
	listA, err = eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	grouped, ungrouped := friendIDs(listA)
	if !containsID(grouped["同事"], "usr_b") || len(ungrouped) != 0 {
		t.Fatalf("grouped %+v ungrouped %v", grouped, ungrouped)
	}
	listB, err = eng.ListFriends(ctx, "usr_b")
	if err != nil {
		t.Fatal(err)
	}
	groupedB, ungroupedB := friendIDs(listB)
	if len(groupedB) != 0 || !containsID(ungroupedB, "usr_a") {
		t.Fatalf("B should not see A's folder %+v %v", groupedB, ungroupedB)
	}
	if _, err := eng.SetFriendGroup(ctx, "usr_a", &types.SetFriendGroupReq{UserId: "usr_c", GroupId: &group.Id}); codeOf(err) != "not_found" {
		t.Fatalf("group non-friend: %v", err)
	}
	other, err := eng.CreateFriendGroup(ctx, "usr_b", &types.CreateFriendGroupReq{Name: "秘密"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SetFriendGroup(ctx, "usr_b", &types.SetFriendGroupReq{UserId: "usr_a", GroupId: &other.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SetFriendGroup(ctx, "usr_a", &types.SetFriendGroupReq{UserId: "usr_b", GroupId: &other.Id}); codeOf(err) != "not_found" {
		t.Fatalf("move into someone else's group: %v", err)
	}
	emptyID := ""
	if _, err := eng.SetFriendGroup(ctx, "usr_a", &types.SetFriendGroupReq{UserId: "usr_b", GroupId: &emptyID}); codeOf(err) != "invalid_argument" {
		t.Fatalf("empty group id: %v", err)
	}

	deleted, err := eng.DeleteFriendGroup(ctx, "usr_a", &types.DeleteFriendGroupReq{GroupId: group.Id})
	if err != nil || deleted.Id != group.Id {
		t.Fatalf("delete group %+v %v", deleted, err)
	}
	listA, err = eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	grouped, ungrouped = friendIDs(listA)
	if _, ok := grouped["同事"]; ok || !containsID(ungrouped, "usr_b") {
		t.Fatalf("delete group should keep the friend ungrouped: %+v %v", grouped, ungrouped)
	}
	if _, err := eng.DeleteFriendGroup(ctx, "usr_a", &types.DeleteFriendGroupReq{GroupId: other.Id}); codeOf(err) != "not_found" {
		t.Fatalf("delete other group: %v", err)
	}

	removed, err := eng.Unfriend(ctx, "usr_a", &types.UnfriendReq{UserId: "usr_b"})
	if err != nil || removed.UserId != "usr_b" {
		t.Fatalf("unfriend %+v %v", removed, err)
	}
	listA, err = eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	_, ungrouped = friendIDs(listA)
	if containsID(ungrouped, "usr_b") {
		t.Fatalf("A still lists B: %+v", listA)
	}
	listB, err = eng.ListFriends(ctx, "usr_b")
	if err != nil {
		t.Fatal(err)
	}
	_, ungrouped = friendIDs(listB)
	if containsID(ungrouped, "usr_a") {
		t.Fatalf("B still lists A: %+v", listB)
	}
	// B's folder membership of A is cleared. A later friendship starts ungrouped.
	if _, err := eng.Unfriend(ctx, "usr_a", &types.UnfriendReq{UserId: "usr_b"}); codeOf(err) != "not_found" {
		t.Fatalf("second unfriend: %v", err)
	}

	resent, err := eng.CreateFriendRequest(ctx, "usr_b", &types.CreateFriendRequestReq{UserId: "usr_a"})
	if err != nil || resent.Status != model.FriendPending || resent.FromUserId != "usr_b" {
		t.Fatalf("request after unfriend %+v %v", resent, err)
	}
	declined, err := eng.DeclineFriendRequest(ctx, "usr_a", &types.FriendRequestActionReq{RequestId: resent.Id})
	if err != nil || declined.Status != model.FriendDeclined {
		t.Fatalf("decline %+v %v", declined, err)
	}
	if _, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"}); err != nil {
		t.Fatalf("request after decline: %v", err)
	}
	pending, err := eng.ListFriendRequests(ctx, "usr_b", model.FriendIncoming)
	if err != nil || len(pending.Items) != 1 {
		t.Fatalf("incoming after new request %+v %v", pending, err)
	}
	cancelled, err := eng.CancelFriendRequest(ctx, "usr_a", &types.FriendRequestActionReq{RequestId: pending.Items[0].Id})
	if err != nil || cancelled.Status != model.FriendCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}

	// The other side already asked: accept that request instead of inserting another.
	reverse, err := eng.CreateFriendRequest(ctx, "usr_b", &types.CreateFriendRequestReq{UserId: "usr_a"})
	if err != nil || reverse.Status != model.FriendPending {
		t.Fatalf("reverse pending %+v %v", reverse, err)
	}
	auto, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil || auto.Status != model.FriendAccepted || auto.Id != reverse.Id {
		t.Fatalf("auto-accept %+v want id %s err %v", auto, reverse.Id, err)
	}
	rows, err := st.Friends().ListPending(ctx, "usr_a", model.FriendOutgoing)
	if err != nil || len(rows) != 0 {
		t.Fatalf("pending after auto-accept %+v %v", rows, err)
	}
	listA, err = eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	grouped, ungrouped = friendIDs(listA)
	if !containsID(ungrouped, "usr_b") {
		t.Fatalf("friends again %+v %v", grouped, ungrouped)
	}
	listB, err = eng.ListFriends(ctx, "usr_b")
	if err != nil {
		t.Fatal(err)
	}
	groupedB, ungroupedB = friendIDs(listB)
	if !containsID(ungroupedB, "usr_a") || containsID(groupedB["秘密"], "usr_a") {
		t.Fatalf("new friendship should be ungrouped on both sides: %+v %v", groupedB, ungroupedB)
	}
}

func TestFriendWebhooks(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000)
	ensureUsers(t, st, clk, "usr_a", "usr_b")
	hook := func(owner string, events []string) string {
		t.Helper()
		got, err := eng.CreateWebhook(ctx, owner, &types.CreateWebhookReq{
			Url: "https://example.com/hooks", Secret: "webhook-secret-16", Events: events,
		})
		if err != nil {
			t.Fatal(err)
		}
		return got.Id
	}
	all := []string{
		model.EventFriendRequested, model.EventFriendAccepted, model.EventFriendDeclined, model.EventFriendRemoved,
	}
	hookA := hook("usr_a", all)
	hookB := hook("usr_b", all)

	created, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	if typesFor(t, st, hookA) != nil {
		t.Fatalf("requester should not get friend.requested, got %v", typesFor(t, st, hookA))
	}
	assertEvent(t, st, hookB, model.EventFriendRequested, created.Id)

	if _, err := eng.AcceptFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: created.Id}); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, st, hookA, model.EventFriendAccepted, created.Id)
	if hasType(typesFor(t, st, hookB), model.EventFriendAccepted) {
		t.Fatalf("acceptor should not get friend.accepted")
	}

	if _, err := eng.Unfriend(ctx, "usr_a", &types.UnfriendReq{UserId: "usr_b"}); err != nil {
		t.Fatal(err)
	}
	body := assertEvent(t, st, hookB, model.EventFriendRemoved, "")
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data["user_id"] != "usr_a" || env.Data["friend_user_id"] != "usr_b" {
		t.Fatalf("removed payload %+v", env.Data)
	}

	next, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(typesFor(t, st, hookB))
	cancelled, err := eng.CancelFriendRequest(ctx, "usr_a", &types.FriendRequestActionReq{RequestId: next.Id})
	if err != nil || cancelled.Status != model.FriendCancelled {
		t.Fatalf("cancel %+v %v", cancelled, err)
	}
	if len(typesFor(t, st, hookB)) != before {
		t.Fatalf("cancel emitted a webhook")
	}

	declinedReq, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.DeclineFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: declinedReq.Id}); err != nil {
		t.Fatal(err)
	}
	assertEvent(t, st, hookA, model.EventFriendDeclined, declinedReq.Id)
}

func TestFriendLimits(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000)
	ensureUsers(t, st, clk, "usr_owner", "usr_extra")
	for i := 0; i < maxFriendGroups; i++ {
		if _, err := eng.CreateFriendGroup(ctx, "usr_owner", &types.CreateFriendGroupReq{Name: fmt.Sprintf("g%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := eng.CreateFriendGroup(ctx, "usr_owner", &types.CreateFriendGroupReq{Name: "overflow"}); codeOf(err) != "conflict" {
		t.Fatalf("group cap: %v", err)
	}

	for i := 0; i < maxPendingFriendRequests; i++ {
		id := fmt.Sprintf("usr_q%03d", i)
		ensureUsers(t, st, clk, id)
		if _, err := eng.CreateFriendRequest(ctx, "usr_owner", &types.CreateFriendRequestReq{UserId: id}); err != nil {
			t.Fatalf("pending %d: %v", i, err)
		}
	}
	ensureUsers(t, st, clk, "usr_q100")
	if _, err := eng.CreateFriendRequest(ctx, "usr_owner", &types.CreateFriendRequestReq{UserId: "usr_q100"}); codeOf(err) != "conflict" {
		t.Fatalf("pending cap: %v", err)
	}

	for i := 0; i < maxFriends; i++ {
		id := fmt.Sprintf("usr_pad%03d", i)
		ensureUsers(t, st, clk, id)
		if err := st.Friends().InsertFriendship(ctx, model.Friendship{UserLow: "usr_full", UserHigh: id, CreatedAt: clk.t}); err != nil {
			t.Fatal(err)
		}
	}
	ensureUsers(t, st, clk, "usr_full")
	pending, err := eng.CreateFriendRequest(ctx, "usr_extra", &types.CreateFriendRequestReq{UserId: "usr_full"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_full", &types.FriendRequestActionReq{RequestId: pending.Id}); codeOf(err) != "conflict" {
		t.Fatalf("friend cap: %v", err)
	}
}

func typesFor(t *testing.T, st store.Store, webhookID string) []string {
	t.Helper()
	rows, err := st.Deliveries().ListPending(context.Background(), 5, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		if row.WebhookID == webhookID {
			out = append(out, row.EventType)
		}
	}
	return out
}

func hasType(types []string, want string) bool {
	for _, item := range types {
		if item == want {
			return true
		}
	}
	return false
}

func assertEvent(t *testing.T, st store.Store, webhookID, eventType, requestID string) string {
	t.Helper()
	rows, err := st.Deliveries().ListPending(context.Background(), 5, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.WebhookID != webhookID || row.EventType != eventType {
			continue
		}
		if requestID == "" {
			return row.Body
		}
		var env struct {
			Data struct {
				RequestID string `json:"request_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(row.Body), &env); err != nil {
			t.Fatal(err)
		}
		if env.Data.RequestID == requestID {
			if strings.Contains(row.Body, "email") || strings.Contains(row.Body, "password") {
				t.Fatalf("webhook leaked a secret: %s", row.Body)
			}
			return row.Body
		}
	}
	t.Fatalf("missing %s for %s on %s", eventType, requestID, webhookID)
	return ""
}

func TestPublicNicknameIsSharedAndNotAnEmail(t *testing.T) {
	ctx := context.Background()
	eng, st, clk := newEngine(t, 1_000)
	ensureUsers(t, st, clk, "usr_a", "usr_b", "usr_c")
	if err := st.Users().SetNickname(ctx, "usr_a", "同名"); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().SetNickname(ctx, "usr_b", "同名"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Users().InsertRegistered(ctx, "usr_mail", "secret-nick@example.com", "hash", clk.t); err != nil {
		t.Fatal(err)
	}

	pending, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_b"})
	if err != nil {
		t.Fatal(err)
	}
	if pending.FromNickname != "同名" || pending.ToNickname != "同名" {
		t.Fatalf("request nicknames %+v", pending)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_b", &types.FriendRequestActionReq{RequestId: pending.Id}); err != nil {
		t.Fatal(err)
	}
	blank, err := eng.CreateFriendRequest(ctx, "usr_a", &types.CreateFriendRequestReq{UserId: "usr_c"})
	if err != nil {
		t.Fatal(err)
	}
	if blank.FromNickname != "同名" || blank.ToNickname != "" {
		t.Fatalf("missing nickname %+v", blank)
	}
	if _, err := eng.AcceptFriendRequest(ctx, "usr_c", &types.FriendRequestActionReq{RequestId: blank.Id}); err != nil {
		t.Fatal(err)
	}

	list, err := eng.ListFriends(ctx, "usr_a")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, friend := range list.Ungrouped {
		seen[friend.UserId] = friend.Nickname
	}
	if seen["usr_b"] != "同名" || seen["usr_c"] != "" {
		t.Fatalf("cards %v", seen)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "email") || strings.Contains(string(raw), "secret-nick") || strings.Contains(string(raw), "password") {
		t.Fatalf("friend list leaked private fields: %s", raw)
	}

	scores, err := eng.GetUserScores(ctx, "usr_b")
	if err != nil || scores.Nickname != "同名" || scores.UserId != "usr_b" {
		t.Fatalf("scores %+v %v", scores, err)
	}
	scoreRaw, _ := json.Marshal(scores)
	if strings.Contains(string(scoreRaw), "email") || strings.Contains(string(scoreRaw), "password") || strings.Contains(string(scoreRaw), "balance") {
		t.Fatalf("scores leaked private fields: %s", scoreRaw)
	}
	missing, err := eng.GetUserScores(ctx, "usr_c")
	if err != nil || missing.Nickname != "" {
		t.Fatalf("unset nickname %+v %v", missing, err)
	}
}

func int64Ptr(n int64) *int64 { return &n }
