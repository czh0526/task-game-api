package scale

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"taskgame/internal/config"
	"taskgame/internal/plane"

	_ "modernc.org/sqlite"
)

func sqlDirectoryRuntime(t *testing.T, dsn, instance string) *Runtime {
	t.Helper()
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		InstanceID: instance,
		Domains:    config.DomainScaleConf{UserShards: 4, TaskShards: 2},
		Directory:  config.DirectoryScaleConf{Enabled: true, Driver: "sqlite", DSN: dsn},
	}, config.StoreConf{
		Driver:    "sqlite",
		DSN:       "file:primary-not-used.db",
		Shards:    2,
		ShardDSNs: []string{"db0", "db1"},
	})
	if err != nil {
		t.Fatalf("New with SQL directory: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestDefaultDirectoryStaysInMemory(t *testing.T) {
	rt, err := New(config.ScaleConf{Enabled: true, InstanceID: "api-1"}, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if _, ok := rt.ControlPlane.Directory.(*plane.InMemoryDirectory); !ok {
		t.Fatalf("default directory = %T, want *plane.InMemoryDirectory", rt.ControlPlane.Directory)
	}
	if rt.directoryDB != nil {
		t.Fatal("SQL directory was opened while Scale.Directory.Enabled is false")
	}
}

func TestSQLDirectoryIsWiredAndPersisted(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "control-plane.db")
	rt := sqlDirectoryRuntime(t, dsn, "api-a")
	ctx := context.Background()
	d, ok := rt.ControlPlane.Directory.(*plane.SQLDirectory)
	if !ok {
		t.Fatalf("directory = %T, want *plane.SQLDirectory", rt.ControlPlane.Directory)
	}
	if rt.directoryDB == nil {
		t.Fatal("Runtime did not retain the SQL directory for Close")
	}
	// The configured topology is synced into the shared tables on startup.
	for domain, want := range map[plane.Domain]int{
		plane.DomainUser:    4,
		plane.DomainTask:    2,
		plane.DomainMessage: 2,
	} {
		got, err := d.ShardCount(ctx, domain)
		if err != nil || got != want {
			t.Fatalf("ShardCount(%s) = %d, %v (want %d)", domain, got, err, want)
		}
	}
	if _, err := rt.ControlPlane.Resolver.Resolve(ctx, plane.DomainMessage, "cht_persist"); err != nil {
		t.Fatal(err)
	}
	// A completely separate connection proves the rows are on disk, not in
	// this process's memory.
	other, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var shard int
	if err := other.QueryRowContext(ctx,
		`SELECT shard FROM shard_directory WHERE domain = ? AND shard_key = ?`,
		"message", "cht_persist").Scan(&shard); err != nil {
		t.Fatalf("directory row is not persisted: %v", err)
	}
	var rows int
	if err := other.QueryRowContext(ctx, `SELECT COUNT(*) FROM shard_topology`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 8 { // 4 user + 2 task + 2 message
		t.Fatalf("shard_topology rows = %d, want 8", rows)
	}
}

func TestSQLDirectorySharedBetweenRuntimes(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "control-plane.db")
	a := sqlDirectoryRuntime(t, dsn, "api-a")
	b := sqlDirectoryRuntime(t, dsn, "api-b")
	ctx := context.Background()
	if _, err := a.ControlPlane.Resolver.Resolve(ctx, plane.DomainMessage, "cht_shared"); err != nil {
		t.Fatal(err)
	}
	if shard, ok, err := b.ControlPlane.Directory.Lookup(ctx, plane.DomainMessage, "cht_shared"); err != nil || !ok || shard < 0 || shard > 1 {
		t.Fatalf("runtime B cannot see runtime A's mapping: %d, %v, %v", shard, ok, err)
	}
	// Both processes declared the same counts; a smaller declaration in one
	// of them must not shrink the shared topology.
	if err := b.ControlPlane.Directory.EnsureShards(ctx, plane.DomainUser, 2); err != nil {
		t.Fatal(err)
	}
	if n, err := a.ControlPlane.Directory.ShardCount(ctx, plane.DomainUser); err != nil || n != 4 {
		t.Fatalf("shared count shrank to %d, %v", n, err)
	}
}

func TestSQLDirectoryCloseReleasesPool(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "control-plane.db")
	rt, err := New(config.ScaleConf{
		Enabled:   true,
		Directory: config.DirectoryScaleConf{Enabled: true, Driver: "sqlite", DSN: dsn},
	}, config.StoreConf{Driver: "sqlite", DSN: "file:unused.db", Shards: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := rt.ControlPlane.Directory.(*plane.SQLDirectory)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Lookup(context.Background(), plane.DomainMessage, "any"); err == nil {
		t.Fatal("SQL directory pool was not closed by Runtime.Close")
	}
}

func TestSQLDirectoryRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name    string
		scale   config.ScaleConf
		primary config.StoreConf
	}{
		{
			name:    "unsupported driver",
			scale:   config.ScaleConf{Enabled: true, Directory: config.DirectoryScaleConf{Enabled: true, Driver: "postgres"}},
			primary: config.StoreConf{Driver: "sqlite", DSN: "file:p.db"},
		},
		{
			name:    "dialect mismatch with inherited DSN",
			scale:   config.ScaleConf{Enabled: true, Directory: config.DirectoryScaleConf{Enabled: true, Driver: "mysql"}},
			primary: config.StoreConf{Driver: "sqlite", DSN: "file:p.db"},
		},
		{
			name:    "empty inherited DSN",
			scale:   config.ScaleConf{Enabled: true, Directory: config.DirectoryScaleConf{Enabled: true, Driver: "sqlite"}},
			primary: config.StoreConf{Driver: "sqlite"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := New(tc.scale, tc.primary)
			if err == nil {
				_ = rt.Close()
				t.Fatal("invalid directory config accepted")
			}
			if rt != nil {
				t.Fatal("New returned a runtime together with an error")
			}
		})
	}
}
