// Package events serves the browser's server-sent event stream.
package events

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/notify"
	"taskgame/internal/svc"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// heartbeatInterval keeps proxies from closing an idle stream. The comment
// frame is ignored by the client parser.
const heartbeatInterval = 15 * time.Second

// StreamHandler pushes lightweight signals to the signed-in user's browser.
// It is registered with rest.WithSSE(), which clears the server write deadline
// and sets the Content-Type / Cache-Control / Connection headers for us.
//
// The stream carries ids only, never message bodies. Clients re-read their
// threads over REST, so a lost frame degrades to a slightly stale screen
// until the fallback poll (or the next resync) runs.
func StreamHandler(sc *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := auth.Require(r.Context())
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}

		// Nginx and similar proxies buffer text/event-stream unless told not to.
		w.Header().Set("X-Accel-Buffering", "no")
		rc := http.NewResponseController(w)
		flush := func() bool { return rc.Flush() == nil }

		ch, unsubscribe := sc.Notifier.Subscribe(userID)
		defer unsubscribe()

		// The first frame forces the headers through any proxy. On a reconnect
		// we send resync instead, which makes the client re-read its threads
		// and so covers anything published while it was disconnected.
		hello := notify.Signal{Type: notify.SignalHello}
		if r.Header.Get("Last-Event-ID") != "" {
			hello.Type = notify.SignalResync
		}
		if !writeSignal(w, flush, sc.Notifier.NextID(), hello) {
			return
		}

		heartbeat := time.NewTicker(heartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case signal, ok := <-ch:
				if !ok || !writeSignal(w, flush, sc.Notifier.NextID(), signal) {
					return
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || !flush() {
					return
				}
			}
		}
	}
}

// writeSignal emits one SSE frame. A false return means the connection is gone.
func writeSignal(w http.ResponseWriter, flush func() bool, id uint64, signal notify.Signal) bool {
	body, err := json.Marshal(signal)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "id: %d\nevent: signal\ndata: %s\n\n", id, body); err != nil {
		return false
	}
	return flush()
}
