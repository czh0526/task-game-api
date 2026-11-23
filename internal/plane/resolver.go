package plane

import (
	"context"
	"fmt"
)

// Resolver turns an aggregate-root key into a physical database.
//
// Two hashes, on purpose:
//   - Resolve (new keys) hashes over the CURRENT shard count, so a freshly
//     created root can land on a newly added shard.
//   - ResolveExisting (legacy keys with no directory row) hashes over the
//     PINNED initial count, so a scale-out never changes where an old root
//     is found. Using the current count there is the classic "index out of
//     range / wrong shard" bug during a rolling upgrade.
//
// Once a directory row exists, neither hash is consulted: the row wins.
type Resolver struct {
	dir  Directory
	topo *Topology
}

func NewResolver(dir Directory, topo *Topology) (*Resolver, error) {
	if dir == nil || topo == nil {
		return nil, fmt.Errorf("plane: resolver needs a directory and a topology")
	}
	return &Resolver{dir: dir, topo: topo}, nil
}

// Resolve finds or creates the placement of a key, writing a directory row on
// a miss. Call it when creating a new aggregate root.
func (r *Resolver) Resolve(ctx context.Context, domain Domain, key string) (Placement, error) {
	return r.resolve(ctx, domain, key, r.topo.Len(domain), true)
}

// ResolveExisting is the read path for roots created before the directory
// existed. It does not write a directory row.
func (r *Resolver) ResolveExisting(ctx context.Context, domain Domain, key string) (Placement, error) {
	return r.resolve(ctx, domain, key, r.topo.Pinned(domain), false)
}

// Place pins a key to an explicit shard (used when a chat/task is created and
// the routing decision must be committed in the same transaction as the row).
func (r *Resolver) Place(ctx context.Context, domain Domain, key string, shard int) (Placement, error) {
	if err := r.dir.Assign(ctx, domain, key, shard); err != nil {
		return Placement{}, err
	}
	// Assign never overwrites an existing row. Re-read it so a caller that
	// pre-pinned the key (a backfill, a migration, or a chat's peer) always
	// gets the authoritative shard instead of writing data to a different
	// physical database than the directory points at.
	if existing, ok, err := r.dir.Lookup(ctx, domain, key); err != nil {
		return Placement{}, err
	} else if ok {
		shard = existing
	}
	dsn, err := r.topo.DSN(domain, shard)
	if err != nil {
		return Placement{}, err
	}
	return Placement{Domain: domain, Key: key, Shard: shard, DSN: dsn, FromDirectory: true}, nil
}

func (r *Resolver) resolve(ctx context.Context, domain Domain, key string, modulus int, assign bool) (Placement, error) {
	if !domain.Valid() || key == "" {
		return Placement{}, fmt.Errorf("plane: invalid resolve (%q, %q)", domain, key)
	}
	if modulus <= 0 {
		return Placement{}, fmt.Errorf("plane: domain %q has no shards", domain)
	}
	if shard, ok, err := r.dir.Lookup(ctx, domain, key); err != nil {
		return Placement{}, err
	} else if ok {
		dsn, err := r.topo.DSN(domain, shard)
		if err != nil {
			return Placement{}, err
		}
		return Placement{Domain: domain, Key: key, Shard: shard, DSN: dsn, FromDirectory: true}, nil
	}
	shard := hashShard(key, modulus)
	if assign {
		if err := r.dir.Assign(ctx, domain, key, shard); err != nil {
			return Placement{}, err
		}
	}
	dsn, err := r.topo.DSN(domain, shard)
	if err != nil {
		return Placement{}, err
	}
	return Placement{Domain: domain, Key: key, Shard: shard, DSN: dsn}, nil
}
