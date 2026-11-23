package plane

import (
	"context"
	"fmt"
	"hash/fnv"
	"sync"
)

// ShardRef is one physical database in a domain.
type ShardRef struct {
	Index int
	DSN   string
}

// Topology holds the ordered DSN list per domain. Order is authoritative:
// scale-out appends to the end and never reorders or removes an existing entry
// while it still holds data.
type Topology struct {
	mu     sync.RWMutex
	shards map[Domain][]ShardRef
	pinned map[Domain]int // shard count at first deploy; fallback hash modulus
}

func NewTopology() *Topology {
	return &Topology{
		shards: make(map[Domain][]ShardRef),
		pinned: make(map[Domain]int),
	}
}

// Register seeds a domain. It pins the initial shard count used by the
// fallback hash for legacy keys that have no directory row.
func (t *Topology) Register(domain Domain, dsns []string) error {
	if !domain.Valid() || len(dsns) == 0 {
		return fmt.Errorf("plane: Register(%q) needs at least one DSN", domain)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	refs := make([]ShardRef, 0, len(dsns))
	for i, dsn := range dsns {
		if dsn == "" {
			return fmt.Errorf("plane: empty DSN at index %d for %q", i, domain)
		}
		refs = append(refs, ShardRef{Index: i, DSN: dsn})
	}
	t.shards[domain] = refs
	if _, ok := t.pinned[domain]; !ok {
		t.pinned[domain] = len(refs)
	}
	return nil
}

// ScaleOutPlan records what adding one shard does. MigratedKeys is always 0:
// existing directory rows keep pointing at their old shard.
type ScaleOutPlan struct {
	Domain       Domain
	From, To     int
	NewShard     int
	NewDSN       string
	MigratedKeys int
	Steps        []string
}

// AddShard appends one database. No existing row changes, so no data moves.
func (t *Topology) AddShard(ctx context.Context, domain Domain, dsn string, dir Directory) (ScaleOutPlan, error) {
	if dsn == "" {
		return ScaleOutPlan{}, fmt.Errorf("plane: new shard DSN is empty")
	}
	t.mu.Lock()
	refs := t.shards[domain]
	if len(refs) == 0 {
		t.mu.Unlock()
		return ScaleOutPlan{}, fmt.Errorf("plane: domain %q is not registered", domain)
	}
	from := len(refs)
	newRef := ShardRef{Index: from, DSN: dsn}
	t.shards[domain] = append(refs, newRef)
	to := len(t.shards[domain])
	t.mu.Unlock()

	if dir != nil {
		if err := dir.EnsureShards(ctx, domain, to); err != nil {
			return ScaleOutPlan{}, err
		}
	}
	return ScaleOutPlan{
		Domain:       domain,
		From:         from,
		To:           to,
		NewShard:     from,
		NewDSN:       dsn,
		MigratedKeys: 0,
		Steps: []string{
			"start the new database and create its schema",
			"append its DSN to the END of the domain's DSN list",
			"roll every API process to the same ordered list",
			"new keys hash over the new count; existing directory rows are untouched",
		},
	}, nil
}

func (t *Topology) Len(domain Domain) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.shards[domain])
}

// Pinned is the initial shard count. Legacy keys without a directory row MUST
// hash over this value, not over Len(): using the current count after a
// scale-out would resolve them to the wrong shard.
func (t *Topology) Pinned(domain Domain) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pinned[domain]
}

func (t *Topology) DSN(domain Domain, shard int) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	refs := t.shards[domain]
	if shard < 0 || shard >= len(refs) {
		return "", fmt.Errorf("plane: shard %d out of range for %q (%d shards)", shard, domain, len(refs))
	}
	return refs[shard].DSN, nil
}

// MigrationPlan is what a shrink or a hot-shard split needs. Executing it is
// intentionally out of scope for the skeleton.
type MigrationPlan struct {
	Domain          Domain
	From, To        int
	Keys            []string
	RequiresRewrite bool
	Note            string
}

// PlanShrink enumerates directory rows that would have to move. It never
// mutates anything: shrinking is the one operation that requires migration.
func (t *Topology) PlanShrink(ctx context.Context, dir Directory, domain Domain, remove int) (MigrationPlan, error) {
	if dir == nil {
		return MigrationPlan{}, fmt.Errorf("plane: shrink needs a directory")
	}
	keys, err := dir.Keys(ctx, domain, remove)
	if err != nil {
		return MigrationPlan{}, err
	}
	return MigrationPlan{
		Domain:          domain,
		From:            remove,
		To:              -1,
		Keys:            keys,
		RequiresRewrite: len(keys) > 0,
		Note: "SKELETON: move these rows, recompute cursors (e.g. chat_reads), " +
			"then rewrite directory rows in one maintenance window. Not executed.",
	}, nil
}

func hashShard(key string, n int) int {
	if n <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum64() % uint64(n))
}
