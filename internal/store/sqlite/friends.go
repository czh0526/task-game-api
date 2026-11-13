package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

type friendRepo struct{ q querier }

func isUniqueConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (r *friendRepo) InsertRequest(ctx context.Context, row model.FriendRequest) error {
	low, high := model.OrderedPair(row.FromUserID, row.ToUserID)
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO friend_requests (
			id, from_user_id, to_user_id, pair_low, pair_high, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.FromUserID, row.ToUserID, low, high, row.Status,
		model.FormatTime(row.CreatedAt), model.FormatTime(row.UpdatedAt))
	if isUniqueConflict(err) {
		return store.ErrConflict
	}
	return err
}

func (r *friendRepo) UpdateRequest(ctx context.Context, row model.FriendRequest) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE friend_requests SET status = ?, updated_at = ? WHERE id = ?`,
		row.Status, model.FormatTime(row.UpdatedAt), row.ID)
	if err != nil {
		if isUniqueConflict(err) {
			return store.ErrConflict
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (r *friendRepo) GetRequest(ctx context.Context, id string) (model.FriendRequest, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests WHERE id = ?`, id)
	out, err := scanFriendRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FriendRequest{}, store.ErrNotFound
	}
	return out, err
}

func (r *friendRepo) FindPendingBetween(ctx context.Context, a, b string) (model.FriendRequest, error) {
	low, high := model.OrderedPair(a, b)
	row := r.q.QueryRowContext(ctx, `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE status = ? AND pair_low = ? AND pair_high = ?`, model.FriendPending, low, high)
	out, err := scanFriendRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FriendRequest{}, store.ErrNotFound
	}
	return out, err
}

func (r *friendRepo) ListPending(ctx context.Context, userID, direction string) ([]model.FriendRequest, error) {
	column := "to_user_id"
	if direction == model.FriendOutgoing {
		column = "from_user_id"
	} else if direction != model.FriendIncoming {
		return nil, fmt.Errorf("friend direction %q", direction)
	}
	query := `
		SELECT id, from_user_id, to_user_id, status, created_at, updated_at
		FROM friend_requests
		WHERE ` + column + ` = ? AND status = ?
		ORDER BY created_at ASC, id ASC`
	rows, err := r.q.QueryContext(ctx, query, userID, model.FriendPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FriendRequest
	for rows.Next() {
		item, err := scanFriendRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if out == nil {
		out = []model.FriendRequest{}
	}
	return out, rows.Err()
}

func (r *friendRepo) CountPendingOutgoing(ctx context.Context, userID string) (int, error) {
	return r.count(ctx, `
		SELECT COUNT(*) FROM friend_requests WHERE from_user_id = ? AND status = ?`,
		userID, model.FriendPending)
}

func (r *friendRepo) InsertFriendship(ctx context.Context, row model.Friendship) error {
	low, high := model.OrderedPair(row.UserLow, row.UserHigh)
	if low == "" || low >= high {
		return fmt.Errorf("friendship pair is not ordered")
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO friendships (user_low, user_high, created_at) VALUES (?, ?, ?)`,
		low, high, model.FormatTime(row.CreatedAt))
	if isUniqueConflict(err) {
		return store.ErrConflict
	}
	return err
}

func (r *friendRepo) DeleteFriendship(ctx context.Context, a, b string) error {
	low, high := model.OrderedPair(a, b)
	res, err := r.q.ExecContext(ctx, `DELETE FROM friendships WHERE user_low = ? AND user_high = ?`, low, high)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (r *friendRepo) GetFriendship(ctx context.Context, a, b string) (model.Friendship, error) {
	low, high := model.OrderedPair(a, b)
	if low == "" || low >= high {
		return model.Friendship{}, store.ErrNotFound
	}
	var created string
	var out model.Friendship
	err := r.q.QueryRowContext(ctx, `
		SELECT user_low, user_high, created_at FROM friendships WHERE user_low = ? AND user_high = ?`,
		low, high).Scan(&out.UserLow, &out.UserHigh, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Friendship{}, store.ErrNotFound
	}
	if err != nil {
		return model.Friendship{}, err
	}
	out.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.Friendship{}, err
	}
	return out, nil
}

func (r *friendRepo) CountFriendships(ctx context.Context, userID string) (int, error) {
	return r.count(ctx, `
		SELECT COUNT(*) FROM friendships WHERE user_low = ? OR user_high = ?`, userID, userID)
}

func (r *friendRepo) ListFriendEdges(ctx context.Context, userID string) ([]model.FriendEdge, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT
			CASE WHEN f.user_low = ? THEN f.user_high ELSE f.user_low END,
			COALESCE(u.integrity, 0),
			COALESCE(u.ability, 0),
			f.created_at,
			COALESCE(m.group_id, '')
		FROM friendships f
		LEFT JOIN users u ON u.id = CASE WHEN f.user_low = ? THEN f.user_high ELSE f.user_low END
		LEFT JOIN friend_group_members m
			ON m.owner_id = ? AND m.friend_user_id = CASE WHEN f.user_low = ? THEN f.user_high ELSE f.user_low END
		WHERE f.user_low = ? OR f.user_high = ?
		ORDER BY f.created_at ASC, 1 ASC`,
		userID, userID, userID, userID, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FriendEdge
	for rows.Next() {
		var edge model.FriendEdge
		var since string
		if err := rows.Scan(&edge.UserID, &edge.Integrity, &edge.Ability, &since, &edge.GroupID); err != nil {
			return nil, err
		}
		edge.Since, err = model.ParseTime(since)
		if err != nil {
			return nil, err
		}
		out = append(out, edge)
	}
	if out == nil {
		out = []model.FriendEdge{}
	}
	return out, rows.Err()
}

func (r *friendRepo) InsertGroup(ctx context.Context, row model.FriendGroup) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO friend_groups (id, owner_id, name, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		row.ID, row.OwnerID, row.Name, row.SortOrder,
		model.FormatTime(row.CreatedAt), model.FormatTime(row.UpdatedAt))
	if isUniqueConflict(err) {
		return store.ErrConflict
	}
	return err
}

func (r *friendRepo) UpdateGroup(ctx context.Context, row model.FriendGroup) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE friend_groups SET name = ?, sort_order = ?, updated_at = ?
		WHERE id = ? AND owner_id = ?`,
		row.Name, row.SortOrder, model.FormatTime(row.UpdatedAt), row.ID, row.OwnerID)
	if isUniqueConflict(err) {
		return store.ErrConflict
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (r *friendRepo) DeleteGroup(ctx context.Context, ownerID, id string) error {
	res, err := r.q.ExecContext(ctx, `DELETE FROM friend_groups WHERE id = ? AND owner_id = ?`, id, ownerID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (r *friendRepo) GetGroup(ctx context.Context, id string) (model.FriendGroup, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT id, owner_id, name, sort_order, created_at, updated_at FROM friend_groups WHERE id = ?`, id)
	out, err := scanFriendGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FriendGroup{}, store.ErrNotFound
	}
	return out, err
}

func (r *friendRepo) ListGroups(ctx context.Context, ownerID string) ([]model.FriendGroup, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_id, name, sort_order, created_at, updated_at
		FROM friend_groups WHERE owner_id = ?
		ORDER BY sort_order ASC, created_at ASC, id ASC`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FriendGroup
	for rows.Next() {
		item, err := scanFriendGroup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if out == nil {
		out = []model.FriendGroup{}
	}
	return out, rows.Err()
}

func (r *friendRepo) CountGroups(ctx context.Context, ownerID string) (int, error) {
	return r.count(ctx, `SELECT COUNT(*) FROM friend_groups WHERE owner_id = ?`, ownerID)
}

func (r *friendRepo) SetMembership(ctx context.Context, ownerID, friendUserID, groupID string) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO friend_group_members (owner_id, friend_user_id, group_id) VALUES (?, ?, ?)
		ON CONFLICT(owner_id, friend_user_id) DO UPDATE SET group_id = excluded.group_id`,
		ownerID, friendUserID, groupID)
	return err
}

func (r *friendRepo) ClearMembership(ctx context.Context, ownerID, friendUserID string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM friend_group_members WHERE owner_id = ? AND friend_user_id = ?`, ownerID, friendUserID)
	return err
}

func (r *friendRepo) ClearMembershipsForPair(ctx context.Context, a, b string) error {
	_, err := r.q.ExecContext(ctx, `
		DELETE FROM friend_group_members
		WHERE (owner_id = ? AND friend_user_id = ?) OR (owner_id = ? AND friend_user_id = ?)`,
		a, b, b, a)
	return err
}

func (r *friendRepo) ClearMembershipsForGroup(ctx context.Context, groupID string) error {
	_, err := r.q.ExecContext(ctx, `DELETE FROM friend_group_members WHERE group_id = ?`, groupID)
	return err
}

func (r *friendRepo) count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := r.q.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFriendRequest(row rowScanner) (model.FriendRequest, error) {
	var out model.FriendRequest
	var created, updated string
	err := row.Scan(&out.ID, &out.FromUserID, &out.ToUserID, &out.Status, &created, &updated)
	if err != nil {
		return model.FriendRequest{}, err
	}
	out.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.FriendRequest{}, err
	}
	out.UpdatedAt, err = model.ParseTime(updated)
	if err != nil {
		return model.FriendRequest{}, err
	}
	return out, nil
}

func scanFriendGroup(row rowScanner) (model.FriendGroup, error) {
	var out model.FriendGroup
	var created, updated string
	err := row.Scan(&out.ID, &out.OwnerID, &out.Name, &out.SortOrder, &created, &updated)
	if err != nil {
		return model.FriendGroup{}, err
	}
	out.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.FriendGroup{}, err
	}
	out.UpdatedAt, err = model.ParseTime(updated)
	if err != nil {
		return model.FriendGroup{}, err
	}
	return out, nil
}
