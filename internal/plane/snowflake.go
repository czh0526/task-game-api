package plane

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Snowflake layout (compatible with the classic 64-bit scheme):
//
//	bit 63        reserved (always 0)
//	bits 22..62   41 bits of milliseconds since Epoch
//	bits 12..21   10 bits of worker id (0..1023)
//	bits 0..11    12 bits of per-millisecond sequence (0..4095)
//
// A worker id is allocated once from the control plane and persisted in
// global_unique, so a restarted process with the same instance id reuses the
// same id and can never emit a duplicate (timestamp, worker, sequence) tuple.
// The returned string is prefix + the 64-bit value in lowercase hex, which
// satisfies every id shape regex in internal/id.
const (
	snowWorkerBits = 10
	snowSeqBits    = 12
	snowMaxWorker  = (1 << snowWorkerBits) - 1
	snowMaxSeq     = (1 << snowSeqBits) - 1
	snowNamespace  = "snowflake_worker"
	snowSlotNS     = "snowflake_worker_slot"
)

// SnowEpoch is the custom epoch. 2024-01-01T00:00:00Z gives ~139 years of
// 41-bit millisecond timestamps before rollover.
var SnowEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// SnowflakeIDGenerator is a concurrency-safe IDGenerator backed by a
// control-plane-allocated worker id.
type SnowflakeIDGenerator struct {
	mu     sync.Mutex
	worker uint16
	epoch  time.Time
	now    func() time.Time
	lastMS int64
	seq    uint16
}

var _ IDGenerator = (*SnowflakeIDGenerator)(nil)

// NewSnowflakeIDGenerator allocates (or restores) a worker id for instance
// via the control-plane UniqueIndex, then returns a generator. instance must
// be non-empty and unique per live process (Scale.InstanceID).
func NewSnowflakeIDGenerator(ctx context.Context, unique UniqueIndex, instance string) (*SnowflakeIDGenerator, error) {
	if unique == nil {
		return nil, fmt.Errorf("plane: snowflake needs a UniqueIndex")
	}
	if instance == "" {
		return nil, fmt.Errorf("plane: snowflake needs a non-empty instance id")
	}
	worker, err := allocateWorker(ctx, unique, instance)
	if err != nil {
		return nil, err
	}
	return newSnowflake(worker, SnowEpoch, time.Now), nil
}

// newSnowflake is the test seam: a fixed worker/clock lets tests assert exact
// ids and exercise clock rollback deterministically.
func newSnowflake(worker uint16, epoch time.Time, now func() time.Time) *SnowflakeIDGenerator {
	if worker > snowMaxWorker {
		panic("plane: snowflake worker id out of range")
	}
	if now == nil {
		now = time.Now
	}
	return &SnowflakeIDGenerator{worker: worker, epoch: epoch, now: now}
}

// allocateWorker reserves a stable worker id for instance. The mapping is
// stored twice: instance -> id (for restart recovery) and id -> instance (the
// actual slot claim). INSERT IGNORE makes concurrent allocations safe: only
// one process can claim a slot; a loser retries with the next slot.
func allocateWorker(ctx context.Context, unique UniqueIndex, instance string) (uint16, error) {
	if owner, ok, err := unique.Owner(ctx, snowNamespace, instance); err != nil {
		return 0, err
	} else if ok {
		worker, err := strconv.ParseUint(owner, 10, 16)
		if err != nil || worker > snowMaxWorker {
			return 0, fmt.Errorf("plane: corrupt worker id %q for %q", owner, instance)
		}
		return uint16(worker), nil
	}

	for slot := 0; slot <= snowMaxWorker; slot++ {
		id := strconv.Itoa(slot)
		if err := unique.Reserve(ctx, snowSlotNS, id, instance); err != nil {
			if err == ErrConflict {
				continue // another process owns this slot
			}
			return 0, err
		}
		if err := unique.Reserve(ctx, snowNamespace, instance, id); err != nil && err != ErrConflict {
			return 0, err
		}
		return uint16(slot), nil
	}
	return 0, fmt.Errorf("plane: no snowflake worker id available")
}

// New returns prefix + hex(snowflake). It never returns a duplicate for the
// same generator, even under clock rollback: the timestamp is clamped to the
// last emitted millisecond and the sequence borrows from a future millisecond
// once it overflows.
func (g *SnowflakeIDGenerator) New(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	ms := g.now().Sub(g.epoch).Milliseconds()
	if ms < g.lastMS {
		ms = g.lastMS // clock moved backwards: keep issuing at the last time
	}
	if ms == g.lastMS && g.seq == snowMaxSeq {
		ms = g.lastMS + 1
		g.seq = 0
	} else if ms == g.lastMS {
		g.seq++
	} else {
		g.seq = 0
	}
	g.lastMS = ms

	id := uint64(ms)<<(snowWorkerBits+snowSeqBits) |
		uint64(g.worker)<<snowSeqBits |
		uint64(g.seq)
	return prefix + strconv.FormatUint(id, 16)
}

// WorkerID is the id allocated to this process (useful for diagnostics).
func (g *SnowflakeIDGenerator) WorkerID() uint16 { return g.worker }
