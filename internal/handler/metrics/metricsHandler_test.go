package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"taskgame/internal/metrics"
)

func TestMetricsHandler(t *testing.T) {
	metrics.Reset()
	metrics.Inc(metrics.LookupTotal, 7)
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	MetricsHandler()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(rec.Body.String(), metrics.LookupTotal+" 7") {
		t.Fatalf("body missing counter:\n%s", rec.Body.String())
	}
}
