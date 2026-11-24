package search

import (
	"context"
	"fmt"
	"time"
)

// CDCProjector drains the change log into a TaskIndex. It is deliberately
// stateless and idempotent: the authoritative "applied" flag lives in the
// change log, so a crash between applying and acknowledging simply replays the
// same upsert/delete (both are idempotent).
type CDCProjector struct {
	log   ChangeLog
	index TaskIndex
	lease projectionLease
	owner string
	ttl   time.Duration
	batch int
}

// projectionLease is the small slice of projection.Lease used here. Defining
// it locally keeps search decoupled from the projection package's transport.
type projectionLease interface {
	Acquire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key, owner string) error
}

const cdcLeaseKey = "taskgame:search:cdc"

// NewCDCProjector builds a projector. lease may be nil (single-process): the
// log's projected flag alone then guarantees at-least-once delivery.
func NewCDCProjector(log ChangeLog, index TaskIndex, lease projectionLease, owner string) (*CDCProjector, error) {
	if log == nil || index == nil {
		return nil, fmt.Errorf("search: cdc projector needs a log and an index")
	}
	if owner == "" {
		owner = "search-local"
	}
	return &CDCProjector{
		log: log, index: index, lease: lease, owner: owner,
		ttl: 30 * time.Second, batch: 64,
	}, nil
}

// Recover drains one batch. It satisfies projection.Projector so the same
// projection.Worker (timer + bus wake-up) that drives message outbox recovery
// can drive the search index too.
func (p *CDCProjector) Recover(ctx context.Context) error {
	if p.lease != nil {
		ok, err := p.lease.Acquire(ctx, cdcLeaseKey, p.owner, p.ttl)
		if err != nil {
			return err
		}
		if !ok {
			return nil // another process is draining right now
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = p.lease.Release(releaseCtx, cdcLeaseKey, p.owner)
		}()
	}
	entries, err := p.log.Pending(ctx, p.batch)
	if err != nil || len(entries) == 0 {
		return err
	}
	applied := make([]string, 0, len(entries))
	for _, e := range entries {
		switch e.Kind {
		case "upsert":
			if err := p.index.Upsert(ctx, e.Doc); err != nil {
				return fmt.Errorf("search: apply upsert %s: %w", e.ID, err)
			}
		case "delete":
			if err := p.index.Delete(ctx, e.Doc.ID); err != nil {
				return fmt.Errorf("search: apply delete %s: %w", e.ID, err)
			}
		default:
			return fmt.Errorf("search: unknown cdc kind %q for %s", e.Kind, e.ID)
		}
		applied = append(applied, e.ID)
	}
	return p.log.MarkProjected(ctx, applied)
}

// Rebuild enqueues an upsert for every live task, then lets Recover drain the
// log. It is the full re-index path: safe to run repeatedly and the way a new
// index or a schema change is backfilled without touching the primary store's
// read semantics.
func Rebuild(ctx context.Context, lister TaskLister, log ChangeLog) (int, error) {
	if lister == nil || log == nil {
		return 0, fmt.Errorf("search: rebuild needs a lister and a log")
	}
	var afterCreated, afterID string
	total := 0
	for {
		rows, err := lister.ListSweep(ctx, afterCreated, afterID, 200)
		if err != nil {
			return total, err
		}
		if len(rows) == 0 {
			return total, nil
		}
		for _, t := range rows {
			if err := log.Append(ctx, Change{Kind: "upsert", Doc: t}); err != nil {
				return total, err
			}
			total++
		}
		last := rows[len(rows)-1]
		afterCreated, afterID = last.CreatedAt, last.ID
		if len(rows) < 200 {
			return total, nil
		}
	}
}

// TaskLister is the read side needed by Rebuild. store.Store satisfies it;
// keeping it narrow makes the rebuild trivially testable.
type TaskLister interface {
	ListSweep(ctx context.Context, afterCreated, afterID string, limit int) ([]TaskDoc, error)
}
