package plane

import (
	"context"
	"fmt"
)

// Reassigner is the maintenance-only half of the directory. It is kept out of
// the Directory interface on purpose: online request paths may only Assign
// (never overwrite) a placement, while a data migration must be able to move
// an existing row under an explicit, audited lockstep operation.
type Reassigner interface {
	// ReassignBatch moves all keys from one shard to another in a single
	// transaction. Every row is compared against `from`; if any row has
	// already moved, nothing is changed. That makes a retried or stale
	// migration harmless instead of silently stealing a live key.
	ReassignBatch(ctx context.Context, domain Domain, from, to int, keys []string) error
	// SyncTopology refreshes the informational DSN snapshot for a shard.
	// EnsureShards only INSERTs, so a newly appended shard needs this after
	// its DSN is decided.
	SyncTopology(ctx context.Context, domain Domain, shard int, dsn string) error
}

var _ Reassigner = (*SQLDirectory)(nil)

// Reassign moves one key. It is a convenience wrapper around ReassignBatch.
func (d *SQLDirectory) Reassign(ctx context.Context, domain Domain, key string, from, to int) error {
	return d.ReassignBatch(ctx, domain, from, to, []string{key})
}

// ReassignBatch implements Reassigner. All keys live in the same control-plane
// database, so the whole batch is one local transaction.
func (d *SQLDirectory) ReassignBatch(ctx context.Context, domain Domain, from, to int, keys []string) error {
	if !domain.Valid() || from < 0 || to < 0 || from == to {
		return fmt.Errorf("plane: invalid reassign (%q, %d -> %d)", domain, from, to)
	}
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("plane: begin reassign: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range keys {
		if key == "" {
			return fmt.Errorf("plane: reassign batch has an empty key")
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE shard_directory SET shard = ? WHERE domain = ? AND shard_key = ? AND shard = ?`,
			to, string(domain), key, from)
		if err != nil {
			return fmt.Errorf("plane: reassign %s/%s: %w", domain, key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("plane: reassign rows for %s/%s: %w", domain, key, err)
		}
		if n != 1 {
			return fmt.Errorf("plane: reassign %s/%s from %d: expected 1 row, got %d (already moved or missing)",
				domain, key, from, n)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("plane: commit reassign: %w", err)
	}
	return nil
}

// SyncTopology records the DSN of an existing shard. It also creates the row
// when a shard was added only in memory, so ShardCount stays correct.
func (d *SQLDirectory) SyncTopology(ctx context.Context, domain Domain, shard int, dsn string) error {
	if !domain.Valid() || shard < 0 || dsn == "" {
		return fmt.Errorf("plane: invalid topology sync (%q, %d, %q)", domain, shard, dsn)
	}
	if _, err := d.db.ExecContext(ctx,
		insertIgnoreSQL(d.driver)+` INTO shard_topology (domain, shard, dsn) VALUES (?, ?, ?)`,
		string(domain), shard, dsn); err != nil {
		return fmt.Errorf("plane: seed topology row: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE shard_topology SET dsn = ? WHERE domain = ? AND shard = ?`,
		dsn, string(domain), shard); err != nil {
		return fmt.Errorf("plane: sync topology DSN: %w", err)
	}
	return nil
}

// CountKeys reports how many directory rows a domain owns. It is used by the
// migration drill to prove a backfill covered every legacy aggregate root.
func (d *SQLDirectory) CountKeys(ctx context.Context, domain Domain) (int, error) {
	if !domain.Valid() {
		return 0, fmt.Errorf("plane: invalid domain %q", domain)
	}
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM shard_directory WHERE domain = ?`, string(domain)).Scan(&n); err != nil {
		return 0, fmt.Errorf("plane: count directory keys: %w", err)
	}
	return n, nil
}
