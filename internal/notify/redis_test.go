package notify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// miniredis is an in-process Redis, so these tests exercise the real RESP
// pub/sub path without starting any external service or leaving a listener.
func newRedisTransportForTest(t *testing.T) *RedisTransport {
	t.Helper()
	tr, err := NewRedisTransport(RedisConfig{Addr: miniredis.RunT(t).Addr(), Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewRedisTransport: %v", err)
	}
	return tr
}

func expectSignal(t *testing.T, ch <-chan Signal, wantChat string) {
	t.Helper()
	select {
	case s := <-ch:
		if s.ChatID != wantChat {
			t.Fatalf("got %+v, want chat %q", s, wantChat)
		}
		if s.MessageID != "msg_1" || s.SenderID != "usr_a" {
			t.Fatalf("signal fields lost in transit: %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a cross-process signal")
	}
}

func TestRedisTransportDeliversAcrossProcesses(t *testing.T) {
	tr := newRedisTransportForTest(t)
	defer tr.Close()

	// One RedisTransport shared by two fan-outs == two API processes attached
	// to the same bus, each with its own Redis subscription.
	a, err := NewRemoteFanout("api-a", tr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewRemoteFanout("api-b", tr)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	chB, unsubB := b.Subscribe("usr_b")
	defer unsubB()
	chA, unsubA := a.Subscribe("usr_a")
	defer unsubA()

	a.Publish([]string{"usr_a", "usr_b"}, Signal{
		Type: SignalChatMessage, ChatID: "cht_1", MessageID: "msg_1", SenderID: "usr_a",
	})
	expectSignal(t, chB, "cht_1") // B receives via Redis
	expectSignal(t, chA, "cht_1") // A receives locally
	// The origin must not receive its own echo a second time.
	select {
	case s := <-chA:
		t.Fatalf("A received a duplicated echo: %+v", s)
	case <-time.After(150 * time.Millisecond):
	}
}

// Two independent RedisTransport values model two real machines most closely.
func TestTwoRedisTransportsShareTheBus(t *testing.T) {
	mr := miniredis.RunT(t)
	trA, err := NewRedisTransport(RedisConfig{Addr: mr.Addr(), Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer trA.Close()
	trB, err := NewRedisTransport(RedisConfig{Addr: mr.Addr(), Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer trB.Close()

	a, err := NewRemoteFanout("api-a", trA)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewRemoteFanout("api-b", trB)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	chB, unsubB := b.Subscribe("usr_b")
	defer unsubB()
	chA, unsubA := a.Subscribe("usr_a")
	defer unsubA()

	a.Publish([]string{"usr_b"}, Signal{Type: SignalChatMessage, ChatID: "cht_1", MessageID: "msg_1", SenderID: "usr_a"})
	expectSignal(t, chB, "cht_1")
	b.Publish([]string{"usr_a"}, Signal{Type: SignalChatMessage, ChatID: "cht_2", MessageID: "msg_1", SenderID: "usr_a"})
	expectSignal(t, chA, "cht_2")
}

func TestRedisTransportStopUnregisters(t *testing.T) {
	tr := newRedisTransportForTest(t)
	defer tr.Close()
	b, err := NewRemoteFanout("api-b", tr)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := b.Subscribe("usr_b")
	defer unsub()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := NewRemoteFanout("api-a", tr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Publish([]string{"usr_b"}, Signal{Type: SignalChatMessage, ChatID: "cht_1"})
	select {
	case s := <-ch:
		t.Fatalf("closed fan-out still received %+v", s)
	case <-time.After(150 * time.Millisecond):
	}
}

// A foreign writer on the same channel must not stall our receive loop.
func TestRedisTransportIgnoresCorruptFrames(t *testing.T) {
	tr := newRedisTransportForTest(t)
	defer tr.Close()
	b, err := NewRemoteFanout("api-b", tr)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ch, unsub := b.Subscribe("usr_b")
	defer unsub()

	for _, bad := range []string{"{not-json", `{"origin":"x"}`, `{}`} {
		if err := tr.pub.Publish(context.Background(), tr.channel, bad).Err(); err != nil {
			t.Fatal(err)
		}
	}
	a, err := NewRemoteFanout("api-a", tr)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Publish([]string{"usr_b"}, Signal{Type: SignalChatMessage, ChatID: "cht_ok", MessageID: "msg_1", SenderID: "usr_a"})
	expectSignal(t, ch, "cht_ok")
}

func TestDecodeEnvelope(t *testing.T) {
	env := Envelope{
		Origin:  "api-a",
		UserIDs: []string{"usr_a", "usr_b"},
		Signal:  Signal{Type: SignalChatMessage, ChatID: "cht_1", MessageID: "msg_1"},
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Origin != "api-a" || len(got.UserIDs) != 2 || got.Signal.ChatID != "cht_1" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	for _, bad := range []string{"{", `{"origin":"x"}`, `{}`} {
		if _, err := decodeEnvelope([]byte(bad)); err == nil {
			t.Fatalf("accepted invalid envelope %q", bad)
		}
	}
}

func TestRedisTransportValidationAndClose(t *testing.T) {
	if _, err := NewRedisTransport(RedisConfig{}); err == nil {
		t.Fatal("missing Addr must fail")
	}
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	tr, err := NewRedisTransport(RedisConfig{Addr: addr, Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Channel() != DefaultRedisChannel {
		t.Fatalf("default channel = %q", tr.Channel())
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Publish(context.Background(), Envelope{Origin: "x", UserIDs: []string{"u"}}); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("publish after close: %v", err)
	}
	if _, err := tr.Subscribe(context.Background(), func(Envelope) {}); !errors.Is(err, ErrTransportClosed) {
		t.Fatalf("subscribe after close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("double close: %v", err)
	}
}

func TestNewRedisTransportFailsFastWhenRedisDown(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	if _, err := NewRedisTransport(RedisConfig{Addr: addr, Timeout: 300 * time.Millisecond}); err == nil {
		t.Fatal("expected PING to fail when Redis is down")
	}
}
