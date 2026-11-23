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
