package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

type confirmInput struct {
	Actor        string
	TaskID       string
	SubmissionID string
	MilestoneID  string
	Auto         bool
}

// ConfirmTask settles in_review work. Competitive settlement is winner_take_all.
// Milestone mode releases one milestone; the task stays active until every milestone is released.
func (e *Engine) ConfirmTask(ctx context.Context, userID string, req *types.ConfirmTaskReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "confirm"), req.IdempotencyKey, req, func(ctx context.Context, r store.Repos) (*types.Task, error) {
		return e.confirm(ctx, r, confirmInput{
			Actor:        userID,
			TaskID:       req.TaskId,
			SubmissionID: req.SubmissionId,
			MilestoneID:  req.MilestoneId,
		})
	})
}

func (e *Engine) confirm(ctx context.Context, r store.Repos, in confirmInput) (*types.Task, error) {
	t, err := loadTask(ctx, r, in.TaskID)
	if err != nil {
		return nil, err
	}
	if t.Status != model.StatusInReview {
		if in.Auto {
			return nil, errAutoDeferred
		}
		return nil, biz.Conflict("task is not in review")
	}
	if !in.Auto && t.PublisherID != in.Actor {
		return nil, biz.Forbidden("only the publisher can confirm")
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
	milestoneID := trim(in.MilestoneID)
	pool := acc.Held
	milestoneIdx := -1
	if t.Payment.Mode == model.PayMilestone {
		idx, id, err := pickMilestone(t, milestoneID, in.Auto)
		if err != nil {
			return nil, err
		}
		milestoneIdx = idx
		milestoneID = id
		pool = t.Payment.Milestones[idx].Amount
	} else if milestoneID != "" {
		return nil, biz.Invalid("milestone_id is only valid for milestone payments")
	}
	if pool < 0 || acc.Held < pool {
		return nil, biz.Conflict("escrow hold is below the release amount")
	}

	relevant := subs
	if milestoneID != "" {
		relevant = nil
		for _, s := range subs {
			if s.MilestoneID == milestoneID {
				relevant = append(relevant, s)
			}
		}
	}
	if len(relevant) == 0 {
		if in.Auto {
			return nil, errAutoDeferred
		}
		return nil, biz.Conflict("task has no submission to confirm")
	}
	chosen, err := chooseSubmission(t, relevant, trim(in.SubmissionID), in.Auto)
	if err != nil {
		return nil, err
	}

	pays, refund, scoreUsers, err := e.planPayout(t, pool, chosen)
	if err != nil {
		if in.Auto {
			var be *biz.Error
			if errors.As(err, &be) {
				return nil, errAutoDeferred
			}
		}
		return nil, err
	}
	var got int64
	for _, p := range pays {
		got += p.Amount
	}
	if got+refund != pool {
		return nil, fmt.Errorf("settlement split %d+%d != %d", got, refund, pool)
	}

	now := e.now()
	var releases []model.EscrowEntry
	for _, p := range pays {
		if p.Amount <= 0 {
			continue
		}
		if _, err := r.Users().Ensure(ctx, p.UserID, now); err != nil {
			return nil, err
		}
		if err := r.Users().AddBalance(ctx, p.UserID, p.Amount); err != nil {
			return nil, err
		}
		ent := model.EscrowEntry{
			ID: id.New("ent_"), EscrowID: acc.ID, Type: model.EscrowRelease,
			Amount: p.Amount, ToUserID: p.UserID, CreatedAt: now,
		}
		if err := r.Escrow().InsertEntry(ctx, ent); err != nil {
			return nil, err
		}
		releases = append(releases, ent)
	}
	var refundEntry *model.EscrowEntry
	if refund > 0 {
		if _, err := r.Users().Ensure(ctx, t.PublisherID, now); err != nil {
			return nil, err
		}
		if err := r.Users().AddBalance(ctx, t.PublisherID, refund); err != nil {
			return nil, err
		}
		ent := model.EscrowEntry{
			ID: id.New("ent_"), EscrowID: acc.ID, Type: model.EscrowRefund,
			Amount: refund, ToUserID: t.PublisherID, CreatedAt: now,
		}
		if err := r.Escrow().InsertEntry(ctx, ent); err != nil {
			return nil, err
		}
		refundEntry = &ent
	}
	acc.Held -= pool
	if err := r.Escrow().Update(ctx, acc); err != nil {
		return nil, err
	}

	fully := true
	if milestoneIdx >= 0 {
		t.Payment.Milestones[milestoneIdx].Status = model.MilestoneReleased
		for _, m := range t.Payment.Milestones {
			if m.Status != model.MilestoneReleased {
				fully = false
			}
		}
	}
	if fully {
		t.Status = model.StatusSettled
		if chosen != nil {
			win := chosen.ID
			t.WinningSubmissionID = &win
		}
	} else {
		t.Status = model.StatusActive
	}
	t.ReviewDeadline = nil
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return nil, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"task": dto}
	if chosen != nil {
		body["submission_id"] = chosen.ID
	}
	if err := e.emit(ctx, r, parties(t), model.EventTaskConfirmed, body); err != nil {
		return nil, err
	}
	for _, ent := range releases {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRelease, map[string]any{"task": dto, "entry": toEntry(ent)}); err != nil {
			return nil, err
		}
	}
	if refundEntry != nil {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{"task": dto, "entry": toEntry(*refundEntry)}); err != nil {
			return nil, err
		}
	}
	if fully {
		if err := e.emit(ctx, r, parties(t), model.EventTaskSettled, body); err != nil {
			return nil, err
		}
		if in.Auto {
			if err := e.emit(ctx, r, parties(t), model.EventTaskAutoSettled, body); err != nil {
				return nil, err
			}
		}
		seen := map[string]struct{}{}
		for _, uid := range dedupe(scoreUsers) {
			if err := e.recordScore(ctx, r, uid, t.ID, scoreReasonSettle, integrityOnSettle, abilityOnSettle); err != nil {
				return nil, err
			}
			seen[uid] = struct{}{}
		}
		if _, ok := seen[t.PublisherID]; !ok {
			if err := e.recordScore(ctx, r, t.PublisherID, t.ID, scoreReasonSettle, integrityOnSettle, 0); err != nil {
				return nil, err
			}
		}
	}
	return dto, nil
}

func pickMilestone(t model.Task, milestoneID string, auto bool) (int, string, error) {
	if milestoneID == "" {
		var ids []string
		idx := -1
		for i, m := range t.Payment.Milestones {
			if m.Status == model.MilestoneSubmitted {
				ids = append(ids, m.ID)
				idx = i
			}
		}
		if len(ids) != 1 {
			if auto {
				return 0, "", errAutoDeferred
			}
			if len(ids) == 0 {
				return 0, "", biz.Invalid("milestone_id is required")
			}
			return 0, "", biz.Invalid("milestone_id is required when multiple milestones are in review: " + strings.Join(ids, ", "))
		}
		return idx, ids[0], nil
	}
	idx := milestoneIndex(t, milestoneID)
	if idx < 0 {
		return 0, "", biz.Invalid("unknown milestone_id")
	}
	if t.Payment.Milestones[idx].Status != model.MilestoneSubmitted {
		return 0, "", biz.Conflict("milestone is not awaiting confirmation")
	}
	return idx, milestoneID, nil
}

func chooseSubmission(t model.Task, relevant []model.Submission, submissionID string, auto bool) (*model.Submission, error) {
	if t.Kind == model.KindCompetitive && submissionID == "" {
		if len(relevant) != 1 {
			if auto {
				return nil, errAutoDeferred
			}
			ids := make([]string, len(relevant))
			for i, s := range relevant {
				ids[i] = s.ID
			}
			return nil, biz.Invalid("submission_id is required when multiple submissions exist: " + strings.Join(ids, ", "))
		}
		submissionID = relevant[0].ID
	}
	if submissionID != "" {
		for i := range relevant {
			if relevant[i].ID == submissionID {
				return &relevant[i], nil
			}
		}
		return nil, biz.Invalid("submission_id does not match a submission on this task")
	}
	if len(relevant) == 1 {
		return &relevant[0], nil
	}
	return &relevant[0], nil
}

func (e *Engine) planPayout(t model.Task, pool int64, chosen *model.Submission) ([]payout, int64, []string, error) {
	switch t.Kind {
	case model.KindExclusive:
		if len(t.ExecutorIDs) != 1 {
			return nil, 0, nil, biz.Conflict("exclusive task has no single executor")
		}
		uid := t.ExecutorIDs[0]
		return []payout{{UserID: uid, Amount: pool}}, 0, []string{uid}, nil
	case model.KindCompetitive:
		if chosen == nil {
			return nil, 0, nil, biz.Invalid("submission_id is required")
		}
		if !contains(t.ExecutorIDs, chosen.ExecutorID) {
			return nil, 0, nil, biz.Conflict("winning submission has no executor")
		}
		// Locked rule: competitive.settlement_rule = winner_take_all.
		return []payout{{UserID: chosen.ExecutorID, Amount: pool}}, 0, []string{chosen.ExecutorID}, nil
	case model.KindCollaborative:
		filled := filledRoles(t)
		if len(filled) == 0 {
			return nil, 0, nil, biz.Conflict("no filled seats to settle")
		}
		pays, refund, err := splitBps(pool, filled)
		if err != nil {
			return nil, 0, nil, err
		}
		users := make([]string, 0, len(filled))
		for _, role := range filled {
			users = append(users, role.AssigneeUserID)
		}
		return pays, refund, users, nil
	default:
		return nil, 0, nil, biz.Conflict("unknown task kind")
	}
}
