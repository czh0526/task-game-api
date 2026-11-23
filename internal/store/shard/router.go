// Package shard routes chat messages to independent message databases.
//
// This is a deliberately small prototype: only the messages table is sharded.
// Chats, chat_reads, idempotency keys and webhooks stay in the primary store,
// so the public API, the engine and the frontend are unchanged.
package shard

import (
	"hash/fnv"

	"taskgame/internal/model"
)

// Router resolves a chat to a shard with a stable hash. Direct chats hash the
// ordered pair, so both members derive the same shard without a lookup; other
// chats hash the chat id. The hash is a pure function: every API process and
// every restart computes the same answer.
type Router struct {
	N int
}

func NewRouter(n int) Router {
	if n < 1 {
		n = 1
	}
	return Router{N: n}
}

func (r Router) Enabled() bool { return r.N > 1 }

// ShardFor is used when a chat is created, before any directory row exists.
func (r Router) ShardFor(c model.Chat) int {
	key := c.ID
	if c.Type == model.ChatDirect && c.PairLow != "" {
		low, high := model.OrderedPair(c.PairLow, c.PairHigh)
		key = low + "#" + high
	}
	return r.of(key)
}

// ShardForID is the fallback for chats created before the directory existed.
func (r Router) ShardForID(chatID string) int { return r.of(chatID) }

func (r Router) of(key string) int {
	if r.N <= 1 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum64() % uint64(r.N))
}
