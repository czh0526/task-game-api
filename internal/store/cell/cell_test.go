package cell_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"taskgame/internal/account"
	"taskgame/internal/auth"
	"taskgame/internal/engine"
	"taskgame/internal/model"
	"taskgame/internal/store/cell"
	"taskgame/internal/types"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// openCells creates N independent SQLite databases. SQLite is used so the
// distribution proof runs in CI without a MySQL server; the production config
// uses the same cell.Store with three MySQL DSNs.
func openCells(t *testing.T, n int) (*cell.Store, []string) {
	t.Helper()
	dir := t.TempDir()
	dsns := make([]string, n)
	for i := range dsns {
		dsns[i] = filepath.Join(dir, fmt.Sprintf("cell-%d.db", i))
	}
	st, err := cell.Open(cell.Config{Driver: "sqlite", DSNs: dsns, InitialBalance: 1_000_000})
	if err != nil {
		t.Fatalf("open %d cells: %v", n, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dsns
}

// pin reproduces what the auth + cell middleware do for one HTTP request:
// put the caller id on the context, derive the routing key from the URL (or
// the caller id), resolve it, and pin the context to that cell.
func pin(st *cell.Store, uid, path string) context.Context {
	ctx := context.Background()
	if uid != "" {
		ctx = auth.WithUserID(ctx, uid)
	}
	key, kind := cell.KeyFromPath(path)
	if key == "" && uid != "" {
		key, kind = uid, cell.KindUser
	}
	if key == "" {
		return ctx
	}
	ctx = cell.WithHint(ctx, key, kind)
	return cell.WithCell(ctx, st.Locator().CellFor(ctx, key, kind))
}

func openRaw(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableNames(t *testing.T, dsn string) []string {
	t.Helper()
	db := openRaw(t, dsn)
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func counts(t *testing.T, dsn string) map[string]int {
	t.Helper()
	db := openRaw(t, dsn)
	out := map[string]int{}
	for _, tbl := range []string{"users", "tasks", "messages", "chats"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tbl, err)
		}
		out[tbl] = n
	}
	return out
}

func TestThreeCellsHaveIdenticalSchemas(t *testing.T) {
	st, dsns := openCells(t, 3)
	if st.Len() != 3 {
		t.Fatalf("Len = %d, want 3", st.Len())
	}
	base := tableNames(t, dsns[0])
	for _, name := range []string{"users", "tasks", "messages", "chats", "escrow_accounts", "submissions", "webhooks", "friendships"} {
		found := false
		for _, got := range base {
			if got == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("cell 0 is missing table %q; full schema required: %v", name, base)
		}
	}
	for i := 1; i < len(dsns); i++ {
		if got := tableNames(t, dsns[i]); !reflect.DeepEqual(got, base) {
			t.Fatalf("cell %d schema differs from cell 0:\n cell0=%v\n cell%d=%v", i, base, i, got)
		}
	}
}

func taskReq() *types.CreateTaskReq {
	return &types.CreateTaskReq{
		Kind:        model.KindExclusive,
		Title:       "Split me across cells",
		Description: "A task used to prove horizontal distribution.",
		Payment:     types.Payment{Mode: model.PayOneShot, Amount: 100},
		Acceptance: types.Acceptance{
			Deadline:    time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339),
			Quality:     "done",
			ReviewHours: 72,
		},
		AssigneePolicy: types.AssigneePolicy{Type: model.PolicyOpen},
	}
}

// TestThreeCellsEachHoldOneThirdOfUsersTasksMessages is the acceptance test for
// plan A: three databases, same schema, each holding a slice of every table,
// with no database holding the full data set.
func TestThreeCellsEachHoldOneThirdOfUsersTasksMessages(t *testing.T) {
	const users = 30
	st, dsns := openCells(t, 3)
	accounts := account.New(st, "test-jwt-secret-0123456789", 24*time.Hour, account.WithBcryptCost(bcrypt.MinCost))
	eng := engine.New(st, "credits")
	ctx := context.Background()

	ids := make([]string, 0, users)
	for i := 0; i < users; i++ {
		email := fmt.Sprintf("cell-user-%d@example.com", i)
		sess, err := accounts.Register(ctx, email, "password-123", nil)
		if err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		ids = append(ids, sess.UserID)
	}

	taskIDs := make([]string, 0, users)
	for _, uid := range ids {
		created, err := eng.CreateTask(pin(st, uid, "/v1/tasks"), uid, taskReq())
		if err != nil {
			t.Fatalf("create task for %s: %v", uid, err)
		}
		taskIDs = append(taskIDs, created.Id)
	}

	// 15 direct chats, each with one message from each participant: 30 messages.
	for i := 0; i < users; i += 2 {
		a, b := ids[i], ids[i+1]
		chat, err := eng.CreateChat(pin(st, a, "/v1/chats"), a, &types.CreateChatReq{
			Type: model.ChatDirect, UserId: b,
		})
		if err != nil {
			t.Fatalf("create chat %d: %v", i, err)
		}
		for _, sender := range []string{a, b} {
			path := "/v1/chats/" + chat.Id + "/messages"
			if _, err := eng.PostMessage(pin(st, sender, path), sender, &types.PostMessageReq{
				ChatId: chat.Id, Type: "text", Text: "hello from " + sender,
			}); err != nil {
				t.Fatalf("post message in %s by %s: %v", chat.Id, sender, err)
			}
		}
	}

	// Every cell must own a slice of each table, and no cell may own all rows.
	totals := map[string]int{}
	for i, dsn := range dsns {
		c := counts(t, dsn)
		t.Logf("cell %d: users=%d tasks=%d messages=%d chats=%d", i, c["users"], c["tasks"], c["messages"], c["chats"])
		for _, tbl := range []string{"users", "tasks", "messages"} {
			totals[tbl] += c[tbl]
			if c[tbl] < 3 || c[tbl] > 20 {
				t.Errorf("cell %d has %d %s, expected a roughly even 1/3 share (3..20)", i, c[tbl], tbl)
			}
		}
		if c["chats"] < 1 {
			t.Errorf("cell %d has no chats", i)
		}
	}
	for _, tbl := range []string{"users", "tasks", "messages"} {
		if totals[tbl] != users {
			t.Errorf("total %s = %d, want %d (rows lost or duplicated across cells)", tbl, totals[tbl], users)
		}
	}
	// No full primary database: no single cell has all the users.
	for i, dsn := range dsns {
		if counts(t, dsn)["users"] == users {
			t.Errorf("cell %d holds every user: a full primary database still exists", i)
		}
	}

	// Cross-cell reads must still work: a user from another cell can read a
	// task, the market feed merges all cells, and login finds an email anywhere.
	other := ids[0]
	got, err := eng.GetTask(pin(st, other, "/v1/tasks/"+taskIDs[users-1]), taskIDs[users-1])
	if err != nil || got == nil {
		t.Fatalf("cross-cell GetTask: %v", err)
	}
	list, err := eng.ListTasks(ctx, "", &types.ListTasksReq{Limit: 100})
	if err != nil {
		t.Fatalf("ListTasks across cells: %v", err)
	}
	if len(list.Items) != users || list.Total != users {
		t.Errorf("market feed returned %d items / total %d, want %d (scatter-gather merge)", len(list.Items), list.Total, users)
	}
	if _, err := accounts.Login(ctx, "cell-user-17@example.com", "password-123"); err != nil {
		t.Errorf("login by email across cells: %v", err)
	}
}

func TestKeyFromPath(t *testing.T) {
	cases := []struct {
		path, key, kind string
	}{
		{"/v1/tasks", "", ""},
		{"/v1/tasks/tsk_abc/accept", "tsk_abc", cell.KindTask},
		{"/v1/chats/cht_xyz/messages", "cht_xyz", cell.KindChat},
		{"/v1/users/usr_1/avatar", "usr_1", cell.KindUser},
		{"/v1/me", "", ""},
	}
	for _, c := range cases {
		key, kind := cell.KeyFromPath(c.path)
		if key != c.key || kind != c.kind {
			t.Errorf("KeyFromPath(%q) = (%q,%q), want (%q,%q)", c.path, key, kind, c.key, c.kind)
		}
	}
}
