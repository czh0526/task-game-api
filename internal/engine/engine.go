// Package engine implements the task state machine, simulated escrow, and webhook enqueue.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/compensation"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/search"
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

// errNoDue ends one auto-settle pass when no in_review task is waiting.
var errNoDue = errors.New("no due review")

// Engine is safe for concurrent use. Mutations run inside store transactions.
type Engine struct {
	store       store.Store
	now         func() time.Time
	currency    string
	deferred    sync.Map
	timeouts    TimeoutConfig
	arbiters    map[string]struct{}
	notifier    SignalNotifier
	comp        *compensation.Coordinator // optional cross-domain saga driver
	cdc         TaskChangeRecorder        // optional search-index change recorder
	taskIndex   search.TaskIndex          // optional read-side index for simple listings
	cursorEpoch CursorEpochSource         // optional per-chat generation for cursor invalidation
	bodyStore   EventBodyStore            // optional shared store for webhook event bodies
}

// EventBodyStore stores one copy of a webhook event body so N deliveries keep
// only a small reference. archive.BlobStore satisfies it.
type EventBodyStore interface {
	Put(ctx context.Context, key string, data []byte) (ref string, err error)
	Get(ctx context.Context, ref string) ([]byte, error)
}

// WithEventBodyStore enables de-amplified deliveries: one body blob per event,
// N delivery rows holding only its reference.
func WithEventBodyStore(s EventBodyStore) Option {
	return func(e *Engine) { e.bodyStore = s }
}

// CursorEpochSource reports a chat's cursor generation. A physical cutover
// bumps it so outstanding rowid/offset cursors are detected as stale.
type CursorEpochSource interface {
	Epoch(ctx context.Context, chatID string) (int64, error)
}

// TaskChangeRecorder records a task upsert after the business transaction has
// committed. It is intentionally best effort: the search index is a read
// model, and a full Rebuild can always repair missed changes.
type TaskChangeRecorder interface {
	RecordTask(ctx context.Context, doc search.TaskDoc) error
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

// WithCompensation attaches the cross-domain saga driver. When set (and the
// user/task domains are physically split), FundTask records the fund hold as
// a saga so a crash between the balance deduction and the escrow insert can
// be replayed or compensated by the background reconciler.
func WithCompensation(c *compensation.Coordinator) Option {
	return func(e *Engine) { e.comp = c }
}

// WithTaskChangeRecorder attaches the search-index CDC recorder. When absent,
// task writes do not touch the index (the legacy path).
func WithTaskChangeRecorder(r TaskChangeRecorder) Option {
	return func(e *Engine) { e.cdc = r }
}

// WithTaskIndex attaches a read-side index. When present, simple "latest
// tasks" listings are answered from the index (one query) instead of
// scattering to every shard; complex filters still use the store.
func WithTaskIndex(idx search.TaskIndex) Option {
	return func(e *Engine) { e.taskIndex = idx }
}

// WithCursorEpoch attaches a per-chat cursor generation source. After a
// physical message cutover bumps a chat's epoch, ListMessages treats cursors
// minted under the old epoch as stale and restarts from the first page.
func WithCursorEpoch(src CursorEpochSource) Option {
	return func(e *Engine) { e.cursorEpoch = src }
}

// recordTaskDTO is called after a task write commits (from call). Failures are
// best effort: a periodic Rebuild repairs any missed upsert. Recording after
// commit—rather than inside the transaction—avoids ghost index rows when the
// business transaction rolls back.
func (e *Engine) recordTaskDTO(ctx context.Context, t *types.Task) {
	if e.cdc == nil || t == nil || t.Id == "" {
		return
	}
	doc := search.TaskDoc{
		ID: t.Id, Title: t.Title, Description: t.Description,
		Keywords: append([]string{}, t.Keywords...),
		Kind:     t.Kind, Status: t.Status, CreatedAt: t.CreatedAt,
	}
	_ = e.cdc.RecordTask(ctx, doc)
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

func saveEscrow(ctx context.Context, r store.Repos, acc model.EscrowAccount) error {
	err := r.Escrow().Update(ctx, acc)
	if errors.Is(err, store.ErrConflict) {
		return biz.Conflict("escrow changed concurrently")
	}
	return err
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
	// De-amplification: store the body once and let each delivery hold only a
	// small content-addressed reference. The blob write happens before any
	// delivery row, so a ref is never persisted without its body. Without a
	// store, fall back to the legacy one-full-body-per-delivery behaviour.
	bodyRef := ""
	if e.bodyStore != nil {
		ref, err := e.bodyStore.Put(ctx, "webhook-event:"+eventID, body)
		if err != nil {
			return err
		}
		bodyRef = ref
	}
	for _, h := range matched {
		deliveryBody := string(body)
		if bodyRef != "" {
			deliveryBody = ""
		}
		if err := r.Deliveries().Insert(ctx, model.Delivery{
			ID:        id.New("dlv_"),
			WebhookID: h.ID,
			EventID:   eventID,
			EventType: eventType,
			Body:      deliveryBody,
			BodyRef:   bodyRef,
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
	n := 0
	var skip []string
	for {
		var taskID string
		err := e.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
			id, err := r.Tasks().ClaimDueReview(ctx, e.now(), skip)
			if err != nil {
				return err
			}
			if id == "" {
				return errNoDue
			}
			taskID = id
			_, err = e.confirm(ctx, r, confirmInput{TaskID: id, Auto: true})
			return err
		})
		switch {
		case err == nil:
			n++
		case errors.Is(err, errNoDue):
			timed, terr := e.sweepTimeouts(ctx)
			if terr != nil {
				return n, terr
			}
			return n + timed, nil
		case errors.Is(err, errAutoDeferred):
			if taskID != "" {
				skip = append(skip, taskID)
			}
			if _, loaded := e.deferred.LoadOrStore(taskID, true); !loaded {
				logx.Infof("auto-settle deferred %s: left in review (winner_take_all will not guess a winner)", taskID)
			}
		default:
			logx.Errorf("auto-settle %s: %v", taskID, err)
			if taskID != "" {
				skip = append(skip, taskID)
			}
		}
		if len(skip) > 10000 {
			break
		}
	}
	timed, err := e.sweepTimeouts(ctx)
	if err != nil {
		return n, err
	}
	return n + timed, nil
}
