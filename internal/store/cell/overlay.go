package cell

import (
	"context"
	"errors"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// overlayRepos wraps the Repos handed to an InTx callback. Writes stay inside
// the single-cell transaction (no 2PC), but reads that reference a user or a
// webhook owned by another cell are resolved across the OTHER cells.
//
// The local cell is always read through the transaction itself. That matters
// for SQLite, which allows only one connection per database: opening a second
// query against the same database while the transaction holds that connection
// would deadlock.
type overlayRepos struct {
	s     *Store
	inner store.Repos
	cell  int
}

func (o *overlayRepos) Users() store.UserRepo {
	return &overlayUserRepo{s: o.s, inner: o.inner.Users(), cell: o.cell}
}
func (o *overlayRepos) Webhooks() store.WebhookRepo {
	return &overlayWebhookRepo{s: o.s, inner: o.inner.Webhooks(), cell: o.cell}
}
func (o *overlayRepos) Tasks() store.TaskRepo             { return o.inner.Tasks() }
func (o *overlayRepos) Submissions() store.SubmissionRepo { return o.inner.Submissions() }
func (o *overlayRepos) Escrow() store.EscrowRepo          { return o.inner.Escrow() }
func (o *overlayRepos) Deliveries() store.DeliveryRepo    { return o.inner.Deliveries() }
func (o *overlayRepos) Chats() store.ChatRepo {
	return &overlayChatRepo{s: o.s, inner: o.inner.Chats(), cell: o.cell}
}
func (o *overlayRepos) Invites() store.InviteRepo { return o.inner.Invites() }
func (o *overlayRepos) Friends() store.FriendRepo {
	return &overlayFriendRepo{s: o.s, inner: o.inner.Friends(), cell: o.cell}
}
func (o *overlayRepos) Idem() store.IdemRepo { return o.inner.Idem() }

// overlayUserRepo routes reads to the cell that owns the user id, while all
// writes remain in the surrounding transaction's cell.
type overlayUserRepo struct {
	s     *Store
	inner store.UserRepo
	cell  int
}

func (r *overlayUserRepo) others() []store.Store {
	out := make([]store.Store, 0, len(r.s.cells)-1)
	for i, c := range r.s.cells {
		if i != r.cell {
			out = append(out, c)
		}
	}
	return out
}

// repoFor keeps all user-row writes in the cell that owns the user id. When
// that cell is also the transaction's cell we must use the transaction
// connection (SQLite allows only one connection per database). When it is a
// different cell we route through the Store, which writes to that cell only.
// These idempotent / commutative user updates are best-effort across cells:
// this is a cell-based prototype, not a 2PC transaction.
func (r *overlayUserRepo) repoFor(ctx context.Context, id string) store.UserRepo {
	if r.s.byKey(ctx, id, KindUser) == r.cell {
		return r.inner
	}
	return r.s.Users()
}

// Ensure creates the user row in the cell that owns the user id, not in
// whichever cell the current transaction happens to run in. Otherwise a
// cross-cell operation (accepting a task, opening a chat) would duplicate the
// peer's row into the task's cell and skew the user distribution.
func (r *overlayUserRepo) Ensure(ctx context.Context, id string, now time.Time) (model.User, error) {
	return r.repoFor(ctx, id).Ensure(ctx, id, now)
}

func (r *overlayUserRepo) Get(ctx context.Context, id string) (model.User, error) {
	u, err := r.inner.Get(ctx, id)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return u, err
	}
	for _, c := range r.others() {
		if u, gerr := c.Users().Get(ctx, id); gerr == nil {
			return u, nil
		}
	}
	return model.User{}, store.ErrNotFound
}

func (r *overlayUserRepo) InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error) {
	return r.inner.InsertRegistered(ctx, id, email, passwordHash, now)
}

func (r *overlayUserRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	u, err := r.inner.GetByEmail(ctx, email)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return u, err
	}
	for _, c := range r.others() {
		if u, gerr := c.Users().GetByEmail(ctx, email); gerr == nil {
			return u, nil
		}
	}
	return model.User{}, store.ErrNotFound
}

func (r *overlayUserRepo) SetNickname(ctx context.Context, id, nickname string) error {
	return r.repoFor(ctx, id).SetNickname(ctx, id, nickname)
}

func (r *overlayUserRepo) SetAvatar(ctx context.Context, id string, jpeg []byte, updatedAt string) error {
	return r.repoFor(ctx, id).SetAvatar(ctx, id, jpeg, updatedAt)
}

func (r *overlayUserRepo) GetAvatar(ctx context.Context, id string) (model.UserAvatar, error) {
	a, err := r.inner.GetAvatar(ctx, id)
	if err == nil {
		return a, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return a, err
	}
	for _, c := range r.others() {
		if a, gerr := c.Users().GetAvatar(ctx, id); gerr == nil {
			return a, nil
		}
	}
	return model.UserAvatar{}, store.ErrNotFound
}

func (r *overlayUserRepo) AddBalance(ctx context.Context, id string, delta int64) error {
	return r.repoFor(ctx, id).AddBalance(ctx, id, delta)
}

func (r *overlayUserRepo) ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error {
	return r.repoFor(ctx, id).ApplyScore(ctx, id, deltaIntegrity, deltaAbility)
}

func (r *overlayUserRepo) AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error {
	return r.repoFor(ctx, ev.UserID).AddScoreEvent(ctx, ev)
}

func (r *overlayUserRepo) ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error) {
	out, err := r.inner.ListScoreEvents(ctx, userID, limit)
	if err != nil || len(out) > 0 {
		return out, err
	}
	for _, c := range r.others() {
		if more, gerr := c.Users().ListScoreEvents(ctx, userID, limit); gerr == nil && len(more) > 0 {
			return more, nil
		}
	}
	return out, nil
}

// overlayWebhookRepo finds subscriptions in every cell but writes the new
// webhook (and its deliveries) in the current transaction's cell.
type overlayWebhookRepo struct {
	s     *Store
	inner store.WebhookRepo
	cell  int
}

func (r *overlayWebhookRepo) Insert(ctx context.Context, w model.Webhook) error {
	return r.inner.Insert(ctx, w)
}

func (r *overlayWebhookRepo) ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error) {
	out, err := r.inner.ListByOwners(ctx, ownerIDs)
	if err != nil {
		return nil, err
	}
	for i, c := range r.s.cells {
		if i == r.cell {
			continue
		}
		part, gerr := c.Webhooks().ListByOwners(ctx, ownerIDs)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, part...)
	}
	return out, nil
}
