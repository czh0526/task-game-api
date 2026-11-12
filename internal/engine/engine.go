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
	abilityOnSettle     int64 = 10
	integrityOnSettle   int64 = 1
	scoreReasonSettle         = "task_settled"
	scoreReasonDropout        = "collaborative_dropout"
	defaultReviewHours        = 72
	defaultMaxRevisions       = 3
	maxReviewHours            = 24 * 90
	maxMoney            int64 = 900_000_000_000_000
	scoreRecentLimit          = 20
)

// TimeoutConfig is how long money can sit in each unfinished state.
// Zero values mean 72 hours, matching the review window.
type TimeoutConfig struct {
	OpenTimeout       time.Duration
	StartTimeout      time.Duration
	SeatTimeout       time.Duration
	WinnerSelectHours int
}

// errAutoDeferred means the 72h hook will not guess an outcome.
var errAutoDeferred = errors.New("auto-settle deferred")

// Engine is safe for concurrent use. Mutations run inside store transactions.
type Engine struct {
	store    store.Store
	now      func() time.Time
	currency string
	deferred sync.Map
	timeouts TimeoutConfig
	arbiters map[string]struct{}
}

// Option configures an Engine.
type Option func(*Engine)

// WithTimeouts sets recruiting, start, seat, and winner-select limits.
func WithTimeouts(cfg TimeoutConfig) Option {
	return func(e *Engine) {
		e.timeouts = cfg
	}
}

// WithArbiters sets the user ids allowed to call arbitrate.
func WithArbiters(ids []string) Option {
	return func(e *Engine) {
		e.arbiters = map[string]struct{}{}
		for _, id := range ids {
			if id != "" {
				e.arbiters[id] = struct{}{}
			}
		}
	}
}

// IsArbiter reports whether userID is configured as an escrow arbiter.
func (e *Engine) IsArbiter(userID string) bool {
	if e == nil || userID == "" {
		return false
	}
	_, ok := e.arbiters[userID]
	return ok
}

func (e *Engine) openTimeout() time.Duration { return positiveOr(e.timeouts.OpenTimeout, 72*time.Hour) }
func (e *Engine) startTimeout() time.Duration {
	return positiveOr(e.timeouts.StartTimeout, 72*time.Hour)
}
func (e *Engine) seatTimeout() time.Duration { return positiveOr(e.timeouts.SeatTimeout, 72*time.Hour) }

func (e *Engine) winnerSelectHours() int {
	if e.timeouts.WinnerSelectHours <= 0 {
		return defaultReviewHours
	}
	return e.timeouts.WinnerSelectHours
}

func positiveOr(d, fallback time.Duration) time.Duration {
	if d <= 0 {
		return fallback
	}
	return d
}

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
	subs, err := r.Submissions().ListByTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	out := e.toTask(t, acc, subs)
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

// AutoSettleDue confirms in_review tasks whose review_deadline has passed,
// then runs recruiting, start, seat, and winner-select timeouts.
// Competitive tasks with more than one current submission are not paid here:
// winner_take_all will not guess a winner. A later winner_select_deadline
// refunds that pool if the publisher still has not chosen.
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
	timed, err := e.sweepTimeouts(ctx)
	if err != nil {
		return n, err
	}
	return n + timed, nil
}
