// Package plane is the global control plane for the multi-database design.
//
// A SQL-backed implementation (SQLDirectory in sql_directory.go) persists the
// routing rows across processes and restarts; it is selected by
// Scale.Directory.Enabled. InMemoryDirectory stays the default for a single
// process. Neither moves an existing table out of the primary store: the User
// / Task repositories are still unsplit (see internal/scale.TODOs).
package plane

import (
	"context"
	"fmt"
	"sync"
)

// Domain is one aggregate-root family. Each domain has its own shard key and
// can be scaled independently.
type Domain string

const (
	DomainUser    Domain = "user"
	DomainTask    Domain = "task"
	DomainMessage Domain = "message"
)

func (d Domain) Valid() bool {
	switch d {
	case DomainUser, DomainTask, DomainMessage:
		return true
	}
	return false
}

// Placement is the resolved location of one aggregate root.
type Placement struct {
	Domain        Domain
	Key           string
	Shard         int
	DSN           string
	FromDirectory bool // true when the directory row, not the hash, decided
}

// Directory is the authoritative key -> shard mapping. It is the only source
// of truth: adding a shard must never change an existing row.
//
// A SQL implementation should use the shard_directory table in Schema and
// write the row in the SAME transaction as the aggregate root it describes
// (the current prototype does exactly this for chat_shards). SQLDirectory
// persists the rows but owns its own pool, so it exposes AssignInTx for that
// future same-transaction write; until the P2 repository split happens the
// directory row and the business row are eventually consistent.
type Directory interface {
	Lookup(ctx context.Context, domain Domain, key string) (shard int, ok bool, err error)
	Assign(ctx context.Context, domain Domain, key string, shard int) error
	Keys(ctx context.Context, domain Domain, shard int) ([]string, error)
	ShardCount(ctx context.Context, domain Domain) (int, error)
	// EnsureShards only ever raises the count. Shrinking requires a migration.
	EnsureShards(ctx context.Context, domain Domain, count int) error
}

// InMemoryDirectory is the runnable default. It is process-local, so it is
// suitable for tests and for a single API process; a multi-process deployment
// must use a shared SQL/etcd directory.
type InMemoryDirectory struct {
	mu       sync.RWMutex
	assigned map[Domain]map[string]int
	counts   map[Domain]int
}

func NewInMemoryDirectory() *InMemoryDirectory {
	return &InMemoryDirectory{
		assigned: make(map[Domain]map[string]int),
		counts:   make(map[Domain]int),
	}
}

func (d *InMemoryDirectory) Lookup(_ context.Context, domain Domain, key string) (int, bool, error) {
	if !domain.Valid() {
		return 0, false, fmt.Errorf("plane: invalid domain %q", domain)
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	shard, ok := d.assigned[domain][key]
	return shard, ok, nil
}

// Assign is idempotent: an existing mapping is never overwritten.
func (d *InMemoryDirectory) Assign(_ context.Context, domain Domain, key string, shard int) error {
	if !domain.Valid() || key == "" || shard < 0 {
		return fmt.Errorf("plane: invalid assignment (%q, %q, %d)", domain, key, shard)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.assigned[domain] == nil {
		d.assigned[domain] = make(map[string]int)
	}
	if _, exists := d.assigned[domain][key]; !exists {
		d.assigned[domain][key] = shard
	}
	if shard+1 > d.counts[domain] {
		d.counts[domain] = shard + 1
	}
	return nil
}

func (d *InMemoryDirectory) Keys(_ context.Context, domain Domain, shard int) ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := []string{}
	for key, s := range d.assigned[domain] {
		if s == shard {
			out = append(out, key)
		}
	}
	return out, nil
}

func (d *InMemoryDirectory) ShardCount(_ context.Context, domain Domain) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.counts[domain], nil
}

func (d *InMemoryDirectory) EnsureShards(_ context.Context, domain Domain, count int) error {
	if !domain.Valid() || count < 1 {
		return fmt.Errorf("plane: invalid shard count %d for %q", count, domain)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if count > d.counts[domain] {
		d.counts[domain] = count
	}
	return nil
}
