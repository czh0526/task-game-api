// Package projection coordinates message_outbox projection across API
// processes. A shared Lease makes sure two processes never project the same
// row at the same time; a Bus wakes the other processes as soon as a new
// outbox row appears, so they do not have to wait for the periodic scan.
//
// Correctness does NOT depend on either component: shard.Set.Insert is
// idempotent on message id and message_outbox.projected is the authoritative
// fact. The lease/bus only remove duplicate work and shorten crash recovery.
package projection

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrClosed is returned after Close.
var ErrClosed = errors.New("projection: closed")

const (
	// DefaultTimeout bounds Redis commands issued on a request's behalf.
	DefaultTimeout = 2 * time.Second
	leasePrefix    = "taskgame:lease:"
)

// Endpoint is one Redis server. Channel is used by the Bus; the Lease ignores
// it. All API processes of a deployment must share Addr and Channel.
type Endpoint struct {
	Addr     string
	Username string
	Password string
	DB       int
	Channel  string
	Timeout  time.Duration
}

func (e Endpoint) timeout() time.Duration {
	if e.Timeout <= 0 {
		return DefaultTimeout
	}
	return e.Timeout
}

func newRedisClient(e Endpoint, subscribe bool) *redis.Client {
	opts := &redis.Options{
		Addr:         e.Addr,
		Username:     e.Username,
		Password:     e.Password,
		DB:           e.DB,
		DialTimeout:  e.timeout(),
		ReadTimeout:  e.timeout(),
		WriteTimeout: e.timeout(),
		MaxRetries:   -1,
	}
	if subscribe {
		// A pub/sub socket blocks waiting for a message. -1 disables the
		// read/write deadline so it is not torn down every few seconds.
		opts.ReadTimeout = -1
		opts.WriteTimeout = -1
	}
	return redis.NewClient(opts)
}

// Lease is a TTL-backed distributed mutex. Acquire returns true only for the
// holder; a lease expires on its own if the holder crashes, so another process
// can take over without a cleanup job.
type Lease interface {
	Acquire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)
	Release(ctx context.Context, key, owner string) error
	Close() error
}

// MemoryLease is the in-process implementation (single node and tests).
type MemoryLease struct {
	mu     sync.Mutex
	held   map[string]leaseEntry
	closed bool
}

type leaseEntry struct {
	owner string
	until time.Time
}

func NewMemoryLease() *MemoryLease {
	return &MemoryLease{held: make(map[string]leaseEntry)}
}

func (m *MemoryLease) Acquire(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false, ErrClosed
	}
	now := time.Now()
	if e, ok := m.held[key]; ok && e.until.After(now) && e.owner != owner {
		return false, nil
	}
	// Same owner re-acquires: refresh the TTL instead of blocking itself.
	m.held[key] = leaseEntry{owner: owner, until: now.Add(ttl)}
	return true, nil
}

func (m *MemoryLease) Release(_ context.Context, key, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.held[key]; ok && e.owner == owner {
		delete(m.held, key)
	}
	return nil
}

func (m *MemoryLease) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.held = make(map[string]leaseEntry)
	return nil
}

var _ Lease = (*MemoryLease)(nil)

// RedisLease uses SET NX PX. Release is a Lua compare-and-delete so a slow
// holder cannot delete a lease that has already expired and been re-acquired
// by another process.
type RedisLease struct {
	client *redis.Client
}

const releaseLeaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`

func NewRedisLease(e Endpoint) *RedisLease {
	return &RedisLease{client: newRedisClient(e, false)}
}

func (r *RedisLease) Acquire(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	return r.client.SetNX(ctx, leasePrefix+key, owner, ttl).Result()
}

func (r *RedisLease) Release(ctx context.Context, key, owner string) error {
	return r.client.Eval(ctx, releaseLeaseScript, []string{leasePrefix + key}, owner).Err()
}

func (r *RedisLease) Close() error { return r.client.Close() }

var _ Lease = (*RedisLease)(nil)
