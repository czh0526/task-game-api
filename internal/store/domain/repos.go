package domain

import (
	"context"
	"errors"

	"taskgame/internal/plane"
	"taskgame/internal/store"
)

// Store implements store.Store by delegating repository accessors to Repos.
func (s *Store) Users() store.UserRepo             { return (&Repos{s: s}).Users() }
func (s *Store) Tasks() store.TaskRepo             { return (&Repos{s: s}).Tasks() }
func (s *Store) Submissions() store.SubmissionRepo { return (&Repos{s: s}).Submissions() }
func (s *Store) Escrow() store.EscrowRepo          { return (&Repos{s: s}).Escrow() }
func (s *Store) Webhooks() store.WebhookRepo       { return (&Repos{s: s}).Webhooks() }
func (s *Store) Deliveries() store.DeliveryRepo    { return (&Repos{s: s}).Deliveries() }
func (s *Store) Chats() store.ChatRepo             { return (&Repos{s: s}).Chats() }
func (s *Store) Invites() store.InviteRepo         { return (&Repos{s: s}).Invites() }
func (s *Store) Friends() store.FriendRepo         { return (&Repos{s: s}).Friends() }
func (s *Store) Idem() store.IdemRepo              { return (&Repos{s: s}).Idem() }

// Repos is the domain-routing implementation of store.Repos. Each accessor
// returns a thin wrapper; the physical store is chosen per method call from
// the aggregate-root key and the control-plane directory.
type Repos struct {
	s     *Store
	scope *scopeState
}

func (r *Repos) Users() store.UserRepo             { return &userRepo{r.s} }
func (r *Repos) Tasks() store.TaskRepo             { return &taskRepo{r.s} }
func (r *Repos) Submissions() store.SubmissionRepo { return &submissionRepo{r.s} }
func (r *Repos) Escrow() store.EscrowRepo          { return &escrowRepo{r.s} }
func (r *Repos) Webhooks() store.WebhookRepo       { return &webhookRepo{r.s} }
func (r *Repos) Deliveries() store.DeliveryRepo    { return &deliveryRepo{r.s} }
func (r *Repos) Chats() store.ChatRepo             { return &chatRepo{r.s} }
func (r *Repos) Invites() store.InviteRepo         { return &inviteRepo{r.s} }
func (r *Repos) Friends() store.FriendRepo         { return &friendRepo{r.s} }
func (r *Repos) Idem() store.IdemRepo              { return &idemRepo{r.s} }

func (s *Store) userRepos(ctx context.Context, id string, create bool) (store.Repos, error) {
	return s.pick(ctx, plane.DomainUser, id, create)
}

func (s *Store) taskRepos(ctx context.Context, taskID string, create bool) (store.Repos, error) {
	return s.pick(ctx, plane.DomainTask, taskID, create)
}

func (s *Store) chatRepos(ctx context.Context, chatID string, create bool) (store.Repos, error) {
	return s.pick(ctx, plane.DomainMessage, chatID, create)
}

// createPlacement keeps a newly created aggregate root co-located with the
// request's routing key (usually the authenticated user). The generated id is
// then pinned to that shard in the directory. This preserves the cell-mode
// guarantee that a user, their tasks and their chats live in one database.
func (s *Store) createPlacement(ctx context.Context, d plane.Domain, newKey string) (plane.Placement, error) {
	if route, ok := RouteFrom(ctx); ok && route.Domain == d && route.Key != "" {
		return s.place(ctx, d, route.Key, false)
	}
	return s.place(ctx, d, newKey, true)
}

var errFound = errors.New("domain: found")

// findAcross fans a read out to every shard of a domain and returns the first
// hit. When no shard has the row, the first real error (or ErrNotFound) wins.
func findAcross[T any](s *Store, ctx context.Context, d plane.Domain, fn func(store.Repos) (T, error)) (T, error) {
	var zero T
	var value T
	var firstErr error
	found := false
	_ = s.allRepos(ctx, d, func(r store.Repos) error {
		v, err := fn(r)
		if err == nil {
			value, found = v, true
			return errFound
		}
		if firstErr == nil {
			firstErr = err
		}
		return nil
	})
	if found {
		return value, nil
	}
	if firstErr != nil {
		return zero, firstErr
	}
	return zero, store.ErrNotFound
}

// writeAcross broadcasts an id-only operation to one domain. Only the owning
// shard changes a row; the others return ErrNotFound, which is not an error
// for a fan-out. It is used where no aggregate-root key is available (for
// example Delivery.Finish, which only carries a delivery id).
//
// SKELETON LIMIT: unlike pick(), this path does not consult TxScope. Stage 4
// must replace these broadcasts with explicit routing or outbox compensation.
func writeAcross(s *Store, ctx context.Context, d plane.Domain, fn func(store.Repos) error) error {
	var first error
	_ = s.allRepos(ctx, d, func(r store.Repos) error {
		if err := fn(r); err != nil && !errors.Is(err, store.ErrNotFound) && first == nil {
			first = err
		}
		return nil
	})
	return first
}
