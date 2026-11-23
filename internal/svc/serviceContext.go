package svc

import (
	"context"
	"fmt"
	"time"

	"taskgame/internal/account"
	"taskgame/internal/compensation"
	"taskgame/internal/config"
	"taskgame/internal/engine"
	"taskgame/internal/id"
	"taskgame/internal/middleware"
	"taskgame/internal/notify"
	"taskgame/internal/plane"
	"taskgame/internal/projection"
	"taskgame/internal/scale"
	"taskgame/internal/store"
	"taskgame/internal/store/cell"
	"taskgame/internal/store/domain"
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
	if c.Store.DSN == "" && len(c.Store.Cells) == 0 {
		return nil, fmt.Errorf("Store.DSN or Store.Cells is required")
	}
	// The scale skeleton is built before the store opens so a bad config
	// fails fast and cannot leak a connection pool.
	rt, err := scale.New(c.Scale, c.Store)
	if err != nil {
		return nil, fmt.Errorf("scale: %w", err)
	}
	st, err := openStore(c, rt)
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
	// In cell mode the auth middleware also pins each request to the database
	// that owns the aggregate root named in the URL (or the caller's cell).
	var locator *cell.Locator
	if cs, ok := st.(*cell.Store); ok {
		locator = cs.Locator()
	}
	_, domainMode := st.(*domain.Store)
	engineOpts := []engine.Option{
		engine.WithTimeouts(engine.TimeoutConfig{
			OpenTimeout:       c.AutoSettle.OpenTimeout,
			StartTimeout:      c.AutoSettle.StartTimeout,
			SeatTimeout:       c.AutoSettle.SeatTimeout,
			WinnerSelectHours: c.AutoSettle.WinnerSelectHours,
		}),
		engine.WithArbiters(c.Escrow.ArbiterUserIds),
		engine.WithSignalNotifier(notifier),
	}
	if rt != nil && rt.Compensation != nil {
		engineOpts = append(engineOpts, engine.WithCompensation(rt.Compensation))
	}
	// P0: when a persistent control plane is enabled, route registration and
	// login through global_unique(email -> user_id) + Snowflake ids. When it
	// is off, account.New keeps the original behavior.
	accountOpts := []account.Option{}
	if rt != nil && rt.ControlPlane != nil && rt.ControlPlane.Unique != nil && rt.ControlPlane.IDs != nil {
		accountOpts = append(accountOpts, account.WithIdentity(rt.ControlPlane.Unique, rt.ControlPlane.IDs))
	}
	sc := &ServiceContext{
		Config:   c,
		Auth:     middleware.NewAuthMiddlewareWithDomain(c.Auth, domainMode, locator).Handle,
		Engine:   engine.New(st, c.Escrow.Currency, engineOpts...),
		Accounts: account.New(st, c.Auth.JwtSecret, c.Auth.TokenTTL, accountOpts...),
		Store:    st,
		Notifier: notifier,
		Scale:    rt,
		Dispatcher: webhook.NewDispatcher(st, webhook.Config{
			Mode:        c.Webhook.Mode,
			Timeout:     c.Webhook.Timeout,
			MaxAttempts: c.Webhook.MaxAttempts,
		}),
	}
	// P0: attach the fund-hold handler so the saga reconciler can replay or
	// compensate a cross-domain balance deduction after a crash.
	if rt != nil && rt.Compensation != nil {
		rt.Compensation.RegisterHandler(compensation.KindFundHold, compensation.NewFundHoldHandler(st))
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

func openStore(c config.Config, rt *scale.Runtime) (store.Store, error) {
	// Per-domain sharding (P2): every aggregate-root family has its own ordered
	// DSN list. Domains without explicit DSNs inherit Store.Cells, so the first
	// cutover is logical only and keeps all data co-located.
	if c.Store.Mode == "domain" {
		dsns := scale.DomainDSNs(c.Scale, c.Store)
		if len(dsns) == 0 {
			return nil, fmt.Errorf("Store.Mode=domain needs Store.Cells or per-domain DSNs")
		}
		var control *plane.ControlPlane
		if rt != nil {
			control = rt.ControlPlane
		}
		return domain.Open(domain.Config{
			Driver:             c.Store.Driver,
			DSNs:               dsns,
			InitialBalance:     c.Escrow.InitialBalance,
			Control:            control,
			DirectoryCoLocated: rt != nil && rt.DirectoryCoLocated(),
		})
	}
	// Horizontal cell sharding (plan A): N independent databases, each with
	// the full schema and roughly 1/N of every table. No primary database.
	if len(c.Store.Cells) > 0 {
		return cell.Open(cell.Config{
			Driver:         c.Store.Driver,
			DSNs:           c.Store.Cells,
			InitialBalance: c.Escrow.InitialBalance,
		})
	}
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
			// Auto-settle uses InTx, which is pinned to ONE cell. Sweep every
			// cell so a due task is never skipped in a sharded deployment.
			var first error
			for _, cctx := range sc.backgroundContexts(ctx) {
				if _, err := sc.Engine.AutoSettleDue(cctx); err != nil && first == nil {
					first = err
				}
			}
			return first
		})
	}
	sc.loop(ctx, sc.Config.Webhook.Interval, 2*time.Second, "webhook", func() error {
		return sc.Dispatcher.DeliverPending(ctx)
	})
	if sc.Scale != nil && sc.Scale.Compensation != nil {
		sc.loop(ctx, sc.Scale.CompensationInterval(), 5*time.Second, "compensation", func() error {
			_, err := sc.Scale.Compensation.ReconcileOnce(ctx, 32)
			return err
		})
	}
}

// backgroundContexts returns one context per cell. In a single-database
// deployment it is just the original context.
func (sc *ServiceContext) backgroundContexts(ctx context.Context) []context.Context {
	if cs, ok := sc.Store.(*cell.Store); ok {
		out := make([]context.Context, cs.Len())
		for i := range out {
			out[i] = cell.WithCell(ctx, i)
		}
		return out
	}
	return []context.Context{ctx}
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
