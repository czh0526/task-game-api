package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// CancelTask closes a task that has not started review, or refunds unfinished per-seat work.
func (e *Engine) CancelTask(ctx context.Context, userID string, req *types.CancelTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "cancel"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		reason := trim(req.Reason)
		if len(reason) > 2000 {
			return nil, biz.Invalid("reason must be at most 2000 characters")
		}
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.PublisherID != userID {
			return nil, biz.Forbidden("only the publisher can cancel this task")
		}
		subs, err := r.Submissions().ListByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		switch t.Status {
		case model.StatusDraft:
			return e.markCancelled(ctx, r, t, "", reason, nil)
		case model.StatusSettled, model.StatusCancelled:
			return nil, biz.Conflict("task is already " + t.Status)
		case model.StatusDisputed:
			return nil, biz.Conflict("task is disputed; use arbitrate or mutual resolve")
		case model.StatusInReview:
			return nil, biz.Conflict("task is in review; use revise, confirm, or dispute")
		case model.StatusOpen:
			return e.cancelRefundAll(ctx, r, t, "", reason)
		case model.StatusActive:
			if len(subs) == 0 {
				return e.cancelRefundAll(ctx, r, t, "", reason)
			}
			if isPerSeatCollab(t) {
				return e.cancelPerSeatRemainder(ctx, r, t, reason)
			}
			return nil, biz.Conflict("work has started; use revise, confirm, or dispute")
		default:
			return nil, biz.Conflict("task cannot be cancelled")
		}
	})
}

// ReviseTask sends one current submission back to the executor.
func (e *Engine) ReviseTask(ctx context.Context, userID string, req *types.ReviseTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "revise"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		reason := trim(req.Reason)
		if reason == "" || len(reason) > 2000 {
			return nil, biz.Invalid("reason is required and must be at most 2000 characters")
		}
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.PublisherID != userID {
			return nil, biz.Forbidden("only the publisher can request a revision")
		}
		if t.Status == model.StatusDisputed {
			return nil, biz.Conflict("task is disputed; use arbitrate or mutual resolve")
		}
		subs, err := r.Submissions().ListByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		target, seatIdx, err := pickRevision(t, subs, trim(req.RoleId), trim(req.SubmissionId))
		if err != nil {
			return nil, err
		}
		maxRev := t.Acceptance.MaxRevisions
		if maxRev <= 0 {
			maxRev = defaultMaxRevisions
		}
		roleID := target.RoleID
		if seatIdx >= 0 {
			roleID = t.Collaborative.Roles[seatIdx].ID
		}
		if revisionCount(subs, t.Kind, roleID, target.ExecutorID) >= maxRev {
			return nil, biz.Conflict("revision limit reached; confirm or dispute")
		}
		if err := r.Submissions().MarkStatus(ctx, target.ID, model.SubmissionSuperseded); err != nil {
			return nil, err
		}
		for i := range subs {
			if subs[i].ID == target.ID {
				subs[i].Status = model.SubmissionSuperseded
			}
		}
		if target.MilestoneID != "" {
			idx := milestoneIndex(t, target.MilestoneID)
			if idx >= 0 && (t.Payment.Milestones[idx].Status == model.MilestoneSubmitted || t.Payment.Milestones[idx].Status == model.MilestoneDisputed) {
				t.Payment.Milestones[idx].Status = model.MilestonePending
			}
		}
		if seatIdx >= 0 {
			t.Collaborative.Roles[seatIdx].Status = model.RoleFilled
			e.syncCollaborativeProgress(&t)
		} else if t.Kind == model.KindCompetitive {
			if len(currentSubmissions(subs)) == 0 {
				t.Status = model.StatusActive
				clearReview(&t)
			}
		} else {
			t.Status = model.StatusActive
			clearReview(&t)
		}
		t.UpdatedAt = e.now()
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		data := map[string]any{"task": dto, "submission_id": target.ID, "reason": reason}
		if roleID != "" {
			data["role_id"] = roleID
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskRevisionRequested, data); err != nil {
			return nil, err
		}
		return dto, nil
	})
}

// ResolveTask records one side of a dispute settlement. Matching proposals apply it.
func (e *Engine) ResolveTask(ctx context.Context, userID string, req *types.ResolveTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "resolve"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if !taskDisputed(t) {
			return nil, biz.Conflict("task is not disputed")
		}
		side, err := resolveSide(t, userID)
		if err != nil {
			return nil, err
		}
		pot, err := disputeAmount(ctx, r, t)
		if err != nil {
			return nil, err
		}
		proposal, pays, err := e.normalizeOutcome(t, req.Outcome, trim(req.Reason), trim(req.RoleId), trim(req.SubmissionId), req.Splits, false, pot)
		if err != nil {
			return nil, err
		}
		proposal.UserID = userID
		proposal.At = e.now()
		if t.DisputeRoleID != "" {
			proposal.RoleID = t.DisputeRoleID
		}
		other := t.Resolve.Executor
		if side == "executor" {
			other = t.Resolve.Publisher
		}
		if side == "publisher" {
			t.Resolve.Publisher = &proposal
		} else {
			t.Resolve.Executor = &proposal
		}
		if other != nil && !proposalEqual(*other, proposal) {
			if side == "publisher" {
				t.Resolve.Executor = nil
			} else {
				t.Resolve.Publisher = nil
			}
			other = nil
		}
		t.UpdatedAt = e.now()
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskResolveProposed, map[string]any{
			"task": dto, "outcome": proposal.Outcome, "user_id": userID, "side": side,
		}); err != nil {
			return nil, err
		}
		if other == nil || !proposalEqual(*other, proposal) {
			return dto, nil
		}
		return e.applyDisputeOutcome(ctx, r, t, outcomeInput{
			Outcome: proposal.Outcome, Reason: proposal.Reason, RoleID: proposal.RoleID,
			SubmissionID: proposal.SubmissionID, Pays: pays, Via: "resolve",
		})
	})
}

// ArbitrateTask applies a configured arbiter's outcome immediately.
func (e *Engine) ArbitrateTask(ctx context.Context, userID string, req *types.ArbitrateTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "arbitrate"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		if !e.IsArbiter(userID) {
			return nil, biz.Forbidden("only a configured arbiter can arbitrate")
		}
		reason := trim(req.Reason)
		if reason == "" || len(reason) > 2000 {
			return nil, biz.Invalid("reason is required and must be at most 2000 characters")
		}
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if !taskDisputed(t) {
			return nil, biz.Conflict("task is not disputed")
		}
		pot, err := disputeAmount(ctx, r, t)
		if err != nil {
			return nil, err
		}
		proposal, pays, err := e.normalizeOutcome(t, req.Outcome, reason, trim(req.RoleId), trim(req.SubmissionId), req.Splits, true, pot)
		if err != nil {
			return nil, err
		}
		if t.DisputeRoleID != "" {
			proposal.RoleID = t.DisputeRoleID
		}
		return e.applyDisputeOutcome(ctx, r, t, outcomeInput{
			Outcome: proposal.Outcome, Reason: reason, RoleID: proposal.RoleID,
			SubmissionID: proposal.SubmissionID, Pays: pays, Via: "arbitrate", Actor: userID,
		})
	})
}

func (e *Engine) disputeSeat(ctx context.Context, r store.Repos, t model.Task, userID, reason, roleID string) (*types.Task, error) {
	if t.Collaborative == nil {
		return nil, biz.Conflict("task is missing collaborative settings")
	}
	if t.Status != model.StatusActive && t.Status != model.StatusInReview {
		return nil, biz.Conflict("task is not in review")
	}
	if t.DisputeRoleID != "" {
		return nil, biz.Conflict("a seat is already disputed")
	}
	if t.EscrowID == "" {
		return nil, biz.Conflict("task has no escrow")
	}
	var submitted []int
	for i, role := range t.Collaborative.Roles {
		if role.Status == model.RoleSubmitted {
			submitted = append(submitted, i)
		}
	}
	idx := -1
	if roleID == "" {
		if len(submitted) != 1 {
			if len(submitted) == 0 {
				return nil, biz.Conflict("task is not in review")
			}
			ids := make([]string, len(submitted))
			for i, n := range submitted {
				ids[i] = t.Collaborative.Roles[n].ID
			}
			return nil, biz.Invalid("role_id is required when multiple seats are awaiting confirmation: " + strings.Join(ids, ", "))
		}
		idx = submitted[0]
	} else {
		idx = seatIndex(t, roleID)
		if idx < 0 {
			return nil, biz.Invalid("unknown role_id")
		}
		if t.Collaborative.Roles[idx].Status != model.RoleSubmitted {
			return nil, biz.Conflict("seat is not awaiting review")
		}
	}
	role := &t.Collaborative.Roles[idx]
	if userID != t.PublisherID && role.AssigneeUserID != userID {
		return nil, biz.Forbidden("only the publisher or that seat's executor can dispute")
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if acc.Frozen > 0 {
		return nil, biz.Conflict("escrow is already frozen")
	}
	amount, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, role.ID)
	if err != nil {
		return nil, err
	}
	if amount > acc.Held {
		return nil, biz.Conflict("escrow hold is below the release amount")
	}
	now := e.now()
	if amount > 0 {
		entry := model.EscrowEntry{
			ID: id.New("ent_"), EscrowID: acc.ID, Type: model.EscrowFreeze, Amount: amount, CreatedAt: now,
		}
		if err := r.Escrow().InsertEntry(ctx, entry); err != nil {
			return nil, err
		}
		acc.Frozen = amount
		if err := r.Escrow().Update(ctx, acc); err != nil {
			return nil, err
		}
	}
	role.Status = model.RoleDisputed
	t.DisputeRoleID = role.ID
	t.DisputeReason = reason
	t.Resolve = model.ResolveState{}
	e.syncCollaborativeProgress(&t)
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	if err := e.emit(ctx, r, parties(t), model.EventTaskDisputed, map[string]any{
		"task": dto, "reason": reason, "role_id": role.ID,
	}); err != nil {
		return nil, err
	}
	return dto, nil
}

func (e *Engine) markCancelled(ctx context.Context, r store.Repos, t model.Task, code, reason string, refund *model.EscrowEntry) (*types.Task, error) {
	now := e.now()
	t.Status = model.StatusCancelled
	t.CancelReason = reason
	t.CancelCode = code
	t.CancelledAt = &now
	clearReview(&t)
	t.StartDeadline = nil
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"task": dto}
	if reason != "" {
		data["reason"] = reason
	}
	if code != "" {
		data["reason"] = code
	}
	if err := e.emit(ctx, r, parties(t), model.EventTaskCancelled, data); err != nil {
		return nil, err
	}
	if refund != nil && refund.ID != "" {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{"task": dto, "entry": toEntry(*refund), "reason": code}); err != nil {
			return nil, err
		}
	}
	return dto, nil
}

func (e *Engine) cancelRefundAll(ctx context.Context, r store.Repos, t model.Task, code, reason string) (*types.Task, error) {
	if t.DisputeRoleID != "" || t.Status == model.StatusDisputed {
		return nil, biz.Conflict("task is disputed; use arbitrate or mutual resolve")
	}
	var refund *model.EscrowEntry
	if t.EscrowID != "" {
		acc, err := r.Escrow().GetByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		if acc.Frozen > 0 {
			return nil, biz.Conflict("escrow is frozen")
		}
		now := e.now()
		if acc.Held > 0 {
			ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, acc.Held, model.EscrowRefund, now)
			if err != nil {
				return nil, err
			}
			refund = &ent
		}
		if err := r.Escrow().Update(ctx, acc); err != nil {
			return nil, err
		}
	}
	if t.Collaborative != nil {
		for i := range t.Collaborative.Roles {
			role := &t.Collaborative.Roles[i]
			if role.Status == model.RoleSettled || role.Status == model.RoleRefunded {
				continue
			}
			role.Status = model.RoleRefunded
		}
	}
	if err := e.zeroExecutors(ctx, r, t); err != nil {
		return nil, err
	}
	return e.markCancelled(ctx, r, t, code, reason, refund)
}

func (e *Engine) cancelPerSeatRemainder(ctx context.Context, r store.Repos, t model.Task, reason string) (*types.Task, error) {
	if t.Collaborative == nil {
		return nil, biz.Conflict("task is missing collaborative settings")
	}
	if t.EscrowID == "" {
		return nil, biz.Conflict("task has no escrow")
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if acc.Frozen > 0 && t.DisputeRoleID == "" {
		return nil, biz.Conflict("escrow is frozen")
	}
	type unfinished struct {
		index int
		user  string
	}
	var seats []unfinished
	blocked := false
	for i, role := range t.Collaborative.Roles {
		switch role.Status {
		case model.RoleOpen, model.RoleDropped, "":
			seats = append(seats, unfinished{index: i})
		case model.RoleFilled:
			seats = append(seats, unfinished{index: i, user: role.AssigneeUserID})
		case model.RoleSubmitted, model.RoleDisputed:
			blocked = true
		}
	}
	if len(seats) == 0 {
		if blocked {
			return nil, biz.Conflict("submitted seats cannot be cancelled; confirm, revise, or dispute")
		}
		return nil, biz.Conflict("task cannot be cancelled")
	}
	now := e.now()
	var refunds []model.EscrowEntry
	for _, seat := range seats {
		role := &t.Collaborative.Roles[seat.index]
		amount, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, role.ID)
		if err != nil {
			return nil, err
		}
		if amount > spendable(acc, t) {
			return nil, biz.Conflict("escrow hold is below the release amount")
		}
		if amount > 0 {
			ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, amount, model.EscrowRefund, now)
			if err != nil {
				return nil, err
			}
			refunds = append(refunds, ent)
		}
		role.Status = model.RoleRefunded
		if seat.user != "" {
			if err := e.recordScore(ctx, r, seat.user, t.ID, scoreReasonDropout, 0, 0); err != nil {
				return nil, err
			}
		}
	}
	if err := r.Escrow().Update(ctx, acc); err != nil {
		return nil, err
	}
	terminal := true
	anySettled := false
	for _, role := range t.Collaborative.Roles {
		switch role.Status {
		case model.RoleSettled:
			anySettled = true
		case model.RoleRefunded:
		default:
			terminal = false
		}
	}
	if terminal {
		if anySettled {
			t.Status = model.StatusSettled
			clearReview(&t)
		} else {
			t.Status = model.StatusCancelled
			t.CancelReason = reason
			t.CancelledAt = &now
			clearReview(&t)
			t.StartDeadline = nil
		}
	} else {
		e.syncCollaborativeProgress(&t)
	}
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	for _, ent := range refunds {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{"task": dto, "entry": toEntry(ent)}); err != nil {
			return nil, err
		}
	}
	switch t.Status {
	case model.StatusCancelled:
		if err := e.emit(ctx, r, parties(t), model.EventTaskCancelled, map[string]any{"task": dto, "reason": reason}); err != nil {
			return nil, err
		}
	case model.StatusSettled:
		if err := e.emit(ctx, r, parties(t), model.EventTaskSettled, map[string]any{"task": dto}); err != nil {
			return nil, err
		}
		if err := e.scorePublisherIfNeeded(ctx, r, t, nil); err != nil {
			return nil, err
		}
	}
	return dto, nil
}

func (e *Engine) zeroExecutors(ctx context.Context, r store.Repos, t model.Task) error {
	ids := append([]string{}, t.ExecutorIDs...)
	if t.Collaborative != nil {
		for _, role := range t.Collaborative.Roles {
			if role.AssigneeUserID != "" {
				ids = append(ids, role.AssigneeUserID)
			}
		}
	}
	for _, uid := range dedupe(ids) {
		if err := e.recordScore(ctx, r, uid, t.ID, scoreReasonDropout, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

type outcomeInput struct {
	Outcome      string
	Reason       string
	RoleID       string
	SubmissionID string
	Pays         []payout
	Via          string
	Actor        string
}

func (e *Engine) applyDisputeOutcome(ctx context.Context, r store.Repos, t model.Task, in outcomeInput) (*types.Task, error) {
	if t.EscrowID == "" {
		return nil, biz.Conflict("task has no escrow")
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	pot := acc.Frozen
	if pot == 0 && acc.Held > 0 && t.DisputeRoleID == "" {
		pot = acc.Held
	}
	seatIdx := -1
	if t.DisputeRoleID != "" {
		seatIdx = seatIndex(t, t.DisputeRoleID)
		if seatIdx < 0 {
			return nil, biz.Conflict("disputed seat is missing")
		}
		share, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, t.DisputeRoleID)
		if err != nil {
			return nil, err
		}
		pot = share
		if acc.Frozen < pot {
			return nil, biz.Conflict("frozen amount is below the disputed seat")
		}
	}
	now := e.now()
	var unfrozen *model.EscrowEntry
	if acc.Frozen > 0 {
		amount := acc.Frozen
		if t.DisputeRoleID != "" {
			amount = pot
		}
		ent, err := e.unfreeze(ctx, r, &acc, amount, now)
		if err != nil {
			return nil, err
		}
		if ent.ID != "" {
			unfrozen = &ent
		}
	}
	var releases, refunds []model.EscrowEntry
	var paid []string
	var winner string
	switch in.Outcome {
	case model.OutcomePayExecutor:
		pays, win, err := e.payPlan(ctx, r, t, pot, in.SubmissionID, seatIdx)
		if err != nil {
			return nil, err
		}
		winner = win
		releases, paid, err = e.creditPays(ctx, r, t, &acc, pays, now)
		if err != nil {
			return nil, err
		}
	case model.OutcomeRefundPublisher:
		if pot > 0 {
			ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, pot, model.EscrowRefund, now)
			if err != nil {
				return nil, err
			}
			if ent.ID != "" {
				refunds = append(refunds, ent)
			}
		}
	case model.OutcomeSplit:
		var err error
		releases, refunds, paid, err = e.creditMixed(ctx, r, t, &acc, in.Pays, now)
		if err != nil {
			return nil, err
		}
	default:
		return nil, biz.Invalid("outcome must be pay_executor, refund_publisher, or split")
	}
	if err := r.Escrow().Update(ctx, acc); err != nil {
		return nil, err
	}
	paidSomeone := len(paid) > 0
	if seatIdx >= 0 {
		if paidSomeone {
			t.Collaborative.Roles[seatIdx].Status = model.RoleSettled
		} else {
			t.Collaborative.Roles[seatIdx].Status = model.RoleRefunded
		}
		t.DisputeRoleID = ""
		t.DisputeReason = ""
		t.Resolve = model.ResolveState{}
		e.syncCollaborativeProgress(&t)
	} else if paidSomeone || in.Outcome == model.OutcomePayExecutor {
		if isUnifiedCollab(t) && t.Collaborative != nil {
			for i := range t.Collaborative.Roles {
				role := &t.Collaborative.Roles[i]
				if role.Status == model.RoleFilled || role.Status == model.RoleSubmitted || role.Status == model.RoleDisputed {
					role.Status = model.RoleSettled
				}
			}
		}
		t.Status = model.StatusSettled
		if winner != "" {
			t.WinningSubmissionID = &winner
		}
		clearReview(&t)
		t.DisputeRoleID = ""
		t.DisputeReason = ""
		t.Resolve = model.ResolveState{}
	} else {
		t.Status = model.StatusCancelled
		t.CancelReason = in.Reason
		if in.Reason == "" {
			t.CancelReason = in.Outcome
		}
		t.CancelCode = in.Outcome
		t.CancelledAt = &now
		clearReview(&t)
		t.DisputeRoleID = ""
		t.DisputeReason = ""
		t.Resolve = model.ResolveState{}
	}
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	if in.Via == "arbitrate" {
		body := map[string]any{"task": dto, "outcome": in.Outcome, "reason": in.Reason, "user_id": in.Actor}
		if in.RoleID != "" {
			body["role_id"] = in.RoleID
		}
		if winner != "" {
			body["submission_id"] = winner
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskArbitrated, body); err != nil {
			return nil, err
		}
	}
	if unfrozen != nil {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowUnfreeze, map[string]any{"task": dto, "entry": toEntry(*unfrozen)}); err != nil {
			return nil, err
		}
	}
	for _, ent := range releases {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRelease, map[string]any{"task": dto, "entry": toEntry(ent)}); err != nil {
			return nil, err
		}
	}
	for _, ent := range refunds {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{"task": dto, "entry": toEntry(ent)}); err != nil {
			return nil, err
		}
	}
	if t.Status == model.StatusSettled {
		body := map[string]any{"task": dto, "outcome": in.Outcome}
		if in.RoleID != "" {
			body["role_id"] = in.RoleID
		}
		if winner != "" {
			body["submission_id"] = winner
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskSettled, body); err != nil {
			return nil, err
		}
		for _, uid := range dedupe(paid) {
			if err := e.recordScore(ctx, r, uid, t.ID, scoreReasonSettle, integrityOnSettle, abilityOnSettle); err != nil {
				return nil, err
			}
		}
		if err := e.scorePublisherIfNeeded(ctx, r, t, paid); err != nil {
			return nil, err
		}
	}
	if t.Status == model.StatusCancelled {
		if err := e.emit(ctx, r, parties(t), model.EventTaskCancelled, map[string]any{"task": dto, "reason": t.CancelReason, "outcome": in.Outcome}); err != nil {
			return nil, err
		}
	}
	return dto, nil
}

func (e *Engine) scorePublisherIfNeeded(ctx context.Context, r store.Repos, t model.Task, paid []string) error {
	if contains(paid, t.PublisherID) {
		return nil
	}
	return e.recordScore(ctx, r, t.PublisherID, t.ID, scoreReasonSettle, integrityOnSettle, 0)
}

func (e *Engine) payPlan(ctx context.Context, r store.Repos, t model.Task, pot int64, submissionID string, seatIdx int) ([]payout, string, error) {
	if seatIdx >= 0 {
		role := t.Collaborative.Roles[seatIdx]
		if role.AssigneeUserID == "" {
			return nil, "", biz.Conflict("disputed seat has no executor")
		}
		if submissionID != "" {
			subs, err := r.Submissions().ListByTask(ctx, t.ID)
			if err != nil {
				return nil, "", err
			}
			found := false
			for _, s := range subs {
				if s.ID == submissionID && s.ExecutorID == role.AssigneeUserID {
					found = true
				}
			}
			if !found {
				return nil, "", biz.Invalid("submission_id does not match a submission on this task")
			}
		}
		return []payout{{UserID: role.AssigneeUserID, Amount: pot}}, "", nil
	}
	switch t.Kind {
	case model.KindExclusive:
		if len(t.ExecutorIDs) != 1 {
			return nil, "", biz.Conflict("exclusive task has no single executor")
		}
		return []payout{{UserID: t.ExecutorIDs[0], Amount: pot}}, "", nil
	case model.KindCompetitive:
		if submissionID == "" {
			return nil, "", biz.Invalid("submission_id is required to pay a competitive executor")
		}
		subs, err := r.Submissions().ListByTask(ctx, t.ID)
		if err != nil {
			return nil, "", err
		}
		var chosen *model.Submission
		for i := range subs {
			if subs[i].ID == submissionID && submissionCurrent(subs[i]) {
				chosen = &subs[i]
				break
			}
		}
		if chosen == nil {
			return nil, "", biz.Invalid("submission_id does not match a submission on this task")
		}
		return []payout{{UserID: chosen.ExecutorID, Amount: pot}}, chosen.ID, nil
	case model.KindCollaborative:
		pays, _, users, err := e.planPayout(t, pot, nil)
		if err != nil {
			return nil, "", err
		}
		_ = users
		return pays, "", nil
	default:
		return nil, "", biz.Conflict("unknown task kind")
	}
}

func (e *Engine) creditPays(ctx context.Context, r store.Repos, t model.Task, acc *model.EscrowAccount, pays []payout, now time.Time) ([]model.EscrowEntry, []string, error) {
	var releases []model.EscrowEntry
	var paid []string
	for _, p := range pays {
		if p.Amount <= 0 || p.UserID == "" {
			continue
		}
		ent, err := e.creditEscrow(ctx, r, acc, p.UserID, p.Amount, model.EscrowRelease, now)
		if err != nil {
			return nil, nil, err
		}
		if ent.ID != "" {
			releases = append(releases, ent)
			paid = append(paid, p.UserID)
		}
	}
	return releases, paid, nil
}

func (e *Engine) creditMixed(ctx context.Context, r store.Repos, t model.Task, acc *model.EscrowAccount, pays []payout, now time.Time) ([]model.EscrowEntry, []model.EscrowEntry, []string, error) {
	var releases, refunds []model.EscrowEntry
	var paid []string
	for _, p := range pays {
		if p.Amount <= 0 || p.UserID == "" {
			continue
		}
		kind := model.EscrowRelease
		if p.UserID == t.PublisherID {
			kind = model.EscrowRefund
		}
		ent, err := e.creditEscrow(ctx, r, acc, p.UserID, p.Amount, kind, now)
		if err != nil {
			return nil, nil, nil, err
		}
		if ent.ID == "" {
			continue
		}
		if kind == model.EscrowRefund {
			refunds = append(refunds, ent)
		} else {
			releases = append(releases, ent)
			paid = append(paid, p.UserID)
		}
	}
	return releases, refunds, paid, nil
}

func (e *Engine) normalizeOutcome(t model.Task, outcome, reason, roleID, submissionID string, lines []types.SplitLine, reasonRequired bool, pot int64) (model.ResolveProposal, []payout, error) {
	if reasonRequired && (reason == "" || len(reason) > 2000) {
		return model.ResolveProposal{}, nil, biz.Invalid("reason is required and must be at most 2000 characters")
	}
	if len(reason) > 2000 {
		return model.ResolveProposal{}, nil, biz.Invalid("reason must be at most 2000 characters")
	}
	switch outcome {
	case model.OutcomePayExecutor, model.OutcomeRefundPublisher, model.OutcomeSplit:
	default:
		return model.ResolveProposal{}, nil, biz.Invalid("outcome must be pay_executor, refund_publisher, or split")
	}
	if t.DisputeRoleID != "" {
		if roleID != "" && roleID != t.DisputeRoleID {
			return model.ResolveProposal{}, nil, biz.Invalid("role_id does not match the disputed seat")
		}
		roleID = t.DisputeRoleID
	} else if roleID != "" {
		return model.ResolveProposal{}, nil, biz.Invalid("role_id is only valid for a per-seat dispute")
	}
	if outcome == model.OutcomePayExecutor && t.Kind == model.KindCompetitive && t.DisputeRoleID == "" && submissionID == "" {
		return model.ResolveProposal{}, nil, biz.Invalid("submission_id is required to pay a competitive executor")
	}
	prop := model.ResolveProposal{Outcome: outcome, Reason: reason, RoleID: roleID, SubmissionID: submissionID}
	if outcome != model.OutcomeSplit {
		if len(lines) > 0 {
			return model.ResolveProposal{}, nil, biz.Invalid("splits are only valid when outcome is split")
		}
		return prop, nil, nil
	}
	splits, pays, err := normalizeSplits(lines, pot)
	if err != nil {
		return model.ResolveProposal{}, nil, err
	}
	prop.Splits = splits
	return prop, pays, nil
}

func disputeAmount(ctx context.Context, r store.Repos, t model.Task) (int64, error) {
	if t.EscrowID == "" {
		return 0, biz.Conflict("task has no escrow")
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return 0, err
	}
	if t.DisputeRoleID != "" {
		if t.Collaborative == nil {
			return 0, biz.Conflict("task is missing collaborative settings")
		}
		return seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, t.DisputeRoleID)
	}
	if acc.Frozen > 0 {
		return acc.Frozen, nil
	}
	return acc.Held, nil
}

func normalizeSplits(lines []types.SplitLine, pot int64) ([]model.Split, []payout, error) {
	if len(lines) == 0 {
		return nil, nil, biz.Invalid("splits are required when outcome is split")
	}
	if len(lines) > 50 {
		return nil, nil, biz.Invalid("splits has too many items")
	}
	var amounts, bps int
	var sumAmt, sumBps int64
	out := make([]model.Split, 0, len(lines))
	seen := map[string]struct{}{}
	for _, line := range lines {
		uid := trim(line.UserId)
		if !id.ValidUserID(uid) {
			return nil, nil, biz.Invalid("splits contain an invalid user_id")
		}
		if _, ok := seen[uid]; ok {
			return nil, nil, biz.Invalid("splits contain a duplicate user_id")
		}
		seen[uid] = struct{}{}
		hasAmt := line.Amount != nil
		hasBps := line.ShareBps != nil
		if hasAmt == hasBps {
			return nil, nil, biz.Invalid("each split needs amount or share_bps, not both")
		}
		if hasAmt {
			amounts++
			if *line.Amount < 0 || *line.Amount > maxMoney {
				return nil, nil, biz.Invalid("split amount is out of range")
			}
			sumAmt += *line.Amount
			amt := *line.Amount
			out = append(out, model.Split{UserID: uid, Amount: &amt})
			continue
		}
		bps++
		if *line.ShareBps < 0 || *line.ShareBps > 10000 {
			return nil, nil, biz.Invalid("split share_bps must be between 0 and 10000")
		}
		sumBps += *line.ShareBps
		share := *line.ShareBps
		out = append(out, model.Split{UserID: uid, ShareBps: &share})
	}
	if amounts > 0 && bps > 0 {
		return nil, nil, biz.Invalid("splits must all use amount or all use share_bps")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	pays := make([]payout, len(out))
	if amounts > 0 {
		if sumAmt != pot {
			return nil, nil, biz.Invalid("split amounts must sum to the frozen amount")
		}
		for i, s := range out {
			pays[i] = payout{UserID: s.UserID, Amount: *s.Amount}
		}
		return out, pays, nil
	}
	if sumBps != 10000 {
		return nil, nil, biz.Invalid("split share_bps must sum to 10000")
	}
	var floor int64
	maxI := 0
	for i, s := range out {
		amt := pot * *s.ShareBps / 10000
		pays[i] = payout{UserID: s.UserID, Amount: amt}
		floor += amt
		if *s.ShareBps > *out[maxI].ShareBps {
			maxI = i
		}
	}
	if pot-floor < 0 {
		return nil, nil, biz.Invalid("split rounding failed")
	}
	pays[maxI].Amount += pot - floor
	return out, pays, nil
}

func proposalEqual(a, b model.ResolveProposal) bool {
	if a.Outcome != b.Outcome || a.RoleID != b.RoleID || a.SubmissionID != b.SubmissionID {
		return false
	}
	ab, _ := json.Marshal(a.Splits)
	bb, _ := json.Marshal(b.Splits)
	return string(ab) == string(bb)
}

func resolveSide(t model.Task, userID string) (string, error) {
	if userID == t.PublisherID {
		return "publisher", nil
	}
	if t.DisputeRoleID != "" && t.Collaborative != nil {
		for _, role := range t.Collaborative.Roles {
			if role.ID == t.DisputeRoleID && role.AssigneeUserID == userID {
				return "executor", nil
			}
		}
		return "", biz.Forbidden("only the publisher or the disputed seat's executor can resolve")
	}
	if contains(t.ExecutorIDs, userID) {
		return "executor", nil
	}
	return "", biz.Forbidden("only the publisher or an executor can resolve")
}

func pickRevision(t model.Task, subs []model.Submission, roleID, submissionID string) (model.Submission, int, error) {
	current := currentSubmissions(subs)
	if len(current) == 0 {
		return model.Submission{}, -1, biz.Conflict("task has no submission to revise")
	}
	if t.Kind == model.KindCollaborative {
		if t.Collaborative == nil {
			return model.Submission{}, -1, biz.Conflict("task is missing collaborative settings")
		}
		var submitted []int
		var ids []string
		for i, role := range t.Collaborative.Roles {
			if role.Status == model.RoleSubmitted {
				submitted = append(submitted, i)
				ids = append(ids, role.ID)
			}
		}
		if len(submitted) == 0 {
			return model.Submission{}, -1, biz.Conflict("task is not awaiting review")
		}
		idx := -1
		if roleID != "" {
			idx = seatIndex(t, roleID)
			if idx < 0 {
				return model.Submission{}, -1, biz.Invalid("unknown role_id")
			}
			if t.Collaborative.Roles[idx].Status != model.RoleSubmitted {
				return model.Submission{}, -1, biz.Conflict("seat is not awaiting review")
			}
		} else if len(submitted) == 1 {
			idx = submitted[0]
		} else {
			return model.Submission{}, -1, biz.Invalid("role_id is required when multiple seats are awaiting confirmation: " + strings.Join(ids, ", "))
		}
		role := t.Collaborative.Roles[idx]
		var found *model.Submission
		for i := range current {
			if current[i].ExecutorID != role.AssigneeUserID {
				continue
			}
			if current[i].RoleID != "" && current[i].RoleID != role.ID {
				continue
			}
			if submissionID != "" && current[i].ID != submissionID {
				continue
			}
			found = &current[i]
			break
		}
		if found == nil {
			if submissionID != "" {
				return model.Submission{}, -1, biz.Invalid("submission_id does not match a submission on this task")
			}
			return model.Submission{}, -1, biz.Conflict("seat has no submission to revise")
		}
		return *found, idx, nil
	}
	if t.Status != model.StatusInReview {
		return model.Submission{}, -1, biz.Conflict("task is not awaiting review")
	}
	if submissionID == "" {
		if len(current) != 1 {
			ids := make([]string, len(current))
			for i, s := range current {
				ids[i] = s.ID
			}
			return model.Submission{}, -1, biz.Invalid("submission_id is required when multiple submissions exist: " + strings.Join(ids, ", "))
		}
		return current[0], -1, nil
	}
	for _, s := range current {
		if s.ID == submissionID {
			return s, -1, nil
		}
	}
	return model.Submission{}, -1, biz.Invalid("submission_id does not match a submission on this task")
}

func revisionCount(subs []model.Submission, kind, roleID, executorID string) int {
	n := 0
	for _, s := range subs {
		if s.Status != model.SubmissionSuperseded {
			continue
		}
		switch kind {
		case model.KindExclusive:
			n++
		case model.KindCompetitive:
			if s.ExecutorID == executorID {
				n++
			}
		default:
			if s.RoleID == roleID || (s.RoleID == "" && s.ExecutorID == executorID) {
				n++
			}
		}
	}
	return n
}
