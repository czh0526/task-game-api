package config

import (
	"os"
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

// The Scale example configs were removed when etc/ was reduced to the single
// default file. Keep their parsing coverage by writing inline YAML to a temp
// file and loading it through the same conf.Load path as the real process.
func loadInline(t *testing.T, body string) Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inline.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write inline config: %v", err)
	}
	var c Config
	if err := conf.Load(path, &c); err != nil {
		t.Fatalf("conf.Load(inline): %v", err)
	}
	return c
}

// conf.Load requires every non-optional section, so inline configs share a
// complete header and only differ in the Scale block under test.
const inlineHeader = `
Name: taskgame-api
Host: 0.0.0.0
Port: 8891
Log:
  Mode: console
  Encoding: plain
  Level: info
  Stat: false
Auth:
  JwtSecret: dev-jwt-secret-change-me-32b
  TokenTTL: 24h
  ApiKeys:
    - Key: dev-publisher-key
      UserId: usr_publisher
Store:
  Driver: mysql
  DSN: "root:123456@tcp(127.0.0.1:3306)/taskgame?charset=utf8mb4"
Escrow:
  InitialBalance: 1000000
  Currency: credits
  ArbiterUserIds:
    - usr_arbiter
Webhook:
  Mode: log
  Interval: 2s
  Timeout: 5s
  MaxAttempts: 3
AutoSettle:
  Enabled: true
  Interval: 30s
  OpenTimeout: 72h
  StartTimeout: 72h
  SeatTimeout: 72h
  WinnerSelectHours: 72
Scale:
`

const skeletonYAML = inlineHeader + `  Enabled: true
  PubSub: shared
  InstanceID: api-a
  Directory:
    Enabled: true
    Driver: sqlite
    DSN: "file:data/control-plane.db"
  Domains:
    UserShards: 4
    TaskShards: 2
  Archive:
    Enabled: true
    Dir: "data/blobs"
    HotWindow: 2160h
  Search:
    Enabled: true
    Driver: memory
  Projection:
    Enabled: true
    Interval: 5s
    LeaseTTL: 30s
`

const redisYAML = inlineHeader + `  Enabled: true
  PubSub: redis
  InstanceID: api-a
  Redis:
    Addr: "127.0.0.1:6379"
    Channel: "taskgame.signals"
    Timeout: 2s
  Directory:
    Enabled: true
    Driver: sqlite
    DSN: "file:data/control-plane.db"
  Projection:
    Enabled: true
    Interval: 5s
    LeaseTTL: 30s
    Channel: "taskgame.outbox"
`

func TestDefaultConfigIsThreeMySQLCells(t *testing.T) {
	c := loadExample(t, "taskgame.yaml")
	if c.Store.Driver != "mysql" {
		t.Fatalf("Store.Driver = %q, want mysql", c.Store.Driver)
	}
	if len(c.Store.Cells) != 3 {
		t.Fatalf("Store.Cells = %d entries, want 3 independent databases", len(c.Store.Cells))
	}
	seen := make(map[string]bool, len(c.Store.Cells))
	for i, dsn := range c.Store.Cells {
		if dsn == "" || seen[dsn] {
			t.Fatalf("Store.Cells[%d] is empty or duplicated: %q", i, dsn)
		}
		seen[dsn] = true
	}
	// Cell mode must not declare a full primary database or message shards.
	if c.Store.DSN != "" {
		t.Fatalf("cell mode must not keep a primary DSN, got %q", c.Store.DSN)
	}
	if c.Store.Shards != 0 || len(c.Store.ShardDSNs) != 0 {
		t.Fatalf("cell mode must not enable message-only sharding: %+v", c.Store)
	}
	// The default config enables only the cross-process SSE signal bus.
	// Other Scale sub-systems stay off in cell mode.
	if !c.Scale.Enabled || c.Scale.PubSub != "redis" {
		t.Fatalf("default config must enable redis pub/sub, got %+v", c.Scale)
	}
	if c.Scale.Redis.Addr != "127.0.0.1:6379" || c.Scale.Redis.Channel == "" {
		t.Fatalf("default redis endpoint not configured: %+v", c.Scale.Redis)
	}
	// InstanceID is optional. An empty value makes each API process generate
	// its own unique id, so two instances copied from this file do not drop
	// each other's SSE signals as echoes.
	if c.Scale.Projection.Enabled || c.Scale.Directory.Enabled ||
		c.Scale.Domains.UserShards != 0 || c.Scale.Archive.Enabled || c.Scale.Search.Enabled {
		t.Fatalf("default config must not enable the legacy skeleton sub-systems: %+v", c.Scale)
	}
	if err := c.Scale.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestScaleSkeletonConfigLoads(t *testing.T) {
	c := loadInline(t, skeletonYAML)
	if !c.Scale.Enabled {
		t.Fatal("Scale.Enabled should be true")
	}
	if err := c.Scale.Validate(); err != nil {
		t.Fatalf("config is invalid: %v", err)
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

func TestScaleRedisConfigLoads(t *testing.T) {
	c := loadInline(t, redisYAML)
	if !c.Scale.Enabled || c.Scale.PubSub != "redis" {
		t.Fatalf("redis pub/sub not enabled: %+v", c.Scale)
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
		t.Fatalf("config is invalid: %v", err)
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
