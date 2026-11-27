package cell

import (
	"context"
	"errors"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// broadcast applies a mutation to every cell. Only the cell owning the row
// changes anything; the other cells report no matching row, which is ignored.
func broadcast(s *Store, fn func(store.Store) error) error {
	var first error
	for _, c := range s.cells {
		err := fn(c)
		if err != nil && !errors.Is(err, store.ErrNotFound) && first == nil {
			first = err
		}
	}
	return first
}

// firstOf returns the first successful cell result. It is used for lookups by
// an id whose owning cell is not encoded in the id (invites, friend requests).
func firstOf[T any](s *Store, fn func(store.Store) (T, error)) (T, error) {
	var zero T
	var firstErr error
	for _, c := range s.cells {
		v, err := fn(c)
		if err == nil {
			return v, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return zero, firstErr
	}
	return zero, store.ErrNotFound
}

// allOf merges a list read across every cell.
func allOf[T any](s *Store, fn func(store.Store) ([]T, error)) ([]T, error) {
	var out []T
	for _, c := range s.cells {
		part, err := fn(c)
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	if out == nil {
		out = []T{}
	}
	return out, nil
}

// ----------------------------------------------------------------- chat ----

type routingChatRepo struct{ s *Store }

func (r *routingChatRepo) InsertChat(ctx context.Context, c model.Chat) error {
	i := r.s.byRequest(ctx)
	if err := r.s.cells[i].Chats().InsertChat(ctx, c); err != nil {
		return err
	}
	r.s.loc.Remember(c.ID, i)
	return nil
}

func (r *routingChatRepo) UpdateChat(ctx context.Context, c model.Chat) error {
	return r.s.cells[r.s.byKey(ctx, c.ID, KindChat)].Chats().UpdateChat(ctx, c)
}

func (r *routingChatRepo) GetChat(ctx context.Context, id string) (model.Chat, error) {
	return r.s.cells[r.s.byKey(ctx, id, KindChat)].Chats().GetChat(ctx, id)
}

func (r *routingChatRepo) FindDirect(ctx context.Context, a, b string) (model.Chat, error) {
	return firstOf(r.s, func(c store.Store) (model.Chat, error) {
		return c.Chats().FindDirect(ctx, a, b)
	})
}

// ListByTask: chats are keyed by chat id, so a task's chats may be in any cell.
func (r *routingChatRepo) ListByTask(ctx context.Context, taskID string) ([]model.Chat, error) {
	return allOf(r.s, func(c store.Store) ([]model.Chat, error) {
		return c.Chats().ListByTask(ctx, taskID)
	})
}

func (r *routingChatRepo) InsertMessage(ctx context.Context, m model.Message) error {
	return r.s.cells[r.s.byKey(ctx, m.ChatID, KindChat)].Chats().InsertMessage(ctx, m)
}

// ListDirectUnreads is a per-user inbox. Chats are spread over cells, so the
// unread summary is merged from all of them.
func (r *routingChatRepo) ListDirectUnreads(ctx context.Context, userID string) ([]model.DirectUnread, error) {
	return allOf(r.s, func(c store.Store) ([]model.DirectUnread, error) {
		return c.Chats().ListDirectUnreads(ctx, userID)
	})
}

func (r *routingChatRepo) MessageRowID(ctx context.Context, chatID, messageID string) (int64, error) {
	return r.s.cells[r.s.byKey(ctx, chatID, KindChat)].Chats().MessageRowID(ctx, chatID, messageID)
}

func (r *routingChatRepo) LatestMessageRow(ctx context.Context, chatID string) (int64, string, bool, error) {
	return r.s.cells[r.s.byKey(ctx, chatID, KindChat)].Chats().LatestMessageRow(ctx, chatID)
}

func (r *routingChatRepo) CountUnread(ctx context.Context, userID, chatID string) (int64, error) {
	return r.s.cells[r.s.byKey(ctx, chatID, KindChat)].Chats().CountUnread(ctx, userID, chatID)
}

func (r *routingChatRepo) AdvanceRead(ctx context.Context, userID, chatID string, rowid int64, now time.Time) error {
	return r.s.cells[r.s.byKey(ctx, chatID, KindChat)].Chats().AdvanceRead(ctx, userID, chatID, rowid, now)
}

func (r *routingChatRepo) ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error) {
	return r.s.cells[r.s.byKey(ctx, chatID, KindChat)].Chats().ListMessages(ctx, chatID, limit, offset)
}

// --------------------------------------------------------------- invite ----

type routingInviteRepo struct{ s *Store }

func (r *routingInviteRepo) Upsert(ctx context.Context, inv model.Invite) error {
	i := r.s.byRequest(ctx)
	if err := r.s.cells[i].Invites().Upsert(ctx, inv); err != nil {
		return err
	}
	r.s.loc.Remember(inv.ID, i)
	return nil
}

func (r *routingInviteRepo) Update(ctx context.Context, inv model.Invite) error {
	return r.s.cells[r.s.byRequest(ctx)].Invites().Update(ctx, inv)
}

func (r *routingInviteRepo) Get(ctx context.Context, id string) (model.Invite, error) {
	return firstOf(r.s, func(c store.Store) (model.Invite, error) {
		return c.Invites().Get(ctx, id)
	})
}

func (r *routingInviteRepo) Find(ctx context.Context, taskID, userID string) (model.Invite, error) {
	return firstOf(r.s, func(c store.Store) (model.Invite, error) {
		return c.Invites().Find(ctx, taskID, userID)
	})
}

func (r *routingInviteRepo) ListByTask(ctx context.Context, taskID string) ([]model.Invite, error) {
	return allOf(r.s, func(c store.Store) ([]model.Invite, error) {
		return c.Invites().ListByTask(ctx, taskID)
	})
}

func (r *routingInviteRepo) ListByUser(ctx context.Context, userID, status string) ([]model.Invite, error) {
	return allOf(r.s, func(c store.Store) ([]model.Invite, error) {
		return c.Invites().ListByUser(ctx, userID, status)
	})
}

func (r *routingInviteRepo) DeleteByRole(ctx context.Context, taskID, roleID string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Invites().DeleteByRole(ctx, taskID, roleID)
	})
}

func (r *routingInviteRepo) DeleteByUser(ctx context.Context, taskID, userID string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Invites().DeleteByUser(ctx, taskID, userID)
	})
}

// --------------------------------------------------------------- friend ----

// Friend rows are written in the caller's cell and may be queried by the peer,
// so reads are fanned out. Writes stay pinned to one cell.
type routingFriendRepo struct{ s *Store }

func (r *routingFriendRepo) InsertRequest(ctx context.Context, row model.FriendRequest) error {
	return r.s.cells[r.s.byRequest(ctx)].Friends().InsertRequest(ctx, row)
}

func (r *routingFriendRepo) UpdateRequest(ctx context.Context, row model.FriendRequest) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().UpdateRequest(ctx, row)
	})
}

func (r *routingFriendRepo) GetRequest(ctx context.Context, id string) (model.FriendRequest, error) {
	return firstOf(r.s, func(c store.Store) (model.FriendRequest, error) {
		return c.Friends().GetRequest(ctx, id)
	})
}

func (r *routingFriendRepo) FindPendingBetween(ctx context.Context, a, b string) (model.FriendRequest, error) {
	return firstOf(r.s, func(c store.Store) (model.FriendRequest, error) {
		return c.Friends().FindPendingBetween(ctx, a, b)
	})
}

func (r *routingFriendRepo) ListPending(ctx context.Context, userID, direction string) ([]model.FriendRequest, error) {
	return allOf(r.s, func(c store.Store) ([]model.FriendRequest, error) {
		return c.Friends().ListPending(ctx, userID, direction)
	})
}

func (r *routingFriendRepo) CountPendingOutgoing(ctx context.Context, userID string) (int, error) {
	total := 0
	for _, c := range r.s.cells {
		n, err := c.Friends().CountPendingOutgoing(ctx, userID)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// InsertFriendship writes the pair into BOTH participants' cells. Each user
// then has a complete local view of their own friendships, so the friend list
// and its group membership can be read with a single-cell JOIN.
func (r *routingFriendRepo) InsertFriendship(ctx context.Context, row model.Friendship) error {
	seen := map[int]bool{}
	for _, uid := range []string{row.UserLow, row.UserHigh} {
		i := r.s.byKey(ctx, uid, KindUser)
		if seen[i] {
			continue
		}
		seen[i] = true
		if err := r.s.cells[i].Friends().InsertFriendship(ctx, row); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (r *routingFriendRepo) DeleteFriendship(ctx context.Context, a, b string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().DeleteFriendship(ctx, a, b)
	})
}

func (r *routingFriendRepo) GetFriendship(ctx context.Context, a, b string) (model.Friendship, error) {
	return firstOf(r.s, func(c store.Store) (model.Friendship, error) {
		return c.Friends().GetFriendship(ctx, a, b)
	})
}

// CountFriendships reads only the caller's own cell: the pair row is stored
// once in each participant's cell, so summing all cells would double count.
func (r *routingFriendRepo) CountFriendships(ctx context.Context, userID string) (int, error) {
	i := r.s.byKey(ctx, userID, KindUser)
	return r.s.cells[i].Friends().CountFriendships(ctx, userID)
}

// ListFriendEdges joins friendships to the users table inside each cell.
// In a cell deployment the friendship may live in one cell while the friend's
// user row lives in another, so the LEFT JOIN yields empty profile fields.
// Re-read each friend from its owning cell to restore nickname and scores.
func (r *routingFriendRepo) ListFriendEdges(ctx context.Context, userID string) ([]model.FriendEdge, error) {
	i := r.s.byKey(ctx, userID, KindUser)
	edges, err := r.s.cells[i].Friends().ListFriendEdges(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range edges {
		u, gerr := r.s.Users().Get(ctx, edges[i].UserID)
		if gerr != nil {
			continue // keep whatever the local join produced
		}
		edges[i].Nickname = u.Nickname
		edges[i].Integrity = u.Integrity
		edges[i].Ability = u.Ability
	}
	return edges, nil
}

func (r *routingFriendRepo) InsertGroup(ctx context.Context, row model.FriendGroup) error {
	return r.s.cells[r.s.byRequest(ctx)].Friends().InsertGroup(ctx, row)
}

func (r *routingFriendRepo) UpdateGroup(ctx context.Context, row model.FriendGroup) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().UpdateGroup(ctx, row)
	})
}

func (r *routingFriendRepo) DeleteGroup(ctx context.Context, ownerID, id string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().DeleteGroup(ctx, ownerID, id)
	})
}

func (r *routingFriendRepo) GetGroup(ctx context.Context, id string) (model.FriendGroup, error) {
	return firstOf(r.s, func(c store.Store) (model.FriendGroup, error) {
		return c.Friends().GetGroup(ctx, id)
	})
}

func (r *routingFriendRepo) ListGroups(ctx context.Context, ownerID string) ([]model.FriendGroup, error) {
	i := r.s.byKey(ctx, ownerID, KindUser)
	return r.s.cells[i].Friends().ListGroups(ctx, ownerID)
}

func (r *routingFriendRepo) CountGroups(ctx context.Context, ownerID string) (int, error) {
	i := r.s.byKey(ctx, ownerID, KindUser)
	return r.s.cells[i].Friends().CountGroups(ctx, ownerID)
}

func (r *routingFriendRepo) SetMembership(ctx context.Context, ownerID, friendUserID, groupID string) error {
	i := r.s.byKey(ctx, ownerID, KindUser)
	return r.s.cells[i].Friends().SetMembership(ctx, ownerID, friendUserID, groupID)
}

func (r *routingFriendRepo) ClearMembership(ctx context.Context, ownerID, friendUserID string) error {
	i := r.s.byKey(ctx, ownerID, KindUser)
	return r.s.cells[i].Friends().ClearMembership(ctx, ownerID, friendUserID)
}

func (r *routingFriendRepo) ClearMembershipsForPair(ctx context.Context, a, b string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().ClearMembershipsForPair(ctx, a, b)
	})
}

func (r *routingFriendRepo) ClearMembershipsForGroup(ctx context.Context, groupID string) error {
	return broadcast(r.s, func(c store.Store) error {
		return c.Friends().ClearMembershipsForGroup(ctx, groupID)
	})
}

// ----------------------------------------------------------------- idem ----

// Idempotency keys are pinned to the same cell as the surrounding InTx so a
// repeated request replays from the same database.
type routingIdemRepo struct{ s *Store }

func (r *routingIdemRepo) Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	return r.s.cells[r.s.byRequest(ctx)].Idem().Get(ctx, userID, method, path, key)
}

func (r *routingIdemRepo) GetLocked(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	return r.s.cells[r.s.byRequest(ctx)].Idem().GetLocked(ctx, userID, method, path, key)
}

func (r *routingIdemRepo) Put(ctx context.Context, rec model.IdemRecord) error {
	return r.s.cells[r.s.byRequest(ctx)].Idem().Put(ctx, rec)
}
