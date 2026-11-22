package projection

import (
	"context"
	"sync/atomic"
	"time"
)

// DefaultInterval is the periodic fallback scan. The event bus only makes
// recovery faster; this ticker is what guarantees it eventually happens.
const DefaultInterval = 5 * time.Second

// Projector is implemented by *sqlstore.DB (Recover scans message_outbox).
type Projector interface {
	Recover(ctx context.Context) error
}

// Worker drains pending outbox rows on every wake-up hint and on a timer.
// It is deliberately stateless: the lease lives in the store, the authoritative
// projected flag lives in the primary database.
type Worker struct {
	projector Projector
	bus       Bus
	interval  time.Duration
	wake      chan struct{}
	runs      atomic.Int64
	errs      atomic.Int64
}

func NewWorker(p Projector, b Bus, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Worker{projector: p, bus: b, interval: interval, wake: make(chan struct{}, 1)}
}

// Wake is non-blocking: a burst of hints collapses into one scan.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Runs / Errors are diagnostic counters (also handy in tests).
func (w *Worker) Runs() int64   { return w.runs.Load() }
func (w *Worker) Errors() int64 { return w.errs.Load() }

// Run blocks until ctx is cancelled. If the bus is unavailable the worker
// still keeps the periodic fallback running.
func (w *Worker) Run(ctx context.Context) {
	var stop func()
	if w.bus != nil {
		s, err := w.bus.Subscribe(ctx, func(string) { w.Wake() })
		if err == nil {
			stop = s
		}
	}
	if stop != nil {
		defer stop()
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Wake()
		case <-w.wake:
			if err := w.projector.Recover(ctx); err != nil && ctx.Err() == nil {
				w.errs.Add(1)
			}
			w.runs.Add(1)
		}
	}
}
