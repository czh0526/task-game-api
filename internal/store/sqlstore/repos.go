package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/store/shard"
)

type userRepo struct {
	q       querier
	initial int64
	d       Dialect
	blobs   LargeValueStore
}

func (r *userRepo) Ensure(ctx context.Context, id string, now time.Time) (model.User, error) {
	_, err := r.q.ExecContext(ctx, r.d.UserInsertIgnore(), id, r.initial, model.FormatTime(now))
	if err != nil {
		return model.User{}, err
	}
	return r.Get(ctx, id)
}

func (r *userRepo) Get(ctx context.Context, id string) (model.User, error) {
	var u model.User
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT id, balance, integrity, ability, created_at, COALESCE(email, ''), COALESCE(nickname, '')
		FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Balance, &u.Integrity, &u.Ability, &created, &u.Email, &u.Nickname)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, store.ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	u.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

// GetCredentials is the password-bearing by-id read used by control-plane
// login (email -> user_id -> credentials). Get deliberately hides the hash,
// so this is a separate, explicitly named method rather than a widened Get.
func (r *userRepo) GetCredentials(ctx context.Context, id string) (model.User, error) {
	return r.GetByEmailWHEREID(ctx, id)
}

// GetByEmailWHEREID reads a user plus password hash by primary key. The name
// is internal; GetCredentials is the stable entry point.
func (r *userRepo) GetByEmailWHEREID(ctx context.Context, id string) (model.User, error) {
	var u model.User
	var created string
	var hash sql.NullString
	err := r.q.QueryRowContext(ctx, `
		SELECT id, balance, integrity, ability, created_at, COALESCE(email, ''), COALESCE(nickname, ''), password_hash
		FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Balance, &u.Integrity, &u.Ability, &created, &u.Email, &u.Nickname, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, store.ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	u.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.User{}, err
	}
	u.PasswordHash = hash.String
	return u, nil
}

func (r *userRepo) InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error) {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO users (id, balance, integrity, ability, created_at, email, password_hash)
		VALUES (?, ?, 0, 0, ?, ?, ?)`,
		id, r.initial, model.FormatTime(now), email, passwordHash)
	if err != nil {
		if r.d.IsEmailConflict(err) {
			return model.User{}, store.ErrConflict
		}
		return model.User{}, err
	}
	return r.Get(ctx, id)
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	var u model.User
	var created string
	var hash sql.NullString
	err := r.q.QueryRowContext(ctx, `
		SELECT id, balance, integrity, ability, created_at, COALESCE(email, ''), COALESCE(nickname, ''), password_hash
		FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Balance, &u.Integrity, &u.Ability, &created, &u.Email, &u.Nickname, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return model.User{}, store.ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}
	if hash.Valid {
		u.PasswordHash = hash.String
	}
	u.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (r *userRepo) SetNickname(ctx context.Context, id, nickname string) error {
	res, err := r.q.ExecContext(ctx, `UPDATE users SET nickname = ? WHERE id = ?`, nickname, id)
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

func (r *userRepo) SetAvatar(ctx context.Context, id string, jpeg []byte, updatedAt string) error {
	// With a blob tier, store the JPEG once outside the database and keep only
	// a small ref inline. A ref is content-addressed by FileBlobStore; other
	// implementations may dedupe too. The blob write happens before the SQL
	// update: if the transaction later rolls back, the orphan blob is simply
	// unreachable (no ref points at it), which is the same best-effort tradeoff
	// webhook body externalization makes.
	ref := ""
	if r.blobs != nil {
		var err error
		ref, err = r.blobs.Put(ctx, "avatar:"+id, jpeg)
		if err != nil {
			return err
		}
	}
	var res sql.Result
	var err error
	if ref != "" {
		res, err = r.q.ExecContext(ctx, `
			UPDATE users SET avatar = NULL, avatar_ref = ?, avatar_updated_at = ? WHERE id = ?`,
			ref, updatedAt, id)
	} else {
		res, err = r.q.ExecContext(ctx, `
			UPDATE users SET avatar = ?, avatar_ref = '', avatar_updated_at = ? WHERE id = ?`,
			jpeg, updatedAt, id)
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

func (r *userRepo) GetAvatar(ctx context.Context, id string) (model.UserAvatar, error) {
	var inline []byte
	var ref, updated string
	err := r.q.QueryRowContext(ctx, `
		SELECT avatar, COALESCE(avatar_ref, ''), COALESCE(avatar_updated_at, '')
		FROM users WHERE id = ?`, id).
		Scan(&inline, &ref, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.UserAvatar{}, store.ErrNotFound
	}
	if err != nil {
		return model.UserAvatar{}, err
	}
	// A ref is authoritative: once written, the inline blob is cleared. When a
	// row still has both (mid-migration or a failed cleanup), prefer the ref.
	var data []byte
	if ref != "" {
		if r.blobs == nil {
			return model.UserAvatar{}, fmt.Errorf("store: avatar ref %q present but no blob store configured", ref)
		}
		data, err = r.blobs.Get(ctx, ref)
		if err != nil {
			return model.UserAvatar{}, err
		}
	} else {
		data = inline
	}
	if len(data) == 0 {
		return model.UserAvatar{}, store.ErrNotFound
	}
	return model.UserAvatar{UserID: id, Data: data, UpdatedAt: updated}, nil
}

func (r *userRepo) AddBalance(ctx context.Context, id string, delta int64) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE users SET balance = balance + ? WHERE id = ? AND balance + ? >= 0`, delta, id, delta)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrInsufficient
	}
	return nil
}

func (r *userRepo) ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE users SET integrity = integrity + ?, ability = ability + ? WHERE id = ?`,
		deltaIntegrity, deltaAbility, id)
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

func (r *userRepo) AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO score_events (id, user_id, delta_integrity, delta_ability, reason, task_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.UserID, ev.DeltaIntegrity, ev.DeltaAbility, ev.Reason, ev.TaskID, model.FormatTime(ev.CreatedAt))
	return err
}

func (r *userRepo) ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, user_id, delta_integrity, delta_ability, reason, task_id, created_at
		FROM score_events WHERE user_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ScoreEvent
	for rows.Next() {
		var ev model.ScoreEvent
		var created string
		if err := rows.Scan(&ev.ID, &ev.UserID, &ev.DeltaIntegrity, &ev.DeltaAbility, &ev.Reason, &ev.TaskID, &created); err != nil {
			return nil, err
		}
		ev.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if out == nil {
		out = []model.ScoreEvent{}
	}
	return out, rows.Err()
}

type taskRepo struct {
	q querier
	d Dialect
}

const taskCols = `id, kind, title, description, keywords, attributes, status, publisher_id, payment_mode, payment_amount, payment_conditions, milestones, acceptance_deadline, acceptance_quality, acceptance_constraints, review_hours, assignee_policy, competitive, collaborative, executor_ids, winning_submission_id, review_deadline, escrow_id, dispute_reason, cancel_reason, cancel_code, cancelled_at, chat_id, funded_at, start_deadline, winner_select_deadline, dispute_role_id, resolve_json, max_revisions, created_at, updated_at`

func (r *taskRepo) Insert(ctx context.Context, t model.Task) error {
	args, err := taskArgs(t)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`) VALUES (`+placeholders(len(args))+`)`, args...)
	return err
}

func (r *taskRepo) Update(ctx context.Context, t model.Task) error {
	args, err := taskArgs(t)
	if err != nil {
		return err
	}
	sets := make([]string, 0, 23)
	for _, name := range strings.Split(taskCols, ", ")[1:] {
		sets = append(sets, name+" = ?")
	}
	q := `UPDATE tasks SET ` + strings.Join(sets, ", ") + ` WHERE id = ?`
	upd := append(args[1:], args[0])
	res, err := r.q.ExecContext(ctx, q, upd...)
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

func (r *taskRepo) Get(ctx context.Context, id string) (model.Task, error) {
	return r.get(ctx, id, LockNone)
}

// Lock reads the task and holds the row until the transaction ends.
func (r *taskRepo) Lock(ctx context.Context, id string) (model.Task, error) {
	return r.get(ctx, id, LockUpdate)
}

// TryLock is Lock, except MySQL uses FOR UPDATE SKIP LOCKED.
// ErrBusy means another transaction already holds the row.
func (r *taskRepo) TryLock(ctx context.Context, id string) (model.Task, error) {
	t, err := r.get(ctx, id, LockSkipLocked)
	if err == nil || !r.d.SupportsSkipLocked() || !errors.Is(err, store.ErrNotFound) {
		return t, err
	}
	_, plain := r.get(ctx, id, LockNone)
	if errors.Is(plain, store.ErrNotFound) {
		return model.Task{}, store.ErrNotFound
	}
	if plain != nil {
		return model.Task{}, plain
	}
	return model.Task{}, store.ErrBusy
}

func (r *taskRepo) get(ctx context.Context, id string, mode LockMode) (model.Task, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`+r.d.LockSuffix(mode), id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, store.ErrNotFound
	}
	return t, err
}

// ClaimDueReview locks one in_review task whose review_deadline has passed.
// exclude skips ids this pass already considered. An empty id means nothing is left.
// Call it inside a transaction; MySQL keeps FOR UPDATE SKIP LOCKED until commit.
func (r *taskRepo) ClaimDueReview(ctx context.Context, now time.Time, exclude []string) (string, error) {
	q := `SELECT id FROM tasks WHERE status = ? AND review_deadline IS NOT NULL AND review_deadline != '' AND review_deadline <= ?`
	args := []any{model.StatusInReview, model.FormatTime(now)}
	if len(exclude) > 0 {
		q += ` AND id NOT IN (` + placeholders(len(exclude)) + `)`
		for _, id := range exclude {
			args = append(args, id)
		}
	}
	mode := LockUpdate
	if r.d.SupportsSkipLocked() {
		mode = LockSkipLocked
	}
	q += ` ORDER BY review_deadline ASC, id ASC LIMIT 1` + r.d.LockSuffix(mode)
	var id string
	err := r.q.QueryRowContext(ctx, q, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListSweep pages open, active, and in_review tasks in (created_at, id) order.
// Empty afterCreated starts at the beginning. The cursor walks every row, including
// past the old fixed page of 50.
func (r *taskRepo) ListSweep(ctx context.Context, afterCreated, afterID string, limit int) ([]model.Task, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + taskCols + ` FROM tasks WHERE status IN (?, ?, ?) AND (? = '' OR created_at > ? OR (created_at = ? AND id > ?)) ORDER BY created_at ASC, id ASC LIMIT ?`
	rows, err := r.q.QueryContext(ctx, q,
		model.StatusOpen, model.StatusActive, model.StatusInReview,
		afterCreated, afterCreated, afterCreated, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []model.Task{}
	}
	return out, rows.Err()
}

// taskWhere builds the shared WHERE clause and arguments for List and Count.
// Keyword and attribute predicates are dialect-specific. The Haversine
// formula filters by distance from a coordinate when one is supplied.
func (r *taskRepo) taskWhere(f model.TaskFilter) (string, []any) {
	q := ` WHERE 1 = 1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if len(f.Statuses) > 0 {
		q += ` AND status IN (` + placeholders(len(f.Statuses)) + `)`
		for _, status := range f.Statuses {
			args = append(args, status)
		}
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Q != "" {
		// Full-text across title, description and keywords. instr() treats the
		// needle literally, so %, _ and \ in user input need no escaping.
		q += ` AND (instr(lower(tasks.title), lower(?)) > 0` +
			` OR instr(lower(tasks.description), lower(?)) > 0` +
			` OR ` + r.d.KeywordSearchSQL() + `)`
		args = append(args, f.Q, f.Q, f.Q)
	}
	if f.Keyword != "" {
		q += r.d.KeywordEqualsSQL()
		args = append(args, f.Keyword)
	}
	if f.Urgency != "" {
		q += ` AND ` + r.d.AttrExpr("$.urgency") + ` = ?`
		args = append(args, f.Urgency)
	}
	if f.Recurrence != "" {
		q += ` AND ` + r.d.AttrExpr("$.recurrence") + ` = ?`
		args = append(args, f.Recurrence)
	}
	if f.ExecutorRequirement != "" {
		q += r.d.ExecutorReqSQL()
		args = append(args, f.ExecutorRequirement)
	}
	if f.MinAmount > 0 {
		q += ` AND payment_amount >= ?`
		args = append(args, f.MinAmount)
	}
	if f.MaxAmount > 0 {
		q += ` AND payment_amount <= ?`
		args = append(args, f.MaxAmount)
	}
	if f.Latitude != nil && f.Longitude != nil {
		q += ` AND ` + r.d.AttrExpr("$.latitude") + ` IS NOT NULL`
		q += ` AND ` + r.d.AttrExpr("$.longitude") + ` IS NOT NULL`
		q += ` AND (` + r.d.HaversineSQL() + `) <= ?`
		args = append(args, *f.Latitude, *f.Latitude, *f.Longitude, f.RadiusKm)
	}
	return q, args
}

func (r *taskRepo) taskOrderBy(f model.TaskFilter) (string, []any) {
	switch f.Sort {
	case "amount_asc":
		return ` ORDER BY payment_amount ASC, created_at DESC, id DESC`, nil
	case "amount_desc":
		return ` ORDER BY payment_amount DESC, created_at DESC, id DESC`, nil
	case "deadline_asc":
		return ` ORDER BY acceptance_deadline ASC, created_at DESC, id DESC`, nil
	case "distance_asc":
		if f.Latitude != nil && f.Longitude != nil {
			return ` ORDER BY (` + r.d.HaversineSQL() + `) ASC, created_at DESC`, []any{*f.Latitude, *f.Latitude, *f.Longitude}
		}
	}
	return ` ORDER BY created_at DESC, id DESC`, nil
}

func (r *taskRepo) List(ctx context.Context, f model.TaskFilter) ([]model.Task, error) {
	where, args := r.taskWhere(f)
	order, orderArgs := r.taskOrderBy(f)
	q := `SELECT ` + taskCols + ` FROM tasks` + where + order + ` LIMIT ? OFFSET ?`
	args = append(args, orderArgs...)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if out == nil {
		out = []model.Task{}
	}
	return out, rows.Err()
}

func (r *taskRepo) Count(ctx context.Context, f model.TaskFilter) (int64, error) {
	where, args := r.taskWhere(f)
	var n int64
	err := r.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`+where, args...).Scan(&n)
	return n, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(s scanner) (model.Task, error) {
	var t model.Task
	var conditions, milestones, constraints, policy, executors, created, updated, deadline string
	var keywordsJSON, attributesJSON, resolveJSON string
	var competitive, collaborative, winning, review, cancelled, funded, startBy, winnerBy sql.NullString
	var reviewHours, maxRevisions int
	err := s.Scan(
		&t.ID, &t.Kind, &t.Title, &t.Description, &keywordsJSON, &attributesJSON, &t.Status, &t.PublisherID,
		&t.Payment.Mode, &t.Payment.Amount, &conditions, &milestones,
		&deadline, &t.Acceptance.Quality, &constraints, &reviewHours,
		&policy, &competitive, &collaborative, &executors,
		&winning, &review, &t.EscrowID, &t.DisputeReason,
		&t.CancelReason, &t.CancelCode, &cancelled, &t.ChatID, &funded, &startBy, &winnerBy,
		&t.DisputeRoleID, &resolveJSON, &maxRevisions, &created, &updated,
	)
	if err != nil {
		return model.Task{}, err
	}
	t.Acceptance.ReviewHours = reviewHours
	t.Acceptance.MaxRevisions = maxRevisions
	t.Keywords, err = unmarshalSlice[string](keywordsJSON)
	if err != nil {
		return model.Task{}, err
	}
	if attributesJSON != "" && attributesJSON != "null" && attributesJSON != "{}" {
		if err := json.Unmarshal([]byte(attributesJSON), &t.Attributes); err != nil {
			return model.Task{}, err
		}
	}
	if t.Attributes.ExecutorRequirements == nil {
		t.Attributes.ExecutorRequirements = []string{}
	}
	t.Payment.Conditions, err = unmarshalSlice[string](conditions)
	if err != nil {
		return model.Task{}, err
	}
	t.Payment.Milestones, err = unmarshalSlice[model.Milestone](milestones)
	if err != nil {
		return model.Task{}, err
	}
	t.Acceptance.Constraints, err = unmarshalSlice[string](constraints)
	if err != nil {
		return model.Task{}, err
	}
	t.ExecutorIDs, err = unmarshalSlice[string](executors)
	if err != nil {
		return model.Task{}, err
	}
	if err := json.Unmarshal([]byte(policy), &t.AssigneePolicy); err != nil {
		return model.Task{}, err
	}
	if t.AssigneePolicy.UserIDs == nil {
		t.AssigneePolicy.UserIDs = []string{}
	}
	if competitive.Valid && competitive.String != "" && competitive.String != "null" {
		var c model.Competitive
		if err := json.Unmarshal([]byte(competitive.String), &c); err != nil {
			return model.Task{}, err
		}
		t.Competitive = &c
	}
	if collaborative.Valid && collaborative.String != "" && collaborative.String != "null" {
		var c model.Collaborative
		if err := json.Unmarshal([]byte(collaborative.String), &c); err != nil {
			return model.Task{}, err
		}
		if c.Roles == nil {
			c.Roles = []model.Role{}
		}
		t.Collaborative = &c
	}
	if winning.Valid {
		v := winning.String
		t.WinningSubmissionID = &v
	}
	t.Acceptance.Deadline, err = model.ParseTime(deadline)
	if err != nil {
		return model.Task{}, err
	}
	if review.Valid && review.String != "" {
		rt, err := model.ParseTime(review.String)
		if err != nil {
			return model.Task{}, err
		}
		t.ReviewDeadline = &rt
	}
	t.CancelledAt, err = parseNullTime(cancelled)
	if err != nil {
		return model.Task{}, err
	}
	t.FundedAt, err = parseNullTime(funded)
	if err != nil {
		return model.Task{}, err
	}
	t.StartDeadline, err = parseNullTime(startBy)
	if err != nil {
		return model.Task{}, err
	}
	t.WinnerSelectDeadline, err = parseNullTime(winnerBy)
	if err != nil {
		return model.Task{}, err
	}
	if resolveJSON != "" && resolveJSON != "null" {
		if err := json.Unmarshal([]byte(resolveJSON), &t.Resolve); err != nil {
			return model.Task{}, err
		}
	}
	t.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.Task{}, err
	}
	t.UpdatedAt, err = model.ParseTime(updated)
	if err != nil {
		return model.Task{}, err
	}
	return t, nil
}

func taskArgs(t model.Task) ([]any, error) {
	keywordsJSON, err := jsonText(nilSlice(t.Keywords))
	if err != nil {
		return nil, err
	}
	attributesJSON, err := json.Marshal(t.Attributes)
	if err != nil {
		return nil, err
	}
	if string(attributesJSON) == "null" {
		attributesJSON = []byte("{}")
	}
	conditions, err := jsonText(nilSlice(t.Payment.Conditions))
	if err != nil {
		return nil, err
	}
	milestones, err := jsonText(nilMilestones(t.Payment.Milestones))
	if err != nil {
		return nil, err
	}
	constraints, err := jsonText(nilSlice(t.Acceptance.Constraints))
	if err != nil {
		return nil, err
	}
	if t.AssigneePolicy.UserIDs == nil {
		t.AssigneePolicy.UserIDs = []string{}
	}
	policy, err := json.Marshal(t.AssigneePolicy)
	if err != nil {
		return nil, err
	}
	competitive, err := jsonOrNull(t.Competitive)
	if err != nil {
		return nil, err
	}
	collaborative, err := jsonOrNull(t.Collaborative)
	if err != nil {
		return nil, err
	}
	executors, err := jsonText(nilSlice(t.ExecutorIDs))
	if err != nil {
		return nil, err
	}
	var winning any
	if t.WinningSubmissionID != nil && *t.WinningSubmissionID != "" {
		winning = *t.WinningSubmissionID
	}
	var review any
	if t.ReviewDeadline != nil {
		review = model.FormatTime(*t.ReviewDeadline)
	}
	resolveJSON, err := json.Marshal(t.Resolve)
	if err != nil {
		return nil, err
	}
	if string(resolveJSON) == "null" || string(resolveJSON) == "{}" {
		resolveJSON = []byte("{}")
	}
	return []any{
		t.ID, t.Kind, t.Title, t.Description, keywordsJSON, string(attributesJSON), t.Status, t.PublisherID,
		t.Payment.Mode, t.Payment.Amount, conditions, milestones,
		model.FormatTime(t.Acceptance.Deadline), t.Acceptance.Quality, constraints, t.Acceptance.ReviewHours,
		string(policy), competitive, collaborative, executors,
		winning, review, t.EscrowID, t.DisputeReason,
		t.CancelReason, t.CancelCode, formatNullTime(t.CancelledAt), t.ChatID,
		formatNullTime(t.FundedAt), formatNullTime(t.StartDeadline), formatNullTime(t.WinnerSelectDeadline),
		t.DisputeRoleID, string(resolveJSON), t.Acceptance.MaxRevisions,
		model.FormatTime(t.CreatedAt), model.FormatTime(t.UpdatedAt),
	}, nil
}

func formatNullTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return model.FormatTime(*t)
}

func parseNullTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	parsed, err := model.ParseTime(v.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nilMilestones(s []model.Milestone) []model.Milestone {
	if s == nil {
		return []model.Milestone{}
	}
	return s
}

type submissionRepo struct {
	q     querier
	blobs LargeValueStore
}

func (r *submissionRepo) Insert(ctx context.Context, s model.Submission) error {
	payload := string(s.Payload)
	if payload == "" {
		payload = "{}"
	}
	// Large payloads go to the blob tier. The inline column keeps '{}' so a
	// legacy reader (or one without the blob tier configured) still gets valid
	// JSON rather than a dangling ref it cannot resolve.
	ref := ""
	if r.blobs != nil && len(payload) > LargePayloadThreshold {
		var err error
		ref, err = r.blobs.Put(ctx, "submission:"+s.ID, []byte(payload))
		if err != nil {
			return err
		}
		payload = "{}"
	}
	status := s.Status
	if status == "" {
		status = model.SubmissionCurrent
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO submissions (id, task_id, milestone_id, executor_id, payload, payload_ref, note, role_id, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TaskID, s.MilestoneID, s.ExecutorID, payload, ref, s.Note, s.RoleID, status, model.FormatTime(s.CreatedAt))
	return err
}

func (r *submissionRepo) MarkStatus(ctx context.Context, id, status string) error {
	res, err := r.q.ExecContext(ctx, `UPDATE submissions SET status = ? WHERE id = ?`, status, id)
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

func (r *submissionRepo) ListByTask(ctx context.Context, taskID string) ([]model.Submission, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, task_id, milestone_id, executor_id, payload, COALESCE(payload_ref, ''), note, role_id, status, created_at
		FROM submissions WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Submission
	for rows.Next() {
		var s model.Submission
		var payload, ref, created string
		if err := rows.Scan(&s.ID, &s.TaskID, &s.MilestoneID, &s.ExecutorID, &payload, &ref, &s.Note, &s.RoleID, &s.Status, &created); err != nil {
			return nil, err
		}
		if s.Status == "" {
			s.Status = model.SubmissionCurrent
		}
		// The ref is authoritative: when present, the inline payload is '{}'.
		// Fall back to inline for legacy rows written before externalization.
		if ref != "" {
			if r.blobs == nil {
				return nil, fmt.Errorf("store: submission %s has payload ref %q but no blob store configured", s.ID, ref)
			}
			big, gerr := r.blobs.Get(ctx, ref)
			if gerr != nil {
				return nil, gerr
			}
			payload = string(big)
		}
		s.Payload = json.RawMessage(payload)
		s.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if out == nil {
		out = []model.Submission{}
	}
	return out, rows.Err()
}

type escrowRepo struct {
	q querier
	d Dialect
}

func (r *escrowRepo) Insert(ctx context.Context, a model.EscrowAccount) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO escrow_accounts (id, task_id, held, frozen, currency, version) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.TaskID, a.Held, a.Frozen, a.Currency, a.Version)
	return err
}

func (r *escrowRepo) Update(ctx context.Context, a model.EscrowAccount) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE escrow_accounts SET held = ?, frozen = ?, version = version + 1
		WHERE id = ? AND version = ?`, a.Held, a.Frozen, a.ID, a.Version)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var one int
		err := r.q.QueryRowContext(ctx, `SELECT 1 FROM escrow_accounts WHERE id = ?`, a.ID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		return store.ErrConflict
	}
	return nil
}

func (r *escrowRepo) InsertEntry(ctx context.Context, e model.EscrowEntry) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO escrow_entries (id, escrow_id, type, amount, to_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.EscrowID, e.Type, e.Amount, e.ToUserID, model.FormatTime(e.CreatedAt))
	return err
}

func (r *escrowRepo) GetByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	return r.getByTask(ctx, taskID, LockNone)
}

// LockByTask reads the escrow row and holds it until the transaction ends.
func (r *escrowRepo) LockByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	return r.getByTask(ctx, taskID, LockUpdate)
}

func (r *escrowRepo) getByTask(ctx context.Context, taskID string, mode LockMode) (model.EscrowAccount, error) {
	var a model.EscrowAccount
	err := r.q.QueryRowContext(ctx, `
		SELECT id, task_id, held, frozen, currency, version FROM escrow_accounts WHERE task_id = ?`+r.d.LockSuffix(mode), taskID).
		Scan(&a.ID, &a.TaskID, &a.Held, &a.Frozen, &a.Currency, &a.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return model.EscrowAccount{}, store.ErrNotFound
	}
	if err != nil {
		return model.EscrowAccount{}, err
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, escrow_id, type, amount, to_user_id, created_at
		FROM escrow_entries WHERE escrow_id = ? ORDER BY created_at ASC, id ASC`, a.ID)
	if err != nil {
		return model.EscrowAccount{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var e model.EscrowEntry
		var created string
		if err := rows.Scan(&e.ID, &e.EscrowID, &e.Type, &e.Amount, &e.ToUserID, &created); err != nil {
			return model.EscrowAccount{}, err
		}
		e.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return model.EscrowAccount{}, err
		}
		a.Entries = append(a.Entries, e)
	}
	if a.Entries == nil {
		a.Entries = []model.EscrowEntry{}
	}
	return a, rows.Err()
}

type webhookRepo struct{ q querier }

func (r *webhookRepo) Insert(ctx context.Context, w model.Webhook) error {
	events, err := jsonText(nilSlice(w.Events))
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO webhooks (id, owner_id, url, secret, events, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		w.ID, w.OwnerID, w.URL, w.Secret, events, model.FormatTime(w.CreatedAt))
	return err
}

func (r *webhookRepo) ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error) {
	if len(ownerIDs) == 0 {
		return []model.Webhook{}, nil
	}
	args := make([]any, len(ownerIDs))
	for i, id := range ownerIDs {
		args[i] = id
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, owner_id, url, secret, events, created_at FROM webhooks
		WHERE owner_id IN (`+placeholders(len(ownerIDs))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Webhook
	for rows.Next() {
		var w model.Webhook
		var events, created string
		if err := rows.Scan(&w.ID, &w.OwnerID, &w.URL, &w.Secret, &events, &created); err != nil {
			return nil, err
		}
		w.Events, err = unmarshalSlice[string](events)
		if err != nil {
			return nil, err
		}
		w.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if out == nil {
		out = []model.Webhook{}
	}
	return out, rows.Err()
}

type deliveryRepo struct{ q querier }

func (r *deliveryRepo) Insert(ctx context.Context, d model.Delivery) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (id, webhook_id, event_id, event_type, body, body_ref, status, attempts, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.WebhookID, d.EventID, d.EventType, d.Body, d.BodyRef, d.Status, d.Attempts, d.LastError, model.FormatTime(d.CreatedAt))
	return err
}

func (r *deliveryRepo) ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT d.id, d.webhook_id, d.event_id, d.event_type, d.body, d.body_ref, d.status, d.attempts, d.last_error, d.created_at, w.url, w.secret
		FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.status = ? AND d.attempts < ?
		ORDER BY d.created_at ASC LIMIT ?`, model.DeliveryPending, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Delivery
	for rows.Next() {
		var d model.Delivery
		var created string
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Body, &d.BodyRef, &d.Status, &d.Attempts, &d.LastError, &created, &d.URL, &d.Secret); err != nil {
			return nil, err
		}
		d.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ClaimPending moves up to limit due rows to delivering and increments attempts.
// Each claim is its own autocommit statement so the HTTP POST is not inside the lock.
// A delivering row whose lease has expired can be claimed again.
func (r *deliveryRepo) ClaimPending(ctx context.Context, maxAttempts, limit int, now, until time.Time) ([]model.Delivery, error) {
	if limit <= 0 {
		limit = 20
	}
	nowText := model.FormatTime(now)
	untilText := model.FormatTime(until)
	var out []model.Delivery
	for len(out) < limit {
		token := id.New("clm_")
		res, err := r.q.ExecContext(ctx, `
			UPDATE webhook_deliveries
			SET status = ?, claim_token = ?, claimed_until = ?, attempts = attempts + 1
			WHERE id = (
				SELECT id FROM (
					SELECT id FROM webhook_deliveries
					WHERE attempts < ?
					  AND (
					    status = ?
					    OR (status = ? AND claimed_until IS NOT NULL AND claimed_until <= ?)
					  )
					ORDER BY created_at ASC, id ASC
					LIMIT 1
				) AS picked
			)
			AND attempts < ?
			AND (
				status = ?
				OR (status = ? AND claimed_until IS NOT NULL AND claimed_until <= ?)
			)`,
			model.DeliveryDelivering, token, untilText,
			maxAttempts, model.DeliveryPending, model.DeliveryDelivering, nowText,
			maxAttempts, model.DeliveryPending, model.DeliveryDelivering, nowText)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			break
		}
		row, err := r.byToken(ctx, token)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *deliveryRepo) byToken(ctx context.Context, token string) (model.Delivery, error) {
	var d model.Delivery
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT d.id, d.webhook_id, d.event_id, d.event_type, d.body, d.body_ref, d.status, d.attempts, d.last_error, d.created_at, d.claim_token, w.url, w.secret
		FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id
		WHERE d.claim_token = ?`, token).
		Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Body, &d.BodyRef, &d.Status, &d.Attempts, &d.LastError, &created, &d.ClaimToken, &d.URL, &d.Secret)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Delivery{}, store.ErrNotFound
	}
	if err != nil {
		return model.Delivery{}, err
	}
	d.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.Delivery{}, err
	}
	return d, nil
}

// Finish records the post-attempt status only while claimToken still owns the row.
// A late worker whose lease was taken does not overwrite the new owner.
func (r *deliveryRepo) Finish(ctx context.Context, id, claimToken, status, lastError string) error {
	if len(lastError) > 500 {
		lastError = lastError[:500]
	}
	_, err := r.q.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET status = ?, last_error = ?, claim_token = '', claimed_until = NULL
		WHERE id = ? AND claim_token = ?`,
		status, lastError, id, claimToken)
	return err
}

type chatRepo struct {
	q querier
	d Dialect
	// shards is nil in the default single-database deployment. When enabled,
	// message bodies live in per-shard SQLite files and the primary store
	// keeps only the outbox, the directory and the read cursors.
	shards *shard.Set
	// buf is set only inside InTx; it defers shard writes until commit.
	buf *txBuffer
}

const chatCols = `id, task_id, member_ids, created_at, type, pair_low, pair_high`

func (r *chatRepo) InsertChat(ctx context.Context, c model.Chat) error {
	members, err := jsonText(nilSlice(c.MemberIDs))
	if err != nil {
		return err
	}
	kind := c.Type
	if kind == "" {
		kind = model.ChatGroup
	}
	_, err = r.q.ExecContext(ctx, `
		INSERT INTO chats (id, task_id, member_ids, created_at, type, pair_low, pair_high)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, nullString(c.TaskID), members, model.FormatTime(c.CreatedAt), kind, c.PairLow, c.PairHigh)
	if r.d.IsUnique(err) {
		return store.ErrConflict
	}
	if err != nil {
		return err
	}
	c.Type = kind
	return r.registerShard(ctx, c)
}

func (r *chatRepo) UpdateChat(ctx context.Context, c model.Chat) error {
	members, err := jsonText(nilSlice(c.MemberIDs))
	if err != nil {
		return err
	}
	kind := c.Type
	if kind == "" {
		if c.TaskID != "" {
			kind = model.ChatTask
		} else {
			kind = model.ChatGroup
		}
	}
	res, err := r.q.ExecContext(ctx, `
		UPDATE chats SET task_id = ?, member_ids = ?, type = ?, pair_low = ?, pair_high = ? WHERE id = ?`,
		nullString(c.TaskID), members, kind, c.PairLow, c.PairHigh, c.ID)
	if err != nil {
		if r.d.IsUnique(err) {
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

func (r *chatRepo) ListByTask(ctx context.Context, taskID string) ([]model.Chat, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT `+chatCols+` FROM chats WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []model.Chat{}
	}
	return out, rows.Err()
}

func (r *chatRepo) GetChat(ctx context.Context, id string) (model.Chat, error) {
	row := r.q.QueryRowContext(ctx, `SELECT `+chatCols+` FROM chats WHERE id = ?`, id)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Chat{}, store.ErrNotFound
	}
	return c, err
}

func (r *chatRepo) FindDirect(ctx context.Context, a, b string) (model.Chat, error) {
	low, high := model.OrderedPair(a, b)
	if low == "" || low >= high {
		return model.Chat{}, store.ErrNotFound
	}
	row := r.q.QueryRowContext(ctx, `
		SELECT `+chatCols+` FROM chats
		WHERE type = ? AND pair_low = ? AND pair_high = ?`, model.ChatDirect, low, high)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Chat{}, store.ErrNotFound
	}
	return c, err
}

func scanChat(s scanner) (model.Chat, error) {
	var c model.Chat
	var taskID sql.NullString
	var members, created string
	err := s.Scan(&c.ID, &taskID, &members, &created, &c.Type, &c.PairLow, &c.PairHigh)
	if err != nil {
		return model.Chat{}, err
	}
	if taskID.Valid {
		c.TaskID = taskID.String
	}
	c.MemberIDs, err = unmarshalSlice[string](members)
	if err != nil {
		return model.Chat{}, err
	}
	c.CreatedAt, err = model.ParseTime(created)
	return c, err
}

func (r *chatRepo) InsertMessage(ctx context.Context, m model.Message) error {
	if r.shards != nil {
		idx, err := r.shardFor(ctx, m.ChatID)
		if err != nil {
			return err
		}
		if err := r.writeOutbox(ctx, idx, m); err != nil {
			return err
		}
		if r.buf != nil {
			// Deferred until the surrounding primary transaction commits.
			r.buf.add(idx, m)
			return nil
		}
		if err := r.shards.Insert(ctx, idx, m); err != nil {
			return err
		}
		_, err = r.q.ExecContext(ctx, `UPDATE message_outbox SET projected = 1 WHERE id = ?`, m.ID)
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatID, m.SenderID, m.Type, m.Text, m.AttachmentRef, model.FormatTime(m.CreatedAt))
	return err
}

func (r *chatRepo) ListDirectUnreads(ctx context.Context, userID string) ([]model.DirectUnread, error) {
	if r.shards != nil {
		return r.listDirectUnreadsSharded(ctx, userID)
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT c.id,
		       CASE WHEN c.pair_low = ? THEN c.pair_high ELSE c.pair_low END,
		       (
		         SELECT COUNT(*) FROM messages m
		         WHERE m.chat_id = c.id
		           AND m.sender_id != ?
		           AND m.`+r.d.MessageSeq()+` > COALESCE(
		             (SELECT cr.last_read_rowid FROM chat_reads cr
		              WHERE cr.user_id = ? AND cr.chat_id = c.id),
		             0
		           )
		       )
		FROM chats c
		WHERE c.type = ? AND (c.pair_low = ? OR c.pair_high = ?)`,
		userID, userID, userID, model.ChatDirect, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DirectUnread
	for rows.Next() {
		var row model.DirectUnread
		if err := rows.Scan(&row.ChatID, &row.PeerID, &row.UnreadCount); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if out == nil {
		out = []model.DirectUnread{}
	}
	return out, rows.Err()
}

// listDirectUnreadsSharded reads chat membership and cursors from the primary
// store, then asks each chat's shard for the unread count. The single SQLite
// connection is released before any secondary query runs.
func (r *chatRepo) listDirectUnreadsSharded(ctx context.Context, userID string) ([]model.DirectUnread, error) {
	type directRow struct {
		chatID, peerID string
		cursor         int64
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT c.id,
		       CASE WHEN c.pair_low = ? THEN c.pair_high ELSE c.pair_low END,
		       COALESCE((SELECT cr.last_read_rowid FROM chat_reads cr
		                 WHERE cr.user_id = ? AND cr.chat_id = c.id), 0)
		FROM chats c
		WHERE c.type = ? AND (c.pair_low = ? OR c.pair_high = ?)`,
		userID, userID, model.ChatDirect, userID, userID)
	if err != nil {
		return nil, err
	}
	list := []directRow{}
	for rows.Next() {
		var row directRow
		if err := rows.Scan(&row.chatID, &row.peerID, &row.cursor); err != nil {
			_ = rows.Close()
			return nil, err
		}
		list = append(list, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	out := []model.DirectUnread{}
	for _, row := range list {
		idx, err := r.shardFor(ctx, row.chatID)
		if err != nil {
			return nil, err
		}
		n, err := r.shards.CountUnread(ctx, idx, row.chatID, userID, row.cursor)
		if err != nil {
			return nil, err
		}
		out = append(out, model.DirectUnread{ChatID: row.chatID, PeerID: row.peerID, UnreadCount: n})
	}
	return out, nil
}

func (r *chatRepo) MessageRowID(ctx context.Context, chatID, messageID string) (int64, error) {
	if r.shards != nil {
		idx, err := r.shardFor(ctx, chatID)
		if err != nil {
			return 0, err
		}
		return r.shards.MessageSeq(ctx, idx, chatID, messageID)
	}
	var rowid int64
	err := r.q.QueryRowContext(ctx, `
		SELECT `+r.d.MessageSeq()+` FROM messages WHERE chat_id = ? AND id = ?`, chatID, messageID).Scan(&rowid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	return rowid, err
}

func (r *chatRepo) LatestMessageRow(ctx context.Context, chatID string) (int64, string, bool, error) {
	if r.shards != nil {
		idx, err := r.shardFor(ctx, chatID)
		if err != nil {
			return 0, "", false, err
		}
		return r.shards.Latest(ctx, idx, chatID)
	}
	var rowid int64
	var messageID string
	err := r.q.QueryRowContext(ctx, `
		SELECT `+r.d.MessageSeq()+`, id FROM messages WHERE chat_id = ? ORDER BY `+r.d.MessageSeq()+` DESC LIMIT 1`, chatID).Scan(&rowid, &messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	return rowid, messageID, true, nil
}

func (r *chatRepo) CountUnread(ctx context.Context, userID, chatID string) (int64, error) {
	if r.shards != nil {
		cursor, err := r.readCursor(ctx, userID, chatID)
		if err != nil {
			return 0, err
		}
		idx, err := r.shardFor(ctx, chatID)
		if err != nil {
			return 0, err
		}
		return r.shards.CountUnread(ctx, idx, chatID, userID, cursor)
	}
	var n int64
	err := r.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM messages m
		WHERE m.chat_id = ?
		  AND m.sender_id != ?
		  AND m.`+r.d.MessageSeq()+` > COALESCE(
		    (SELECT cr.last_read_rowid FROM chat_reads cr
		     WHERE cr.user_id = ? AND cr.chat_id = ?),
		    0
		  )`, chatID, userID, userID, chatID).Scan(&n)
	return n, err
}

func (r *chatRepo) AdvanceRead(ctx context.Context, userID, chatID string, rowid int64, now time.Time) error {
	if rowid <= 0 {
		return nil
	}
	_, err := r.q.ExecContext(ctx, r.d.ChatReadUpsert(), userID, chatID, rowid, model.FormatTime(now))
	return err
}

func (r *chatRepo) ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error) {
	if r.shards != nil {
		idx, err := r.shardFor(ctx, chatID)
		if err != nil {
			return nil, err
		}
		return r.shards.List(ctx, idx, chatID, limit, offset)
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, chat_id, sender_id, type, text, attachment_ref, created_at
		FROM messages WHERE chat_id = ? ORDER BY created_at ASC, `+r.d.MessageSeq()+` ASC LIMIT ? OFFSET ?`,
		chatID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Message
	for rows.Next() {
		var m model.Message
		var created string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.Type, &m.Text, &m.AttachmentRef, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if out == nil {
		out = []model.Message{}
	}
	return out, rows.Err()
}

type inviteRepo struct {
	q querier
	d Dialect
}

func (r *inviteRepo) Upsert(ctx context.Context, inv model.Invite) error {
	_, err := r.q.ExecContext(ctx, r.d.InviteUpsert(),
		inv.ID, inv.TaskID, inv.UserID, inv.Status, model.FormatTime(inv.CreatedAt), model.FormatTime(inv.UpdatedAt))
	return err
}

func (r *inviteRepo) Update(ctx context.Context, inv model.Invite) error {
	res, err := r.q.ExecContext(ctx, `UPDATE invites SET status = ?, updated_at = ? WHERE id = ?`,
		inv.Status, model.FormatTime(inv.UpdatedAt), inv.ID)
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

func (r *inviteRepo) Get(ctx context.Context, id string) (model.Invite, error) {
	row := r.q.QueryRowContext(ctx, `SELECT id, task_id, user_id, status, created_at, updated_at FROM invites WHERE id = ?`, id)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Invite{}, store.ErrNotFound
	}
	return inv, err
}

func (r *inviteRepo) Find(ctx context.Context, taskID, userID string) (model.Invite, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT id, task_id, user_id, status, created_at, updated_at FROM invites WHERE task_id = ? AND user_id = ?`, taskID, userID)
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Invite{}, store.ErrNotFound
	}
	return inv, err
}

func (r *inviteRepo) ListByTask(ctx context.Context, taskID string) ([]model.Invite, error) {
	return r.listInvites(ctx, `SELECT id, task_id, user_id, status, created_at, updated_at FROM invites WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
}

func (r *inviteRepo) ListByUser(ctx context.Context, userID, status string) ([]model.Invite, error) {
	if status == "" {
		return r.listInvites(ctx, `SELECT id, task_id, user_id, status, created_at, updated_at FROM invites WHERE user_id = ? ORDER BY created_at DESC, id DESC`, userID)
	}
	return r.listInvites(ctx, `SELECT id, task_id, user_id, status, created_at, updated_at FROM invites WHERE user_id = ? AND status = ? ORDER BY created_at DESC, id DESC`, userID, status)
}

func (r *inviteRepo) listInvites(ctx context.Context, query string, args ...any) ([]model.Invite, error) {
	rows, err := r.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	if out == nil {
		out = []model.Invite{}
	}
	return out, rows.Err()
}

func scanInvite(s scanner) (model.Invite, error) {
	var inv model.Invite
	var created, updated string
	if err := s.Scan(&inv.ID, &inv.TaskID, &inv.UserID, &inv.Status, &created, &updated); err != nil {
		return model.Invite{}, err
	}
	var err error
	inv.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return model.Invite{}, err
	}
	inv.UpdatedAt, err = model.ParseTime(updated)
	if err != nil {
		return model.Invite{}, err
	}
	return inv, nil
}

type idemRepo struct {
	q querier
	d Dialect
}

func (r *idemRepo) Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	return r.get(ctx, userID, method, path, key, false)
}

// GetLocked is Get plus a row lock on drivers that support it.
// Inside a transaction this serializes two writers of the same idempotency key.
func (r *idemRepo) GetLocked(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	return r.get(ctx, userID, method, path, key, true)
}

func (r *idemRepo) get(ctx context.Context, userID, method, path, key string, lock bool) (*model.IdemRecord, error) {
	q := `
		SELECT user_id, method, path, idem_key, request_hash, response_body, created_at
		FROM idempotency_keys WHERE user_id = ? AND method = ? AND path = ? AND idem_key = ?`
	if lock {
		q += r.d.LockSuffix(LockUpdate)
	}
	var rec model.IdemRecord
	var created string
	err := r.q.QueryRowContext(ctx, q, userID, method, path, key).Scan(&rec.UserID, &rec.Method, &rec.Path, &rec.Key, &rec.RequestHash, &rec.ResponseBody, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec.CreatedAt, err = model.ParseTime(created)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *idemRepo) Put(ctx context.Context, rec model.IdemRecord) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO idempotency_keys (user_id, method, path, idem_key, request_hash, response_body, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.UserID, rec.Method, rec.Path, rec.Key, rec.RequestHash, rec.ResponseBody, model.FormatTime(rec.CreatedAt))
	if r.d.IsUnique(err) {
		return store.ErrConflict
	}
	return err
}
