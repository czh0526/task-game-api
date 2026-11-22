package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// Envelope is one cross-process signal delivery. Origin identifies the API
// process that produced the signal, so a receiver can ignore its own echo.
type Envelope struct {
	Origin  string
	UserIDs []string
	Signal  Signal
}

// Transport carries lightweight signals between API processes.
//
// SKELETON BOUNDARY: only LocalTransport (no-op) and MemoryTransport (in-process
// bus for tests) are implemented. A production deployment plugs in Redis
// pub/sub, NATS, or Kafka here. Implementations must be safe for concurrent
// use, must not block the caller, and must deliver to every process including
// the origin (RemoteFanout filters its own echo).
type Transport interface {
	Publish(ctx context.Context, env Envelope) error
	// Subscribe registers a callback. The returned stop function unregisters it.
	// A transport may invoke fn from its own goroutine.
	Subscribe(ctx context.Context, fn func(Envelope)) (stop func(), err error)
	Close() error
}

// LocalTransport is the no-op transport used by a single-process deployment.
type LocalTransport struct{}

func (LocalTransport) Publish(context.Context, Envelope) error { return nil }
func (LocalTransport) Subscribe(_ context.Context, _ func(Envelope)) (func(), error) {
	return func() {}, nil
}
func (LocalTransport) Close() error { return nil }

// MemoryTransport is a process-wide bus. It exists so a test can create two
// RemoteFanout values and prove cross-process fan-out without running a real
// broker. It is NOT a production transport: every subscriber is in this
// process and there is no durability.
type MemoryTransport struct {
	mu   sync.RWMutex
	subs map[*memorySub]struct{}
}

type memorySub struct{ fn func(Envelope) }

func NewMemoryTransport() *MemoryTransport {
	return &MemoryTransport{subs: make(map[*memorySub]struct{})}
}

func (m *MemoryTransport) Publish(_ context.Context, env Envelope) error {
	m.mu.RLock()
	fns := make([]func(Envelope), 0, len(m.subs))
	for s := range m.subs {
		fns = append(fns, s.fn)
	}
	m.mu.RUnlock()
	// Call outside the lock: a callback may publish again.
	for _, fn := range fns {
		fn(env)
	}
	return nil
}

func (m *MemoryTransport) Subscribe(_ context.Context, fn func(Envelope)) (func(), error) {
	s := &memorySub{fn: fn}
	m.mu.Lock()
	m.subs[s] = struct{}{}
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.subs, s)
			m.mu.Unlock()
		})
	}, nil
}

func (m *MemoryTransport) Close() error {
	m.mu.Lock()
	m.subs = make(map[*memorySub]struct{})
	m.mu.Unlock()
	return nil
}

// RemoteFanout delivers a signal to local subscribers and to every other API
// process over a Transport. Signals received from other processes are fanned
// out to local subscribers only (never re-published), so there is no loop.
type RemoteFanout struct {
	local      *Broker
	transport  Transport
	instanceID string
	stop       func()
	closeOnce  sync.Once
}

// NewRemoteFanout builds a cross-process fan-out. An empty instanceID gets a
// random one. The caller owns transport and must close it after Close.
func NewRemoteFanout(instanceID string, t Transport) (*RemoteFanout, error) {
	if t == nil {
		t = LocalTransport{}
	}
	if instanceID == "" {
		instanceID = randomInstanceID()
	}
	r := &RemoteFanout{local: New(), transport: t, instanceID: instanceID}
	stop, err := t.Subscribe(context.Background(), r.receive)
	if err != nil {
		return nil, err
	}
	r.stop = stop
	return r, nil
}

func (r *RemoteFanout) receive(env Envelope) {
	if env.Origin == r.instanceID {
		return
	}
	r.local.Publish(env.UserIDs, env.Signal)
}

func (r *RemoteFanout) Subscribe(userID string) (<-chan Signal, func()) {
	return r.local.Subscribe(userID)
}

func (r *RemoteFanout) Publish(userIDs []string, s Signal) {
	if len(userIDs) == 0 {
		return
	}
	r.local.Publish(userIDs, s)
	// Fanout.Publish cannot return an error (engine.SignalNotifier contract).
	// A production runtime should count transport failures here and rely on
	// the client's 45s fallback poll to close the gap.
	_ = r.transport.Publish(context.Background(), Envelope{
		Origin: r.instanceID, UserIDs: userIDs, Signal: s,
	})
}

func (r *RemoteFanout) NextID() uint64                { return r.local.NextID() }
func (r *RemoteFanout) Subscribers(userID string) int { return r.local.Subscribers(userID) }

// Close unregisters this fan-out from the transport. It does not close the
// transport, which may be shared by other fan-outs in the same process.
func (r *RemoteFanout) Close() error {
	r.closeOnce.Do(func() {
		if r.stop != nil {
			r.stop()
		}
	})
	return nil
}

func randomInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "api-unknown"
	}
	return "api-" + hex.EncodeToString(b[:])
}
