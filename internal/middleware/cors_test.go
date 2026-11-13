package middleware

import (
	"net/http"
	"strings"
	"testing"
)

func TestLocalDevOriginsCoverVite(t *testing.T) {
	origins := localDevOrigins()
	set := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		set[origin] = struct{}{}
	}
	for _, origin := range []string{
		"http://localhost:5173",
		"http://127.0.0.1:5173",
		"http://[::1]:5173",
		"http://localhost:4173",
		"http://127.0.0.1:5185",
	} {
		if _, ok := set[origin]; !ok {
			t.Fatalf("missing %s", origin)
		}
	}
	if _, ok := set["https://evil.example"]; ok {
		t.Fatal("non-local origin was allowed")
	}
	if _, ok := set["http://localhost:5172"]; ok {
		t.Fatal("port below the vite range was allowed")
	}
	if _, ok := set["http://localhost:5186"]; ok {
		t.Fatal("port above the vite range was allowed")
	}
}

func TestSetLocalDevCORSHeaders(t *testing.T) {
	header := make(http.Header)
	header.Set("Access-Control-Allow-Headers", "Content-Type")
	setLocalDevCORSHeaders(header)
	got := header.Get("Access-Control-Allow-Headers")
	for _, name := range []string{"X-Api-Key", "Idempotency-Key", "Authorization", "Content-Type", "Accept"} {
		if !strings.Contains(got, name) {
			t.Fatalf("allow headers %q missing %s", got, name)
		}
	}
	if strings.Count(got, "Content-Type") != 1 {
		t.Fatalf("allow headers should replace the previous value, got %q", got)
	}
	methods := header.Get("Access-Control-Allow-Methods")
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(methods, method) {
			t.Fatalf("allow methods %q missing %s", methods, method)
		}
	}
}
