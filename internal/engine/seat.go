package engine

import (
	"context"
	"net/http"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"
)

// RemoveSeat lets the publisher drop an empty collaborative seat. The seat is
// a tombstone in Collaborative.Roles (status=removed) rather than a deleted
// row: payout math derives every seat's amount from all original share_bps
// (they must sum to 10000), so physically removing one would break later
// confirm/refund/dispute calculations and could overdraw escrow. The tombstone
// refunds that seat's held share to the publisher, drops its invites, and is
// hidden from the API.
func (e *Engine) RemoveSeat(ctx context.Context, userID string, req *types.RemoveSeatReq) (*types.Task, error) {
	return call(e, ctx, userID, http.MethodPost, taskPath(req.TaskId, "seats/"+req.RoleId+"/remove"), req.IdempotencyKey, req,
		func(ctx context.Context, r store.Repos) (*types.Task, error) {
			t, err := loadTask(ctx, r, req.TaskId)
			if err != nil {
				return nil, err
			}
			if t.PublisherID != userID {
				return nil, biz.Forbidden("only the publisher can remove a seat")
			}
			if t.Kind != model.KindCollaborative || t.Collaborative == nil {
				return nil, biz.Conflict("seat removal is only valid for collaborative tasks")
			}
			if t.Status != model.StatusOpen && t.Status != model.StatusActive {
				return nil, biz.Conflict("seat removal is only valid while the task is open or active")
			}
			roleID := trim(req.RoleId)
			if roleID == "" {
				return nil, biz.Invalid("role_id is required")
			}
			idx := seatIndex(t, roleID)
			if idx < 0 {
				return nil, biz.Invalid("unknown role_id")
			}
			role := &t.Collaborative.Roles[idx]
			// Only genuinely empty seats can be removed. Filled seats have an
			// executor; submitted/disputed/settled seats have money or work in
			// flight and must go through confirm/dispute/cancel instead.
			if role.Status != model.RoleOpen && role.Status != model.RoleDropped && role.Status != "" {
				return nil, biz.Conflict("only an empty seat can be removed")
			}
			if role.AssigneeUserID != "" {
				return nil, biz.Conflict("only an empty seat can be removed")
			}
			// Keep at least one live seat. Removing the last one would leave a
			// task that can never accept work; use cancel to close it instead.
			live := 0
			for _, r := range t.Collaborative.Roles {
				if r.Status != model.RoleRemoved {
					live++
				}
			}
			if live <= 1 {
				return nil, biz.Conflict("cannot remove the last seat; cancel the task instead")
			}
			if t.EscrowID == "" {
				return nil, biz.Conflict("task has no escrow")
			}
			acc, err := r.Escrow().LockByTask(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			if acc.Frozen > 0 && t.DisputeRoleID == "" {
				return nil, biz.Conflict("escrow is frozen")
			}
			amount, err := seatPayoutAmount(t.Payment.Amount, t.Collaborative.Roles, role.ID)
			if err != nil {
				return nil, err
			}
			if amount > spendable(acc, t) {
				return nil, biz.Conflict("escrow hold is below the seat share")
			}
			now := e.now()
			var refund *model.EscrowEntry
			if amount > 0 {
				ent, err := e.creditEscrow(ctx, r, &acc, t.PublisherID, amount, model.EscrowRefund, now)
				if err != nil {
					return nil, err
				}
				refund = &ent
			}
			if err := r.Invites().DeleteByRole(ctx, t.ID, role.ID); err != nil {
				return nil, err
			}
			role.Status = model.RoleRemoved
			role.AssigneeUserID = ""
			role.OpenedAt = nil
			if err := saveEscrow(ctx, r, acc); err != nil {
				return nil, err
			}
			e.syncCollaborativeProgress(&t)
			// A per-seat task with every seat removed/refunded is terminal.
			if isPerSeatCollab(t) && perSeatTaskStatus(t) == model.StatusSettled {
				t.Status = model.StatusCancelled
				t.CancelReason = "all seats removed"
				t.CancelledAt = &now
				clearReview(&t)
				t.StartDeadline = nil
			}
			t.UpdatedAt = now
			if err := r.Tasks().Update(ctx, t); err != nil {
				return nil, err
			}
			dto, err := e.dto(ctx, r, t)
			if err != nil {
				return nil, err
			}
			if refund != nil {
				if err := e.emit(ctx, r, parties(t), model.EventEscrowRefund, map[string]any{"task": dto, "entry": toEntry(*refund)}); err != nil {
					return nil, err
				}
			}
			if err := e.emit(ctx, r, parties(t), model.EventTaskSeatRemoved, map[string]any{"task": dto, "role_id": role.ID}); err != nil {
				return nil, err
			}
			return dto, nil
		})
}
