package projection

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
)

// DefaultChannel carries "an outbox row may need projection" hints. The
// payload is just the outbox row id: receivers re-read the primary store, so a
// lost or duplicated hint is harmless.
const DefaultChannel = "taskgame.outbox"

// Bus broadcasts lightweight wake-up hints between API processes.
type Bus interface {
	Publish(ctx context.Context, id string) error
	Subscribe(ctx context.Context, fn func(id string)) (stop func(), err error)
	Close() error
}

// MemoryBus is the in-process implementation (single node and tests).
type MemoryBus struct {
	mu     sync.RWMutex
	subs   map[*memoryBusSub]struct{}
	closed bool
}

type memoryBusSub struct{ fn func(string) }

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{subs: make(map[*memoryBusSub]struct{})}
}

func (b *MemoryBus) Publish(_ context.Context, id string) error {
	b.mu.RLock()
	fns := make([]func(string), 0, len(b.subs))
	for s := range b.subs {
		fns = append(fns, s.fn)
	}
	b.mu.RUnlock()
	for _, fn := range fns {
		fn(id)
	}
	return nil
}

func (b *MemoryBus) Subscribe(_ context.Context, fn func(id string)) (func(), error) {
	s := &memoryBusSub{fn: fn}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
		})
	}, nil
}

func (b *MemoryBus) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	b.subs = make(map[*memoryBusSub]struct{})
	return nil
}

var _ Bus = (*MemoryBus)(nil)

// RedisBus publishes hints on its own channel, separate from the SSE signal
// channel. It owns two clients: one for PUBLISH with normal timeouts, one for
// a blocking SUBSCRIBE with the read deadline disabled.
type RedisBus struct {
	pub     *redis.Client
	sub     *redis.Client
	channel string
	mu      sync.Mutex
	subs    map[*redisBusSub]struct{}
	closed  bool
}

type redisBusSub struct {
	cancel context.CancelFunc
	ps     *redis.PubSub
	done   chan struct{}
	once   sync.Once
}

func NewRedisBus(e Endpoint) *RedisBus {
	channel := e.Channel
	if channel == "" {
		channel = DefaultChannel
	}
	return &RedisBus{
		pub:     newRedisClient(e, false),
		sub:     newRedisClient(e, true),
		channel: channel,
		subs:    make(map[*redisBusSub]struct{}),
	}
}

func (b *RedisBus) Channel() string { return b.channel }

func (b *RedisBus) Publish(ctx context.Context, id string) error {
	return b.pub.Publish(ctx, b.channel, id).Err()
}

func (b *RedisBus) Subscribe(_ context.Context, fn func(id string)) (func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	b.mu.Unlock()

	subCtx, cancel := context.WithCancel(context.Background())
	ps := b.sub.Subscribe(subCtx, b.channel)
	// Wait for the SUBSCRIBE ack so an early Publish cannot be lost.
	if _, err := ps.Receive(subCtx); err != nil {
		cancel()
		_ = ps.Close()
		return nil, err
	}
	s := &redisBusSub{cancel: cancel, ps: ps, done: make(chan struct{})}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		cancel()
		_ = ps.Close()
		return nil, ErrClosed
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	ch := ps.Channel() // go-redis re-subscribes on reconnect internally
	go func() {
		defer close(s.done)
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				if msg.Payload != "" {
					fn(msg.Payload)
				}
			}
		}
	}()
	return func() { b.stopSub(s) }, nil
}

// stopSub is idempotent and must be called WITHOUT holding b.mu.
func (b *RedisBus) stopSub(s *redisBusSub) {
	s.once.Do(func() {
		s.cancel()
		_ = s.ps.Close()
		<-s.done
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	})
}

func (b *RedisBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := make([]*redisBusSub, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.Unlock()
	for _, s := range subs {
		b.stopSub(s)
	}
	var err error
	if e := b.pub.Close(); e != nil {
		err = e
	}
	if e := b.sub.Close(); e != nil && err == nil {
		err = e
	}
	return err
}

var _ Bus = (*RedisBus)(nil)
