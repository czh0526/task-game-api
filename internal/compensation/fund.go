package compensation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/store"
)

// FundPayload carries the data needed to replay or undo a fund hold. The id
// field is the escrow id the forward step created; it is filled in before the
// saga begins when the domain split is planned.
type FundPayload struct {
	UserID       string `json:"user_id"`
	TaskID       string `json:"task_id"`
	Amount       int64  `json:"amount"`
	EscrowID     string `json:"escrow_id"`
	Currency     string `json:"currency"`
	EntryID      string `json:"entry_id"`
	CreatedAtUTC string `json:"created_at"`
}

// FundHoldHandler implements the cross-domain "deduct balance + hold escrow"
// saga. Each effect runs in its OWN store transaction so it can target a
// different physical database when user and task domains are split.
//
// Idempotency:
//   - Forward checks whether the escrow account already exists before writing
//     it or deducting balance, so a replay after a crash is a no-op.
//   - Compensate credits the balance back and marks the escrow released only
//     if the hold is still present; the saga's terminal state prevents any
//     second compensation from the coordinator.
type FundHoldHandler struct {
	store store.Store
}

func NewFundHoldHandler(st store.Store) *FundHoldHandler {
	return &FundHoldHandler{store: st}
}

func decodeFundPayload(rec Record) (FundPayload, error) {
	var p FundPayload
	if err := json.Unmarshal(rec.Payload, &p); err != nil {
		return p, fmt.Errorf("compensation: fund payload: %w", err)
	}
	if p.UserID == "" || p.TaskID == "" || p.EscrowID == "" {
		return p, fmt.Errorf("compensation: fund payload incomplete: %+v", p)
	}
	return p, nil
}

// resolveAmount returns the hold amount, reading it from the task if the
// payload did not carry one (the engine records the saga before loading the
// task in its transaction).
func (h *FundHoldHandler) resolveAmount(ctx context.Context, p FundPayload) (int64, error) {
	if p.Amount > 0 {
		return p.Amount, nil
	}
	var amount int64
	err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		t, err := r.Tasks().Get(ctx, p.TaskID)
		if err != nil {
			return err
		}
		amount = t.Payment.Amount
		return nil
	})
	return amount, err
}

func (h *FundHoldHandler) Forward(ctx context.Context, rec Record) error {
	p, err := decodeFundPayload(rec)
	if err != nil {
		return err
	}
	amount, err := h.resolveAmount(ctx, p)
	if err != nil {
		return fmt.Errorf("compensation: resolve amount: %w", err)
	}
	// Check the task-domain effect first: if the hold exists, the forward step
	// already committed (possibly on a previous attempt).
	exists := false
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		acc, err := r.Escrow().GetByTask(ctx, p.TaskID)
		if err == nil && acc.ID == p.EscrowID {
			exists = true
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if exists {
		return nil
	}

	// Step 1: deduct the publisher balance on the user domain.
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		if _, err := r.Users().Ensure(ctx, p.UserID, parseCreated(p.CreatedAtUTC)); err != nil {
			return err
		}
		return r.Users().AddBalance(ctx, p.UserID, -amount)
	}); err != nil {
		return fmt.Errorf("compensation: deduct balance: %w", err)
	}

	// Step 2: create the escrow hold on the task domain. If this fails the
	// coordinator marks the saga compensating and calls Compensate below.
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		if err := r.Escrow().Insert(ctx, model.EscrowAccount{
			ID: p.EscrowID, TaskID: p.TaskID, Held: amount, Currency: p.Currency,
		}); err != nil {
			return err
		}
		return r.Escrow().InsertEntry(ctx, model.EscrowEntry{
			ID: p.EntryID, EscrowID: p.EscrowID, Type: model.EscrowHold,
			Amount: amount, CreatedAt: parseCreated(p.CreatedAtUTC),
		})
	}); err != nil {
		return fmt.Errorf("compensation: escrow hold: %w", err)
	}
	return nil
}

func (h *FundHoldHandler) Compensate(ctx context.Context, rec Record) error {
	p, err := decodeFundPayload(rec)
	if err != nil {
		return err
	}
	// The escrow account is the source of truth for how much to refund. If it
	// is absent or already released, no money is held and compensation is a
	// no-op, which makes repeated Compensate calls safe.
	var refund int64
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		acc, err := r.Escrow().GetByTask(ctx, p.TaskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if acc.ID == p.EscrowID && acc.Held > 0 {
			refund = acc.Held
		}
		return nil
	}); err != nil {
		return err
	}
	if refund == 0 {
		return nil
	}

	// Refund on the user domain.
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		return r.Users().AddBalance(ctx, p.UserID, refund)
	}); err != nil {
		return fmt.Errorf("compensation: refund balance: %w", err)
	}
	// Zero the hold on the task domain so a replay sees no held money.
	if err := h.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		acc, err := r.Escrow().GetByTask(ctx, p.TaskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		if acc.ID != p.EscrowID || acc.Held == 0 {
			return nil
		}
		acc.Held = 0
		return r.Escrow().Update(ctx, acc)
	}); err != nil {
		return fmt.Errorf("compensation: release hold: %w", err)
	}
	return nil
}

func parseCreated(s string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t
	}
	return time.Unix(0, 0).UTC()
}
