// Package scale assembles the P1-remainder / P2 / P3 skeleton into one
// lifecycle object owned by svc.ServiceContext.
//
// SKELETON BOUNDARY: New is a no-op unless Scale.Enabled is true. Even when
// enabled, no existing table is moved and no API response changes. The pieces
// here are contracts plus runnable in-memory/local implementations that let
// the next stage replace one component at a time.
package scale

import (
	"context"
	"fmt"
	"strings"
	"time"

	"taskgame/internal/archive"
	"taskgame/internal/compensation"
	"taskgame/internal/config"
	"taskgame/internal/id"
	"taskgame/internal/notify"
	"taskgame/internal/plane"
	"taskgame/internal/projection"
	"taskgame/internal/search"
	"taskgame/internal/store/shard"
)

const defaultSearchIndex = "taskgame_tasks"

// TODOs is the explicit list of work this skeleton does NOT do. Keeping it in
// code (rather than only in docs) makes the boundary hard to forget.
//
// The original #1 "replace MemoryTransport with Redis pub/sub" is DONE:
// see notify.RedisTransport (redis.go). The original #2 "shared lease / event
// bus for message_outbox projection" is DONE too: see internal/projection.
// The original #3 "SQL-backed plane.Directory over plane.Schema" is DONE:
// see plane.SQLDirectory, selected by Scale.Directory.Enabled.
// DONE as of the productionization pass: per-domain stores, global_unique +
// Snowflake, archive execution, and the ES/OpenSearch index with a CDC
// projector (see internal/search). NATS/Kafka remain optional transports.
// DONE in the productionization pass: webhook delivery bodies are externalized
// and emit() de-amplified (one body + N refs); messages keep the newest page
// online. Remaining large-column ref migrations (avatar / submission payload)
// are listed below; docs/remaining-work-backlog.md records the completed items
// and the remaining dashboard verification boundary.
var TODOs = []string{
	"P3: add avatar_ref / payload_ref migrations for users avatars and submission payloads",
}

// Runtime is the assembled skeleton. Every field may be nil when its feature
// is off; callers must check.
type Runtime struct {
	cfg            config.ScaleConf
	Fanout         notify.Fanout
	ControlPlane   *plane.ControlPlane
	DomainRouter   *shard.DomainRouter
	Blobs          archive.BlobStore
	Archiver       *archive.Archiver
	TaskIndex      search.TaskIndex
	Projector      *search.Projector // legacy synchronous projector (kept for compatibility)
	CDC            *search.CDCProjector
	SearchLog      search.ChangeLog
	searchRecorder *search.LogRecorder
	Lease          projection.Lease
	Bus            projection.Bus
	Compensation   *compensation.Coordinator
	compLog        compensation.Log
	transport      notify.Transport
	remote         *notify.RemoteFanout
	directoryDB    *plane.SQLDirectory    // non-nil when Scale.Directory.Enabled
	Fence          plane.FenceStore       // maintenance write guard, co-located with directory
	CursorEpoch    plane.CursorEpochStore // per-chat cursor generation for cutover invalidation
	instanceID     string
}

// New returns (nil, nil) when the skeleton is disabled, so the caller can
// assign it unconditionally and keep the PR #38 path byte-for-byte identical.
func New(c config.ScaleConf, primary config.StoreConf) (*Runtime, error) {
	if !c.Enabled {
		return nil, nil
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	instanceID := c.InstanceID
	if instanceID == "" {
		instanceID = id.New("api_")
	}
	rt := &Runtime{cfg: c, instanceID: instanceID}

	// P1: signal fan-out.
	//   local  - single process, no transport at all (PR #38 behaviour)
	//   shared - in-process bus, emulates several API processes in one binary
	//   redis  - REAL cross-process transport; every API process must point at
	//            the same Redis. NewRedisTransport PINGs, so a broken bus
	//            fails at startup instead of silently degrading to one node.
	switch c.PubSub {
	case "shared":
		rt.transport = notify.NewMemoryTransport()
	case "redis":
		tr, err := notify.NewRedisTransport(notify.RedisConfig{
			Addr:     c.Redis.Addr,
			Username: c.Redis.Username,
			Password: c.Redis.Password,
			DB:       c.Redis.DB,
			Channel:  c.Redis.Channel,
			Timeout:  c.Redis.Timeout,
		})
		if err != nil {
			return nil, err
		}
		rt.transport = tr
	default:
		rt.transport = notify.LocalTransport{}
	}
	fanout, err := notify.NewRemoteFanout(instanceID, rt.transport)
	if err != nil {
		return nil, err
	}
	rt.remote, rt.Fanout = fanout, fanout

	// P1 #2: shared-lease outbox projection + wake-up bus.
	if c.Projection.Enabled {
		if err := rt.wireProjection(c); err != nil {
			_ = rt.Close()
			return nil, err
		}
	}

	// P2: global control plane + three-domain router.
	topo := plane.NewTopology()
	if err := registerDomains(topo, c, primary); err != nil {
		_ = rt.Close()
		return nil, err
	}
	dir, err := rt.wireDirectory(c, primary, topo)
	if err != nil {
		_ = rt.Close()
		return nil, err
	}
	if rt.CursorEpoch == nil {
		// No persistent directory: keep the epoch registry process-local.
		rt.CursorEpoch = plane.NewMemoryCursorEpoch()
	}
	cp, err := plane.NewControlPlane(dir, topo)
	if err != nil {
		_ = rt.Close()
		return nil, err
	}
	// P0: with a persistent control plane, upgrade the in-memory defaults to
	// the real global unique index and Snowflake generator. Both write to the
	// SAME control-plane database (or inherit its pool), so email claims and
	// worker-id allocation survive restarts and are shared across processes.
	if rt.directoryDB != nil {
		unique, uerr := plane.NewSQLUnique(context.Background(), rt.directoryDB.DB(), c.Directory.Driver)
		if uerr != nil {
			_ = rt.Close()
			return nil, fmt.Errorf("global unique index: %w", uerr)
		}
		gen, gerr := plane.NewSnowflakeIDGenerator(context.Background(), unique, instanceID)
		if gerr != nil {
			_ = rt.Close()
			return nil, fmt.Errorf("snowflake generator: %w", gerr)
		}
		cp.Unique = unique
		cp.IDs = gen
	}
	rt.ControlPlane = cp
	chatShards := primary.Shards
	if chatShards < 1 {
		chatShards = 1
	}
	dr, err := shard.NewDomainRouter(cp, shard.NewRouter(chatShards))
	if err != nil {
		_ = rt.Close()
		return nil, err
	}
	rt.DomainRouter = dr

	// P0: cross-domain saga reconciler. It reuses the control-plane pool when
	// a persistent directory is configured; otherwise it stays off because a
	// process-local directory cannot coordinate a physical domain split.
	if c.Compensation.Enabled && rt.directoryDB != nil {
		compDir := c.Directory.Driver
		compLog, lerr := compensation.NewSQLLog(context.Background(), rt.directoryDB.DB(), compDir)
		if lerr != nil {
			_ = rt.Close()
			return nil, fmt.Errorf("compensation log: %w", lerr)
		}
		rt.compLog = compLog
		rt.Compensation = compensation.NewCoordinator(compLog, instanceID,
			compensation.WithLeaseTTL(c.Compensation.LeaseTTL),
			compensation.WithMaxRetries(c.Compensation.MaxRetry))
	}

	// P3: cold tier / large objects.
	if c.Archive.Enabled {
		blobs, err := archive.NewFileBlobStore(c.Archive.Dir)
		if err != nil {
			_ = rt.Close()
			return nil, err
		}
		rt.Blobs = blobs
		policies := append([]archive.RetentionPolicy(nil), archive.DefaultPolicies...)
		if c.Archive.HotWindow > 0 {
			for i := range policies {
				policies[i].HotWindow = c.Archive.HotWindow
			}
		}
		rt.Archiver = archive.NewArchiver(blobs, policies)
	}

	// P3: read-side index for cross-shard listings. Memory is the default;
	// elasticsearch/opensearch use the same REST dialect through HTTPTaskIndex.
	if c.Search.Enabled {
		if err := rt.wireSearch(c); err != nil {
			_ = rt.Close()
			return nil, err
		}
	}
	return rt, nil
}

// DomainDSNs returns the physical DSN list each domain should use. Explicit
// Scale.Domains.<Name>.DSNs always win. In Store.Mode=domain a domain without
// explicit DSNs inherits Store.Cells (or Store.DSN), which is how a staged
// rollout keeps untouched domains co-located. Outside domain mode the map is
// empty so the legacy skeleton path stays byte-for-byte compatible.
func DomainDSNs(c config.ScaleConf, primary config.StoreConf) map[plane.Domain][]string {
	out := make(map[plane.Domain][]string, 3)
	if c.Domains.User.DSNs != nil && len(c.Domains.User.DSNs) > 0 {
		out[plane.DomainUser] = c.Domains.User.DSNs
	}
	if c.Domains.Task.DSNs != nil && len(c.Domains.Task.DSNs) > 0 {
		out[plane.DomainTask] = c.Domains.Task.DSNs
	}
	if c.Domains.Message.DSNs != nil && len(c.Domains.Message.DSNs) > 0 {
		out[plane.DomainMessage] = c.Domains.Message.DSNs
	}
	if primary.Mode != "domain" {
		return out
	}
	base := primary.Cells
	if len(base) == 0 && primary.DSN != "" {
		base = []string{primary.DSN}
	}
	if len(base) == 0 {
		return out
	}
	for _, pair := range []struct {
		domain plane.Domain
		conf   config.DomainDSNsConf
	}{
		{plane.DomainUser, c.Domains.User},
		{plane.DomainTask, c.Domains.Task},
		{plane.DomainMessage, c.Domains.Message},
	} {
		if _, ok := out[pair.domain]; ok {
			continue
		}
		dsns := base
		if pair.conf.Shards > 0 && pair.conf.Shards < len(dsns) {
			dsns = dsns[:pair.conf.Shards]
		}
		out[pair.domain] = dsns
	}
	return out
}

// registerDomains seeds the topology. Each domain prefers the real DSN list
// declared under Scale.Domains.<Name>.DSNs; when a domain has none, it falls
// back to the legacy skeleton source so existing configs keep loading:
//   - Message: Store.Shards / ShardDSNs (the PR #38 message-shard prototype)
//   - User/Task: skeleton:// placeholders (their repos live in the primary store)
//
// Using the real DSN list is what makes Store.Mode=domain route to distinct
// physical databases; it does not, by itself, move any table or change any
// API response.
func registerDomains(topo *plane.Topology, c config.ScaleConf, primary config.StoreConf) error {
	wired := DomainDSNs(c, primary)
	n := primary.Shards
	if n < 1 {
		n = 1
	}
	message := wired[plane.DomainMessage]
	if len(message) == 0 {
		message = make([]string, n)
		for i := range message {
			switch {
			case i < len(primary.ShardDSNs) && primary.ShardDSNs[i] != "":
				message[i] = primary.ShardDSNs[i]
			case strings.Contains(primary.ShardDSN, "%d"):
				message[i] = fmt.Sprintf(primary.ShardDSN, i)
			default:
				message[i] = fmt.Sprintf("skeleton://message/shard-%d", i)
			}
		}
	}
	if err := topo.Register(plane.DomainMessage, effectiveDSNs(c.Domains.Message, plane.DomainMessage, message)); err != nil {
		return err
	}
	for _, d := range []struct {
		domain plane.Domain
		count  int
	}{
		{plane.DomainUser, c.Domains.UserShards},
		{plane.DomainTask, c.Domains.TaskShards},
	} {
		count := d.count
		if count < 1 {
			count = 1
		}
		placeholder := wired[d.domain]
		if len(placeholder) == 0 {
			placeholder = make([]string, count)
			for i := range placeholder {
				placeholder[i] = fmt.Sprintf("skeleton://%s/shard-%d (not wired)", d.domain, i)
			}
		}
		var declared config.DomainDSNsConf
		switch d.domain {
		case plane.DomainUser:
			declared = c.Domains.User
		case plane.DomainTask:
			declared = c.Domains.Task
		}
		if err := topo.Register(d.domain, effectiveDSNs(declared, d.domain, placeholder)); err != nil {
			return err
		}
	}
	return nil
}

// effectiveDSNs returns the declared DSN list when present, otherwise the
// legacy fallback. A declared shard count with no DSNs still wins on count:
// it lets a domain reserve N shards before their DSNs are assigned.
func effectiveDSNs(d config.DomainDSNsConf, domain plane.Domain, fallback []string) []string {
	if len(d.DSNs) > 0 {
		return d.DSNs
	}
	if d.Shards > 0 && len(fallback) != d.Shards {
		out := make([]string, d.Shards)
		for i := range out {
			if i < len(fallback) {
				out[i] = fallback[i]
			} else {
				out[i] = fmt.Sprintf("skeleton://%s/shard-%d (not wired)", domain, i)
			}
		}
		return out
	}
	return fallback
}

// wireDirectory chooses the routing directory. With Scale.Directory.Enabled
// the mapping is persisted in SQL and shared by every API process; otherwise
// it stays process-local (the PR #38 / skeleton default). It is opened before
// the primary store, so its DSN can be a separate control-plane database or
// simply inherited from the primary.
func (r *Runtime) wireDirectory(c config.ScaleConf, primary config.StoreConf, topo *plane.Topology) (plane.Directory, error) {
	if !c.Directory.Enabled {
		return plane.NewInMemoryDirectory(), nil
	}
	primaryDriver := primary.Driver
	if primaryDriver == "" {
		primaryDriver = "sqlite"
	}
	driver := c.Directory.Driver
	if driver == "" {
		driver = primaryDriver
	}
	dsn := c.Directory.DSN
	if dsn == "" {
		// Inheriting the primary DSN only makes sense for the same dialect.
		if driver != "" && !strings.EqualFold(driver, primaryDriver) {
			return nil, fmt.Errorf("Scale.Directory.Driver %q cannot inherit Store.DSN of driver %q; set Scale.Directory.DSN explicitly", driver, primaryDriver)
		}
		dsn = primary.DSN
	}
	// New has no ctx parameter; a bounded startup context keeps a broken DSN
	// from hanging the process forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sqlDir, err := plane.OpenSQLDirectoryWithReplica(ctx, driver, dsn, c.Directory.ReplicaDSN, topo)
	if err != nil {
		return nil, fmt.Errorf("SQL directory: %w", err)
	}
	r.directoryDB = sqlDir
	// The maintenance fence shares the control-plane pool. It is cheap (a
	// short-lived cache on the hot path) and lets shardctl freeze a key
	// during physical migration without an out-of-band coordination system.
	fence, ferr := plane.NewSQLFence(ctx, sqlDir.DB(), driver)
	if ferr != nil {
		return nil, fmt.Errorf("maintenance fence: %w", ferr)
	}
	r.Fence = fence
	// Cursor generations share the control plane so a shardctl bump is seen
	// by every API process, invalidating stale message cursors cluster-wide.
	epoch, eerr := plane.NewSQLCursorEpoch(ctx, sqlDir.DB(), driver, false)
	if eerr != nil {
		return nil, fmt.Errorf("cursor epoch: %w", eerr)
	}
	r.CursorEpoch = epoch
	// Persist the configured topology so ShardCount is shared, and a process
	// still rolling with an older, smaller config cannot shrink it.
	for _, domain := range []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage} {
		if err := sqlDir.EnsureShards(ctx, domain, topo.Len(domain)); err != nil {
			return nil, fmt.Errorf("sync topology for %s: %w", domain, err)
		}
	}
	return sqlDir, nil
}

// wireProjection chooses the shared lease/bus backend. With PubSub=redis the
// lease and bus are real Redis objects shared by every API process; otherwise
// they are in-process implementations (single node / tests).
func (r *Runtime) wireProjection(c config.ScaleConf) error {
	e := projection.Endpoint{
		Addr:     c.Redis.Addr,
		Username: c.Redis.Username,
		Password: c.Redis.Password,
		DB:       c.Redis.DB,
		Channel:  c.Projection.Channel,
		Timeout:  c.Redis.Timeout,
	}
	if c.PubSub == "redis" {
		if e.Addr == "" {
			return fmt.Errorf("Scale.Redis.Addr is required for redis-backed projection")
		}
		r.Lease = projection.NewRedisLease(e)
		r.Bus = projection.NewRedisBus(e)
		return nil
	}
	r.Lease = projection.NewMemoryLease()
	r.Bus = projection.NewMemoryBus()
	return nil
}

// wireSearch builds the read-side index and (when CDC is enabled) a durable
// change log + projector. With a persistent directory the log lives in that
// shared database so several API processes drain one stream; otherwise a
// process-local log is used (single-node / tests).
func (r *Runtime) wireSearch(c config.ScaleConf) error {
	var idx search.TaskIndex
	switch c.Search.Driver {
	case "", "memory":
		idx = search.NewMemoryTaskIndex()
	default:
		indexName := c.Search.Index
		if indexName == "" {
			indexName = defaultSearchIndex
		}
		httpIdx, err := search.NewHTTPTaskIndex(search.HTTPIndexConfig{
			URL: c.Search.URL, Index: indexName,
			Username: c.Search.Username, Password: c.Search.Password, APIKey: c.Search.APIKey,
			Refresh: c.Search.Refresh, Timeout: c.Redis.Timeout,
		})
		if err != nil {
			return fmt.Errorf("search index: %w", err)
		}
		// Create the mapping at startup. A pre-existing index is fine; an
		// unreachable cluster must fail fast instead of serving stale results.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpIdx.EnsureIndex(ctx); err != nil {
			return fmt.Errorf("search index: %w", err)
		}
		idx = httpIdx
	}
	r.TaskIndex = idx

	if !c.Search.CDC {
		// No CDC: keep the legacy synchronous projector available for callers
		// that still push changes directly, but nothing is wired automatically.
		p, err := search.NewProjector(idx)
		if err != nil {
			return err
		}
		r.Projector = p
		return nil
	}

	var log search.ChangeLog
	if r.directoryDB != nil {
		sqlLog, err := search.NewSQLChangeLog(context.Background(), r.directoryDB.DB(), c.Directory.Driver, false)
		if err != nil {
			return fmt.Errorf("search change log: %w", err)
		}
		log = sqlLog
	} else {
		log = search.NewMemoryChangeLog()
	}
	r.SearchLog = log
	r.searchRecorder = search.NewLogRecorder(log)
	cdc, err := search.NewCDCProjector(log, idx, r.Lease, r.instanceID)
	if err != nil {
		return err
	}
	r.CDC = cdc
	return nil
}

// SearchRecorder exposes the engine-facing change recorder (nil when CDC off).
func (r *Runtime) SearchRecorder() *search.LogRecorder {
	if r == nil {
		return nil
	}
	return r.searchRecorder
}

func (r *Runtime) SearchInterval() time.Duration {
	if r == nil || r.cfg.Search.Interval <= 0 {
		return 5 * time.Second
	}
	return r.cfg.Search.Interval
}

func (r *Runtime) SearchRebuildInterval() time.Duration {
	if r == nil {
		return 0
	}
	return r.cfg.Search.RebuildInterval
}

func (r *Runtime) InstanceID() string {
	if r == nil {
		return ""
	}
	return r.instanceID
}

func (r *Runtime) ProjectionEnabled() bool {
	return r != nil && r.cfg.Projection.Enabled
}

func (r *Runtime) ProjectionInterval() time.Duration {
	if r == nil || r.cfg.Projection.Interval <= 0 {
		return projection.DefaultInterval
	}
	return r.cfg.Projection.Interval
}

func (r *Runtime) ProjectionLeaseTTL() time.Duration {
	if r == nil || r.cfg.Projection.LeaseTTL <= 0 {
		return 30 * time.Second
	}
	return r.cfg.Projection.LeaseTTL
}

func (r *Runtime) CompensationInterval() time.Duration {
	if r == nil || r.cfg.Compensation.Interval <= 0 {
		return 5 * time.Second
	}
	return r.cfg.Compensation.Interval
}

// DirectoryCoLocated reports whether the SQL directory shares the primary
// business database. That is true when Directory.Enabled and no explicit,
// different DSN was configured (it inherits Store.DSN). Only then can a
// directory row be written inside a business transaction.
func (r *Runtime) DirectoryCoLocated() bool {
	if r == nil || r.directoryDB == nil {
		return false
	}
	return strings.TrimSpace(r.cfg.Directory.DSN) == ""
}

// Transport exposes the bus so a second process (or a test) can attach.
func (r *Runtime) Transport() notify.Transport {
	if r == nil {
		return nil
	}
	return r.transport
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	var err error
	if r.remote != nil {
		err = r.remote.Close()
	}
	if r.transport != nil {
		if terr := r.transport.Close(); err == nil {
			err = terr
		}
	}
	if r.Lease != nil {
		if lerr := r.Lease.Close(); err == nil {
			err = lerr
		}
	}
	if r.Bus != nil {
		if berr := r.Bus.Close(); err == nil {
			err = berr
		}
	}
	if r.directoryDB != nil {
		if derr := r.directoryDB.Close(); err == nil {
			err = derr
		}
	}
	// compLog shares the directory DB when enabled; Close is a no-op then,
	// but call it for completeness (it only closes a pool it owns).
	if r.compLog != nil {
		if cerr := r.compLog.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
