package plane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Schema is the control-plane DDL skeleton. Only routing mappings and global
// uniqueness live here: no business rows, no message bodies, no task payloads.
// Keeping this database tiny and cacheable is what stops it becoming the next
// single point of contention.
//
// SKELETON BOUNDARY: a SQL-backed Directory / UniqueIndex implementation is
// TODO. InMemoryDirectory and InMemoryUnique are the runnable defaults.
const Schema = `
CREATE TABLE IF NOT EXISTS shard_directory (
    domain      VARCHAR(16)  NOT NULL,
    shard_key   VARCHAR(191) NOT NULL,
    shard       INT          NOT NULL,
    created_at  DATETIME     NOT NULL,
    PRIMARY KEY (domain, shard_key)
);
CREATE TABLE IF NOT EXISTS shard_topology (
    domain      VARCHAR(16)  NOT NULL,
    shard       INT          NOT NULL,
    dsn         VARCHAR(512) NOT NULL,
    PRIMARY KEY (domain, shard)
);
CREATE TABLE IF NOT EXISTS global_unique (
    namespace   VARCHAR(32)  NOT NULL,
    value       VARCHAR(255) NOT NULL,
    owner_id    VARCHAR(191) NOT NULL,
    PRIMARY KEY (namespace, value)
);
`

var ErrConflict = errors.New("plane: unique value already owned")

// UniqueIndex holds constraints that cannot be checked inside one shard,
// e.g. email -> user_id. Everything else stays shard-local.
type UniqueIndex interface {
	Reserve(ctx context.Context, namespace, value, ownerID string) error
	Owner(ctx context.Context, namespace, value string) (string, bool, error)
}

type InMemoryUnique struct {
	mu     sync.RWMutex
	owners map[string]string
}

func NewInMemoryUnique() *InMemoryUnique {
	return &InMemoryUnique{owners: make(map[string]string)}
}

func (u *InMemoryUnique) Reserve(_ context.Context, namespace, value, ownerID string) error {
	if namespace == "" || value == "" || ownerID == "" {
		return fmt.Errorf("plane: invalid unique reservation")
	}
	key := namespace + "\x00" + value
	u.mu.Lock()
	defer u.mu.Unlock()
	if owner, ok := u.owners[key]; ok {
		if owner == ownerID {
			return nil // idempotent retry
		}
		return ErrConflict
	}
	u.owners[key] = ownerID
	return nil
}

func (u *InMemoryUnique) Owner(_ context.Context, namespace, value string) (string, bool, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	owner, ok := u.owners[namespace+"\x00"+value]
	return owner, ok, nil
}

// IDGenerator produces ids locally, without a round trip to the control plane.
// A production deployment should swap in Snowflake with a worker id allocated
// once at startup; the interface never changes.
type IDGenerator interface {
	New(prefix string) string
}

type LocalIDGenerator struct {
	instance string
	seq      atomic.Uint64
}

func NewLocalIDGenerator(instance string) *LocalIDGenerator {
	if instance == "" {
		instance = "api"
	}
	return &LocalIDGenerator{instance: instance}
}

func (g *LocalIDGenerator) New(prefix string) string {
	return fmt.Sprintf("%s%s-%d", prefix, g.instance, g.seq.Add(1))
}

// ControlPlane is the small, cacheable global layer from the architecture.
type ControlPlane struct {
	Directory Directory
	Topology  *Topology
	Resolver  *Resolver
	Unique    UniqueIndex
	IDs       IDGenerator
}

func NewControlPlane(dir Directory, topo *Topology) (*ControlPlane, error) {
	resolver, err := NewResolver(dir, topo)
	if err != nil {
		return nil, err
	}
	return &ControlPlane{
		Directory: dir,
		Topology:  topo,
		Resolver:  resolver,
		Unique:    NewInMemoryUnique(),
		IDs:       NewLocalIDGenerator(""),
	}, nil
}
