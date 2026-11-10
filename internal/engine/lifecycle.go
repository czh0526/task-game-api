package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// FundTask holds payment.amount from the publisher and moves draft → open.
func (e *Engine) FundTask(ctx context.Context, userID string, req *types.FundTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "fund"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		if _, err := r.Users().Ensure(ctx, userID, e.now()); err != nil {
			return nil, err
		}
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.PublisherID != userID {
			return nil, biz.Forbidden("only the publisher can fund this task")
		}
		if t.Status != model.StatusDraft {
			return nil, biz.Conflict("task is not a draft")
		}
		if err := r.Users().AddBalance(ctx, userID, -t.Payment.Amount); err != nil {
			if errors.Is(err, store.ErrInsufficient) {
				return nil, biz.Insufficient("publisher balance cannot cover the escrow hold")
			}
			return nil, err
		}
		now := e.now()
		acc := model.EscrowAccount{
			ID: id.New("esc_"), TaskID: t.ID, Held: t.Payment.Amount, Currency: e.currency,
		}
		if err := r.Escrow().Insert(ctx, acc); err != nil {
			return nil, err
		}
		entry := model.EscrowEntry{
			ID: id.New("ent_"), EscrowID: acc.ID, Type: model.EscrowHold, Amount: t.Payment.Amount, CreatedAt: now,
		}
		if err := r.Escrow().InsertEntry(ctx, entry); err != nil {
			return nil, err
		}
		t.EscrowID = acc.ID
		t.Status = model.StatusOpen
		t.UpdatedAt = now
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskFunded, map[string]any{"task": dto}); err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventEscrowHold, map[string]any{"task": dto, "entry": toEntry(entry)}); err != nil {
			return nil, err
		}
		return dto, nil
	})
}

// AcceptTask moves open → active. Competitive tasks stay active until slots fill.
// Collaborative accept fills one materialized seat. role_id is that seat id.
// Dropout later reopens the same seat (on_dropout = replace).
func (e *Engine) AcceptTask(ctx context.Context, userID string, req *types.AcceptTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "accept"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if userID == t.PublisherID {
			return nil, biz.Forbidden("publisher cannot accept their own task")
		}
		if req.RoleId != "" && t.Kind != model.KindCollaborative {
			return nil, biz.Invalid("role_id is only valid for collaborative tasks")
		}
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return nil, biz.Conflict("task is not open for acceptance")
		}
		if t.Kind == model.KindExclusive && t.Status != model.StatusOpen {
			return nil, biz.Conflict("task is not open for acceptance")
		}
		ability, err := e.abilityOf(ctx, r, userID)
		if err != nil {
			return nil, err
		}
		if err := checkPolicy(t, userID, ability); err != nil {
			return nil, err
		}
		switch t.Kind {
		case model.KindExclusive:
			if len(t.ExecutorIDs) > 0 {
				return nil, biz.Conflict("task already has an executor")
			}
			t.ExecutorIDs = []string{userID}
		case model.KindCompetitive:
			if t.Competitive == nil {
				return nil, biz.Conflict("task is missing competitive settings")
			}
			if contains(t.ExecutorIDs, userID) {
				return nil, biz.Conflict("you already accepted this task")
			}
			if len(t.ExecutorIDs) >= t.Competitive.Slots {
				return nil, biz.Conflict("competitive slots are full")
			}
			t.ExecutorIDs = append(t.ExecutorIDs, userID)
		case model.KindCollaborative:
			if t.Collaborative == nil {
				return nil, biz.Conflict("task is missing collaborative settings")
			}
			if req.RoleId == "" {
				return nil, biz.Invalid("role_id is required for collaborative tasks")
			}
			if !id.ValidRefID(req.RoleId) {
				return nil, biz.Invalid("invalid role_id")
			}
			found := false
			for i := range t.Collaborative.Roles {
				role := &t.Collaborative.Roles[i]
				if role.AssigneeUserID == userID && (role.Status == model.RoleFilled || role.Status == model.RoleSubmitted) {
					return nil, biz.Conflict("you already hold a seat")
				}
				if role.ID != req.RoleId {
					continue
				}
				found = true
				if role.Status != model.RoleOpen {
					return nil, biz.Conflict("role is not open")
				}
				role.Status = model.RoleFilled
				role.AssigneeUserID = userID
			}
			if !found {
				return nil, biz.Invalid("unknown role_id")
			}
			if !contains(t.ExecutorIDs, userID) {
				t.ExecutorIDs = append(t.ExecutorIDs, userID)
			}
		default:
			return nil, biz.Conflict("unknown task kind")
		}
		if _, err := r.Users().Ensure(ctx, userID, e.now()); err != nil {
			return nil, err
		}
		switch {
		case isPerSeatCollab(t):
			e.syncCollaborativeProgress(&t)
		case isUnifiedCollab(t):
			// Seats fill while the task is open. Publisher start moves it to active.
		default:
			t.Status = model.StatusActive
		}
		t.UpdatedAt = e.now()
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskAccepted, map[string]any{"task": dto}); err != nil {
			return nil, err
		}
		return dto, nil
	})
}

func (e *Engine) abilityOf(ctx context.Context, r store.Repos, userID string) (int64, error) {
	u, err := r.Users().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return u.Ability, nil
}

func checkPolicy(t model.Task, userID string, ability int64) error {
	switch t.AssigneePolicy.Type {
	case model.PolicySpecific:
		if !contains(t.AssigneePolicy.UserIDs, userID) {
			return biz.Forbidden("you are not on the assignee list")
		}
	case model.PolicyCriteria:
		if t.AssigneePolicy.MinAbility != nil && ability < *t.AssigneePolicy.MinAbility {
			return biz.Forbidden("ability score is below the task minimum")
		}
	case model.PolicyOpen:
	default:
		return biz.Invalid("invalid assignee policy")
	}
	return nil
}

// SubmitTask moves active → in_review and starts the review clock (default 72h).
func (e *Engine) SubmitTask(ctx context.Context, userID string, req *types.SubmitTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "submit"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if !contains(t.ExecutorIDs, userID) {
			return nil, biz.Forbidden("only an executor can submit")
		}
		if err := submissionWindow(t); err != nil {
			return nil, err
		}
		milestoneID := trim(req.MilestoneId)
		if t.Payment.Mode == model.PayMilestone {
			if milestoneID == "" {
				return nil, biz.Invalid("milestone_id is required")
			}
			idx := milestoneIndex(t, milestoneID)
			if idx < 0 {
				return nil, biz.Invalid("unknown milestone_id")
			}
			if t.Payment.Milestones[idx].Status != model.MilestonePending {
				return nil, biz.Conflict("milestone is not pending")
			}
			for _, m := range t.Payment.Milestones {
				if m.Status == model.MilestoneSubmitted {
					return nil, biz.Conflict("a milestone is already in review")
				}
			}
		} else if milestoneID != "" {
			return nil, biz.Invalid("milestone_id is only valid for milestone payments")
		}
		seatIdx := -1
		if t.Kind == model.KindCollaborative {
			if t.Collaborative == nil {
				return nil, biz.Conflict("task is missing collaborative settings")
			}
			for i, role := range t.Collaborative.Roles {
				if role.AssigneeUserID != userID {
					continue
				}
				if role.Status == model.RoleSubmitted {
					return nil, biz.Conflict("you already submitted")
				}
				if role.Status == model.RoleFilled {
					seatIdx = i
					break
				}
			}
			if seatIdx < 0 {
				return nil, biz.Forbidden("only the current seat holder can submit")
			}
		}
		if req.Payload == nil {
			return nil, biz.Invalid("payload is required")
		}
		raw, err := json.Marshal(req.Payload)
		if err != nil || string(raw) == "null" || len(raw) == 0 || raw[0] != '{' {
			return nil, biz.Invalid("payload must be a JSON object")
		}
		if len(raw) > 256*1024 {
			return nil, biz.Invalid("payload is too large")
		}
		note := trim(req.Note)
		if len(note) > 2000 {
			return nil, biz.Invalid("note is too long")
		}
		subs, err := r.Submissions().ListByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		for _, s := range subs {
			if s.MilestoneID != milestoneID {
				continue
			}
			if t.Kind == model.KindCompetitive || t.Kind == model.KindCollaborative {
				if s.ExecutorID == userID {
					return nil, biz.Conflict("you already submitted")
				}
				continue
			}
			return nil, biz.Conflict("delivery already submitted")
		}
		now := e.now()
		sub := model.Submission{
			ID: id.New("sub_"), TaskID: t.ID, MilestoneID: milestoneID, ExecutorID: userID,
			Payload: json.RawMessage(raw), Note: note, CreatedAt: now,
		}
		if err := r.Submissions().Insert(ctx, sub); err != nil {
			return nil, err
		}
		if milestoneID != "" {
			t.Payment.Milestones[milestoneIndex(t, milestoneID)].Status = model.MilestoneSubmitted
		}
		if t.Kind == model.KindCollaborative {
			subs = append(subs, sub)
			markSubmittedSeats(&t, subs)
			e.syncCollaborativeProgress(&t)
		} else {
			e.armReview(&t)
			t.Status = model.StatusInReview
		}
		t.UpdatedAt = now
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		data := map[string]any{
			"task":          dto,
			"submission_id": sub.ID,
			"submission": map[string]any{
				"id":           sub.ID,
				"milestone_id": sub.MilestoneID,
				"executor_id":  sub.ExecutorID,
				"payload":      sub.Payload,
				"note":         sub.Note,
			},
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskSubmitted, data); err != nil {
			return nil, err
		}
		if t.Status == model.StatusInReview {
			if err := e.emit(ctx, r, parties(t), model.EventTaskInReview, data); err != nil {
				return nil, err
			}
		}
		return dto, nil
	})
}

func submissionWindow(t model.Task) error {
	if t.Payment.Mode == model.PayMilestone {
		if t.Status != model.StatusActive {
			return biz.Conflict("task is not active")
		}
		return nil
	}
	if t.Kind == model.KindCompetitive {
		if t.Status != model.StatusActive && t.Status != model.StatusInReview {
			return biz.Conflict("task is not accepting submissions")
		}
		return nil
	}
	if isUnifiedCollab(t) && t.Status == model.StatusOpen {
		return biz.Conflict("task has not started")
	}
	if t.Kind == model.KindCollaborative && (t.Status == model.StatusActive || t.Status == model.StatusInReview) {
		return nil
	}
	if t.Status != model.StatusActive {
		return biz.Conflict("task is not active")
	}
	return nil
}

func milestoneIndex(t model.Task, id string) int {
	for i, m := range t.Payment.Milestones {
		if m.ID == id {
			return i
		}
	}
	return -1
}

// DisputeTask freezes the remaining escrow and moves in_review → disputed.
func (e *Engine) DisputeTask(ctx context.Context, userID string, req *types.DisputeTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "dispute"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		reason := trim(req.Reason)
		if reason == "" || len(reason) > 2000 {
			return nil, biz.Invalid("reason is required and must be at most 2000 characters")
		}
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.Status != model.StatusInReview {
			return nil, biz.Conflict("task is not in review")
		}
		if userID != t.PublisherID && !contains(t.ExecutorIDs, userID) {
			return nil, biz.Forbidden("only the publisher or an executor can dispute")
		}
		if t.EscrowID == "" {
			return nil, biz.Conflict("task has no escrow")
		}
		acc, err := r.Escrow().GetByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if acc.Frozen > 0 {
			return nil, biz.Conflict("escrow is already frozen")
		}
		now := e.now()
		acc.Frozen = acc.Held
		if acc.Held > 0 {
			entry := model.EscrowEntry{
				ID: id.New("ent_"), EscrowID: acc.ID, Type: model.EscrowFreeze, Amount: acc.Held, CreatedAt: now,
			}
			if err := r.Escrow().InsertEntry(ctx, entry); err != nil {
				return nil, err
			}
		}
		if err := r.Escrow().Update(ctx, acc); err != nil {
			return nil, err
		}
		for i := range t.Payment.Milestones {
			if t.Payment.Milestones[i].Status == model.MilestoneSubmitted {
				t.Payment.Milestones[i].Status = model.MilestoneDisputed
			}
		}
		t.Status = model.StatusDisputed
		t.DisputeReason = reason
		t.ReviewDeadline = nil
		t.UpdatedAt = now
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskDisputed, map[string]any{"task": dto, "reason": reason}); err != nil {
			return nil, err
		}
		return dto, nil
	})
}

// DropoutTask reopens a collaborative seat. The dropout earns 0 commission and 0 ability.
func (e *Engine) DropoutTask(ctx context.Context, userID string, req *types.DropoutTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "dropout"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.Kind != model.KindCollaborative || t.Collaborative == nil {
			return nil, biz.Conflict("dropout is only valid for collaborative tasks")
		}
		if t.Collaborative.OnDropout != model.DropoutReplace {
			return nil, biz.Conflict("task dropout rule is not replace")
		}
		if t.Status != model.StatusOpen && t.Status != model.StatusActive {
			return nil, biz.Conflict("dropout is only valid while the task is open or active")
		}
		for _, role := range t.Collaborative.Roles {
			if role.AssigneeUserID == userID && role.Status == model.RoleSubmitted {
				return nil, biz.Conflict("a submitted seat cannot drop out")
			}
		}
		dropped := false
		for i := range t.Collaborative.Roles {
			role := &t.Collaborative.Roles[i]
			if role.AssigneeUserID == userID && role.Status == model.RoleFilled {
				role.Status = model.RoleOpen
				role.AssigneeUserID = ""
				dropped = true
				break
			}
		}
		if !dropped {
			return nil, biz.Forbidden("you do not hold a filled seat")
		}
		still := false
		for _, role := range t.Collaborative.Roles {
			if role.Status == model.RoleFilled && role.AssigneeUserID == userID {
				still = true
			}
		}
		if !still {
			t.ExecutorIDs = removeID(t.ExecutorIDs, userID)
		}
		e.syncCollaborativeProgress(&t)
		t.UpdatedAt = e.now()
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t, userID), model.EventTaskExecutorReplaced, map[string]any{
			"task": dto, "user_id": userID,
		}); err != nil {
			return nil, err
		}
		// Locked rule: dropout gets 0 commission and 0 ability. The event is recorded either way.
		if err := e.recordScore(ctx, r, userID, t.ID, scoreReasonDropout, 0, 0); err != nil {
			return nil, err
		}
		return dto, nil
	})
}
