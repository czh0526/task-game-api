package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"taskgame/internal/projection"
	"taskgame/internal/store"
	"taskgame/internal/store/shard"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// DB is a shared SQL Store. SQLite and MySQL each open a *sql.DB, apply
// their own migrations, and pass a Dialect for the statements that differ.
type DB struct {
	db      *sql.DB
	initial int64
	dialect Dialect
	// shards is nil unless message sharding is enabled. In that case the
	// primary store keeps chats, read cursors and an outbox; message bodies
	// live in the per-shard databases owned by shards.
	shards *shard.Set

	// Optional cross-process outbox coordination. All four fields are zero
	// unless Scale.Projection is enabled, in which case Recover claims each
	// pending row through a shared lease before projecting it.
	outboxOwner string
	outboxTTL   time.Duration
	outboxLease projection.Lease
	outboxBus   projection.Bus

	// project is a test seam: nil means the real shard insert + mark step.
	project func(ctx context.Context, it pendingMessage) error

	// blobs, when set, holds avatar JPEGs and large submission payloads
	// outside the online database. Nil keeps the legacy inline behavior.
	blobs LargeValueStore
}

// SetLargeValues enables large-field externalization. Callers pass the shared
// archive.BlobStore from scale.Runtime; nil is a no-op that preserves the old
// inline-storage behavior. It must be called before serving requests.
func (d *DB) SetLargeValues(blobs LargeValueStore) {
	d.blobs = blobs
}

// SetOutboxCoordinator enables shared-lease projection. Without it (the
// default) Recover behaves exactly as before: this process projects every
// pending row itself. The caller owns lease/bus and closes them via
// scale.Runtime.Close.
func (d *DB) SetOutboxCoordinator(owner string, ttl time.Duration, l projection.Lease, b projection.Bus) {
	d.outboxOwner = owner
	d.outboxTTL = ttl
	d.outboxLease = l
	d.outboxBus = b
}

// New binds an already-migrated *sql.DB to the store interface.
func New(db *sql.DB, initialBalance int64, d Dialect) *DB {
	return &DB{db: db, initial: initialBalance, dialect: d}
}

// NewWithShards enables the sharded message prototype. The engine and every
// handler still see the same store.Store interface.
func NewWithShards(db *sql.DB, initialBalance int64, d Dialect, s *shard.Set) *DB {
	return &DB{db: db, initial: initialBalance, dialect: d, shards: s}
}

func (d *DB) Close() error {
	err := d.db.Close()
	if d.shards != nil {
		if serr := d.shards.Close(); err == nil {
			err = serr
		}
	}
	return err
}

func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *DB) Users() store.UserRepo {
	return &userRepo{q: d.db, initial: d.initial, d: d.dialect, blobs: d.blobs}
}
func (d *DB) Tasks() store.TaskRepo { return &taskRepo{q: d.db, d: d.dialect} }
func (d *DB) Submissions() store.SubmissionRepo {
	return &submissionRepo{q: d.db, blobs: d.blobs}
}
func (d *DB) Escrow() store.EscrowRepo       { return &escrowRepo{q: d.db, d: d.dialect} }
func (d *DB) Webhooks() store.WebhookRepo    { return &webhookRepo{q: d.db} }
func (d *DB) Deliveries() store.DeliveryRepo { return &deliveryRepo{q: d.db} }
func (d *DB) Chats() store.ChatRepo {
	return &chatRepo{q: d.db, d: d.dialect, shards: d.shards}
}
func (d *DB) Invites() store.InviteRepo { return &inviteRepo{q: d.db, d: d.dialect} }
func (d *DB) Friends() store.FriendRepo { return &friendRepo{q: d.db, d: d.dialect} }
func (d *DB) Idem() store.IdemRepo      { return &idemRepo{q: d.db, d: d.dialect} }

func (d *DB) InTx(ctx context.Context, fn func(ctx context.Context, r store.Repos) error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = d.inTx(ctx, fn)
		if !isDeadlock(err) {
			return err
		}
	}
	return err
}

func (d *DB) inTx(ctx context.Context, fn func(ctx context.Context, r store.Repos) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Messages written in this transaction are buffered and projected to
	// their shard only after commit, so a rollback leaves no shard row.
	var buf *txBuffer
	if d.shards != nil {
		buf = &txBuffer{}
	}
	r := &txRepos{q: tx, initial: d.initial, d: d.dialect, shards: d.shards, buf: buf, blobs: d.blobs}
	if err := fn(ctx, r); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Broadcast AFTER commit but BEFORE the local flush, so a process that
	// dies in the gap still leaves a hint for the others. This is only a
	// wake-up hint: correctness comes from the periodic scan + lease, and a
	// lost/failed publish is harmless. Fire-and-forget keeps it off the
	// request's critical path.
	if d.outboxBus != nil && buf != nil {
		for _, it := range buf.items {
			msgID := it.msg.ID
			go func() {
				c, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				_ = d.outboxBus.Publish(c, msgID)
			}()
		}
	}
	return d.flush(ctx, buf)
}

func isDeadlock(err error) bool {
	var me *mysqldriver.MySQLError
	if !errors.As(err, &me) {
		return false
	}
	// 1213 is a deadlock. 1205 is a lock wait timeout. Both roll the transaction back.
	return me.Number == 1213 || me.Number == 1205
}

// txRepos binds every repository to one transaction.
type txRepos struct {
	q       querier
	initial int64
	d       Dialect
	shards  *shard.Set
	buf     *txBuffer
	blobs   LargeValueStore
}

// SQLTxHandle exposes the live transaction so the domain store can write a
// directory row in the same transaction (co-located control plane). Outside a
// transaction (plain DB repos) this method is not present, so callers must
// assert the interface.
func (t *txRepos) SQLTxHandle() (any, string) {
	tx, ok := t.q.(*sql.Tx)
	if !ok {
		return nil, ""
	}
	return tx, t.d.Name()
}

func (t *txRepos) Users() store.UserRepo {
	return &userRepo{q: t.q, initial: t.initial, d: t.d, blobs: t.blobs}
}
func (t *txRepos) Tasks() store.TaskRepo { return &taskRepo{q: t.q, d: t.d} }
func (t *txRepos) Submissions() store.SubmissionRepo {
	return &submissionRepo{q: t.q, blobs: t.blobs}
}
func (t *txRepos) Escrow() store.EscrowRepo       { return &escrowRepo{q: t.q, d: t.d} }
func (t *txRepos) Webhooks() store.WebhookRepo    { return &webhookRepo{q: t.q} }
func (t *txRepos) Deliveries() store.DeliveryRepo { return &deliveryRepo{q: t.q} }
func (t *txRepos) Chats() store.ChatRepo {
	return &chatRepo{q: t.q, d: t.d, shards: t.shards, buf: t.buf}
}
func (t *txRepos) Invites() store.InviteRepo { return &inviteRepo{q: t.q, d: t.d} }
func (t *txRepos) Friends() store.FriendRepo { return &friendRepo{q: t.q, d: t.d} }
func (t *txRepos) Idem() store.IdemRepo      { return &idemRepo{q: t.q, d: t.d} }
