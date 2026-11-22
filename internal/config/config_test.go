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
	if err := c.Scale.Validate(); err != nil {
		t.Fatalf("example config is invalid: %v", err)
	}
}
