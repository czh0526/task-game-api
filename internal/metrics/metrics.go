// Package metrics is a dependency-free, in-process metrics registry. It
// exposes the few numbers the sharded deployment must watch: directory cache
// hit rate, control-plane query count, and cross-domain transactions (which
// should trend to zero). A Prometheus-style text endpoint is provided so it
// can be scraped without adding a client library.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Names of the counters wired into the hot paths.
const (
	LookupTotal     = "directory_lookup_total"
	LookupCacheHit  = "directory_cache_hit_total"
	ControlQPS      = "control_plane_query_total"
	CrossDomainTxn  = "cross_domain_transaction_total"
	FencedWrites    = "fenced_write_total"
	SagaCompensated = "saga_compensated_total"
)

type counter struct {
	value atomic.Uint64
	help  string
}

var (
	mu       sync.RWMutex
	counters = map[string]*counter{}
)

func init() {
	register(LookupTotal, "Directory lookup calls.")
	register(LookupCacheHit, "Directory lookups served from the local cache.")
	register(ControlQPS, "Control-plane SQL queries issued.")
	register(CrossDomainTxn, "Transactions that touched more than one physical store.")
	register(FencedWrites, "Writes rejected because the key was fenced.")
	register(SagaCompensated, "Cross-domain sagas that were compensated.")
}

func register(name, help string) *counter {
	mu.Lock()
	defer mu.Unlock()
	if c, ok := counters[name]; ok {
		return c
	}
	c := &counter{help: help}
	counters[name] = c
	return c
}

// Inc adds delta to a named counter, creating it on first use.
func Inc(name string, delta uint64) {
	register(name, name).value.Add(delta)
}

// Value reads a counter (mainly for tests).
func Value(name string) uint64 {
	mu.RLock()
	c, ok := counters[name]
	mu.RUnlock()
	if !ok {
		return 0
	}
	return c.value.Load()
}

// Reset zeroes all counters. Intended for tests.
func Reset() {
	mu.RLock()
	for _, c := range counters {
		c.value.Store(0)
	}
	mu.RUnlock()
}

// Handler writes the counters in Prometheus text exposition format.
func Handler(w io.Writer) error {
	mu.RLock()
	names := make([]string, 0, len(counters))
	for name := range counters {
		names = append(names, name)
	}
	sort.Strings(names)
	type snapshot struct {
		help  string
		value uint64
	}
	snap := make(map[string]snapshot, len(names))
	for _, name := range names {
		snap[name] = snapshot{help: counters[name].help, value: counters[name].value.Load()}
	}
	mu.RUnlock()
	for _, name := range names {
		s := snap[name]
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
			name, strings.ReplaceAll(s.help, "\n", " "), name, name, s.value); err != nil {
			return err
		}
	}
	return nil
}
