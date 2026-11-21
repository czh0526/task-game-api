package shard

import (
	"testing"

	"taskgame/internal/model"
)

func TestRouterStableAndOrderedPair(t *testing.T) {
	r := NewRouter(2)
	a := model.Chat{ID: "cht_1", Type: model.ChatDirect, PairLow: "usr_a", PairHigh: "usr_b"}
	b := model.Chat{ID: "cht_2", Type: model.ChatDirect, PairLow: "usr_b", PairHigh: "usr_a"}
	if got, want := r.ShardFor(a), r.ShardFor(b); got != want {
		t.Fatalf("direct pair resolved to different shards: %d vs %d", got, want)
	}
	for i := 0; i < 100; i++ {
		if r.ShardFor(a) != r.ShardFor(b) {
			t.Fatal("router is not deterministic")
		}
	}
	if got, want := r.ShardFor(a), r.ShardForID("usr_a#usr_b"); got != want {
		t.Fatalf("direct pair hash %d != fallback hash %d", got, want)
	}
}

func TestRouterDisabledIsZero(t *testing.T) {
	for _, n := range []int{0, 1} {
		r := NewRouter(n)
		if r.Enabled() {
			t.Fatalf("N=%d should be disabled", n)
		}
		if got := r.ShardForID("anything"); got != 0 {
			t.Fatalf("N=%d returned shard %d", n, got)
		}
	}
}

func TestRouterSpreadsAcrossShards(t *testing.T) {
	r := NewRouter(2)
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		id := "cht_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		seen[r.ShardForID(id)] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 shards to be used, got %v", seen)
	}
}
