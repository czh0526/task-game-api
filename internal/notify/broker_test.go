package notify

import (
	"sync"
	"testing"
)

func TestBrokerFanOutAndDedupe(t *testing.T) {
	b := New()
	alice, stopAlice := b.Subscribe("usr_a")
	bob, stopBob := b.Subscribe("usr_b")
	aliceAgain, stopAliceAgain := b.Subscribe("usr_a")
	defer stopAlice()
	defer stopBob()
	defer stopAliceAgain()

	if got := b.Subscribers("usr_a"); got != 2 {
		t.Fatalf("usr_a subscribers = %d, want 2", got)
	}

	sig := Signal{Type: SignalChatMessage, ChatID: "cht_1", MessageID: "msg_1"}
	// A duplicate owner id must not deliver twice to the same subscriber.
	b.Publish([]string{"usr_a", "usr_a", "usr_b", ""}, sig)

	for name, ch := range map[string]<-chan Signal{"alice": alice, "alice again": aliceAgain, "bob": bob} {
		select {
		case got := <-ch:
			if got != sig {
				t.Fatalf("%s got %+v, want %+v", name, got, sig)
			}
		default:
			t.Fatalf("%s received no signal", name)
		}
		select {
		case extra := <-ch:
			t.Fatalf("%s received a duplicate signal %+v", name, extra)
		default:
		}
	}
}

func TestBrokerUnsubscribeIsIdempotentAndClosesChannel(t *testing.T) {
	b := New()
	ch, stop := b.Subscribe("usr_a")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stop()
		}()
	}
	wg.Wait()

	if got := b.Subscribers("usr_a"); got != 0 {
		t.Fatalf("subscribers after unsubscribe = %d, want 0", got)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
	// Publishing after unsubscribe must not panic on a closed channel.
	b.Publish([]string{"usr_a"}, Signal{Type: SignalChatMessage})
}

func TestBrokerSlowSubscriberDoesNotBlock(t *testing.T) {
	b := New()
	ch, stop := b.Subscribe("usr_a")
	defer stop()
	for i := 0; i < subscriberBuffer+10; i++ {
		b.Publish([]string{"usr_a"}, Signal{Type: SignalChatMessage, MessageID: "msg"})
	}
	if got := len(ch); got != subscriberBuffer {
		t.Fatalf("buffered signals = %d, want %d", got, subscriberBuffer)
	}
}

func TestBrokerNextIDIsMonotonic(t *testing.T) {
	b := New()
	first := b.NextID()
	second := b.NextID()
	if second <= first {
		t.Fatalf("NextID not monotonic: %d then %d", first, second)
	}
}

func TestNilBrokerIsSafe(t *testing.T) {
	var b *Broker
	b.Publish([]string{"usr_a"}, Signal{Type: SignalHello})
}
