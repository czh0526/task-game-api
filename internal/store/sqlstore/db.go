package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"taskgame/internal/store"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// DB is a shared SQL Store. SQLite and MySQL each open a *sql.DB, apply
// their own migrations, and pass a Dialect for the statements that differ.
type DB struct {
	db      *sql.DB
	initial int64
	dialect Dialect
}

// New binds an already-migrated *sql.DB to the store interface.
func New(db *sql.DB, initialBalance int64, d Dialect) *DB {
	return &DB{db: db, initial: initialBalance, dialect: d}
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

func (d *DB) Users() store.UserRepo { return &userRepo{q: d.db, initial: d.initial, d: d.dialect} }
func (d *DB) Tasks() store.TaskRepo { return &taskRepo{q: d.db, d: d.dialect} }
func (d *DB) Submissions() store.SubmissionRepo {
	return &submissionRepo{q: d.db}
}
func (d *DB) Escrow() store.EscrowRepo       { return &escrowRepo{q: d.db, d: d.dialect} }
func (d *DB) Webhooks() store.WebhookRepo    { return &webhookRepo{q: d.db} }
func (d *DB) Deliveries() store.DeliveryRepo { return &deliveryRepo{q: d.db} }
func (d *DB) Chats() store.ChatRepo          { return &chatRepo{q: d.db, d: d.dialect} }
func (d *DB) Invites() store.InviteRepo      { return &inviteRepo{q: d.db, d: d.dialect} }
func (d *DB) Friends() store.FriendRepo      { return &friendRepo{q: d.db, d: d.dialect} }
func (d *DB) Idem() store.IdemRepo           { return &idemRepo{q: d.db, d: d.dialect} }

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
	r := &txRepos{q: tx, initial: d.initial, d: d.dialect}
	if err := fn(ctx, r); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
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
}

func (t *txRepos) Users() store.UserRepo { return &userRepo{q: t.q, initial: t.initial, d: t.d} }
func (t *txRepos) Tasks() store.TaskRepo { return &taskRepo{q: t.q, d: t.d} }
func (t *txRepos) Submissions() store.SubmissionRepo {
	return &submissionRepo{q: t.q}
}
func (t *txRepos) Escrow() store.EscrowRepo       { return &escrowRepo{q: t.q, d: t.d} }
func (t *txRepos) Webhooks() store.WebhookRepo    { return &webhookRepo{q: t.q} }
func (t *txRepos) Deliveries() store.DeliveryRepo { return &deliveryRepo{q: t.q} }
func (t *txRepos) Chats() store.ChatRepo          { return &chatRepo{q: t.q, d: t.d} }
func (t *txRepos) Invites() store.InviteRepo      { return &inviteRepo{q: t.q, d: t.d} }
func (t *txRepos) Friends() store.FriendRepo      { return &friendRepo{q: t.q, d: t.d} }
func (t *txRepos) Idem() store.IdemRepo           { return &idemRepo{q: t.q, d: t.d} }
