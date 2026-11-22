package svc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/config"
	"taskgame/internal/plane"
)

func directoryTestConfig(dir string, dsn string) config.Config {
	return config.Config{
		Auth:       config.AuthConf{JwtSecret: "test-jwt-secret-1234567890"},
		Store:      config.StoreConf{Driver: "sqlite", DSN: "file:" + filepath.Join(dir, "taskgame.db") + "?mode=rwc"},
		Escrow:     config.EscrowConf{InitialBalance: 1000000, Currency: "credits"},
		Webhook:    config.WebhookConf{Mode: "log", Interval: time.Second, Timeout: time.Second, MaxAttempts: 3},
		AutoSettle: config.AutoSettleConf{Enabled: false},
		Scale: config.ScaleConf{
			Enabled:    true,
			InstanceID: "api-1",
			Directory: config.DirectoryScaleConf{
				Enabled: true,
				Driver:  "sqlite",
				DSN:     dsn,
			},
		},
	}
}

// scale.New runs BEFORE the primary store opens. On a fresh checkout data/
// does not exist yet, so the control-plane DSN must create its parent
// directory and must not interfere with the primary SQLite file.
func TestSQLDirectoryWorksThroughFullServiceContext(t *testing.T) {
	dir := t.TempDir()
	controlDSN := "file:" + filepath.Join(dir, "nested", "control-plane.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	c := directoryTestConfig(dir, controlDSN)
	sc, err := NewServiceContext(c)
	if err != nil {
		t.Fatalf("NewServiceContext with SQL directory: %v", err)
	}
	defer sc.Close()
	if sc.Store == nil {
		t.Fatal("primary store was not opened")
	}
	for _, name := range []string{"taskgame.db", filepath.Join("nested", "control-plane.db")} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	d, ok := sc.Scale.ControlPlane.Directory.(*plane.SQLDirectory)
	if !ok {
		t.Fatalf("directory = %T, want *plane.SQLDirectory", sc.Scale.ControlPlane.Directory)
	}
	ctx := context.Background()
	if _, err := sc.Scale.ControlPlane.Resolver.Resolve(ctx, plane.DomainMessage, "cht_svc"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := d.Lookup(ctx, plane.DomainMessage, "cht_svc"); err != nil || !ok {
		t.Fatalf("directory row missing: ok=%v err=%v", ok, err)
	}
}

func TestServiceContextKeepsInMemoryDirectoryByDefault(t *testing.T) {
	dir := t.TempDir()
	c := directoryTestConfig(dir, "")
	c.Scale.Directory.Enabled = false
	sc, err := NewServiceContext(c)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if _, ok := sc.Scale.ControlPlane.Directory.(*plane.InMemoryDirectory); !ok {
		t.Fatalf("directory = %T, want *plane.InMemoryDirectory", sc.Scale.ControlPlane.Directory)
	}
}
