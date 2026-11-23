package compensation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Handler executes the forward effect of a saga kind and, if the caller says
// the forward effect did not complete, compensates it. Both operations MUST be
// idempotent: the reconciler may replay either after a crash.
type Handler interface {
	// Forward performs the cross-domain work. Returning an error means the
	// saga must be compensated.
	Forward(ctx context.Context, rec Record) error
	// Compensate undoes a partially completed forward effect.
	Compensate(ctx context.Context, rec Record) error
}

// Coordinator drives sagas. It is safe for concurrent use and for multiple
// processes sharing the same Log, because all claiming goes through Log.Claim.
type Coordinator struct {
	log      Log
	handlers map[string]Handler
	owner    string
	leaseTTL time.Duration
	maxRetry int
	now      func() time.Time
}

func NewCoordinator(log Log, owner string, opts ...Option) *Coordinator {
	c := &Coordinator{
		log:      log,
		handlers: make(map[string]Handler),
		owner:    owner,
		leaseTTL: 30 * time.Second,
		maxRetry: 5,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Option func(*Coordinator)

func WithLeaseTTL(d time.Duration) Option {
	return func(c *Coordinator) {
		if d > 0 {
			c.leaseTTL = d
		}
	}
}

func WithMaxRetries(n int) Option {
	return func(c *Coordinator) {
		if n > 0 {
			c.maxRetry = n
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(c *Coordinator) {
		if now != nil {
			c.now = now
		}
	}
}

func WithHandler(kind string, h Handler) Option {
	return func(c *Coordinator) {
		c.handlers[kind] = h
	}
}

// RegisterHandler attaches a handler after construction. The service graph
// builds the coordinator before the store is available, so the fund handler
// is registered here rather than via an Option.
func (c *Coordinator) RegisterHandler(kind string, h Handler) *Coordinator {
	c.handlers[kind] = h
	return c
}

// Begin records a new saga. It is idempotent: replaying with the same id
// returns the original record rather than overwriting it.
func (c *Coordinator) Begin(ctx context.Context, rec Record) error {
	if err := c.log.Insert(ctx, rec); err != nil && err != ErrConflict {
		return err
	}
	return nil
}

// Complete marks a saga successfully finished. Used by a request path that
// performed the effect itself and only needs the durable journal closed.
func (c *Coordinator) Complete(ctx context.Context, id string) error {
	return c.log.MarkDone(ctx, id)
}

// Abort marks a saga as needing compensation. The background reconciler will
// call the registered handler's Compensate until it succeeds.
func (c *Coordinator) Abort(ctx context.Context, id, errText string) error {
	return c.log.MarkCompensating(ctx, id, errText)
}

// Execute runs a saga synchronously: forward, or forward-then-compensate on
// failure. Used by the request path when the caller needs the result now.
// Replaying an already-finished saga is a no-op, so callers can safely retry.
func (c *Coordinator) Execute(ctx context.Context, rec Record) error {
	h, ok := c.handlers[rec.Kind]
	if !ok {
		return fmt.Errorf("compensation: no handler for %q", rec.Kind)
	}
	if err := c.Begin(ctx, rec); err != nil && err != ErrConflict {
		return err
	}
	existing, err := c.log.Get(ctx, rec.ID)
	if err != nil {
		return err
	}
	if existing.State == StateDone || existing.State == StateCompensated {
		return nil
	}
	if err := h.Forward(ctx, rec); err != nil {
		if cerr := h.Compensate(ctx, rec); cerr != nil {
			return fmt.Errorf("compensation: forward failed (%v) and compensate failed (%v)", err, cerr)
		}
		if merr := c.log.MarkCompensated(ctx, rec.ID); merr != nil {
			return merr
		}
		return err
	}
	return c.log.MarkDone(ctx, rec.ID)
}

// ReconcileOnce claims and drives at most limit unfinished sagas. A saga that
// exhausts its retries is left compensating with its last error recorded,
// never silently dropped. Returns the number of sagas processed.
func (c *Coordinator) ReconcileOnce(ctx context.Context, limit int) (int, error) {
	claimed, err := c.log.Claim(ctx, c.now(), c.owner, c.leaseTTL, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, rec := range claimed {
		h, ok := c.handlers[rec.Kind]
		if !ok {
			_ = c.log.Fail(ctx, rec.ID, c.owner, rec.Attempts+1, "no handler for kind "+rec.Kind)
			processed++
			continue
		}
		switch rec.State {
		case StatePending:
			if err := h.Forward(ctx, rec); err != nil {
				_ = c.log.MarkCompensating(ctx, rec.ID, err.Error())
				processed++
				continue
			}
			if err := c.log.MarkDone(ctx, rec.ID); err != nil {
				return processed, err
			}
		case StateCompensating:
			if err := h.Compensate(ctx, rec); err != nil {
				attempts := rec.Attempts + 1
				if attempts >= c.maxRetry {
					// Give up active retries but keep the saga in a terminal
					// visible state so an operator can intervene.
					_ = c.log.Fail(ctx, rec.ID, c.owner, attempts, err.Error())
				} else {
					_ = c.log.Fail(ctx, rec.ID, c.owner, attempts, err.Error())
				}
				processed++
				continue
			}
			if err := c.log.MarkCompensated(ctx, rec.ID); err != nil {
				return processed, err
			}
		}
		processed++
	}
	return processed, nil
}

// MarshalPayload is a helper for building saga payloads.
func MarshalPayload(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
