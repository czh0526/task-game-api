package shard

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"taskgame/internal/model"
	"taskgame/internal/plane"
)

// DomainRouter generalizes the chat-only Router from the P1 prototype to all
// three aggregate-root domains. The existing Router stays untouched: PR #38
// behaviour is preserved, and the new domains are scaffolding only.
//
// SKELETON BOUNDARY: only Message resolves to real databases today (via the
// existing Set). User and Task placements are computed and checked, but their
// repositories still live in the primary store. Wiring a real per-domain
// store is P2 work, not part of this skeleton.
type DomainRouter struct {
	cp   *plane.ControlPlane
	chat Router
}

func NewDomainRouter(cp *plane.ControlPlane, chat Router) (*DomainRouter, error) {
	if cp == nil {
		return nil, fmt.Errorf("shard: domain router needs a control plane")
	}
	return &DomainRouter{cp: cp, chat: chat}, nil
}

// ChatKey reproduces Router.ShardFor's key without duplicating its hash:
// direct chats hash the ordered pair so both members agree, other chats hash
// the chat id.
func ChatKey(c model.Chat) string {
	if c.Type == model.ChatDirect && c.PairLow != "" {
		low, high := model.OrderedPair(c.PairLow, c.PairHigh)
		return low + "#" + high
	}
	return c.ID
}

// ResolveChat pins a chat in the directory using the SAME shard number the
// current prototype would compute. This is the bridge from PR #38 to a
// directory-authoritative router: once the row exists, reads stop hashing.
func (d *DomainRouter) ResolveChat(ctx context.Context, c model.Chat) (plane.Placement, error) {
	return d.cp.Resolver.Place(ctx, plane.DomainMessage, ChatKey(c), d.chat.ShardFor(c))
}

func (d *DomainRouter) Resolve(ctx context.Context, domain plane.Domain, key string) (plane.Placement, error) {
	return d.cp.Resolver.Resolve(ctx, domain, key)
}

func (d *DomainRouter) ScaleOut(ctx context.Context, domain plane.Domain, dsn string) (plane.ScaleOutPlan, error) {
	return d.cp.Topology.AddShard(ctx, domain, dsn, d.cp.Directory)
}

// ErrCrossShard is returned when one transaction tries to touch two shards of
// the same domain. A real multi-database store must reject this rather than
// silently starting a distributed transaction (there is no 2PC here).
var ErrCrossShard = errors.New("shard: transaction spans more than one shard")

// TxScope is the guard a P2 store binds to each transaction. It is a skeleton:
// it records placements and rejects conflicts, but does not open connections.
type TxScope struct {
	mu         sync.Mutex
	placements map[plane.Domain]plane.Placement
}

func NewTxScope() *TxScope {
	return &TxScope{placements: make(map[plane.Domain]plane.Placement)}
}

func (s *TxScope) Pin(p plane.Placement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.placements[p.Domain]; ok && prev.Shard != p.Shard {
		return fmt.Errorf("%w: %q already pinned to shard %d, got %d",
			ErrCrossShard, p.Domain, prev.Shard, p.Shard)
	}
	s.placements[p.Domain] = p
	return nil
}

func (s *TxScope) Shard(domain plane.Domain) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.placements[domain]
	return p.Shard, ok
}
