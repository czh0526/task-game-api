package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

type userRepo struct {
	q       querier
	initial int64
}

func (r *userRepo) Ensure(ctx context.Context, id string, now time.Time) (model.User, error) {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO users (id, balance, integrity, ability, created_at)
		VALUES (?, ?, 0, 0, ?)
		ON CONFLICT(id) DO NOTHING`, id, r.initial, model.FormatTime(now))
	if err != nil {
		return model.User{}, err
	}
	return r.Get(ctx, id)
}

func (r *userRepo) Get(ctx context.Context, id string) (model.User, error) {
	var u model.User
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT id, balance, integrity, ability, created_at, COALESCE(email, '')
		FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Balance, &u.Integrity, &u.Ability, &created, &u.Email)
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

func (r *userRepo) InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error) {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO users (id, balance, integrity, ability, created_at, email, password_hash)
		VALUES (?, ?, 0, 0, ?, ?, ?)`,
		id, r.initial, model.FormatTime(now), email, passwordHash)
	if err != nil {
		if isEmailConflict(err) {
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
		SELECT id, balance, integrity, ability, created_at, COALESCE(email, ''), password_hash
		FROM users WHERE email = ?`, email).
		Scan(&u.ID, &u.Balance, &u.Integrity, &u.Ability, &created, &u.Email, &hash)
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

func isEmailConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") &&
		(strings.Contains(msg, "users.email") || strings.Contains(msg, "idx_users_email"))
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

type taskRepo struct{ q querier }

const taskCols = `id, kind, title, description, status, publisher_id, payment_mode, payment_amount, payment_conditions, milestones, acceptance_deadline, acceptance_quality, acceptance_constraints, review_hours, assignee_policy, competitive, collaborative, executor_ids, winning_submission_id, review_deadline, escrow_id, dispute_reason, cancel_reason, cancel_code, cancelled_at, chat_id, funded_at, start_deadline, winner_select_deadline, dispute_role_id, resolve_json, max_revisions, created_at, updated_at`

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
	row := r.q.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, store.ErrNotFound
	}
	return t, err
}

func (r *taskRepo) List(ctx context.Context, f model.TaskFilter) ([]model.Task, error) {
	q := `SELECT ` + taskCols + ` FROM tasks WHERE 1 = 1`
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
	q += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
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

func (r *taskRepo) ListDueReview(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT id FROM tasks
		WHERE status = ? AND review_deadline IS NOT NULL AND review_deadline <= ?
		ORDER BY review_deadline ASC LIMIT ?`, model.StatusInReview, model.FormatTime(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(s scanner) (model.Task, error) {
	var t model.Task
	var conditions, milestones, constraints, policy, executors, created, updated, deadline string
	var resolveJSON string
	var competitive, collaborative, winning, review, cancelled, funded, startBy, winnerBy sql.NullString
	var reviewHours, maxRevisions int
	err := s.Scan(
		&t.ID, &t.Kind, &t.Title, &t.Description, &t.Status, &t.PublisherID,
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
		t.ID, t.Kind, t.Title, t.Description, t.Status, t.PublisherID,
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

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

type submissionRepo struct{ q querier }

func (r *submissionRepo) Insert(ctx context.Context, s model.Submission) error {
	payload := string(s.Payload)
	if payload == "" {
		payload = "{}"
	}
	status := s.Status
	if status == "" {
		status = model.SubmissionCurrent
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO submissions (id, task_id, milestone_id, executor_id, payload, note, role_id, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TaskID, s.MilestoneID, s.ExecutorID, payload, s.Note, s.RoleID, status, model.FormatTime(s.CreatedAt))
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
		SELECT id, task_id, milestone_id, executor_id, payload, note, role_id, status, created_at
		FROM submissions WHERE task_id = ? ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Submission
	for rows.Next() {
		var s model.Submission
		var payload, created string
		if err := rows.Scan(&s.ID, &s.TaskID, &s.MilestoneID, &s.ExecutorID, &payload, &s.Note, &s.RoleID, &s.Status, &created); err != nil {
			return nil, err
		}
		if s.Status == "" {
			s.Status = model.SubmissionCurrent
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

type escrowRepo struct{ q querier }

func (r *escrowRepo) Insert(ctx context.Context, a model.EscrowAccount) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO escrow_accounts (id, task_id, held, frozen, currency) VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.TaskID, a.Held, a.Frozen, a.Currency)
	return err
}

func (r *escrowRepo) Update(ctx context.Context, a model.EscrowAccount) error {
	res, err := r.q.ExecContext(ctx, `UPDATE escrow_accounts SET held = ?, frozen = ? WHERE id = ?`, a.Held, a.Frozen, a.ID)
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

func (r *escrowRepo) InsertEntry(ctx context.Context, e model.EscrowEntry) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO escrow_entries (id, escrow_id, type, amount, to_user_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.EscrowID, e.Type, e.Amount, e.ToUserID, model.FormatTime(e.CreatedAt))
	return err
}

func (r *escrowRepo) GetByTask(ctx context.Context, taskID string) (model.EscrowAccount, error) {
	var a model.EscrowAccount
	err := r.q.QueryRowContext(ctx, `
		SELECT id, task_id, held, frozen, currency FROM escrow_accounts WHERE task_id = ?`, taskID).
		Scan(&a.ID, &a.TaskID, &a.Held, &a.Frozen, &a.Currency)
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
		INSERT INTO webhook_deliveries (id, webhook_id, event_id, event_type, body, status, attempts, last_error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.WebhookID, d.EventID, d.EventType, d.Body, d.Status, d.Attempts, d.LastError, model.FormatTime(d.CreatedAt))
	return err
}

func (r *deliveryRepo) ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT d.id, d.webhook_id, d.event_id, d.event_type, d.body, d.status, d.attempts, d.last_error, d.created_at, w.url, w.secret
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
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.EventType, &d.Body, &d.Status, &d.Attempts, &d.LastError, &created, &d.URL, &d.Secret); err != nil {
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

func (r *deliveryRepo) Update(ctx context.Context, id string, attempts int, status, lastError string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE webhook_deliveries SET attempts = ?, status = ?, last_error = ? WHERE id = ?`,
		attempts, status, lastError, id)
	return err
}

type chatRepo struct{ q querier }

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
	if isUniqueConflict(err) {
		return store.ErrConflict
	}
	return err
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
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatID, m.SenderID, m.Type, m.Text, m.AttachmentRef, model.FormatTime(m.CreatedAt))
	return err
}

func (r *chatRepo) ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT id, chat_id, sender_id, type, text, attachment_ref, created_at
		FROM messages WHERE chat_id = ? ORDER BY created_at ASC, rowid ASC LIMIT ? OFFSET ?`,
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

type inviteRepo struct{ q querier }

func (r *inviteRepo) Upsert(ctx context.Context, inv model.Invite) error {
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO invites (id, task_id, user_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, user_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
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

type idemRepo struct{ q querier }

func (r *idemRepo) Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error) {
	var rec model.IdemRecord
	var created string
	err := r.q.QueryRowContext(ctx, `
		SELECT user_id, method, path, idem_key, request_hash, response_body, created_at
		FROM idempotency_keys WHERE user_id = ? AND method = ? AND path = ? AND idem_key = ?`,
		userID, method, path, key).Scan(&rec.UserID, &rec.Method, &rec.Path, &rec.Key, &rec.RequestHash, &rec.ResponseBody, &created)
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
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return store.ErrConflict
	}
	return err
}
