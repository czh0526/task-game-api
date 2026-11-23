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
	"taskgame/internal/config"
	"taskgame/internal/id"
	"taskgame/internal/notify"
	"taskgame/internal/plane"
	"taskgame/internal/projection"
	"taskgame/internal/search"
	"taskgame/internal/store/shard"
)

// TODOs is the explicit list of work this skeleton does NOT do. Keeping it in
// code (rather than only in docs) makes the boundary hard to forget.
//
// The original #1 "replace MemoryTransport with Redis pub/sub" is DONE:
// see notify.RedisTransport (redis.go). The original #2 "shared lease / event
// bus for message_outbox projection" is DONE too: see internal/projection.
// The original #3 "SQL-backed plane.Directory over plane.Schema" is DONE:
// see plane.SQLDirectory, selected by Scale.Directory.Enabled.
// NATS/Kafka remain optional transports.
var TODOs = []string{
	"P2: split User and Task repositories into per-domain stores guarded by shard.TxScope",
	"P2: add global_unique(email -> user_id) and a Snowflake IDGenerator",
	"P3: execute archive batches per shard (copy, verify checksum, then delete)",
	"P3: externalize avatar / payload / delivery body into archive.BlobStore",
	"P3: de-amplify emit() so one event stores one body plus N small pointers",
	"P3: replace MemoryTaskIndex with Elasticsearch/OpenSearch and a CDC projector",
	"P3: keep the newest page of every chat online; archive only older pages",
}

// Runtime is the assembled skeleton. Every field may be nil when its feature
// is off; callers must check.
type Runtime struct {
	cfg          config.ScaleConf
	Fanout       notify.Fanout
	ControlPlane *plane.ControlPlane
	DomainRouter *shard.DomainRouter
	Blobs        archive.BlobStore
	Archiver     *archive.Archiver
	TaskIndex    search.TaskIndex
	Projector    *search.Projector
	Lease        projection.Lease
	Bus          projection.Bus
	transport    notify.Transport
	remote       *notify.RemoteFanout
	directoryDB  *plane.SQLDirectory // non-nil when Scale.Directory.Enabled
	instanceID   string
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
	cp, err := plane.NewControlPlane(dir, topo)
	if err != nil {
		_ = rt.Close()
		return nil, err
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

	// P3: read-side index for cross-shard listings.
	if c.Search.Enabled {
		idx := search.NewMemoryTaskIndex()
		projector, err := search.NewProjector(idx)
		if err != nil {
			_ = rt.Close()
			return nil, err
		}
		rt.TaskIndex, rt.Projector = idx, projector
	}
	return rt, nil
}

// registerDomains seeds the topology. Message uses the REAL PR #38 shard
// configuration; User / Task use clearly-marked placeholder DSNs because
// their repositories have not been split yet.
func registerDomains(topo *plane.Topology, c config.ScaleConf, primary config.StoreConf) error {
	n := primary.Shards
	if n < 1 {
		n = 1
	}
	message := make([]string, n)
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
	if err := topo.Register(plane.DomainMessage, message); err != nil {
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
		list := make([]string, count)
		for i := range list {
			list[i] = fmt.Sprintf("skeleton://%s/shard-%d (not wired)", d.domain, i)
		}
		if err := topo.Register(d.domain, list); err != nil {
			return err
		}
	}
	return nil
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
	sqlDir, err := plane.OpenSQLDirectory(ctx, driver, dsn, topo)
	if err != nil {
		return nil, fmt.Errorf("SQL directory: %w", err)
	}
	r.directoryDB = sqlDir
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
	return err
}
