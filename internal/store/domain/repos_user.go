package domain

import (
	"context"
	"errors"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"
)

// ---------------------------------------------------------------- users ----

type userRepo struct{ s *Store }

func (r *userRepo) Ensure(ctx context.Context, id string, now time.Time) (model.User, error) {
	repos, err := r.s.userRepos(ctx, id, true)
	if err != nil {
		return model.User{}, err
	}
	return repos.Users().Ensure(ctx, id, now)
}

func (r *userRepo) Get(ctx context.Context, id string) (model.User, error) {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return model.User{}, err
	}
	return repos.Users().Get(ctx, id)
}

// GetCredentials reads the password-bearing user from the shard that owns id,
// resolving through the directory. Used by control-plane login.
func (r *userRepo) GetCredentials(ctx context.Context, id string) (model.User, error) {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return model.User{}, err
	}
	return repos.Users().(interface {
		GetCredentials(context.Context, string) (model.User, error)
	}).GetCredentials(ctx, id)
}

func (r *userRepo) InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error) {
	repos, err := r.s.userRepos(ctx, id, true)
	if err != nil {
		return model.User{}, err
	}
	return repos.Users().InsertRegistered(ctx, id, email, passwordHash, now)
}

// GetByEmail fans out to every user shard. Stage 3 replaces this with
// global_unique(email -> user_id) + ResolveExisting, avoiding a scatter.
func (r *userRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	return findAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (model.User, error) {
		return repos.Users().GetByEmail(ctx, email)
	})
}

func (r *userRepo) SetNickname(ctx context.Context, id, nickname string) error {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return err
	}
	return repos.Users().SetNickname(ctx, id, nickname)
}

func (r *userRepo) SetAvatar(ctx context.Context, id string, jpeg []byte, updatedAt string) error {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return err
	}
	return repos.Users().SetAvatar(ctx, id, jpeg, updatedAt)
}

func (r *userRepo) GetAvatar(ctx context.Context, id string) (model.UserAvatar, error) {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return model.UserAvatar{}, err
	}
	return repos.Users().GetAvatar(ctx, id)
}

func (r *userRepo) AddBalance(ctx context.Context, id string, delta int64) error {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return err
	}
	return repos.Users().AddBalance(ctx, id, delta)
}

func (r *userRepo) ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error {
	repos, err := r.s.userRepos(ctx, id, false)
	if err != nil {
		return err
	}
	return repos.Users().ApplyScore(ctx, id, deltaIntegrity, deltaAbility)
}

func (r *userRepo) AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error {
	repos, err := r.s.userRepos(ctx, ev.UserID, false)
	if err != nil {
		return err
	}
	return repos.Users().AddScoreEvent(ctx, ev)
}

func (r *userRepo) ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error) {
	repos, err := r.s.userRepos(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return repos.Users().ListScoreEvents(ctx, userID, limit)
}

// --------------------------------------------------------------- friends ----

type friendRepo struct{ s *Store }

func (r *friendRepo) InsertRequest(ctx context.Context, row model.FriendRequest) error {
	repos, err := r.s.userRepos(ctx, row.FromUserID, false)
	if err != nil {
		return err
	}
	return repos.Friends().InsertRequest(ctx, row)
}

func (r *friendRepo) UpdateRequest(ctx context.Context, row model.FriendRequest) error {
	return writeAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) error {
		return repos.Friends().UpdateRequest(ctx, row)
	})
}

func (r *friendRepo) GetRequest(ctx context.Context, id string) (model.FriendRequest, error) {
	return findAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (model.FriendRequest, error) {
		return repos.Friends().GetRequest(ctx, id)
	})
}

func (r *friendRepo) FindPendingBetween(ctx context.Context, a, b string) (model.FriendRequest, error) {
	return findAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (model.FriendRequest, error) {
		return repos.Friends().FindPendingBetween(ctx, a, b)
	})
}

func (r *friendRepo) ListPending(ctx context.Context, userID, direction string) ([]model.FriendRequest, error) {
	return collectAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) ([]model.FriendRequest, error) {
		return repos.Friends().ListPending(ctx, userID, direction)
	})
}

func (r *friendRepo) CountPendingOutgoing(ctx context.Context, userID string) (int, error) {
	return sumAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (int, error) {
		return repos.Friends().CountPendingOutgoing(ctx, userID)
	})
}

// InsertFriendship keeps the existing double-write: one row in each member's
// user shard. Two members may resolve to the SAME shard (co-located cells or a
// hash collision), so dedupe by placement and treat ErrConflict as success —
// exactly like cell.overlayFriendRepo. Otherwise a second insert into the same
// database violates the primary key and surfaces as a bogus "already friends".
// This is never a 2PC transaction: after a physical split it stays two local
// writes with idempotent repair.
func (r *friendRepo) InsertFriendship(ctx context.Context, row model.Friendship) error {
	seen := map[int]bool{}
	for _, uid := range []string{row.UserLow, row.UserHigh} {
		p, err := r.s.place(ctx, plane.DomainUser, uid, false)
		if err != nil {
			return err
		}
		if seen[p.Shard] {
			continue
		}
		seen[p.Shard] = true
		repos, err := r.s.pickPlacement(ctx, p)
		if err != nil {
			return err
		}
		if err := repos.Friends().InsertFriendship(ctx, row); err != nil &&
			!errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (r *friendRepo) DeleteFriendship(ctx context.Context, a, b string) error {
	return writeAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) error {
		return repos.Friends().DeleteFriendship(ctx, a, b)
	})
}

func (r *friendRepo) GetFriendship(ctx context.Context, a, b string) (model.Friendship, error) {
	return findAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (model.Friendship, error) {
		return repos.Friends().GetFriendship(ctx, a, b)
	})
}

func (r *friendRepo) CountFriendships(ctx context.Context, userID string) (int, error) {
	repos, err := r.s.userRepos(ctx, userID, false)
	if err != nil {
		return 0, err
	}
	return repos.Friends().CountFriendships(ctx, userID)
}

func (r *friendRepo) ListFriendEdges(ctx context.Context, userID string) ([]model.FriendEdge, error) {
	repos, err := r.s.userRepos(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	edges, err := repos.Friends().ListFriendEdges(ctx, userID)
	if err != nil {
		return nil, err
	}
	// The SQL LEFT JOIN only sees users in the shard that owns the friendship
	// view. Peers may live in another shard, so re-read each peer through the
	// cross-shard Users().Get and overwrite the local join's snapshot. This
	// mirrors cell.routingFriendRepo.ListFriendEdges.
	for i := range edges {
		u, gerr := r.s.Users().Get(ctx, edges[i].UserID)
		if gerr != nil {
			continue
		}
		edges[i].Nickname = u.Nickname
		edges[i].Integrity = u.Integrity
		edges[i].Ability = u.Ability
	}
	return edges, nil
}

func (r *friendRepo) InsertGroup(ctx context.Context, row model.FriendGroup) error {
	repos, err := r.s.userRepos(ctx, row.OwnerID, false)
	if err != nil {
		return err
	}
	return repos.Friends().InsertGroup(ctx, row)
}

func (r *friendRepo) UpdateGroup(ctx context.Context, row model.FriendGroup) error {
	repos, err := r.s.userRepos(ctx, row.OwnerID, false)
	if err != nil {
		return err
	}
	return repos.Friends().UpdateGroup(ctx, row)
}

func (r *friendRepo) DeleteGroup(ctx context.Context, ownerID, id string) error {
	repos, err := r.s.userRepos(ctx, ownerID, false)
	if err != nil {
		return err
	}
	return repos.Friends().DeleteGroup(ctx, ownerID, id)
}

func (r *friendRepo) GetGroup(ctx context.Context, id string) (model.FriendGroup, error) {
	return findAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) (model.FriendGroup, error) {
		return repos.Friends().GetGroup(ctx, id)
	})
}

func (r *friendRepo) ListGroups(ctx context.Context, ownerID string) ([]model.FriendGroup, error) {
	repos, err := r.s.userRepos(ctx, ownerID, false)
	if err != nil {
		return nil, err
	}
	return repos.Friends().ListGroups(ctx, ownerID)
}

func (r *friendRepo) CountGroups(ctx context.Context, ownerID string) (int, error) {
	repos, err := r.s.userRepos(ctx, ownerID, false)
	if err != nil {
		return 0, err
	}
	return repos.Friends().CountGroups(ctx, ownerID)
}

func (r *friendRepo) SetMembership(ctx context.Context, ownerID, friendUserID, groupID string) error {
	repos, err := r.s.userRepos(ctx, ownerID, false)
	if err != nil {
		return err
	}
	return repos.Friends().SetMembership(ctx, ownerID, friendUserID, groupID)
}

func (r *friendRepo) ClearMembership(ctx context.Context, ownerID, friendUserID string) error {
	repos, err := r.s.userRepos(ctx, ownerID, false)
	if err != nil {
		return err
	}
	return repos.Friends().ClearMembership(ctx, ownerID, friendUserID)
}

func (r *friendRepo) ClearMembershipsForPair(ctx context.Context, a, b string) error {
	return writeAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) error {
		return repos.Friends().ClearMembershipsForPair(ctx, a, b)
	})
}

func (r *friendRepo) ClearMembershipsForGroup(ctx context.Context, groupID string) error {
	return writeAcross(r.s, ctx, plane.DomainUser, func(repos store.Repos) error {
		return repos.Friends().ClearMembershipsForGroup(ctx, groupID)
	})
}

// ------------------------------------------------------------------ idem ----

type idemRepo struct{ s *Store }

func (r *idemRepo) Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	repos, err := r.s.userRepos(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return repos.Idem().Get(ctx, userID, method, path, key)
}

func (r *idemRepo) GetLocked(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	repos, err := r.s.userRepos(ctx, userID, false)
	if err != nil {
		return nil, err
	}
	return repos.Idem().GetLocked(ctx, userID, method, path, key)
}

func (r *idemRepo) Put(ctx context.Context, rec model.IdemRecord) error {
	repos, err := r.s.userRepos(ctx, rec.UserID, false)
	if err != nil {
		return err
	}
	return repos.Idem().Put(ctx, rec)
}
