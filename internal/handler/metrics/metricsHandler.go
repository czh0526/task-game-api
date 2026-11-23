package metrics

import (
	"net/http"

	"taskgame/internal/metrics"
)

// MetricsHandler exposes the in-process counters in Prometheus text format.
// It is intentionally unauthenticated like /health so a scraper can reach it.
func MetricsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := metrics.Handler(w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
