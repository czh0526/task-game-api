package svc

import (
	"context"
	"fmt"
	"time"

	"taskgame/internal/account"
	"taskgame/internal/config"
	"taskgame/internal/engine"
	"taskgame/internal/id"
	"taskgame/internal/middleware"
	"taskgame/internal/notify"
	"taskgame/internal/projection"
	"taskgame/internal/scale"
	"taskgame/internal/store"
	"taskgame/internal/store/mysql"
	"taskgame/internal/store/shard"
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
	Accounts   *account.Service
	Store      store.Store
	Dispatcher *webhook.Dispatcher
	// Fanout (not *notify.Broker) so a multi-process deployment can supply a
	// RemoteFanout without changing the SSE handler.
	Notifier notify.Fanout
	// Scale is nil unless the P1+ skeleton is explicitly enabled.
	Scale *scale.Runtime
	// projectionWorker is nil unless Scale.Projection is enabled.
	projectionWorker *projection.Worker
	projectionCancel context.CancelFunc
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
	if c.Store.DSN == "" {
		return nil, fmt.Errorf("Store.DSN is required")
	}
	// The scale skeleton is built before the store opens so a bad config
	// fails fast and cannot leak a connection pool.
	rt, err := scale.New(c.Scale, c.Store)
	if err != nil {
		return nil, fmt.Errorf("scale: %w", err)
	}
	st, err := openStore(c)
	if err != nil {
		if rt != nil {
			_ = rt.Close()
		}
		return nil, err
	}
	// PR #38 default: one broker per process. With Scale.Enabled and
	// PubSub=shared, the runtime supplies a RemoteFanout that also publishes
	// over a Transport for the other API processes.
	var notifier notify.Fanout = notify.New()
	if rt != nil && rt.Fanout != nil {
		notifier = rt.Fanout
	}
	sc := &ServiceContext{
		Config: c,
		Auth:   middleware.NewAuthMiddleware(c.Auth).Handle,
		Engine: engine.New(st, c.Escrow.Currency,
			engine.WithTimeouts(engine.TimeoutConfig{
				OpenTimeout:       c.AutoSettle.OpenTimeout,
				StartTimeout:      c.AutoSettle.StartTimeout,
				SeatTimeout:       c.AutoSettle.SeatTimeout,
				WinnerSelectHours: c.AutoSettle.WinnerSelectHours,
			}),
			engine.WithArbiters(c.Escrow.ArbiterUserIds),
			engine.WithSignalNotifier(notifier),
		),
		Accounts: account.New(st, c.Auth.JwtSecret, c.Auth.TokenTTL),
		Store:    st,
		Notifier: notifier,
		Scale:    rt,
		Dispatcher: webhook.NewDispatcher(st, webhook.Config{
			Mode:        c.Webhook.Mode,
			Timeout:     c.Webhook.Timeout,
			MaxAttempts: c.Webhook.MaxAttempts,
		}),
	}
	// P1 #2: attach the shared-lease coordinator to the primary DB and build
	// the worker that reacts to wake-up hints. The store type is asserted
	// narrowly so a store without sharding support stays untouched.
	if rt != nil && rt.ProjectionEnabled() {
		if db, ok := st.(interface {
			SetOutboxCoordinator(string, time.Duration, projection.Lease, projection.Bus)
			Recover(context.Context) error
		}); ok {
			db.SetOutboxCoordinator(rt.InstanceID(), rt.ProjectionLeaseTTL(), rt.Lease, rt.Bus)
			sc.projectionWorker = projection.NewWorker(db, rt.Bus, rt.ProjectionInterval())
		}
	}
	return sc, nil
}

func openStore(c config.Config) (store.Store, error) {
	var st store.Store
	var err error
	switch c.Store.Driver {
	case "", "sqlite":
		st, err = sqlite.Open(c.Store.DSN, c.Escrow.InitialBalance)
	case "mysql":
		st, err = mysql.Open(c.Store.DSN, c.Escrow.InitialBalance)
	default:
		return nil, fmt.Errorf("unsupported store driver %q", c.Store.Driver)
	}
	if err != nil {
		return nil, err
	}
	if c.Store.Shards <= 1 {
		return st, nil
	}
	scfg, err := shardConfig(c.Store)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	set, err := shard.OpenConfig(shard.NewRouter(c.Store.Shards), scfg)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	enabler, ok := st.(interface{ EnableShards(*shard.Set) error })
	if !ok {
		_ = set.Close()
		_ = st.Close()
		return nil, fmt.Errorf("store %T does not support message sharding", st)
	}
	if err := enabler.EnableShards(set); err != nil {
		_ = set.Close()
		_ = st.Close()
		return nil, err
	}
	return st, nil
}

// shardConfig builds one DSN per shard. A list is preferred: MySQL shard
// databases can live on different hosts and every API process reaches them
// over the network. A template containing %d remains as a shorthand.
func shardConfig(c config.StoreConf) (shard.Config, error) {
	driver := c.ShardDriver
	if driver == "" {
		driver = shard.DriverSQLite
	}
	if len(c.ShardDSNs) > 0 {
		return shard.Config{Driver: driver, DSNs: c.ShardDSNs}, nil
	}
	if c.ShardDSN == "" {
		return shard.Config{}, fmt.Errorf("Store.ShardDSNs or Store.ShardDSN is required when Store.Shards > 1")
	}
	dsns, err := shard.ExpandTemplate(c.ShardDSN, c.Shards)
	if err != nil {
		return shard.Config{}, fmt.Errorf("Store.ShardDSN: %w", err)
	}
	return shard.Config{Driver: driver, DSNs: dsns}, nil
}

// Close releases the store and, when enabled, the scale skeleton.
func (sc *ServiceContext) Close() error {
	if sc == nil {
		return nil
	}
	var err error
	// Stop the projection worker before closing the store / bus it uses.
	if sc.projectionCancel != nil {
		sc.projectionCancel()
	}
	if sc.Store != nil {
		err = sc.Store.Close()
	}
	if sc.Scale != nil {
		if serr := sc.Scale.Close(); err == nil {
			err = serr
		}
	}
	return err
}

// StartBackground runs auto-settle and webhook delivery until ctx is cancelled.
func (sc *ServiceContext) StartBackground(ctx context.Context) {
	if sc.projectionWorker != nil {
		pctx, cancel := context.WithCancel(ctx)
		sc.projectionCancel = cancel
		go sc.projectionWorker.Run(pctx)
	}
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
