package cell

import (
	"context"
	"errors"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// firstAcross reads through the transaction first, then the other cells. The
// local cell must be read through the transaction to avoid a second SQLite
// connection; the others are queried directly (they are different databases).
func firstAcross[T any](inner store.FriendRepo, others []store.FriendRepo, fn func(store.FriendRepo) (T, error)) (T, error) {
	v, err := fn(inner)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return v, err
	}
	for _, o := range others {
		if v, gerr := fn(o); gerr == nil {
			return v, nil
		}
	}
	var zero T
	return zero, store.ErrNotFound
}

func allAcross[T any](inner store.FriendRepo, others []store.FriendRepo, fn func(store.FriendRepo) ([]T, error)) ([]T, error) {
	out, err := fn(inner)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		part, gerr := fn(o)
		if gerr != nil {
			return nil, gerr
		}
		out = append(out, part...)
	}
	if out == nil {
		out = []T{}
	}
	return out, nil
}

// overlayFriendRepo makes friend data visible across cells inside a single
// transaction. Reads search every cell; UPDATE/DELETE are broadcast (only the
// owning cell has the row); INSERTs stay in the transaction's cell.
type overlayFriendRepo struct {
	s     *Store
	inner store.FriendRepo
	cell  int
}

func (r *overlayFriendRepo) others() []store.FriendRepo {
	out := make([]store.FriendRepo, 0, len(r.s.cells)-1)
	for i, c := range r.s.cells {
		if i != r.cell {
			out = append(out, c.Friends())
		}
	}
	return out
}

// mutate applies an UPDATE/DELETE in every cell. A cell without the row
// reports ErrNotFound, which is expected and ignored; a real error fails the
// transaction. This is a best-effort cross-cell write, not a 2PC transaction.
func (r *overlayFriendRepo) mutate(fn func(store.FriendRepo) error) error {
	var first error
	apply := func(fr store.FriendRepo) {
		err := fn(fr)
		if err != nil && !errors.Is(err, store.ErrNotFound) && first == nil {
			first = err
		}
	}
	apply(r.inner)
	for _, o := range r.others() {
		apply(o)
	}
	return first
}

func (r *overlayFriendRepo) sum(fn func(store.FriendRepo) (int, error)) (int, error) {
	n, err := fn(r.inner)
	if err != nil {
		return 0, err
	}
	for _, o := range r.others() {
		more, gerr := fn(o)
		if gerr != nil {
			return 0, gerr
		}
		n += more
	}
	return n, nil
}

func (r *overlayFriendRepo) InsertRequest(ctx context.Context, row model.FriendRequest) error {
	return r.inner.InsertRequest(ctx, row)
}
func (r *overlayFriendRepo) UpdateRequest(ctx context.Context, row model.FriendRequest) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.UpdateRequest(ctx, row) })
}
func (r *overlayFriendRepo) GetRequest(ctx context.Context, id string) (model.FriendRequest, error) {
	return firstAcross(r.inner, r.others(), func(fr store.FriendRepo) (model.FriendRequest, error) {
		return fr.GetRequest(ctx, id)
	})
}
func (r *overlayFriendRepo) FindPendingBetween(ctx context.Context, a, b string) (model.FriendRequest, error) {
	return firstAcross(r.inner, r.others(), func(fr store.FriendRepo) (model.FriendRequest, error) {
		return fr.FindPendingBetween(ctx, a, b)
	})
}
func (r *overlayFriendRepo) ListPending(ctx context.Context, userID, direction string) ([]model.FriendRequest, error) {
	return allAcross(r.inner, r.others(), func(fr store.FriendRepo) ([]model.FriendRequest, error) {
		return fr.ListPending(ctx, userID, direction)
	})
}
func (r *overlayFriendRepo) CountPendingOutgoing(ctx context.Context, userID string) (int, error) {
	return r.sum(func(fr store.FriendRepo) (int, error) { return fr.CountPendingOutgoing(ctx, userID) })
}

// InsertFriendship writes the pair into both participants' cells so each user
// has a complete local friend view.
func (r *overlayFriendRepo) InsertFriendship(ctx context.Context, row model.Friendship) error {
	seen := map[int]bool{}
	for _, uid := range []string{row.UserLow, row.UserHigh} {
		i := r.s.byKey(ctx, uid, KindUser)
		if seen[i] {
			continue
		}
		seen[i] = true
		var err error
		if i == r.cell {
			err = r.inner.InsertFriendship(ctx, row)
		} else {
			err = r.s.cells[i].Friends().InsertFriendship(ctx, row)
		}
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}
func (r *overlayFriendRepo) DeleteFriendship(ctx context.Context, a, b string) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.DeleteFriendship(ctx, a, b) })
}
func (r *overlayFriendRepo) GetFriendship(ctx context.Context, a, b string) (model.Friendship, error) {
	return firstAcross(r.inner, r.others(), func(fr store.FriendRepo) (model.Friendship, error) {
		return fr.GetFriendship(ctx, a, b)
	})
}
func (r *overlayFriendRepo) CountFriendships(ctx context.Context, userID string) (int, error) {
	return r.userCell(ctx, userID).CountFriendships(ctx, userID)
}
func (r *overlayFriendRepo) ListFriendEdges(ctx context.Context, userID string) ([]model.FriendEdge, error) {
	return r.userCell(ctx, userID).ListFriendEdges(ctx, userID)
}

// userCell returns the repo whose database owns userID's own friend view.
func (r *overlayFriendRepo) userCell(ctx context.Context, userID string) store.FriendRepo {
	i := r.s.byKey(ctx, userID, KindUser)
	if i == r.cell {
		return r.inner
	}
	return r.s.cells[i].Friends()
}
func (r *overlayFriendRepo) InsertGroup(ctx context.Context, row model.FriendGroup) error {
	return r.userCell(ctx, row.OwnerID).InsertGroup(ctx, row)
}
func (r *overlayFriendRepo) UpdateGroup(ctx context.Context, row model.FriendGroup) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.UpdateGroup(ctx, row) })
}
func (r *overlayFriendRepo) DeleteGroup(ctx context.Context, ownerID, id string) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.DeleteGroup(ctx, ownerID, id) })
}
func (r *overlayFriendRepo) GetGroup(ctx context.Context, id string) (model.FriendGroup, error) {
	return firstAcross(r.inner, r.others(), func(fr store.FriendRepo) (model.FriendGroup, error) {
		return fr.GetGroup(ctx, id)
	})
}
func (r *overlayFriendRepo) ListGroups(ctx context.Context, ownerID string) ([]model.FriendGroup, error) {
	return r.userCell(ctx, ownerID).ListGroups(ctx, ownerID)
}
func (r *overlayFriendRepo) CountGroups(ctx context.Context, ownerID string) (int, error) {
	return r.userCell(ctx, ownerID).CountGroups(ctx, ownerID)
}
func (r *overlayFriendRepo) SetMembership(ctx context.Context, ownerID, friendUserID, groupID string) error {
	return r.userCell(ctx, ownerID).SetMembership(ctx, ownerID, friendUserID, groupID)
}
func (r *overlayFriendRepo) ClearMembership(ctx context.Context, ownerID, friendUserID string) error {
	return r.userCell(ctx, ownerID).ClearMembership(ctx, ownerID, friendUserID)
}
func (r *overlayFriendRepo) ClearMembershipsForPair(ctx context.Context, a, b string) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.ClearMembershipsForPair(ctx, a, b) })
}
func (r *overlayFriendRepo) ClearMembershipsForGroup(ctx context.Context, groupID string) error {
	return r.mutate(func(fr store.FriendRepo) error { return fr.ClearMembershipsForGroup(ctx, groupID) })
}
