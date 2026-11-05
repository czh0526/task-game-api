package svc

import (
	"context"
	"fmt"
	"time"

	"taskgame/internal/config"
	"taskgame/internal/engine"
	"taskgame/internal/id"
	"taskgame/internal/middleware"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"
	"taskgame/internal/webhook"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
)

// ServiceContext is the dependency graph shared by handlers.
type ServiceContext struct {
	Config     config.Config
	Auth       rest.Middleware
	Engine     *engine.Engine
	Store      store.Store
	Dispatcher *webhook.Dispatcher
}

// NewServiceContext opens the store and wires the engine, auth, and webhook worker.
func NewServiceContext(c config.Config) (*ServiceContext, error) {
	if len(c.Auth.JwtSecret) < 16 {
		return nil, fmt.Errorf("Auth.JwtSecret must be at least 16 characters")
	}
	seen := make(map[string]struct{}, len(c.Auth.ApiKeys))
	for _, key := range c.Auth.ApiKeys {
		if key.Key == "" || !id.ValidUserID(key.UserId) {
			return nil, fmt.Errorf("Auth.ApiKeys entry needs a key and a user id")
		}
		if _, ok := seen[key.Key]; ok {
			return nil, fmt.Errorf("duplicate api key")
		}
		seen[key.Key] = struct{}{}
	}
	if c.Store.Driver != "sqlite" {
		return nil, fmt.Errorf("unsupported store driver %q", c.Store.Driver)
	}
	if c.Store.DSN == "" {
		return nil, fmt.Errorf("Store.DSN is required")
	}
	st, err := sqlite.Open(c.Store.DSN, c.Escrow.InitialBalance)
	if err != nil {
		return nil, err
	}
	return &ServiceContext{
		Config: c,
		Auth:   middleware.NewAuthMiddleware(c.Auth).Handle,
		Engine: engine.New(st, c.Escrow.Currency),
		Store:  st,
		Dispatcher: webhook.NewDispatcher(st, webhook.Config{
			Mode:        c.Webhook.Mode,
			Timeout:     c.Webhook.Timeout,
			MaxAttempts: c.Webhook.MaxAttempts,
		}),
	}, nil
}

// Close releases the store.
func (sc *ServiceContext) Close() error {
	if sc == nil || sc.Store == nil {
		return nil
	}
	return sc.Store.Close()
}

// StartBackground runs auto-settle and webhook delivery until ctx is cancelled.
func (sc *ServiceContext) StartBackground(ctx context.Context) {
	if sc.Config.AutoSettle.Enabled {
		sc.loop(ctx, sc.Config.AutoSettle.Interval, 30*time.Second, "auto-settle", func() error {
			_, err := sc.Engine.AutoSettleDue(ctx)
			return err
		})
	}
	sc.loop(ctx, sc.Config.Webhook.Interval, 2*time.Second, "webhook", func() error {
		return sc.Dispatcher.DeliverPending(ctx)
	})
}

func (sc *ServiceContext) loop(ctx context.Context, every, fallback time.Duration, name string, fn func() error) {
	if every <= 0 {
		every = fallback
	}
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := fn(); err != nil && ctx.Err() == nil {
					logx.Errorf("%s: %v", name, err)
				}
			}
		}
	}()
}
