package engine

import "taskgame/internal/notify"

// SignalNotifier pushes a lightweight hint to browsers connected over SSE.
// Implementations must be safe for concurrent use and must not block the
// request that produced the event.
type SignalNotifier interface {
	Publish(userIDs []string, signal notify.Signal)
}

// WithSignalNotifier attaches the browser fan-out to the engine. Without it
// the engine behaves exactly as before, which keeps every existing test and
// caller unchanged.
func WithSignalNotifier(n SignalNotifier) Option {
	return func(e *Engine) {
		e.notifier = n
	}
}

// publishChatMessage is called by PostMessage only after its transaction has
// committed. It sends ids, not message bodies: the browsers re-read the thread
// over REST, so a dropped hint degrades to a slightly stale screen.
func (e *Engine) publishChatMessage(memberIDs []string, chatID, messageID, senderID string) {
	if e == nil || e.notifier == nil || len(memberIDs) == 0 {
		return
	}
	e.notifier.Publish(memberIDs, notify.Signal{
		Type:      notify.SignalChatMessage,
		ChatID:    chatID,
		MessageID: messageID,
		SenderID:  senderID,
	})
}
