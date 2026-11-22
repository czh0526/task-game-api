package svc

import (
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/config"
)

func projectionTestConfig(dir string, enabled bool) config.Config {
	return config.Config{
		Auth:       config.AuthConf{JwtSecret: "test-jwt-secret-1234567890"},
		Store:      config.StoreConf{Driver: "sqlite", DSN: "file:" + filepath.Join(dir, "taskgame.db") + "?mode=rwc"},
		Escrow:     config.EscrowConf{InitialBalance: 1000000, Currency: "credits"},
		Webhook:    config.WebhookConf{Mode: "log", Interval: time.Second, Timeout: time.Second, MaxAttempts: 3},
		AutoSettle: config.AutoSettleConf{Enabled: false},
		Scale: config.ScaleConf{
			Enabled:    enabled,
			PubSub:     "shared",
			InstanceID: "api-1",
			Projection: config.ProjectionScaleConf{
				Enabled: enabled, Interval: time.Hour, LeaseTTL: time.Minute,
			},
		},
	}
}

// The narrow type assertion in NewServiceContext must succeed for the real
// *sqlstore.DB, otherwise the worker would silently never be built.
func TestProjectionWorkerIsWiredWhenEnabled(t *testing.T) {
	sc, err := NewServiceContext(projectionTestConfig(t.TempDir(), true))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if sc.projectionWorker == nil {
		t.Fatal("projection worker was not wired")
	}
	if sc.Scale == nil || sc.Scale.Lease == nil || sc.Scale.Bus == nil {
		t.Fatal("lease or bus missing from the runtime")
	}
}

func TestDefaultBuildHasNoProjectionWorker(t *testing.T) {
	sc, err := NewServiceContext(projectionTestConfig(t.TempDir(), false))
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	if sc.projectionWorker != nil || sc.Scale != nil {
		t.Fatal("PR #38 default must not build the projection worker or skeleton")
	}
}
