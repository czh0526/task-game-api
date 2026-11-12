package engine

import (
	"context"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"

	"github.com/zeromicro/go-zero/core/logx"
)

// sweepTimeouts closes recruiting, unstarted unified tasks, idle executors, open per-seat
// shares, and competitive reviews whose winner was never chosen.
func (e *Engine) sweepTimeouts(ctx context.Context) (int, error) {
	rows, err := e.store.Tasks().List(ctx, model.TaskFilter{
		Statuses: []string{model.StatusOpen, model.StatusActive, model.StatusInReview},
		Limit:    50,
		Offset:   0,
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, row := range rows {
		changed := false
		err := e.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
			t, err := r.Tasks().Get(ctx, row.ID)
			if err != nil {
				return err
			}
			ok, err := e.applyTimeout(ctx, r, t)
			changed = ok
			return err
		})
		if err != nil {
			logx.Errorf("timeout %s: %v", row.ID, err)
			continue
		}
		if changed {
			n++
		}
	}
	return n, nil
}

func (e *Engine) applyTimeout(ctx context.Context, r store.Repos, t model.Task) (bool, error) {
	if t.Status == model.StatusDisputed || (t.DisputeRoleID != "" && t.Status == model.StatusDisputed) {
		return false, nil
	}
	now := e.now()
	if isPerSeatCollab(t) && (t.Status == model.StatusOpen || t.Status == model.StatusActive || t.Status == model.StatusInReview) {
		return e.timeoutOpenSeats(ctx, r, t, now)
	}
	if t.Status == model.StatusOpen && isUnifiedCollab(t) && t.StartDeadline != nil && !now.Before(*t.StartDeadline) && unifiedReadyToStart(t) {
		if _, err := e.cancelRefundAll(ctx, r, t, model.CancelStartTimeout, model.CancelStartTimeout); err != nil {
			return false, err
		}
		return true, nil
	}
	if t.Status == model.StatusOpen && !isPerSeatCollab(t) {
		if deadline := e.openDeadline(t); deadline != nil && !now.Before(*deadline) {
			if isUnifiedCollab(t) && !hasOpenSeat(t) {
				// Fully seated unified tasks wait on start_deadline instead.
			} else {
				if _, err := e.cancelRefundAll(ctx, r, t, model.CancelOpenTimeout, model.CancelOpenTimeout); err != nil {
					return false, err
				}
				return true, nil
			}
		}
	}
	subs, err := r.Submissions().ListByTask(ctx, t.ID)
	if err != nil {
		return false, err
	}
	current := currentSubmissions(subs)
	if t.Status == model.StatusActive && len(current) == 0 && !t.Acceptance.Deadline.IsZero() && !now.Before(t.Acceptance.Deadline) {
		if _, err := e.cancelRefundAll(ctx, r, t, model.CancelOpenTimeout, model.CancelOpenTimeout); err != nil {
			return false, err
		}
		return true, nil
	}
	if t.Kind == model.KindCompetitive && t.Status == model.StatusInReview && len(current) > 1 && t.WinningSubmissionID == nil {
		if t.WinnerSelectDeadline == nil && t.ReviewDeadline != nil && !now.Before(*t.ReviewDeadline) {
			deadline := t.ReviewDeadline.Add(hoursDuration(e.winnerSelectHours()))
			t.WinnerSelectDeadline = &deadline
			t.UpdatedAt = now
			if err := r.Tasks().Update(ctx, t); err != nil {
				return false, err
			}
			return false, nil
		}
		if t.WinnerSelectDeadline != nil && !now.Before(*t.WinnerSelectDeadline) {
			if _, err := e.cancelRefundAll(ctx, r, t, model.CancelWinnerSelectTimeout, model.CancelWinnerSelectTimeout); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (e *Engine) timeoutOpenSeats(ctx context.Context, r store.Repos, t model.Task, now time.Time) (bool, error) {
	if t.Collaborative == nil || t.EscrowID == "" {
		return false, nil
	}
	if t.DisputeRoleID == "" && t.Status == model.StatusDisputed {
		return false, nil
	}
	acc, err := r.Escrow().GetByTask(ctx, t.ID)
	if err != nil {
		return false, err
	}
	if acc.Frozen > 0 && t.DisputeRoleID == "" {
		return false, nil
	}
	var due []int
	for i, role := range t.Collaborative.Roles {
		if role.Status != model.RoleOpen && role.Status != model.RoleDropped && role.Status != "" {
			continue
		}
		deadline := e.seatRefundDeadline(t, role)
		if deadline == nil || now.Before(*deadline) {
			continue
		}
		due = append(due, i)
	}
	if len(due) == 0 {
		return false, nil
	}
	var refunds []model.EscrowEntry
	for _, idx := range due {
		role := &t.Collaborative.Roles[idx]
		amount, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, role.ID)
		if err != nil {
			return false, err
		}
		if amount > spendable(acc, t) {
			return false, err
		}
		if amount > 0 {
			ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, amount, model.EscrowRefund, now)
			if err != nil {
				return false, err
			}
			refunds = append(refunds, ent)
		}
		role.Status = model.RoleRefunded
	}
	if err := r.Escrow().Update(ctx, acc); err != nil {
		return false, err
	}
	e.syncCollaborativeProgress(&t)
	t.UpdatedAt = now
	if err := r.Tasks().Update(ctx, t); err != nil {
		return false, err
	}
	dto, err := e.dto(ctx, r, t)
	if err != nil {
		return false, err
	}
	for _, ent := range refunds {
		if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{
			"task": dto, "entry": toEntry(ent), "reason": model.CancelSeatTimeout,
		}); err != nil {
			return false, err
		}
	}
	if t.Status == model.StatusSettled {
		if err := e.emit(ctx, r, parties(t), model.EventTaskSettled, map[string]any{
			"task": dto, "reason": model.CancelSeatTimeout,
		}); err != nil {
			return false, err
		}
		if err := e.scorePublisherIfNeeded(ctx, r, t, nil); err != nil {
			return false, err
		}
	}
	return true, nil
}
