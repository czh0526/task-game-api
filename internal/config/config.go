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
//
// The shipped default (etc/taskgame.yaml) is horizontal cell sharding: N
// independent MySQL databases with identical full schemas. Each cell owns
// roughly 1/N of users, tasks, messages and every other business table; there
// is no full primary database. Cells takes precedence over DSN/Shards.
//
// Two older modes remain for backwards compatibility and for the PR #38
// prototype: a single database (DSN) and message-only sharding (Shards +
// ShardDSNs, which keeps a primary database). SQLite is available for local
// single-process debugging by setting Driver: sqlite explicitly.
type StoreConf struct {
	// Mode selects the store implementation:
	//   - "" or "cell" (default): unit-based sharding over Cells (plan A).
	//   - "domain": per-aggregate-root stores (P2). Requires Scale.Directory and
	//     Scale.Domains DSN lists; see docs/per-domain-sharding-plan.md.
	// Keeping the default empty preserves byte-for-byte behaviour of existing
	// configs: no new package is constructed unless "domain" is requested.
	Mode string `json:",optional"`
	// Driver is sqlite or mysql. When omitted, openStore falls back to sqlite
	// for backwards compatibility with old local configs.
	Driver string `json:",optional,options=sqlite|mysql"`
	DSN    string `json:",optional"`
	// Cells enables horizontal, unit-based sharding. One DSN per cell, in cell
	// order. They may be different MySQL instances (different hosts/ports) or
	// different schemas on one server. Every API process must use the same
	// ordered list. When set, DSN and Shards are ignored.
	Cells []string `json:",optional"`
	// Shards > 1 enables the older message-only sharding prototype: message
	// bodies are routed by chat into N independent databases while a primary
	// database keeps the other tables. 0 or 1 keeps single-database behaviour.
	Shards int `json:",optional,default=0"`
	// ShardDriver selects the shard SQL dialect: sqlite (one file per shard)
	// or mysql (network databases, one per API process that connects).
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
	Enabled      bool                  `json:",optional"`
	PubSub       string                `json:",optional"` // local (default) | shared | redis
	InstanceID   string                `json:",optional"` // identifies this API process on the bus
	Redis        RedisScaleConf        `json:",optional"` // used only when PubSub=redis
	Projection   ProjectionScaleConf   `json:",optional"` // shared-lease outbox projection
	Directory    DirectoryScaleConf    `json:",optional"` // SQL-backed routing directory
	Domains      DomainScaleConf       `json:",optional"`
	Archive      ArchiveScaleConf      `json:",optional"`
	Search       SearchScaleConf       `json:",optional"`
	Compensation CompensationScaleConf `json:",optional"` // cross-domain saga reconciler
}

// CompensationScaleConf enables the durable saga reconciler for cross-domain
// operations (currently fund_hold). The log lives in the control-plane DB, so
// a single process or several can reconcile without double-compensating.
type CompensationScaleConf struct {
	Enabled  bool          `json:",optional"`
	Interval time.Duration `json:",optional,default=5s"`
	LeaseTTL time.Duration `json:",optional,default=30s"`
	MaxRetry int           `json:",optional,default=5"`
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
	Enabled    bool   `json:",optional"`
	Driver     string `json:",optional"` // sqlite | mysql; default: inherit Store.Driver
	DSN        string `json:",optional"` // default: inherit Store.DSN
	ReplicaDSN string `json:",optional"` // optional read replica for Lookup; writes always use DSN
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

// DomainScaleConf declares, per aggregate-root domain, the ordered DSN list
// and shard count used by Store.Mode=domain. Each domain may point at a
// different set of databases; a deployment that wants logical domains without
// physical separation simply repeats the same cell DSNs for all three.
//
// UserShards / TaskShards are the legacy skeleton counts (their repositories
// used to live in the primary store). They are still parsed for backwards
// compatibility but DSNs, when present, take precedence over them.
type DomainScaleConf struct {
	UserShards int            `json:",optional"`
	TaskShards int            `json:",optional"`
	User       DomainDSNsConf `json:",optional"`
	Task       DomainDSNsConf `json:",optional"`
	Message    DomainDSNsConf `json:",optional"`
}

// DomainDSNsConf is one domain's physical placement. Shards is the declared
// count (used as the fallback hash modulus for legacy keys); DSNs[i] is the
// DSN of shard i. A missing DSNs list falls back to the legacy skeleton path
// so existing configs keep loading unchanged.
type DomainDSNsConf struct {
	Shards int      `json:",optional"`
	DSNs   []string `json:",optional"`
}

// Validate checks that a domain's DSN list and shard count agree. An empty
// config is valid: it means "not wired", and registerDomains keeps the legacy
// placeholder behaviour.
func (d DomainDSNsConf) Validate(domain string) error {
	if d.Shards < 0 {
		return fmt.Errorf("Scale.Domains.%s.Shards cannot be negative", domain)
	}
	if len(d.DSNs) == 0 {
		return nil
	}
	if d.Shards > 0 && len(d.DSNs) != d.Shards {
		return fmt.Errorf("Scale.Domains.%s: %d DSNs but Shards=%d", domain, len(d.DSNs), d.Shards)
	}
	for i, dsn := range d.DSNs {
		if dsn == "" {
			return fmt.Errorf("Scale.Domains.%s.DSNs[%d] is empty", domain, i)
		}
	}
	return nil
}

type ArchiveScaleConf struct {
	Enabled   bool          `json:",optional"`
	Dir       string        `json:",optional"`
	HotWindow time.Duration `json:",optional"`
}

// SearchScaleConf configures the read-side task index. Driver "memory" keeps
// the in-process index (default, tests); "elasticsearch" / "opensearch" use
// the HTTP index. CDC makes task writes append to a durable change stream
// which a background projector drains; RebuildInterval periodically re-enqueues
// every live task as a safety net (full re-index / repair).
type SearchScaleConf struct {
	Enabled         bool          `json:",optional"`
	Driver          string        `json:",optional"` // memory | elasticsearch | opensearch
	URL             string        `json:",optional"` // ES/OS base URL, e.g. http://localhost:9200
	Index           string        `json:",optional"` // index name; default taskgame_tasks
	Username        string        `json:",optional"`
	Password        string        `json:",optional"`
	APIKey          string        `json:",optional"`
	Refresh         bool          `json:",optional"` // wait_for refresh on writes
	CDC             bool          `json:",optional"` // record task writes into the change stream
	Interval        time.Duration `json:",optional,default=5s"`
	RebuildInterval time.Duration `json:",optional,default=0"` // 0 disables periodic rebuild
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
	for name, d := range map[string]DomainDSNsConf{
		"User": c.Domains.User, "Task": c.Domains.Task, "Message": c.Domains.Message,
	} {
		if err := d.Validate(name); err != nil {
			return err
		}
	}
	if c.Archive.Enabled && c.Archive.Dir == "" {
		return fmt.Errorf("Scale.Archive.Dir is required when archive is enabled")
	}
	if c.Archive.HotWindow < 0 {
		return fmt.Errorf("Scale.Archive.HotWindow cannot be negative")
	}
	switch c.Search.Driver {
	case "", "memory", "elasticsearch", "opensearch":
	default:
		return fmt.Errorf("Scale.Search.Driver must be memory, elasticsearch or opensearch, got %q", c.Search.Driver)
	}
	if c.Search.Driver == "elasticsearch" || c.Search.Driver == "opensearch" {
		if c.Search.URL == "" {
			return fmt.Errorf("Scale.Search.URL is required for %s", c.Search.Driver)
		}
		if c.Search.Interval < 0 || c.Search.RebuildInterval < 0 {
			return fmt.Errorf("Scale.Search durations cannot be negative")
		}
	}
	if c.Search.Username != "" && c.Search.APIKey != "" {
		return fmt.Errorf("Scale.Search.Username and APIKey are mutually exclusive")
	}
	if c.Compensation.Interval < 0 || c.Compensation.LeaseTTL < 0 {
		return fmt.Errorf("Scale.Compensation durations cannot be negative")
	}
	if c.Compensation.MaxRetry < 0 {
		return fmt.Errorf("Scale.Compensation.MaxRetry cannot be negative")
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
