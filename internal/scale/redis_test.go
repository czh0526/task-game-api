package scale

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"taskgame/internal/config"
	"taskgame/internal/notify"
)

func TestValidateRejectsRedisWithoutAddr(t *testing.T) {
	if err := (config.ScaleConf{Enabled: true, PubSub: "redis"}).Validate(); err == nil {
		t.Fatal("PubSub=redis without Redis.Addr must fail")
	}
	if err := (config.ScaleConf{Enabled: true, PubSub: "redis",
		Redis: config.RedisScaleConf{Addr: "127.0.0.1:6379"}}).Validate(); err != nil {
		t.Fatalf("valid redis config rejected: %v", err)
	}
}

// scale.New must actually attach a RedisTransport, not silently fall back to
// the in-process bus, when PubSub=redis.
func TestRedisPubSubWiresRealTransport(t *testing.T) {
	mr := miniredis.RunT(t)
	rt, err := New(config.ScaleConf{
		Enabled:    true,
		PubSub:     "redis",
		InstanceID: "api-1",
		Redis: config.RedisScaleConf{
			Addr:    mr.Addr(),
			Channel: "taskgame.test",
			Timeout: 500 * time.Millisecond,
		},
	}, config.StoreConf{Shards: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.Fanout == nil {
		t.Fatal("fanout missing")
	}
	other, err := notify.NewRemoteFanout("api-2", rt.Transport())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ch, unsub := other.Subscribe("usr_b")
	defer unsub()
	rt.Fanout.Publish([]string{"usr_b"}, notify.Signal{Type: notify.SignalChatMessage, ChatID: "cht_redis"})
	select {
	case s := <-ch:
		if s.ChatID != "cht_redis" {
			t.Fatalf("got %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second process did not receive the signal over Redis")
	}
}
