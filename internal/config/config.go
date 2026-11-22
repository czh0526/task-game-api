package config

import (
	"fmt"
	"time"

	"github.com/zeromicro/go-zero/rest"
)

// Config is the process config loaded from etc/taskgame.yaml.
type Config struct {
	rest.RestConf
	Auth       AuthConf
	Store      StoreConf
	Scale      ScaleConf
	Escrow     EscrowConf
	Webhook    WebhookConf
	AutoSettle AutoSettleConf
}

// AuthConf is HS256 JWT verification plus static X-Api-Key mappings.
type AuthConf struct {
	JwtSecret string
	ApiKeys   []ApiKeyConf `json:",optional"`
	// TokenTTL is the lifetime of JWTs from register and login.
	// Zero means 24h. Claims match `make token`: sub, iat, exp, HS256.
	TokenTTL time.Duration `json:",optional"`
}

// ApiKeyConf maps one X-Api-Key header value to a user id.
type ApiKeyConf struct {
	Key    string
	UserId string
}

// StoreConf selects the Store implementation.
// sqlite is the single-writer default. mysql is the shared database for more than one API process.
// The shard driver is independent of the primary driver: the primary store can be
// SQLite or MySQL while each shard database is a SQLite file or a MySQL DSN.
type StoreConf struct {
	Driver string `json:",default=sqlite,options=sqlite|mysql"`
	DSN    string
	// Shards > 1 enables the message-sharding prototype: message bodies are
	// routed by chat into N independent databases. 0 or 1 keeps the plain
	// single-database behaviour.
	Shards int `json:",optional,default=0"`
	// ShardDriver selects the shard SQL dialect: sqlite (default, one file per
	// shard) or mysql (network databases, one per API process that connects).
	ShardDriver string `json:",optional,default=sqlite"`
	// ShardDSNs is the preferred form: one DSN per shard, in shard order. A
	// MySQL list may point at different hosts. ShardDSN is kept as a shorthand
	// template containing %d, expanded into one DSN per shard.
	ShardDSN  string   `json:",optional"`
	ShardDSNs []string `json:",optional"`
}

// ScaleConf is the opt-in multi-database skeleton (P1 remainder / P2 / P3).
// With Enabled=false the process behaves exactly like PR #38: no new package
// is constructed, no config file needs a Scale section.
type ScaleConf struct {
	Enabled    bool                `json:",optional"`
	PubSub     string              `json:",optional"` // local (default) | shared | redis
	InstanceID string              `json:",optional"` // identifies this API process on the bus
	Redis      RedisScaleConf      `json:",optional"` // used only when PubSub=redis
	Projection ProjectionScaleConf `json:",optional"` // shared-lease outbox projection
	Directory  DirectoryScaleConf  `json:",optional"` // SQL-backed routing directory
	Domains    DomainScaleConf     `json:",optional"`
	Archive    ArchiveScaleConf    `json:",optional"`
	Search     SearchScaleConf     `json:",optional"`
}

// ProjectionScaleConf turns message_outbox crash recovery into a coordinated
// multi-process task: a shared Lease prevents duplicate projection and a Bus
// wakes the other processes when a new outbox row appears. With Enabled=false
// Recover stays the original single-process scan.
type ProjectionScaleConf struct {
	Enabled  bool          `json:",optional"`
	Interval time.Duration `json:",optional,default=5s"`  // periodic fallback scan
	LeaseTTL time.Duration `json:",optional,default=30s"` // crash takeover window
	Channel  string        `json:",optional"`             // default: taskgame.outbox
}

// DirectoryScaleConf persists the routing directory and shard topology in SQL
// so every API process sees the same authoritative key -> shard mapping. With
// Enabled=false the control plane stays process-local (InMemoryDirectory) and
// loses its mappings on restart.
//
// Driver / DSN default to Store.Driver / Store.DSN, i.e. the primary database
// doubles as the tiny control plane (only three small tables are added). A
// multi-machine deployment should point DSN at a shared MySQL database that
// every process can reach; an explicit Driver must match the DSN's dialect.
type DirectoryScaleConf struct {
	Enabled bool   `json:",optional"`
	Driver  string `json:",optional"` // sqlite | mysql; default: inherit Store.Driver
	DSN     string `json:",optional"` // default: inherit Store.DSN
}

// RedisScaleConf is the real cross-process signal bus. Every API process of
// one deployment must point at the same Redis and the same Channel.
type RedisScaleConf struct {
	Addr     string        `json:",optional"`
	Username string        `json:",optional"`
	Password string        `json:",optional"`
	DB       int           `json:",optional"`
	Channel  string        `json:",optional"` // default: taskgame.signals
	Timeout  time.Duration `json:",optional,default=2s"`
}

// DomainScaleConf declares how many databases each aggregate-root domain would
// have. User / Task counts are scaffolding only: their repositories still live
// in the primary store. Message is taken from Store.Shards, NOT from here.
type DomainScaleConf struct {
	UserShards int `json:",optional"`
	TaskShards int `json:",optional"`
}

type ArchiveScaleConf struct {
	Enabled   bool          `json:",optional"`
	Dir       string        `json:",optional"`
	HotWindow time.Duration `json:",optional"`
}

type SearchScaleConf struct {
	Enabled bool   `json:",optional"`
	Driver  string `json:",optional"` // memory (default)
}

func (c ScaleConf) Validate() error {
	if !c.Enabled {
		return nil
	}
	switch c.PubSub {
	case "", "local", "shared", "redis":
	default:
		return fmt.Errorf("Scale.PubSub must be local, shared or redis, got %q", c.PubSub)
	}
	if c.PubSub == "redis" && c.Redis.Addr == "" {
		return fmt.Errorf("Scale.Redis.Addr is required when Scale.PubSub is redis")
	}
	if c.Redis.Timeout < 0 {
		return fmt.Errorf("Scale.Redis.Timeout cannot be negative")
	}
	if c.Projection.Interval < 0 || c.Projection.LeaseTTL < 0 {
		return fmt.Errorf("Scale.Projection durations cannot be negative")
	}
	switch c.Directory.Driver {
	case "", "sqlite", "mysql":
	default:
		return fmt.Errorf("Scale.Directory.Driver must be sqlite or mysql, got %q", c.Directory.Driver)
	}
	if c.Domains.UserShards < 0 || c.Domains.TaskShards < 0 {
		return fmt.Errorf("Scale.Domains shard counts cannot be negative")
	}
	if c.Archive.Enabled && c.Archive.Dir == "" {
		return fmt.Errorf("Scale.Archive.Dir is required when archive is enabled")
	}
	if c.Archive.HotWindow < 0 {
		return fmt.Errorf("Scale.Archive.HotWindow cannot be negative")
	}
	switch c.Search.Driver {
	case "", "memory":
	default:
		return fmt.Errorf("Scale.Search.Driver %q is not implemented in the skeleton", c.Search.Driver)
	}
	return nil
}

// EscrowConf is the simulated credit ledger. No real payment provider is called.
type EscrowConf struct {
	InitialBalance int64  `json:",default=1000000"`
	Currency       string `json:",default=credits"`
	// ArbiterUserIds may call POST /tasks/{taskId}/arbitrate. Publisher and executor are not arbiters.
	ArbiterUserIds []string `json:",optional"`
}

// WebhookConf controls the outbound delivery worker.
// Mode log records the signature and does not POST. Mode post delivers HTTP.
type WebhookConf struct {
	Mode        string        `json:",default=log,options=log|post"`
	Interval    time.Duration `json:",default=2s"`
	Timeout     time.Duration `json:",default=5s"`
	MaxAttempts int           `json:",default=3"`
}

// AutoSettleConf runs review auto-settle and the workflow timeouts.
// Zero timeout durations mean 72h, the same window as review_hours.
type AutoSettleConf struct {
	Enabled           bool          `json:",default=true"`
	Interval          time.Duration `json:",default=30s"`
	OpenTimeout       time.Duration `json:",default=72h"`
	StartTimeout      time.Duration `json:",default=72h"`
	SeatTimeout       time.Duration `json:",default=72h"`
	WinnerSelectHours int           `json:",default=72"`
}
