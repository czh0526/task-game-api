package notify

import "testing"

func TestRemoteFanoutDeliversAcrossProcesses(t *testing.T) {
	bus := NewMemoryTransport()
	defer bus.Close()

	// Two fan-outs emulate two API processes: A serves the sender's browser,
	// B serves the recipient's browser.
	a, err := NewRemoteFanout("api-a", bus)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewRemoteFanout("api-b", bus)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	chB, unsubB := b.Subscribe("usr_b")
	defer unsubB()
	chA, unsubA := a.Subscribe("usr_a")
	defer unsubA()

	a.Publish([]string{"usr_a", "usr_b"}, Signal{Type: SignalChatMessage, ChatID: "cht_1"})

	select {
	case s := <-chB:
		if s.ChatID != "cht_1" {
			t.Fatalf("B got %+v", s)
		}
	default:
		t.Fatal("B did not receive the cross-process signal")
	}
	select {
	case s := <-chA:
		if s.ChatID != "cht_1" {
			t.Fatalf("A got %+v", s)
		}
	default:
		t.Fatal("A did not receive its local signal")
	}
	// The origin must not receive its own echo a second time.
	select {
	case s := <-chA:
		t.Fatalf("A received a duplicated echo: %+v", s)
	default:
	}
}

func TestRemoteFanoutStopUnregisters(t *testing.T) {
	bus := NewMemoryTransport()
	defer bus.Close()
	b, err := NewRemoteFanout("api-b", bus)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := b.Subscribe("usr_b")
	defer unsub()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := NewRemoteFanout("api-a", bus)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	a.Publish([]string{"usr_b"}, Signal{Type: SignalChatMessage})
	select {
	case s := <-ch:
		t.Fatalf("closed fan-out still received %+v", s)
	default:
	}
}

func TestLocalTransportIsNoop(t *testing.T) {
	r, err := NewRemoteFanout("only", LocalTransport{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ch, unsub := r.Subscribe("usr_a")
	defer unsub()
	r.Publish([]string{"usr_a"}, Signal{Type: SignalHello})
	select {
	case s := <-ch:
		if s.Type != SignalHello {
			t.Fatalf("got %+v", s)
		}
	default:
		t.Fatal("local publish did not reach local subscriber")
	}
}
