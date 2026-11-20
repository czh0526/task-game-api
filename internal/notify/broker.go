// Package notify fans out lightweight signals to browsers connected over SSE.
// A signal carries only ids; the client re-reads the actual data over REST,
// so a dropped notification can always be repaired by a fallback refresh.
package notify

import (
	"sync"
	"sync/atomic"
)

// Signal is a small hint pushed to a signed-in browser. It deliberately holds
// no message body: the client fetches the thread after being notified.
type Signal struct {
	Type      string `json:"type"`
	ChatID    string `json:"chat_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	SenderID  string `json:"sender_id,omitempty"`
}

const (
	// SignalChatMessage means a message was committed in one of the user's chats.
	SignalChatMessage = "chat.message"
	// SignalHello is sent once when a stream opens.
	SignalHello = "hello"
	// SignalResync replaces SignalHello on a reconnect that carries Last-Event-ID.
	SignalResync = "resync"
)

// subscriberBuffer absorbs bursts without ever blocking the writer. A full
// buffer drops the hint; the client's fallback poll closes the gap.
const subscriberBuffer = 16

// Broker keeps one in-process subscription list per user. It is intentionally
// process-local: a multi-instance deployment needs a shared pub/sub instead.
type Broker struct {
	mu   sync.RWMutex
	subs map[string]map[chan Signal]struct{}
	seq  atomic.Uint64
}

// New builds an empty broker.
func New() *Broker {
	return &Broker{subs: make(map[string]map[chan Signal]struct{})}
}

// Subscribe registers a client for userID and returns its channel plus an
// idempotent unsubscribe function. The channel is closed on unsubscribe.
func (b *Broker) Subscribe(userID string) (<-chan Signal, func()) {
	ch := make(chan Signal, subscriberBuffer)
	b.mu.Lock()
	if b.subs[userID] == nil {
		b.subs[userID] = make(map[chan Signal]struct{})
	}
	b.subs[userID][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			set := b.subs[userID]
			if set == nil {
				return
			}
			if _, ok := set[ch]; !ok {
				return
			}
			delete(set, ch)
			// Closing under the write lock keeps Publish (which holds RLock)
			// from sending on a closed channel.
			close(ch)
			if len(set) == 0 {
				delete(b.subs, userID)
			}
		})
	}
	return ch, unsubscribe
}

// Publish delivers a signal to every connected client of the given users.
// Duplicate user ids are ignored, and a slow subscriber is skipped rather
// than blocking the request that produced the event.
func (b *Broker) Publish(userIDs []string, s Signal) {
	if b == nil || len(userIDs) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(userIDs))
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		for ch := range b.subs[uid] {
			select {
			case ch <- s:
			default:
			}
		}
	}
}

// NextID returns a monotonically increasing id for an SSE frame.
func (b *Broker) NextID() uint64 {
	return b.seq.Add(1)
}

// Subscribers reports how many clients currently listen for userID.
func (b *Broker) Subscribers(userID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[userID])
}
