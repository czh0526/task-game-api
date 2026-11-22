package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultRedisChannel is shared by every API process of one deployment.
// Signals carry ids only, so all environments may reuse the same channel name.
const DefaultRedisChannel = "taskgame.signals"

// ErrTransportClosed is returned by Publish / Subscribe after Close.
var ErrTransportClosed = errors.New("notify: transport is closed")

// RedisConfig describes one Redis endpoint for cross-process signal fan-out.
// Password is read from the config file and is never logged by this package.
type RedisConfig struct {
	Addr     string
	Username string
	Password string
	DB       int
	Channel  string
	Timeout  time.Duration
}

// RedisTransport is a real cross-process Transport backed by Redis pub/sub.
//
// Design notes:
//   - PUBLISH uses a pooled client with short socket timeouts, so a stalled
//     Redis can never block the request that produced the signal.
//   - SUBSCRIBE uses a SECOND client with the read timeout disabled: a pub/sub
//     socket blocks waiting for a message, and a normal 3s read timeout would
//     tear it down every few seconds.
//   - Redis pub/sub is at-most-once. A signal published while a process is
//     down is lost; that is acceptable because signals contain ids only and
//     the browser's 45s fallback poll repairs the gap.
type RedisTransport struct {
	pub     *redis.Client
	sub     *redis.Client
	channel string
	timeout time.Duration

	mu     sync.Mutex
	subs   map[*redisSub]struct{}
	closed bool
}

type redisSub struct {
	pubsub *redis.PubSub
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

var _ Transport = (*RedisTransport)(nil)

// NewRedisTransport connects to Redis and verifies the endpoint with PING.
// Failing fast is deliberate: a misconfigured bus would otherwise make a
// multi-instance deployment silently behave like independent single nodes.
func NewRedisTransport(cfg RedisConfig) (*RedisTransport, error) {
	if cfg.Addr == "" {
		return nil, errors.New("notify: redis Addr is required")
	}
	if cfg.Channel == "" {
		cfg.Channel = DefaultRedisChannel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	base := &redis.Options{
		Addr:         cfg.Addr,
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.Timeout,
		ReadTimeout:  cfg.Timeout,
		WriteTimeout: cfg.Timeout,
		MaxRetries:   -1, // no retry: a failed hint is cheaper than a stalled request
	}
	subOpts := *base
	// go-redis: -1 disables the socket read/write deadline on a blocking
	// subscribe connection.
	subOpts.ReadTimeout = -1
	subOpts.WriteTimeout = -1

	r := &RedisTransport{
		pub:     redis.NewClient(base),
		sub:     redis.NewClient(&subOpts),
		channel: cfg.Channel,
		timeout: cfg.Timeout,
		subs:    make(map[*redisSub]struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	if err := r.pub.Ping(ctx).Err(); err != nil {
		_ = r.pub.Close()
		_ = r.sub.Close()
		return nil, fmt.Errorf("notify: redis ping %s: %w", cfg.Addr, err)
	}
	return r, nil
}

// Channel exposes the subscribed channel (useful for diagnostics/tests).
func (r *RedisTransport) Channel() string { return r.channel }

// Publish serializes the envelope and sends it once. Failures are returned to
// RemoteFanout, which counts them and relies on the client fallback poll.
func (r *RedisTransport) Publish(ctx context.Context, env Envelope) error {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return ErrTransportClosed
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("notify: encode envelope: %w", err)
	}
	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := r.pub.Publish(c, r.channel, payload).Err(); err != nil {
		return fmt.Errorf("notify: redis publish: %w", err)
	}
	return nil
}

// Subscribe opens one Redis subscription. Receive() waits for the SUBSCRIBE
// acknowledgement before returning, so a Publish issued immediately after
// cannot be lost during startup. The caller's ctx bounds only the setup; the
// subscription lives until the returned stop function is called.
func (r *RedisTransport) Subscribe(_ context.Context, fn func(Envelope)) (func(), error) {
	if fn == nil {
		return nil, errors.New("notify: subscribe callback is nil")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrTransportClosed
	}
	r.mu.Unlock()

	subCtx, cancel := context.WithCancel(context.Background())
	pubsub := r.sub.Subscribe(subCtx, r.channel)
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, fmt.Errorf("notify: redis subscribe: %w", err)
	}
	s := &redisSub{pubsub: pubsub, cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if r.closed { // Close raced with the subscribe above
		r.mu.Unlock()
		cancel()
		_ = pubsub.Close()
		return nil, ErrTransportClosed
	}
	r.subs[s] = struct{}{}
	r.mu.Unlock()

	go r.receiveLoop(subCtx, s, fn)
	return func() { r.stopSub(s) }, nil
}

func (r *RedisTransport) receiveLoop(ctx context.Context, s *redisSub, fn func(Envelope)) {
	defer close(s.done)
	for {
		msg, err := s.pubsub.ReceiveMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Transient error (reconnect, Redis restart, ...). Back off and
			// keep listening; go-redis re-subscribes the channel itself.
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		env, err := decodeEnvelope([]byte(msg.Payload))
		if err != nil {
			continue // ignore foreign/corrupt frames, never stall the bus
		}
		fn(env)
	}
}

// decodeEnvelope rejects frames that are not ours (e.g. someone else publishing
// on the same channel) instead of delivering a zero-valued signal.
func decodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	if env.Origin == "" || len(env.UserIDs) == 0 {
		return Envelope{}, errors.New("notify: incomplete envelope")
	}
	return env, nil
}

// stopSub is idempotent and must be called WITHOUT holding r.mu: the receive
// goroutine may still be running and stopSub waits for it.
func (r *RedisTransport) stopSub(s *redisSub) {
	s.once.Do(func() {
		s.cancel()
		_ = s.pubsub.Close()
		<-s.done
		r.mu.Lock()
		delete(r.subs, s)
		r.mu.Unlock()
	})
}

// Close stops every subscription and releases both Redis clients.
func (r *RedisTransport) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	subs := make([]*redisSub, 0, len(r.subs))
	for s := range r.subs {
		subs = append(subs, s)
	}
	r.mu.Unlock()
	for _, s := range subs {
		r.stopSub(s)
	}
	var err error
	if e := r.pub.Close(); e != nil {
		err = e
	}
	if e := r.sub.Close(); e != nil && err == nil {
		err = e
	}
	return err
}
