package engine

import (
	"context"
	"net/http"
	"strings"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// StartTask moves a unified collaborative task from open to active once every seat is filled.
func (e *Engine) StartTask(ctx context.Context, userID string, req *types.StartTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "start"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		t, err := loadTask(ctx, r, req.TaskId)
		if err != nil {
			return nil, err
		}
		if t.Kind != model.KindCollaborative || t.Collaborative == nil {
			return nil, biz.Conflict("start is only valid for collaborative tasks")
		}
		if !isUnifiedCollab(t) {
			return nil, biz.Conflict("start is only valid for unified settlement")
		}
		if t.PublisherID != userID {
			return nil, biz.Forbidden("only the publisher can start this task")
		}
		if t.Status == model.StatusActive || t.Status == model.StatusInReview || t.Status == model.StatusSettled {
			return nil, biz.Conflict("task has already started")
		}
		if t.Status != model.StatusOpen {
			return nil, biz.Conflict("task is not open")
		}
		var openIDs []string
		for _, role := range t.Collaborative.Roles {
			if role.Status == model.RoleFilled && role.AssigneeUserID != "" {
				continue
			}
			openIDs = append(openIDs, role.ID)
		}
		if len(openIDs) > 0 {
			return nil, biz.Conflict("unified start requires every seat to be filled; open: " + strings.Join(openIDs, ", "))
		}
		t.Status = model.StatusActive
		t.UpdatedAt = e.now()
		if err := r.Tasks().Update(ctx, t); err != nil {
			return nil, err
		}
		dto, err := e.dto(ctx, r, t)
		if err != nil {
			return nil, err
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskStarted, map[string]any{"task": dto}); err != nil {
			return nil, err
		}
		return dto, nil
	})
}

func settlementModeOf(t model.Task) string {
	if t.Kind != model.KindCollaborative || t.Collaborative == nil {
		return ""
	}
	if t.Collaborative.SettlementMode == "" {
		return model.SettlementUnified
	}
	return t.Collaborative.SettlementMode
}

func isUnifiedCollab(t model.Task) bool {
	return settlementModeOf(t) == model.SettlementUnified
}

func isPerSeatCollab(t model.Task) bool {
	return settlementModeOf(t) == model.SettlementPerSeat
}

func (e *Engine) armReview(t *model.Task) {
	hours := t.Acceptance.ReviewHours
	if hours <= 0 {
		hours = defaultReviewHours
	}
	deadline := e.now().Add(hoursDuration(hours))
	t.ReviewDeadline = &deadline
}

// perSeatTaskStatus is the task status implied by seat statuses.
// A paid seat plus an open seat stays active. Refunds alone leave the task open
// so the remaining seats can still be accepted. Settled means every seat is
// paid or refunded.
func perSeatTaskStatus(t model.Task) string {
	if t.Collaborative == nil {
		return t.Status
	}
	var openN, filledN, submittedN, paidN, refundN int
	for _, role := range t.Collaborative.Roles {
		switch role.Status {
		case model.RoleFilled:
			filledN++
		case model.RoleSubmitted:
			submittedN++
		case model.RoleSettled:
			paidN++
		case model.RoleRefunded:
			refundN++
		default:
			openN++
		}
	}
	if openN+filledN+submittedN+paidN+refundN == 0 {
		return model.StatusOpen
	}
	if openN == 0 && filledN == 0 && submittedN == 0 {
		return model.StatusSettled
	}
	if filledN == 0 && submittedN == 0 && paidN == 0 {
		return model.StatusOpen
	}
	if openN == 0 && filledN == 0 && submittedN > 0 {
		return model.StatusInReview
	}
	return model.StatusActive
}

func collaborativeUnifiedReady(t model.Task) bool {
	if t.Collaborative == nil || len(t.Collaborative.Roles) == 0 {
		return false
	}
	for _, role := range t.Collaborative.Roles {
		if role.Status != model.RoleSubmitted && role.Status != model.RoleSettled {
			return false
		}
	}
	return true
}

// syncCollaborativeProgress applies the mode's task status after a seat change.
// Unified tasks that are still open have not been started, so this leaves them open.
func (e *Engine) syncCollaborativeProgress(t *model.Task) {
	if t.Collaborative == nil {
		return
	}
	if isPerSeatCollab(*t) {
		next := perSeatTaskStatus(*t)
		if next == model.StatusInReview {
			if t.Status != model.StatusInReview || t.ReviewDeadline == nil {
				e.armReview(t)
			}
		} else {
			t.ReviewDeadline = nil
		}
		t.Status = next
		return
	}
	if !isUnifiedCollab(*t) {
		return
	}
	if t.Status != model.StatusActive && t.Status != model.StatusInReview {
		return
	}
	if collaborativeUnifiedReady(*t) {
		if t.Status != model.StatusInReview || t.ReviewDeadline == nil {
			e.armReview(t)
		}
		t.Status = model.StatusInReview
		return
	}
	t.Status = model.StatusActive
	t.ReviewDeadline = nil
}

func markSubmittedSeats(t *model.Task, subs []model.Submission) {
	if t.Collaborative == nil {
		return
	}
	have := map[string]struct{}{}
	for _, s := range subs {
		if s.MilestoneID == "" {
			have[s.ExecutorID] = struct{}{}
		}
	}
	for i := range t.Collaborative.Roles {
		role := &t.Collaborative.Roles[i]
		if role.Status != model.RoleFilled || role.AssigneeUserID == "" {
			continue
		}
		if _, ok := have[role.AssigneeUserID]; ok {
			role.Status = model.RoleSubmitted
		}
	}
}

func seatIndex(t model.Task, roleID string) int {
	if t.Collaborative == nil {
		return -1
	}
	for i, role := range t.Collaborative.Roles {
		if role.ID == roleID {
			return i
		}
	}
	return -1
}

func findSubmission(subs []model.Submission, milestoneID, executorID string) *model.Submission {
	for i := range subs {
		if subs[i].ExecutorID == executorID && subs[i].MilestoneID == milestoneID {
			return &subs[i]
		}
	}
	return nil
}

func ensureUnifiedReady(t model.Task, subs []model.Submission, milestoneID string) error {
	if t.Collaborative == nil {
		return biz.Conflict("task is missing collaborative settings")
	}
	var openIDs, missing []string
	for _, role := range t.Collaborative.Roles {
		switch role.Status {
		case model.RoleSettled, model.RoleRefunded:
			continue
		case model.RoleFilled, model.RoleSubmitted:
			if role.AssigneeUserID == "" || findSubmission(subs, milestoneID, role.AssigneeUserID) == nil {
				missing = append(missing, role.ID)
			}
		default:
			openIDs = append(openIDs, role.ID)
		}
	}
	if len(openIDs) > 0 {
		return biz.Conflict("unified settlement requires every seat to be filled; open: " + strings.Join(openIDs, ", "))
	}
	if len(missing) > 0 {
		return biz.Conflict("unified settlement requires a submission from every filled seat; missing: " + strings.Join(missing, ", "))
	}
	return nil
}

func (e *Engine) creditEscrow(ctx context.Context, r store.Repos, acc *model.EscrowAccount, toUser string, amount int64, entryType string, now time.Time) (model.EscrowEntry, error) {
	if amount <= 0 {
		return model.EscrowEntry{}, nil
	}
	if _, err := r.Users().Ensure(ctx, toUser, now); err != nil {
		return model.EscrowEntry{}, err
	}
	if err := r.Users().AddBalance(ctx, toUser, amount); err != nil {
		return model.EscrowEntry{}, err
	}
	ent := model.EscrowEntry{
		ID: id.New("ent_"), EscrowID: acc.ID, Type: entryType,
		Amount: amount, ToUserID: toUser, CreatedAt: now,
	}
	if err := r.Escrow().InsertEntry(ctx, ent); err != nil {
		return model.EscrowEntry{}, err
	}
	acc.Held -= amount
	return ent, nil
}

type seatAction struct {
	index int
	pay   bool
}

func (e *Engine) confirmPerSeat(ctx context.Context, r store.Repos, t model.Task, in confirmInput) (*types.Task, error) {
	if t.Collaborative == nil {
		return nil, biz.Conflict("task is missing collaborative settings")
	}
	switch t.Status {
	case model.StatusOpen, model.StatusActive, model.StatusInReview:
	default:
		if in.Auto {
			return nil, errAutoDeferred
		}
		return nil, biz.Conflict("task is not open for per-seat settlement")
	}
	if !in.Auto && t.PublisherID != in.Actor {
		return nil, biz.Forbidden("only the publisher can confirm")
	}
	if trim(in.MilestoneID) != "" {
		return nil, biz.Invalid("milestone_id is only valid for milestone payments")
	}
	if t.EscrowID == "" {
		return nil, biz.Conflict("task has no escrow")
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if acc.Frozen > 0 {
		return nil, biz.Conflict("escrow is frozen")
	}
	subs, err := r.Submissions().ListByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	actions, err := perSeatActions(t, trim(in.RoleID), in.Auto)
	if err != nil {
		return nil, err
	}

	now := e.now()
	var releases []model.EscrowEntry
	var refunds []model.EscrowEntry
	type confirmed struct {
		roleID string
		subID  string
	}
	var confirmedSeats []confirmed
	var paidUsers []string
	for _, act := range actions {
		role := &t.Collaborative.Roles[act.index]
		amount, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, role.ID)
		if err != nil {
			return nil, err
		}
		if amount > acc.Held {
			return nil, biz.Conflict("escrow hold is below the release amount")
		}
		if act.pay {
			sub := findSubmission(subs, "", role.AssigneeUserID)
			if sub == nil {
				if in.Auto {
					return nil, errAutoDeferred
				}
				return nil, biz.Conflict("seat has no submission to confirm")
			}
			if trim(in.SubmissionID) != "" && sub.ID != trim(in.SubmissionID) {
				return nil, biz.Invalid("submission_id does not match a submission on this task")
			}
			if amount > 0 {
				ent, err := e.creditEscrow(ctx, r, &acc, role.AssigneeUserID, amount, model.EscrowRelease, now)
				if err != nil {
					return nil, err
				}
				releases = append(releases, ent)
			}
			role.Status = model.RoleSettled
			paidUsers = append(paidUsers, role.AssigneeUserID)
			confirmedSeats = append(confirmedSeats, confirmed{roleID: role.ID, subID: sub.ID})
			continue
		}
		if amount > 0 {
			ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, amount, model.EscrowRefund, now)
			if err != nil {
				return nil, err
			}
			refunds = append(refunds, ent)
		}
		role.Status = model.RoleRefunded
	}
	if perSeatTaskStatus(t) == model.StatusSettled && acc.Held != 0 {
		return nil, biz.Conflict("escrow hold does not match the settled seats")
	}
	if err := r.Escrow().Update(ctx, acc); err != nil {
		return nil, err
	}
	e.syncCollaborativeProgress(&t)
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	for _, seat := range confirmedSeats {
		body := map[string]any{"task": dto, "role_id": seat.roleID, "submission_id": seat.subID}
		if err := e.emit(ctx, r, parties(t), model.EventTaskConfirmed, body); err != nil {
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
		body := map[string]any{"task": dto}
		if len(confirmedSeats) == 1 {
			body["role_id"] = confirmedSeats[0].roleID
			body["submission_id"] = confirmedSeats[0].subID
		}
		if err := e.emit(ctx, r, parties(t), model.EventTaskSettled, body); err != nil {
			return nil, err
		}
		if in.Auto {
			if err := e.emit(ctx, r, parties(t), model.EventTaskAutoSettled, body); err != nil {
				return nil, err
			}
		}
	}
	seen := map[string]struct{}{}
	for _, uid := range paidUsers {
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		if err := e.recordScore(ctx, r, uid, t.ID, scoreReasonSettle, integrityOnSettle, abilityOnSettle); err != nil {
			return nil, err
		}
	}
	if t.Status == model.StatusSettled {
		if _, ok := seen[t.PublisherID]; !ok {
			if err := e.recordScore(ctx, r, t.PublisherID, t.ID, scoreReasonSettle, integrityOnSettle, 0); err != nil {
				return nil, err
			}
		}
	}
	return dto, nil
}

func perSeatActions(t model.Task, roleID string, auto bool) ([]seatAction, error) {
	if t.Collaborative == nil {
		return nil, biz.Conflict("task is missing collaborative settings")
	}
	if auto {
		var actions []seatAction
		for i, role := range t.Collaborative.Roles {
			switch role.Status {
			case model.RoleSubmitted:
				actions = append(actions, seatAction{index: i, pay: true})
			case model.RoleSettled, model.RoleRefunded:
			default:
				return nil, errAutoDeferred
			}
		}
		if len(actions) == 0 {
			return nil, errAutoDeferred
		}
		return actions, nil
	}
	if roleID != "" {
		idx := seatIndex(t, roleID)
		if idx < 0 {
			return nil, biz.Invalid("unknown role_id")
		}
		switch t.Collaborative.Roles[idx].Status {
		case model.RoleSubmitted:
			return []seatAction{{index: idx, pay: true}}, nil
		case model.RoleOpen, model.RoleDropped, "":
			return []seatAction{{index: idx, pay: false}}, nil
		case model.RoleFilled:
			return nil, biz.Conflict("seat has not submitted")
		case model.RoleSettled:
			return nil, biz.Conflict("seat is already settled")
		case model.RoleRefunded:
			return nil, biz.Conflict("seat is already refunded")
		default:
			return nil, biz.Conflict("seat cannot be confirmed")
		}
	}
	var submitted, open []int
	var submittedIDs, filledIDs []string
	for i, role := range t.Collaborative.Roles {
		switch role.Status {
		case model.RoleSubmitted:
			submitted = append(submitted, i)
			submittedIDs = append(submittedIDs, role.ID)
		case model.RoleFilled:
			filledIDs = append(filledIDs, role.ID)
		case model.RoleOpen, model.RoleDropped, "":
			open = append(open, i)
		}
	}
	switch {
	case len(submitted) > 1:
		return nil, biz.Invalid("role_id is required when multiple seats are awaiting confirmation: " + strings.Join(submittedIDs, ", "))
	case len(submitted) == 1:
		return []seatAction{{index: submitted[0], pay: true}}, nil
	case len(filledIDs) > 0:
		return nil, biz.Conflict("seat has not submitted: " + strings.Join(filledIDs, ", "))
	case len(open) > 0:
		actions := make([]seatAction, 0, len(open))
		for _, i := range open {
			actions = append(actions, seatAction{index: i, pay: false})
		}
		return actions, nil
	default:
		return nil, biz.Conflict("no seats left to settle")
	}
}
