package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store/shard"
)

// txBuffer holds messages written inside the primary transaction. They are
// projected to their shard only after the transaction commits, so a rollback
// can never leave a message behind in a shard database.
type txBuffer struct {
	items []pendingMessage
}

type pendingMessage struct {
	shard int
	msg   model.Message
}

func (b *txBuffer) add(i int, m model.Message) {
	b.items = append(b.items, pendingMessage{shard: i, msg: m})
}

// flush projects buffered messages and marks their outbox rows as done.
// Insert is idempotent on message id, so a retry is harmless. This stays
// synchronous on the writing process: PostMessage must not return before its
// own message is readable. The shared lease is used by Recover (crash replay),
// not here.
func (d *DB) flush(ctx context.Context, b *txBuffer) error {
	if d.shards == nil || b == nil {
		return nil
	}
	for _, it := range b.items {
		if err := d.projectOne(ctx, it); err != nil {
			return err
		}
	}
	return nil
}

// projectOne is the single projection step, shared by flush and Recover.
// d.project is a test seam; nil means the real shard insert + mark.
func (d *DB) projectOne(ctx context.Context, it pendingMessage) error {
	if d.project != nil {
		return d.project(ctx, it)
	}
	if err := d.shards.Insert(ctx, it.shard, it.msg); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `UPDATE message_outbox SET projected = 1 WHERE id = ?`, it.msg.ID)
	return err
}

// claimOutbox takes a shared lease on one outbox row. It returns (noop, true)
// when no coordinator is configured, preserving the original single-process
// behaviour. A failed Acquire is treated as "another worker has it": skip the
// row now and let the next scan retry.
func (d *DB) claimOutbox(ctx context.Context, id string) (func(), bool) {
	noop := func() {}
	if d.outboxLease == nil {
		return noop, true
	}
	ttl := d.outboxTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	key := "outbox:" + id
	ok, err := d.outboxLease.Acquire(ctx, key, d.outboxOwner, ttl)
	if err != nil || !ok {
		return noop, false
	}
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = d.outboxLease.Release(c, key, d.outboxOwner)
	}, true
}

// EnableShards turns on the sharded message prototype and replays outbox rows
// left unprojected by a previous process. Call it once at startup.
func (d *DB) EnableShards(s *shard.Set) error {
	if s == nil || !s.Router.Enabled() {
		return errors.New("shard: at least 2 shards required")
	}
	d.shards = s
	return d.Recover(context.Background())
}

// Recover projects outbox rows left behind by a crash between the primary
// commit and flush. With a coordinator every row is claimed through a shared
// lease first, so N API processes can safely run Recover against the same
// primary database without duplicating work.
func (d *DB) Recover(ctx context.Context) error {
	if d.shards == nil {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, chat_id, shard, sender_id, type, text, attachment_ref, created_at
		FROM message_outbox WHERE projected = 0`)
	if err != nil {
		return err
	}
	items := []pendingMessage{}
	for rows.Next() {
		var it pendingMessage
		var created string
		if err := rows.Scan(&it.msg.ID, &it.msg.ChatID, &it.shard, &it.msg.SenderID,
			&it.msg.Type, &it.msg.Text, &it.msg.AttachmentRef, &created); err != nil {
			_ = rows.Close()
			return err
		}
		it.msg.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, it := range items {
		release, ok := d.claimOutbox(ctx, it.msg.ID)
		if !ok {
			continue // another process holds the lease
		}
		if err := d.projectOne(ctx, it); err != nil {
			release() // failure: free the row immediately so another worker retries
			return err
		}
		// Success: keep the lease until its TTL expires. Releasing it here
		// would let a process holding an older pending snapshot claim this
		// same row again. The row is now projected=1, so no future scan will
		// pick it up; the expired lease is harmless.
	}
	return nil
}

// shardFor resolves a chat through the directory table, falling back to the
// pure hash for chats created before the directory existed.
func (r *chatRepo) shardFor(ctx context.Context, chatID string) (int, error) {
	var idx int
	err := r.q.QueryRowContext(ctx, `SELECT shard FROM chat_shards WHERE chat_id = ?`, chatID).Scan(&idx)
	if errors.Is(err, sql.ErrNoRows) {
		return r.shards.Router.ShardForID(chatID), nil
	}
	if err != nil {
		return 0, err
	}
	return idx, nil
}

// registerShard records the chat->shard mapping in the same transaction as
// the chat row, so both sides see one consistent answer.
func (r *chatRepo) registerShard(ctx context.Context, c model.Chat) error {
	if r.shards == nil {
		return nil
	}
	idx := r.shards.Router.ShardFor(c)
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO chat_shards (chat_id, shard, created_at) VALUES (?, ?, ?)`,
		c.ID, idx, model.FormatTime(c.CreatedAt))
	if err != nil && !r.d.IsUnique(err) {
		return err
	}
	return nil
}

// writeOutbox records the message in the primary transaction. The body is
// projected to its shard after commit by flush (or by Recover after a crash).
func (r *chatRepo) writeOutbox(ctx context.Context, idx int, m model.Message) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO message_outbox (id, chat_id, shard, sender_id, type, text, attachment_ref, created_at, projected)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		m.ID, m.ChatID, idx, m.SenderID, m.Type, m.Text, m.AttachmentRef, model.FormatTime(m.CreatedAt))
	return err
}

func (r *chatRepo) readCursor(ctx context.Context, userID, chatID string) (int64, error) {
	var cursor int64
	err := r.q.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(last_read_rowid), 0) FROM chat_reads
		WHERE user_id = ? AND chat_id = ?`, userID, chatID).Scan(&cursor)
	return cursor, err
}
