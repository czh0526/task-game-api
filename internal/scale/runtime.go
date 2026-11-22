// Package scale assembles the P1-remainder / P2 / P3 skeleton into one
// lifecycle object owned by svc.ServiceContext.
//
// SKELETON BOUNDARY: New is a no-op unless Scale.Enabled is true. Even when
// enabled, no existing table is moved and no API response changes. The pieces
// here are contracts plus runnable in-memory/local implementations that let
// the next stage replace one component at a time.
package scale

import (
	"fmt"
	"strings"

	"taskgame/internal/archive"
	"taskgame/internal/config"
	"taskgame/internal/notify"
	"taskgame/internal/plane"
	"taskgame/internal/search"
	"taskgame/internal/store/shard"
)

// TODOs is the explicit list of work this skeleton does NOT do. Keeping it in
// code (rather than only in docs) makes the boundary hard to forget.
//
// The original #1 "replace MemoryTransport with Redis pub/sub" is now DONE:
// see notify.RedisTransport (redis.go). NATS/Kafka remain optional transports.
var TODOs = []string{
	"P1: move message_outbox projection to a shared lease / event bus",
	"P2: implement a SQL-backed plane.Directory over plane.Schema",
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
	transport    notify.Transport
	remote       *notify.RemoteFanout
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
	rt := &Runtime{cfg: c}

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
	fanout, err := notify.NewRemoteFanout(c.InstanceID, rt.transport)
	if err != nil {
		return nil, err
	}
	rt.remote, rt.Fanout = fanout, fanout

	// P2: global control plane + three-domain router.
	dir := plane.NewInMemoryDirectory()
	topo := plane.NewTopology()
	if err := registerDomains(topo, c, primary); err != nil {
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
	return err
}
