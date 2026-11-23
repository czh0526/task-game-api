package plane

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timeForTest() time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
}

func openTestSQLDir(t *testing.T, dsn string, topo *Topology) *SQLDirectory {
	t.Helper()
	d, err := OpenSQLDirectory(context.Background(), "sqlite", dsn, topo)
	if err != nil {
		t.Fatalf("OpenSQLDirectory: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func tempDSN(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "control-plane.db")
}

func testTopology(t *testing.T) *Topology {
	t.Helper()
	topo := NewTopology()
	if err := topo.Register(DomainMessage, []string{"db0", "db1"}); err != nil {
		t.Fatal(err)
	}
	return topo
}

func TestSQLDirectoryMigrateIsIdempotent(t *testing.T) {
	d := openTestSQLDir(t, tempDSN(t), nil)
	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	rows, err := d.DB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ('shard_directory', 'shard_topology', 'global_unique')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shard_directory", "shard_topology", "global_unique"} {
		if !seen[name] {
			t.Fatalf("Schema table %q was not created (got %v)", name, seen)
		}
	}
}

func TestSQLDirectoryAssignLookupKeysAndCount(t *testing.T) {
	d := openTestSQLDir(t, tempDSN(t), testTopology(t))
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		shard := i % 2
		if err := d.Assign(ctx, DomainMessage, fmt.Sprintf("cht_%02d", i), shard); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := d.Lookup(ctx, DomainMessage, "cht_07"); err != nil || !ok {
		t.Fatalf("Lookup miss: ok=%v err=%v", ok, err)
	}
	if shard, ok, err := d.Lookup(ctx, DomainMessage, "cht_07"); err != nil || !ok || shard != 1 {
		t.Fatalf("cht_07 shard = %d, %v, %v", shard, ok, err)
	}
	if _, ok, err := d.Lookup(ctx, DomainMessage, "missing"); err != nil || ok {
		t.Fatalf("missing key should be a clean miss: %v, %v", ok, err)
	}
	keys, err := d.Keys(ctx, DomainMessage, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 6 || keys[0] != "cht_00" || keys[5] != "cht_10" {
		t.Fatalf("Keys(shard 0) = %v", keys)
	}
	// Assign writes a topology row too, so the count reflects real usage.
	if n, err := d.ShardCount(ctx, DomainMessage); err != nil || n != 2 {
		t.Fatalf("ShardCount = %d, %v", n, err)
	}
	if n, err := d.ShardCount(ctx, DomainTask); err != nil || n != 0 {
		t.Fatalf("empty domain count = %d, %v", n, err)
	}
}

func TestSQLDirectoryAssignNeverOverwrites(t *testing.T) {
	d := openTestSQLDir(t, tempDSN(t), nil)
	ctx := context.Background()
	if err := d.Assign(ctx, DomainMessage, "cht_fixed", 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Assign(ctx, DomainMessage, "cht_fixed", 1); err != nil {
		t.Fatal(err)
	}
	shard, ok, err := d.Lookup(ctx, DomainMessage, "cht_fixed")
	if err != nil || !ok || shard != 0 {
		t.Fatalf("existing row was overwritten: shard=%d ok=%v err=%v", shard, ok, err)
	}
	// Concurrent idempotent retries must not error or move the row.
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(s int) { done <- d.Assign(ctx, DomainMessage, "cht_fixed", s) }(i % 2)
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent Assign: %v", err)
		}
	}
	if shard, _, _ := d.Lookup(ctx, DomainMessage, "cht_fixed"); shard != 0 {
		t.Fatalf("concurrent Assign moved the row to %d", shard)
	}
}

func TestSQLDirectoryIsSharedAcrossProcesses(t *testing.T) {
	dsn := tempDSN(t)
	// Two independent pools over one file emulate two API processes.
	a := openTestSQLDir(t, dsn, testTopology(t))
	b := openTestSQLDir(t, dsn, testTopology(t))
	ctx := context.Background()
	if err := a.Assign(ctx, DomainMessage, "cht_shared", 1); err != nil {
		t.Fatal(err)
	}
	if shard, ok, err := b.Lookup(ctx, DomainMessage, "cht_shared"); err != nil || !ok || shard != 1 {
		t.Fatalf("process B cannot see A's row: %d, %v, %v", shard, ok, err)
	}
	if err := b.Assign(ctx, DomainMessage, "cht_shared", 0); err != nil {
		t.Fatal(err)
	}
	if shard, _, _ := a.Lookup(ctx, DomainMessage, "cht_shared"); shard != 1 {
		t.Fatal("process B overwrote A's mapping")
	}
	// EnsureShards only ever raises the shared count.
	if err := a.EnsureShards(ctx, DomainMessage, 3); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureShards(ctx, DomainMessage, 2); err != nil {
		t.Fatal(err)
	}
	for _, d := range []*SQLDirectory{a, b} {
		if n, err := d.ShardCount(ctx, DomainMessage); err != nil || n != 3 {
			t.Fatalf("ShardCount = %d, %v (want 3: only grows)", n, err)
		}
	}
}

func TestSQLDirectoryEnsureShardsPersistsDSNs(t *testing.T) {
	topo := testTopology(t)
	d := openTestSQLDir(t, tempDSN(t), topo)
	ctx := context.Background()
	if err := d.EnsureShards(ctx, DomainMessage, 2); err != nil {
		t.Fatal(err)
	}
	rows, err := d.DB().QueryContext(ctx,
		`SELECT shard, dsn FROM shard_topology WHERE domain = ? ORDER BY shard`, string(DomainMessage))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		shard int
		dsn   string
	}
	got := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.shard, &r.dsn); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].dsn != "db0" || got[1].dsn != "db1" {
		t.Fatalf("topology rows = %+v", got)
	}
}

func TestAssignInTxRollbackAndCommit(t *testing.T) {
	dsn := tempDSN(t)
	d := openTestSQLDir(t, dsn, nil)
	ctx := context.Background()

	tx, err := d.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AssignInTx(ctx, tx, "sqlite", DomainMessage, "cht_rolled_back", 0, d.now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.Lookup(ctx, DomainMessage, "cht_rolled_back"); err != nil || ok {
		t.Fatalf("rolled-back row is visible: ok=%v err=%v", ok, err)
	}

	tx, err = d.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := AssignInTx(ctx, tx, "sqlite", DomainMessage, "cht_committed", 1, d.now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if shard, ok, err := d.Lookup(ctx, DomainMessage, "cht_committed"); err != nil || !ok || shard != 1 {
		t.Fatalf("committed row = %d, %v, %v", shard, ok, err)
	}
}

func TestNewSQLDirectoryDoesNotOwnExternalDB(t *testing.T) {
	dsn := tempDSN(t)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewSQLDirectory(context.Background(), db, "sqlite", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Assign(context.Background(), DomainMessage, "cht_ext", 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// Close must NOT have closed the caller's pool.
	if _, err := db.ExecContext(context.Background(), `SELECT 1`); err != nil {
		t.Fatalf("external pool was closed by SQLDirectory.Close: %v", err)
	}
	_ = db.Close()
	if err := d.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
}

func TestSQLDirectoryValidation(t *testing.T) {
	ctx := context.Background()
	d := openTestSQLDir(t, tempDSN(t), nil)
	if err := d.Assign(ctx, Domain("bogus"), "k", 0); err == nil {
		t.Fatal("invalid domain accepted")
	}
	if err := d.Assign(ctx, DomainMessage, "", 0); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := d.Assign(ctx, DomainMessage, "k", -1); err == nil {
		t.Fatal("negative shard accepted")
	}
	if _, _, err := d.Lookup(ctx, Domain("bogus"), "k"); err == nil {
		t.Fatal("invalid Lookup domain accepted")
	}
	if err := d.EnsureShards(ctx, DomainMessage, 0); err == nil {
		t.Fatal("zero shard count accepted")
	}
	if _, err := d.Keys(ctx, DomainMessage, -1); err == nil {
		t.Fatal("negative shard in Keys accepted")
	}
	if _, err := OpenSQLDirectory(ctx, "postgres", tempDSN(t), nil); err == nil {
		t.Fatal("unsupported driver accepted")
	}
	if _, err := OpenSQLDirectory(ctx, "sqlite", "", nil); err == nil {
		t.Fatal("empty DSN accepted")
	}
	if _, err := NewSQLDirectory(ctx, nil, "sqlite", nil); err == nil {
		t.Fatal("nil *sql.DB accepted")
	}
	if err := (*SQLDirectory)(nil).Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestSQLDialectHelpers(t *testing.T) {
	if got := insertIgnoreSQL("mysql"); got != "INSERT IGNORE" {
		t.Fatalf("mysql verb = %q", got)
	}
	if got := insertIgnoreSQL("sqlite"); got != "INSERT OR IGNORE" {
		t.Fatalf("sqlite verb = %q", got)
	}
	for in, want := range map[string]string{"": "sqlite", "sqlite": "sqlite", "MYSQL": "mysql", " mysql ": "mysql"} {
		got, err := normalizeDriver(in)
		if err != nil || got != want {
			t.Fatalf("normalizeDriver(%q) = %q, %v (want %q)", in, got, err, want)
		}
	}
	if _, err := normalizeDriver("postgres"); err == nil {
		t.Fatal("normalizeDriver accepted postgres")
	}
	if got := formatPlaneTime(timeForTest()); !strings.HasPrefix(got, "2026-01-02 03:04:05") {
		t.Fatalf("formatPlaneTime = %q", got)
	}
}

// The SQL directory must satisfy the same routing contract as the in-memory
// one: a scale-out never moves an existing row, new keys can land on the new
// shard, and legacy fallback does not write a directory row.
func TestResolverWithSQLDirectory(t *testing.T) {
	dsn := tempDSN(t)
	topo := testTopology(t)
	d := openTestSQLDir(t, dsn, topo)
	cp, err := NewControlPlane(d, topo)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	placements := map[string]int{}
	for i := 0; i < 40; i++ {
		key := fmt.Sprintf("cht_%d", i)
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, key)
		if err != nil {
			t.Fatal(err)
		}
		placements[key] = p.Shard
	}
	plan, err := topo.AddShard(ctx, DomainMessage, "db2", d)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MigratedKeys != 0 {
		t.Fatalf("scale-out migrated rows: %+v", plan)
	}
	for key, want := range placements {
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, key)
		if err != nil || p.Shard != want || !p.FromDirectory {
			t.Fatalf("%s moved: got %d want %d err=%v", key, p.Shard, want, err)
		}
	}
	landed := 0
	for i := 0; i < 100; i++ {
		p, err := cp.Resolver.Resolve(ctx, DomainMessage, fmt.Sprintf("new_%d", i))
		if err != nil {
			t.Fatal(err)
		}
		if p.Shard == 2 {
			landed++
		}
	}
	if landed == 0 {
		t.Fatal("no new key landed on the appended shard")
	}
	if _, err := cp.Resolver.ResolveExisting(ctx, DomainMessage, "legacy_chat"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Lookup(ctx, DomainMessage, "legacy_chat"); ok {
		t.Fatal("legacy fallback wrote a directory row")
	}
	// A second process sees the same rows and count after the scale-out.
	other := openTestSQLDir(t, dsn, testTopology(t))
	for key, want := range placements {
		if shard, ok, err := other.Lookup(ctx, DomainMessage, key); err != nil || !ok || shard != want {
			t.Fatalf("other process: %s = %d, %v, %v", key, shard, ok, err)
		}
	}
	if n, err := other.ShardCount(ctx, DomainMessage); err != nil || n != 3 {
		t.Fatalf("other process count = %d, %v", n, err)
	}
}
