package scale

import (
	"context"
	"testing"
	"time"

	"taskgame/internal/config"
	"taskgame/internal/notify"
	"taskgame/internal/plane"
)

func TestDisabledIsNoop(t *testing.T) {
	rt, err := New(config.ScaleConf{}, config.StoreConf{Shards: 2})
	if err != nil || rt != nil {
		t.Fatalf("disabled skeleton must be nil: %v, %v", rt, err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := []config.ScaleConf{
		{Enabled: true, PubSub: "kafka"},
		{Enabled: true, Domains: config.DomainScaleConf{UserShards: -1}},
		{Enabled: true, Archive: config.ArchiveScaleConf{Enabled: true}},
		{Enabled: true, Search: config.SearchScaleConf{Enabled: true, Driver: "elasticsearch"}},
	}
	for i, c := range cases {
		if err := c.Validate(); err == nil {
			t.Fatalf("case %d: invalid config accepted", i)
		}
	}
	if err := (config.ScaleConf{}).Validate(); err != nil {
		t.Fatalf("disabled config must always validate: %v", err)
	}
}

func TestEnabledWiresEveryComponent(t *testing.T) {
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "shared",
		InstanceID: "api-1",
		Domains:    config.DomainScaleConf{UserShards: 4, TaskShards: 2},
		Archive:    config.ArchiveScaleConf{Enabled: true, Dir: t.TempDir(), HotWindow: 48 * time.Hour},
		Search:     config.SearchScaleConf{Enabled: true, Driver: "memory"},
	}, config.StoreConf{Shards: 2, ShardDSNs: []string{"db0", "db1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Fanout == nil || rt.ControlPlane == nil || rt.DomainRouter == nil {
		t.Fatal("pub/sub, control plane or domain router missing")
	}
	if rt.Blobs == nil || rt.Archiver == nil || rt.TaskIndex == nil || rt.Projector == nil {
		t.Fatal("archive or search components missing")
	}
	if rt.ControlPlane.Topology.Len(plane.DomainMessage) != 2 ||
		rt.ControlPlane.Topology.Len(plane.DomainUser) != 4 ||
		rt.ControlPlane.Topology.Len(plane.DomainTask) != 2 {
		t.Fatal("domain topology was not seeded from config")
	}
	batches, err := rt.Archiver.Plan(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range batches {
		if b.Before.After(time.Now().Add(-47 * time.Hour)) {
			t.Fatalf("HotWindow override not applied: %+v", b)
		}
	}
	if len(TODOs) == 0 {
		t.Fatal("skeleton boundary is not documented in code")
	}
}

func TestDeclaredDomainDSNsPopulateTopology(t *testing.T) {
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "shared",
		InstanceID: "api-1",
		Domains: config.DomainScaleConf{
			User:    config.DomainDSNsConf{Shards: 2, DSNs: []string{"u0", "u1"}},
			Task:    config.DomainDSNsConf{Shards: 3, DSNs: []string{"t0", "t1", "t2"}},
			Message: config.DomainDSNsConf{Shards: 2, DSNs: []string{"m0", "m1"}},
		},
	}, config.StoreConf{Driver: "mysql", Cells: []string{"c0", "c1", "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	topo := rt.ControlPlane.Topology
	for _, tc := range []struct {
		domain plane.Domain
		want   []string
	}{
		{plane.DomainUser, []string{"u0", "u1"}},
		{plane.DomainTask, []string{"t0", "t1", "t2"}},
		{plane.DomainMessage, []string{"m0", "m1"}},
	} {
		if topo.Len(tc.domain) != len(tc.want) {
			t.Fatalf("%s Len = %d, want %d", tc.domain, topo.Len(tc.domain), len(tc.want))
		}
		for i, want := range tc.want {
			got, err := topo.DSN(tc.domain, i)
			if err != nil || got != want {
				t.Fatalf("%s DSN[%d] = %q (%v), want %q", tc.domain, i, got, err, want)
			}
		}
	}
}

func TestCoLocatedDomainsShareTheSameDSNs(t *testing.T) {
	cells := []string{"db0", "db1", "db2"}
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "shared",
		InstanceID: "api-1",
		Domains: config.DomainScaleConf{
			User:    config.DomainDSNsConf{Shards: 3, DSNs: cells},
			Task:    config.DomainDSNsConf{Shards: 3, DSNs: cells},
			Message: config.DomainDSNsConf{Shards: 3, DSNs: cells},
		},
	}, config.StoreConf{Driver: "mysql", Cells: cells})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	for _, domain := range []plane.Domain{plane.DomainUser, plane.DomainTask, plane.DomainMessage} {
		for i, want := range cells {
			got, _ := rt.ControlPlane.Topology.DSN(domain, i)
			if got != want {
				t.Fatalf("co-located %s DSN[%d] = %q, want %q", domain, i, got, want)
			}
		}
	}
}

func TestSharedPubSubReachesAnotherProcess(t *testing.T) {
	rt, err := New(config.ScaleConf{Enabled: true, PubSub: "shared", InstanceID: "api-1"},
		config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	other, err := notify.NewRemoteFanout("api-2", rt.Transport())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ch, unsub := other.Subscribe("usr_b")
	defer unsub()
	rt.Fanout.Publish([]string{"usr_b"}, notify.Signal{Type: notify.SignalChatMessage, ChatID: "cht_1"})
	select {
	case s := <-ch:
		if s.ChatID != "cht_1" {
			t.Fatalf("got %+v", s)
		}
	default:
		t.Fatal("second process did not receive the signal")
	}
}

func TestModeDomainInheritsCellsAndOverridesOneDomain(t *testing.T) {
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "shared",
		InstanceID: "api-1",
		Domains: config.DomainScaleConf{
			// Only the message domain is physically cut over; user and task
			// inherit the existing cell DSNs.
			Message: config.DomainDSNsConf{Shards: 2, DSNs: []string{"m0", "m1"}},
		},
	}, config.StoreConf{Mode: "domain", Driver: "mysql", Cells: []string{"c0", "c1", "c2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	topo := rt.ControlPlane.Topology
	for _, tc := range []struct {
		domain plane.Domain
		want   []string
	}{
		{plane.DomainUser, []string{"c0", "c1", "c2"}},
		{plane.DomainTask, []string{"c0", "c1", "c2"}},
		{plane.DomainMessage, []string{"m0", "m1"}},
	} {
		if topo.Len(tc.domain) != len(tc.want) {
			t.Fatalf("%s Len = %d, want %d", tc.domain, topo.Len(tc.domain), len(tc.want))
		}
		for i, want := range tc.want {
			got, err := topo.DSN(tc.domain, i)
			if err != nil || got != want {
				t.Fatalf("%s DSN[%d] = %q (%v), want %q", tc.domain, i, got, err, want)
			}
		}
	}
}

func TestSearchCDCWiring(t *testing.T) {
	// CDC off: legacy synchronous projector, no log/recorder/projector stream.
	off, err := New(config.ScaleConf{
		Enabled: true, PubSub: "shared", InstanceID: "api-cdc-off",
		Search: config.SearchScaleConf{Enabled: true},
	}, config.StoreConf{Shards: 2, ShardDSNs: []string{"db0", "db1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer off.Close()
	if off.TaskIndex == nil || off.Projector == nil || off.CDC != nil || off.SearchLog != nil {
		t.Fatalf("cdc-off wiring: index=%v projector=%v cdc=%v log=%v",
			off.TaskIndex != nil, off.Projector != nil, off.CDC != nil, off.SearchLog != nil)
	}

	// CDC on without a persistent directory: process-local log + projector.
	on, err := New(config.ScaleConf{
		Enabled: true, PubSub: "shared", InstanceID: "api-cdc-on",
		Search: config.SearchScaleConf{Enabled: true, CDC: true, Interval: 2 * time.Second},
	}, config.StoreConf{Shards: 2, ShardDSNs: []string{"db0", "db1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer on.Close()
	if on.TaskIndex == nil || on.CDC == nil || on.SearchLog == nil || on.SearchRecorder() == nil {
		t.Fatalf("cdc-on wiring incomplete: index=%v cdc=%v log=%v rec=%v",
			on.TaskIndex != nil, on.CDC != nil, on.SearchLog != nil, on.SearchRecorder() != nil)
	}
	if got := on.SearchInterval(); got != 2*time.Second {
		t.Fatalf("search interval: %v", got)
	}
}
