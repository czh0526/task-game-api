package engine

import (
	"context"
	"errors"
	"net/http"
	"unicode"
	"unicode/utf8"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

const (
	maxFriendGroups                = 50
	maxFriends                     = 500
	maxPendingFriendRequests       = 100
	maxGroupNameRunes              = 32
	minGroupSort             int64 = -1_000_000
	maxGroupSort             int64 = 1_000_000
)

// ListFriends returns the caller's friends in personal groups. Another user's
// grouping of the same people is not included. Emails and secrets are not.
func (e *Engine) ListFriends(ctx context.Context, userID string) (*types.FriendList, error) {
	groups, err := e.store.Friends().ListGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	edges, err := e.store.Friends().ListFriendEdges(ctx, userID)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		known[g.ID] = struct{}{}
	}
	byGroup := make(map[string][]types.Friend, len(groups))
	ungrouped := make([]types.Friend, 0)
	for _, edge := range edges {
		card := toFriend(edge)
		if edge.GroupID != "" {
			if _, ok := known[edge.GroupID]; ok {
				byGroup[edge.GroupID] = append(byGroup[edge.GroupID], card)
				continue
			}
		}
		ungrouped = append(ungrouped, card)
	}
	outGroups := make([]types.FriendGroupWithFriends, 0, len(groups))
	for _, g := range groups {
		friends := byGroup[g.ID]
		if friends == nil {
			friends = []types.Friend{}
		}
		outGroups = append(outGroups, types.FriendGroupWithFriends{
			Id: g.ID, Name: g.Name, SortOrder: g.SortOrder, Friends: friends,
		})
	}
	return &types.FriendList{Groups: outGroups, Ungrouped: ungrouped}, nil
}

// CreateFriendRequest sends a mutual-friend request.
//
// Already friends, or a pending request the caller already sent, is 409 and
// does not insert a second row. If the other user already has a pending
// request toward the caller, that existing request is accepted instead.
func (e *Engine) CreateFriendRequest(ctx context.Context, userID string, req *types.CreateFriendRequestReq) (*types.FriendRequest, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/friends/requests", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendRequest, error) {
		target := trim(req.UserId)
		if !id.ValidUserID(target) {
			return nil, biz.Invalid("user_id is invalid")
		}
		if target == userID {
			return nil, biz.Invalid("cannot friend yourself")
		}
		if _, err := r.Users().Ensure(ctx, userID, e.now()); err != nil {
			return nil, err
		}
		if _, err := r.Users().Get(ctx, target); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, biz.NotFound("user not found")
			}
			return nil, err
		}
		if err := e.rejectUnlessNotFriends(ctx, r, userID, target); err != nil {
			return nil, err
		}
		pending, err := r.Friends().FindPendingBetween(ctx, userID, target)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if err == nil {
			if pending.FromUserID == userID {
				return nil, biz.Conflict("a pending friend request already exists")
			}
			return e.acceptRequest(ctx, r, userID, pending)
		}
		n, err := r.Friends().CountPendingOutgoing(ctx, userID)
		if err != nil {
			return nil, err
		}
		if n >= maxPendingFriendRequests {
			return nil, biz.Conflict("pending friend request limit reached")
		}
		now := e.now()
		row := model.FriendRequest{
			ID: id.New("frq_"), FromUserID: userID, ToUserID: target,
			Status: model.FriendPending, CreatedAt: now, UpdatedAt: now,
		}
		if err := r.Friends().InsertRequest(ctx, row); err != nil {
			if errors.Is(err, store.ErrConflict) {
				again, ferr := r.Friends().FindPendingBetween(ctx, userID, target)
				if ferr != nil {
					return nil, biz.Conflict("a pending friend request already exists")
				}
				if again.FromUserID == userID {
					return nil, biz.Conflict("a pending friend request already exists")
				}
				return e.acceptRequest(ctx, r, userID, again)
			}
			return nil, err
		}
		if err := e.emit(ctx, r, []string{target}, model.EventFriendRequested, friendRequestData(row)); err != nil {
			return nil, err
		}
		out := toFriendRequest(row)
		return &out, nil
	})
}

// ListFriendRequests returns the caller's pending incoming or outgoing requests.
func (e *Engine) ListFriendRequests(ctx context.Context, userID, direction string) (*types.FriendRequestList, error) {
	switch direction {
	case model.FriendIncoming, model.FriendOutgoing:
	default:
		return nil, biz.Invalid("direction must be incoming or outgoing")
	}
	rows, err := e.store.Friends().ListPending(ctx, userID, direction)
	if err != nil {
		return nil, err
	}
	items := make([]types.FriendRequest, 0, len(rows))
	for _, row := range rows {
		items = append(items, toFriendRequest(row))
	}
	return &types.FriendRequestList{Items: items}, nil
}

// AcceptFriendRequest accepts a pending request addressed to the caller.
func (e *Engine) AcceptFriendRequest(ctx context.Context, userID string, req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/friend-requests/"+req.RequestId+"/accept", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendRequest, error) {
		row, err := e.loadFriendRequest(ctx, r, req.RequestId)
		if err != nil {
			return nil, err
		}
		return e.acceptRequest(ctx, r, userID, row)
	})
}

// DeclineFriendRequest declines a pending request addressed to the caller.
func (e *Engine) DeclineFriendRequest(ctx context.Context, userID string, req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/friend-requests/"+req.RequestId+"/decline", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendRequest, error) {
		row, err := e.loadFriendRequest(ctx, r, req.RequestId)
		if err != nil {
			return nil, err
		}
		if row.ToUserID != userID {
			return nil, biz.Forbidden("only the recipient can decline")
		}
		if row.Status != model.FriendPending {
			return nil, biz.Conflict("friend request is not pending")
		}
		row.Status = model.FriendDeclined
		row.UpdatedAt = e.now()
		if err := r.Friends().UpdateRequest(ctx, row); err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, []string{row.FromUserID}, model.EventFriendDeclined, friendRequestData(row)); err != nil {
			return nil, err
		}
		out := toFriendRequest(row)
		return &out, nil
	})
}

// CancelFriendRequest cancels the caller's own pending outgoing request.
// Cancel does not emit a webhook.
func (e *Engine) CancelFriendRequest(ctx context.Context, userID string, req *types.FriendRequestActionReq) (*types.FriendRequest, error) {
	return call(e, ctx, userID, http.MethodDelete, "/v1/friend-requests/"+req.RequestId, req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendRequest, error) {
		row, err := e.loadFriendRequest(ctx, r, req.RequestId)
		if err != nil {
			return nil, err
		}
		if row.FromUserID != userID {
			return nil, biz.Forbidden("only the requester can cancel")
		}
		if row.Status != model.FriendPending {
			return nil, biz.Conflict("friend request is not pending")
		}
		row.Status = model.FriendCancelled
		row.UpdatedAt = e.now()
		if err := r.Friends().UpdateRequest(ctx, row); err != nil {
			return nil, err
		}
		out := toFriendRequest(row)
		return &out, nil
	})
}

// Unfriend ends the mutual friendship and clears both users' group membership for the pair.
func (e *Engine) Unfriend(ctx context.Context, userID string, req *types.UnfriendReq) (*types.RemovedFriend, error) {
	return call(e, ctx, userID, http.MethodDelete, "/v1/friends/"+req.UserId, req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.RemovedFriend, error) {
		other := trim(req.UserId)
		if !id.ValidUserID(other) {
			return nil, biz.Invalid("userId is invalid")
		}
		if _, err := r.Friends().GetFriendship(ctx, userID, other); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, biz.NotFound("friend not found")
			}
			return nil, err
		}
		if err := r.Friends().DeleteFriendship(ctx, userID, other); err != nil {
			return nil, err
		}
		if err := r.Friends().ClearMembershipsForPair(ctx, userID, other); err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, []string{other}, model.EventFriendRemoved, map[string]any{
			"user_id": userID, "friend_user_id": other,
		}); err != nil {
			return nil, err
		}
		return &types.RemovedFriend{UserId: other}, nil
	})
}

// ListFriendGroups returns the caller's folders without their members.
func (e *Engine) ListFriendGroups(ctx context.Context, userID string) (*types.FriendGroupList, error) {
	rows, err := e.store.Friends().ListGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]types.FriendGroup, 0, len(rows))
	for _, row := range rows {
		items = append(items, toFriendGroup(row))
	}
	return &types.FriendGroupList{Items: items}, nil
}

// CreateFriendGroup adds a personal folder. Names are trimmed and unique per owner.
func (e *Engine) CreateFriendGroup(ctx context.Context, userID string, req *types.CreateFriendGroupReq) (*types.FriendGroup, error) {
	return call(e, ctx, userID, http.MethodPost, "/v1/friend-groups", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendGroup, error) {
		name, err := cleanGroupName(req.Name)
		if err != nil {
			return nil, err
		}
		n, err := r.Friends().CountGroups(ctx, userID)
		if err != nil {
			return nil, err
		}
		if n >= maxFriendGroups {
			return nil, biz.Conflict("friend group limit reached")
		}
		now := e.now()
		row := model.FriendGroup{
			ID: id.New("fgp_"), OwnerID: userID, Name: name, SortOrder: int64(n),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := r.Friends().InsertGroup(ctx, row); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, biz.Conflict("group name already exists")
			}
			return nil, err
		}
		out := toFriendGroup(row)
		return &out, nil
	})
}

// UpdateFriendGroup renames and/or reorders a group the caller owns.
func (e *Engine) UpdateFriendGroup(ctx context.Context, userID string, req *types.PatchFriendGroupReq) (*types.FriendGroup, error) {
	return call(e, ctx, userID, http.MethodPatch, "/v1/friend-groups/"+req.GroupId, req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendGroup, error) {
		if req.Name == nil && req.SortOrder == nil {
			return nil, biz.Invalid("name or sort_order is required")
		}
		row, err := e.loadOwnedGroup(ctx, r, userID, req.GroupId)
		if err != nil {
			return nil, err
		}
		if req.Name != nil {
			name, err := cleanGroupName(*req.Name)
			if err != nil {
				return nil, err
			}
			row.Name = name
		}
		if req.SortOrder != nil {
			if *req.SortOrder < minGroupSort || *req.SortOrder > maxGroupSort {
				return nil, biz.Invalid("sort_order must be between -1000000 and 1000000")
			}
			row.SortOrder = *req.SortOrder
		}
		row.UpdatedAt = e.now()
		if err := r.Friends().UpdateGroup(ctx, row); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, biz.Conflict("group name already exists")
			}
			return nil, err
		}
		out := toFriendGroup(row)
		return &out, nil
	})
}

// DeleteFriendGroup removes a folder. Friends in it become ungrouped and stay friends.
func (e *Engine) DeleteFriendGroup(ctx context.Context, userID string, req *types.DeleteFriendGroupReq) (*types.FriendGroup, error) {
	return call(e, ctx, userID, http.MethodDelete, "/v1/friend-groups/"+req.GroupId, req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendGroup, error) {
		row, err := e.loadOwnedGroup(ctx, r, userID, req.GroupId)
		if err != nil {
			return nil, err
		}
		if err := r.Friends().ClearMembershipsForGroup(ctx, row.ID); err != nil {
			return nil, err
		}
		if err := r.Friends().DeleteGroup(ctx, userID, row.ID); err != nil {
			return nil, err
		}
		out := toFriendGroup(row)
		return &out, nil
	})
}

// SetFriendGroup moves a friend into one of the caller's groups, or ungroups them
// when group_id is null or omitted. An empty string is invalid.
func (e *Engine) SetFriendGroup(ctx context.Context, userID string, req *types.SetFriendGroupReq) (*types.FriendPlacement, error) {
	return call(e, ctx, userID, http.MethodPut, "/v1/friends/"+req.UserId+"/group", req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.FriendPlacement, error) {
		friendID := trim(req.UserId)
		if !id.ValidUserID(friendID) {
			return nil, biz.Invalid("userId is invalid")
		}
		if _, err := r.Friends().GetFriendship(ctx, userID, friendID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, biz.NotFound("friend not found")
			}
			return nil, err
		}
		if req.GroupId == nil {
			if err := r.Friends().ClearMembership(ctx, userID, friendID); err != nil {
				return nil, err
			}
			return &types.FriendPlacement{UserId: friendID}, nil
		}
		groupID := trim(*req.GroupId)
		if groupID == "" {
			return nil, biz.Invalid("group_id is invalid")
		}
		if _, err := e.loadOwnedGroup(ctx, r, userID, groupID); err != nil {
			return nil, err
		}
		if err := r.Friends().SetMembership(ctx, userID, friendID, groupID); err != nil {
			return nil, err
		}
		return &types.FriendPlacement{UserId: friendID, GroupId: &groupID}, nil
	})
}

func (e *Engine) acceptRequest(ctx context.Context, r store.Repos, userID string, row model.FriendRequest) (*types.FriendRequest, error) {
	if row.ToUserID != userID {
		return nil, biz.Forbidden("only the recipient can accept")
	}
	if row.Status != model.FriendPending {
		return nil, biz.Conflict("friend request is not pending")
	}
	if err := e.rejectUnlessNotFriends(ctx, r, row.FromUserID, row.ToUserID); err != nil {
		return nil, err
	}
	for _, uid := range []string{row.FromUserID, row.ToUserID} {
		n, err := r.Friends().CountFriendships(ctx, uid)
		if err != nil {
			return nil, err
		}
		if n >= maxFriends {
			return nil, biz.Conflict("friend limit reached")
		}
	}
	now := e.now()
	if err := r.Friends().InsertFriendship(ctx, model.Friendship{
		UserLow: row.FromUserID, UserHigh: row.ToUserID, CreatedAt: now,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, biz.Conflict("already friends")
		}
		return nil, err
	}
	row.Status = model.FriendAccepted
	row.UpdatedAt = now
	if err := r.Friends().UpdateRequest(ctx, row); err != nil {
		return nil, err
	}
	if err := e.emit(ctx, r, []string{row.FromUserID}, model.EventFriendAccepted, friendRequestData(row)); err != nil {
		return nil, err
	}
	out := toFriendRequest(row)
	return &out, nil
}

func (e *Engine) rejectUnlessNotFriends(ctx context.Context, r store.Repos, a, b string) error {
	_, err := r.Friends().GetFriendship(ctx, a, b)
	if err == nil {
		return biz.Conflict("already friends")
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (e *Engine) loadFriendRequest(ctx context.Context, r store.Repos, requestID string) (model.FriendRequest, error) {
	if !id.ValidFriendRequestID(requestID) {
		return model.FriendRequest{}, biz.Invalid("requestId must match ^frq_")
	}
	row, err := r.Friends().GetRequest(ctx, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return model.FriendRequest{}, biz.NotFound("friend request not found")
	}
	return row, err
}

func (e *Engine) loadOwnedGroup(ctx context.Context, r store.Repos, userID, groupID string) (model.FriendGroup, error) {
	if !id.ValidFriendGroupID(groupID) {
		return model.FriendGroup{}, biz.Invalid("groupId must match ^fgp_")
	}
	row, err := r.Friends().GetGroup(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.OwnerID != userID) {
		return model.FriendGroup{}, biz.NotFound("group not found")
	}
	return row, err
}

func cleanGroupName(raw string) (string, error) {
	name := trim(raw)
	if name == "" || utf8.RuneCountInString(name) > maxGroupNameRunes {
		return "", biz.Invalid("group name must be 1 to 32 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", biz.Invalid("group name must be 1 to 32 characters")
		}
	}
	return name, nil
}

func friendRequestData(row model.FriendRequest) map[string]any {
	return map[string]any{
		"request_id":   row.ID,
		"from_user_id": row.FromUserID,
		"to_user_id":   row.ToUserID,
	}
}

func toFriendRequest(row model.FriendRequest) types.FriendRequest {
	return types.FriendRequest{
		Id:         row.ID,
		FromUserId: row.FromUserID,
		ToUserId:   row.ToUserID,
		Status:     row.Status,
		CreatedAt:  model.FormatTime(row.CreatedAt),
		UpdatedAt:  model.FormatTime(row.UpdatedAt),
	}
}

func toFriend(edge model.FriendEdge) types.Friend {
	return types.Friend{
		UserId:    edge.UserID,
		Integrity: edge.Integrity,
		Ability:   edge.Ability,
		Since:     model.FormatTime(edge.Since),
	}
}

func toFriendGroup(row model.FriendGroup) types.FriendGroup {
	return types.FriendGroup{Id: row.ID, Name: row.Name, SortOrder: row.SortOrder}
}
