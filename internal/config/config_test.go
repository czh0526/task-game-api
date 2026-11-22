package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/conf"
)

func loadExample(t *testing.T, name string) Config {
	t.Helper()
	var c Config
	path := filepath.Join("..", "..", "etc", name)
	if err := conf.Load(path, &c); err != nil {
		t.Fatalf("conf.Load(%s): %v", name, err)
	}
	return c
}

func TestDefaultConfigLeavesScaleDisabled(t *testing.T) {
	c := loadExample(t, "taskgame.yaml")
	if c.Scale.Enabled {
		t.Fatal("the default config must not enable the skeleton")
	}
	if err := c.Scale.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScaleSkeletonExampleLoads(t *testing.T) {
	c := loadExample(t, "taskgame.scale-skeleton.yaml")
	if !c.Scale.Enabled {
		t.Fatal("Scale.Enabled should be true in the example")
	}
	if err := c.Scale.Validate(); err != nil {
		t.Fatalf("example config is invalid: %v", err)
	}
	if c.Scale.PubSub != "shared" || c.Scale.InstanceID != "api-a" {
		t.Fatalf("pub/sub config not parsed: %+v", c.Scale)
	}
	if c.Scale.Domains.UserShards != 4 || c.Scale.Domains.TaskShards != 2 {
		t.Fatalf("domain counts not parsed: %+v", c.Scale.Domains)
	}
	if c.Scale.Archive.HotWindow != 2160*time.Hour {
		t.Fatalf("HotWindow = %v", c.Scale.Archive.HotWindow)
	}
	if c.Store.DSN == "" {
		t.Fatal("Store.DSN missing")
	}
	if !c.Scale.Projection.Enabled || c.Scale.Projection.Interval != 5*time.Second ||
		c.Scale.Projection.LeaseTTL != 30*time.Second {
		t.Fatalf("projection config not parsed: %+v", c.Scale.Projection)
	}
	if !c.Scale.Directory.Enabled || c.Scale.Directory.Driver != "sqlite" || c.Scale.Directory.DSN == "" {
		t.Fatalf("SQL directory config not parsed: %+v", c.Scale.Directory)
	}
}

func TestScaleRedisExampleLoads(t *testing.T) {
	c := loadExample(t, "taskgame.scale-redis.yaml")
	if !c.Scale.Enabled || c.Scale.PubSub != "redis" {
		t.Fatalf("redis pub/sub not enabled in example: %+v", c.Scale)
	}
	if c.Scale.Redis.Addr == "" || c.Scale.Redis.Channel == "" {
		t.Fatalf("redis endpoint not parsed: %+v", c.Scale.Redis)
	}
	if c.Scale.Redis.Timeout != 2*time.Second {
		t.Fatalf("Timeout = %v", c.Scale.Redis.Timeout)
	}
	if !c.Scale.Projection.Enabled || c.Scale.Projection.Channel == "" {
		t.Fatalf("projection config not parsed: %+v", c.Scale.Projection)
	}
	if !c.Scale.Directory.Enabled || c.Scale.Directory.DSN == "" {
		t.Fatalf("SQL directory config not parsed: %+v", c.Scale.Directory)
	}
	if err := c.Scale.Validate(); err != nil {
		t.Fatalf("example config is invalid: %v", err)
	}
}

func TestDirectoryDriverValidation(t *testing.T) {
	for _, driver := range []string{"", "sqlite", "mysql"} {
		c := ScaleConf{Enabled: true, Directory: DirectoryScaleConf{Enabled: true, Driver: driver, DSN: "file:p.db"}}
		if err := c.Validate(); err != nil {
			t.Fatalf("driver %q rejected: %v", driver, err)
		}
	}
	c := ScaleConf{Enabled: true, Directory: DirectoryScaleConf{Enabled: true, Driver: "postgres"}}
	if err := c.Validate(); err == nil {
		t.Fatal("unsupported directory driver accepted")
	}
}
