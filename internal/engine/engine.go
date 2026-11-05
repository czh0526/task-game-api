// Package engine implements the task state machine, simulated escrow, and webhook enqueue.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/store"
	"taskgame/internal/types"

	"github.com/zeromicro/go-zero/core/logx"
)

const (
	abilityOnSettle    int64 = 10
	integrityOnSettle  int64 = 1
	scoreReasonSettle        = "task_settled"
	scoreReasonDropout       = "collaborative_dropout"
	defaultReviewHours       = 72
	maxReviewHours           = 24 * 90
	maxMoney           int64 = 900_000_000_000_000
	scoreRecentLimit         = 20
)

// errAutoDeferred means the 72h hook will not guess an outcome.
var errAutoDeferred = errors.New("auto-settle deferred")

// Engine is safe for concurrent use. Mutations run inside store transactions.
type Engine struct {
	store    store.Store
	now      func() time.Time
	currency string
	deferred sync.Map
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock overrides the clock. Tests use this to fire the auto-settle hook.
func WithClock(now func() time.Time) Option {
	return func(e *Engine) {
		if now != nil {
			e.now = now
		}
	}
}

// New builds an engine. currency empty means credits.
func New(st store.Store, currency string, opts ...Option) *Engine {
	if currency == "" {
		currency = model.CurrencyCredits
	}
	e := &Engine{
		store:    st,
		now:      time.Now,
		currency: currency,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func taskPath(id, action string) string {
	p := "/v1/tasks/" + id
	if action != "" {
		p += "/" + action
	}
	return p
}

func (e *Engine) emit(ctx context.Context, r store.Repos, owners []string, eventType string, data any) error {
	owners = dedupe(owners)
	if len(owners) == 0 {
		return nil
	}
	hooks, err := r.Webhooks().ListByOwners(ctx, owners)
	if err != nil {
		return err
	}
	var matched []model.Webhook
	for _, h := range hooks {
		if contains(h.Events, eventType) {
			matched = append(matched, h)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	now := e.now()
	env := map[string]any{
		"id":         id.New("evt_"),
		"type":       eventType,
		"created_at": model.FormatTime(now),
		"data":       data,
	}
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	eventID, _ := env["id"].(string)
	for _, h := range matched {
		if err := r.Deliveries().Insert(ctx, model.Delivery{
			ID:        id.New("dlv_"),
			WebhookID: h.ID,
			EventID:   eventID,
			EventType: eventType,
			Body:      string(body),
			Status:    model.DeliveryPending,
			Attempts:  0,
			CreatedAt: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) dto(ctx context.Context, r store.Repos, t model.Task) (*types.Task, error) {
	var acc *model.EscrowAccount
	if t.EscrowID != "" {
		a, err := r.Escrow().GetByTask(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		acc = &a
	}
	out := toTask(t, acc)
	return &out, nil
}

func (e *Engine) recordScore(ctx context.Context, r store.Repos, userID, taskID, reason string, dInt, dAbi int64) error {
	u, err := r.Users().Ensure(ctx, userID, e.now())
	if err != nil {
		return err
	}
	if dInt != 0 || dAbi != 0 {
		if err := r.Users().ApplyScore(ctx, userID, dInt, dAbi); err != nil {
			return err
		}
		u.Integrity += dInt
		u.Ability += dAbi
	}
	ev := model.ScoreEvent{
		ID:             id.New("sco_"),
		UserID:         userID,
		DeltaIntegrity: dInt,
		DeltaAbility:   dAbi,
		Reason:         reason,
		TaskID:         taskID,
		CreatedAt:      e.now(),
	}
	if err := r.Users().AddScoreEvent(ctx, ev); err != nil {
		return err
	}
	return e.emit(ctx, r, []string{userID}, model.EventScoreUpdated, map[string]any{
		"user_id":         u.ID,
		"integrity":       u.Integrity,
		"ability":         u.Ability,
		"delta_integrity": dInt,
		"delta_ability":   dAbi,
		"reason":          reason,
		"task_id":         taskID,
	})
}

// AutoSettleDue confirms in_review tasks whose review_deadline has passed.
// Competitive tasks with more than one submission are left in review: winner_take_all
// will not guess a winner. That path is the v0.1 placeholder for a human confirm.
func (e *Engine) AutoSettleDue(ctx context.Context) (int, error) {
	ids, err := e.store.Tasks().ListDueReview(ctx, e.now(), 50)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, taskID := range ids {
		err := e.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
			_, err := e.confirm(ctx, r, confirmInput{TaskID: taskID, Auto: true})
			return err
		})
		switch {
		case err == nil:
			n++
		case errors.Is(err, errAutoDeferred):
			if _, loaded := e.deferred.LoadOrStore(taskID, true); !loaded {
				logx.Infof("auto-settle deferred %s: left in review (winner_take_all will not guess a winner)", taskID)
			}
		default:
			logx.Errorf("auto-settle %s: %v", taskID, err)
		}
	}
	return n, nil
}
