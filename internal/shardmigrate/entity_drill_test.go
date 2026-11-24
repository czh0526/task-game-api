package shardmigrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/shardmigrate"
	"taskgame/internal/store/domain"
)

type drillEnv struct {
	dsns      []string
	control   string
	topo      *plane.Topology
	directory *plane.SQLDirectory
	cp        *plane.ControlPlane
}

func newDrillEnv(t *testing.T) *drillEnv {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	env := &drillEnv{dsns: make([]string, 4), control: filepath.Join(dir, "control.db")}
	for i := range env.dsns {
		env.dsns[i] = filepath.Join(dir, fmt.Sprintf("shard-%d.db", i))
	}
	env.topo = plane.NewTopology()
	for _, d := range []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage} {
		if err := env.topo.Register(d, env.dsns[:3]); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	env.directory, err = plane.OpenSQLDirectory(ctx, "sqlite", env.control, env.topo)
	if err != nil {
		t.Fatal(err)
	}
	env.cp, err = plane.NewControlPlane(env.directory, env.topo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.directory.Close() })
	return env
}

func (e *drillEnv) open(t *testing.T, grown plane.Domain) *domain.Store {
	t.Helper()
	dsns := map[plane.Domain][]string{
		plane.DomainUser:    e.dsns[:3],
		plane.DomainTask:    e.dsns[:3],
		plane.DomainMessage: e.dsns[:3],
	}
	if e.topo.Len(grown) == 4 {
		dsns[grown] = e.dsns
	}
	st, err := domain.Open(domain.Config{
		Driver: "sqlite", DSNs: dsns, InitialBalance: 1_000_000, Control: e.cp,
	})
	if err != nil {
		t.Fatalf("open domain store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func (e *drillEnv) grow(t *testing.T, domainName plane.Domain) {
	t.Helper()
	if err := shardmigrate.EnsureSchema(context.Background(), "sqlite", e.dsns[3]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.topo.AddShard(context.Background(), domainName, e.dsns[3], e.directory); err != nil {
		t.Fatal(err)
	}
}

func scalarInt(t *testing.T, dsn, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUserDomainPhysicalCutover(t *testing.T) {
	ctx := context.Background()
	env := newDrillEnv(t)
	st := env.open(t, plane.DomainUser)
	a, b, c := "usr_a", "usr_b", "usr_c"
	for id, shard := range map[string]int{a: 0, b: 1, c: 0} {
		if _, err := env.cp.Resolver.Place(ctx, plane.DomainUser, id, shard); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Users().Ensure(ctx, id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Users().SetNickname(ctx, a, "Alice"); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().AddBalance(ctx, a, 500); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().ApplyScore(ctx, a, 7, 9); err != nil {
		t.Fatal(err)
	}
	if err := st.Users().AddScoreEvent(ctx, model.ScoreEvent{
		ID: "ev_a", UserID: a, DeltaIntegrity: 7, DeltaAbility: 9, Reason: "drill", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	low, high := model.OrderedPair(a, b)
	if err := st.Friends().InsertRequest(ctx, model.FriendRequest{
		ID: "fr_ab", FromUserID: a, ToUserID: b, Status: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Friends().InsertFriendship(ctx, model.Friendship{UserLow: low, UserHigh: high, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	lowAC, highAC := model.OrderedPair(a, c)
	if err := st.Friends().InsertFriendship(ctx, model.Friendship{UserLow: lowAC, UserHigh: highAC, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.Friends().InsertGroup(ctx, model.FriendGroup{
		ID: "fg_a", OwnerID: a, Name: "close", SortOrder: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Friends().SetMembership(ctx, a, b, "fg_a"); err != nil {
		t.Fatal(err)
	}
	if err := st.Idem().Put(ctx, model.IdemRecord{
		UserID: a, Method: "POST", Path: "/v1/tasks", Key: "idem_a",
		RequestHash: "hash", ResponseBody: "{}", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// A-B is double-written (A on shard 0, B on shard 1); A-C has one row
	// because both users share shard 0. Both must survive the move.
	if got := scalarInt(t, env.dsns[0], `SELECT COUNT(*) FROM friendships`); got != 2 {
		t.Fatalf("source shard 0 friendships = %d, want 2", got)
	}

	env.grow(t, plane.DomainUser)
	keys, err := shardmigrate.PlanEntity(ctx, "sqlite", env.dsns[0], shardmigrate.UserSpec, []string{a})
	if err != nil || len(keys) != 1 {
		t.Fatalf("plan user: %v %v", keys, err)
	}
	report, err := shardmigrate.CopyEntity(ctx, "sqlite", env.dsns[0], env.dsns[3], shardmigrate.UserSpec, 3, keys)
	if err != nil {
		t.Fatalf("copy user: %v", err)
	}
	if report.Copied["users"] != 1 || report.Copied["score_events"] != 1 ||
		report.Copied["friendships"] != 2 || report.Copied["friend_requests"] != 1 ||
		report.Copied["idempotency_keys"] != 1 {
		t.Fatalf("copy report = %+v", report)
	}
	if err := shardmigrate.VerifyEntity(ctx, "sqlite", env.dsns[0], env.dsns[3], shardmigrate.UserSpec, keys); err != nil {
		t.Fatalf("verify user: %v", err)
	}
	if _, err := shardmigrate.CopyEntity(ctx, "sqlite", env.dsns[0], env.dsns[3], shardmigrate.UserSpec, 3, keys); err != nil {
		t.Fatalf("idempotent copy: %v", err)
	}
	if err := shardmigrate.CutoverEntity(ctx, "sqlite", env.control, plane.DomainUser, keys, 0, 3, env.dsns[3]); err != nil {
		t.Fatalf("cutover user: %v", err)
	}
	env.directory.InvalidateAll() // cutover ran in another directory instance
	if _, err := shardmigrate.CleanupEntity(ctx, "sqlite", env.dsns[0], shardmigrate.UserSpec, keys); err != nil {
		t.Fatalf("cleanup user: %v", err)
	}
	if got := scalarInt(t, env.dsns[0], `SELECT COUNT(*) FROM users WHERE id = ?`, a); got != 0 {
		t.Fatalf("source still has %d rows for user A", got)
	}
	if got := scalarInt(t, env.dsns[0], `SELECT COUNT(*) FROM friendships`); got != 2 {
		t.Fatalf("cleanup removed peer-owned friendships: %d rows left, want 2", got)
	}
	p, err := env.cp.Resolver.ResolveExisting(ctx, plane.DomainUser, a)
	if err != nil || p.Shard != 3 || !p.FromDirectory {
		t.Fatalf("user A placement = %+v err=%v, want shard 3 from directory", p, err)
	}

	st2 := env.open(t, plane.DomainUser)
	u, err := st2.Users().Get(ctx, a)
	if err != nil || u.Nickname != "Alice" || u.Balance != 1_000_500 || u.Integrity != 7 || u.Ability != 9 {
		t.Fatalf("user after cutover: %+v err=%v", u, err)
	}
	events, err := st2.Users().ListScoreEvents(ctx, a, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("score events after cutover: %d err=%v", len(events), err)
	}
	edges, err := st2.Friends().ListFriendEdges(ctx, a)
	if err != nil || len(edges) != 2 {
		t.Fatalf("friend edges after cutover: %d err=%v", len(edges), err)
	}
	if n, err := st2.Friends().CountFriendships(ctx, a); err != nil || n != 2 {
		t.Fatalf("friendship count after cutover: %d err=%v", n, err)
	}
	incoming, err := st2.Friends().ListPending(ctx, b, "incoming")
	if err != nil || len(incoming) != 1 {
		t.Fatalf("incoming request after cutover: %d err=%v", len(incoming), err)
	}
	idem, err := st2.Idem().Get(ctx, a, "POST", "/v1/tasks", "idem_a")
	if err != nil || idem == nil {
		t.Fatalf("idempotency record after cutover: %v err=%v", idem, err)
	}
	if err := st2.Users().AddBalance(ctx, a, 100); err != nil {
		t.Fatalf("write after cutover: %v", err)
	}
	if got := scalarInt(t, env.dsns[3], `SELECT balance FROM users WHERE id = ?`, a); got != 1_000_600 {
		t.Fatalf("balance on new shard = %d, want 1000600", got)
	}

	if err := shardmigrate.RollbackEntity(ctx, "sqlite", env.control, env.dsns[0], env.dsns[3],
		shardmigrate.UserSpec, 0, 3, keys); err != nil {
		t.Fatalf("rollback user: %v", err)
	}
	env.directory.InvalidateAll()
	if p, err = env.cp.Resolver.ResolveExisting(ctx, plane.DomainUser, a); err != nil || p.Shard != 0 {
		t.Fatalf("placement after rollback = %+v err=%v", p, err)
	}
	if got := scalarInt(t, env.dsns[0], `SELECT balance FROM users WHERE id = ?`, a); got != 1_000_600 {
		t.Fatalf("restored balance = %d, want 1000600", got)
	}
	if got := scalarInt(t, env.dsns[3], `SELECT COUNT(*) FROM users WHERE id = ?`, a); got != 0 {
		t.Fatalf("rollback left user A on the new shard: %d", got)
	}
}

func TestTaskDomainPhysicalCutover(t *testing.T) {
	ctx := context.Background()
	env := newDrillEnv(t)
	st := env.open(t, plane.DomainTask)
	taskID := "task_drill"
	if _, err := env.cp.Resolver.Place(ctx, plane.DomainTask, taskID, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Tasks().Insert(ctx, model.Task{
		ID: taskID, Kind: model.KindExclusive, Title: "drill", Status: model.StatusOpen,
		PublisherID: "usr_a", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Submissions().Insert(ctx, model.Submission{
		ID: "sub_1", TaskID: taskID, ExecutorID: "usr_b", Payload: []byte(`{"v":1}`),
		Note: "note", Status: "current", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Escrow().Insert(ctx, model.EscrowAccount{
		ID: "esc_1", TaskID: taskID, Held: 100, Currency: "credits", Version: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Escrow().InsertEntry(ctx, model.EscrowEntry{
		ID: "ent_1", EscrowID: "esc_1", Type: "hold", Amount: 100, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Invites().Upsert(ctx, model.Invite{
		ID: "inv_1", TaskID: taskID, UserID: "usr_b", Status: "pending",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := scalarInt(t, env.dsns[0], `SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID); got != 1 {
		t.Fatalf("task not on source shard: %d", got)
	}

	env.grow(t, plane.DomainTask)
	keys, err := shardmigrate.PlanEntity(ctx, "sqlite", env.dsns[0], shardmigrate.TaskSpec, []string{taskID})
	if err != nil || len(keys) != 1 {
		t.Fatalf("plan task: %v %v", keys, err)
	}
	report, err := shardmigrate.CopyEntity(ctx, "sqlite", env.dsns[0], env.dsns[3], shardmigrate.TaskSpec, 3, keys)
	if err != nil {
		t.Fatalf("copy task: %v", err)
	}
	if report.Copied["tasks"] != 1 || report.Copied["submissions"] != 1 ||
		report.Copied["escrow_accounts"] != 1 || report.Copied["escrow_entries"] != 1 ||
		report.Copied["invites"] != 1 {
		t.Fatalf("copy report = %+v", report)
	}
	if err := shardmigrate.VerifyEntity(ctx, "sqlite", env.dsns[0], env.dsns[3], shardmigrate.TaskSpec, keys); err != nil {
		t.Fatalf("verify task: %v", err)
	}
	if err := shardmigrate.CutoverEntity(ctx, "sqlite", env.control, plane.DomainTask, keys, 0, 3, env.dsns[3]); err != nil {
		t.Fatalf("cutover task: %v", err)
	}
	env.directory.InvalidateAll()
	if _, err := shardmigrate.CleanupEntity(ctx, "sqlite", env.dsns[0], shardmigrate.TaskSpec, keys); err != nil {
		t.Fatalf("cleanup task: %v", err)
	}
	p, err := env.cp.Resolver.ResolveExisting(ctx, plane.DomainTask, taskID)
	if err != nil || p.Shard != 3 {
		t.Fatalf("task placement = %+v err=%v, want shard 3", p, err)
	}

	st2 := env.open(t, plane.DomainTask)
	got, err := st2.Tasks().Get(ctx, taskID)
	if err != nil || got.Title != "drill" {
		t.Fatalf("task after cutover: %+v err=%v", got, err)
	}
	subs, err := st2.Submissions().ListByTask(ctx, taskID)
	if err != nil || len(subs) != 1 || string(subs[0].Payload) != `{"v":1}` {
		t.Fatalf("submissions after cutover: %+v err=%v", subs, err)
	}
	escrow, err := st2.Escrow().GetByTask(ctx, taskID)
	if err != nil || escrow.Held != 100 {
		t.Fatalf("escrow after cutover: %+v err=%v", escrow, err)
	}
	if n := scalarInt(t, env.dsns[3], `SELECT COUNT(*) FROM escrow_entries WHERE escrow_id = ?`, "esc_1"); n != 1 {
		t.Fatalf("escrow entries on new shard: %d", n)
	}
	invites, err := st2.Invites().ListByTask(ctx, taskID)
	if err != nil || len(invites) != 1 {
		t.Fatalf("invites after cutover: %d err=%v", len(invites), err)
	}
	got.Status = model.StatusActive
	if err := st2.Tasks().Update(ctx, got); err != nil {
		t.Fatalf("task update after cutover: %v", err)
	}
	if n := scalarInt(t, env.dsns[3], `SELECT COUNT(*) FROM tasks WHERE id = ? AND status = ?`, taskID, model.StatusActive); n != 1 {
		t.Fatalf("updated task not on new shard: %d", n)
	}

	if err := shardmigrate.RollbackEntity(ctx, "sqlite", env.control, env.dsns[0], env.dsns[3],
		shardmigrate.TaskSpec, 0, 3, keys); err != nil {
		t.Fatalf("rollback task: %v", err)
	}
	env.directory.InvalidateAll()
	if p, err = env.cp.Resolver.ResolveExisting(ctx, plane.DomainTask, taskID); err != nil || p.Shard != 0 {
		t.Fatalf("placement after rollback = %+v err=%v", p, err)
	}
	if n := scalarInt(t, env.dsns[0], `SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID); n != 1 {
		t.Fatalf("task not restored on source: %d", n)
	}
	if n := scalarInt(t, env.dsns[3], `SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID); n != 0 {
		t.Fatalf("rollback left task on new shard: %d", n)
	}
}
