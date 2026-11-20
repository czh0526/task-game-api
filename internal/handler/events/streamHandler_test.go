package events

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"taskgame/internal/notify"
)

func TestWriteSignalFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	sig := notify.Signal{Type: notify.SignalChatMessage, ChatID: "cht_1", MessageID: "msg_1", SenderID: "usr_a"}
	if !writeSignal(rec, func() bool { return true }, 42, sig) {
		t.Fatal("writeSignal reported a closed connection")
	}
	body := rec.Body.String()
	for _, want := range []string{"id: 42\n", "event: signal\n", "data: {", `"type":"chat.message"`, `"chat_id":"cht_1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("frame missing %q: %s", want, body)
		}
	}
	line := ""
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "data: ") {
			line = strings.TrimPrefix(l, "data: ")
		}
	}
	var decoded notify.Signal
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("data is not JSON: %v", err)
	}
	if decoded != sig {
		t.Fatalf("decoded %+v, want %+v", decoded, sig)
	}
}

func TestWriteSignalStopsWhenFlushFails(t *testing.T) {
	rec := httptest.NewRecorder()
	if writeSignal(rec, func() bool { return false }, 1, notify.Signal{Type: notify.SignalHello}) {
		t.Fatal("writeSignal should report a dead connection when flush fails")
	}
}
