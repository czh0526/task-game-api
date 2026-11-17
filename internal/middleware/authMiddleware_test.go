package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"taskgame/internal/config"
)

func TestTaskListIsPublicWithoutCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("anonymous list: called=%v status=%d", called, rec.Code)
	}
}

func TestTaskListStillValidatesSuppliedCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{
		ApiKeys: []config.ApiKeyConf{{Key: "good-key", UserId: "usr_publisher"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
	req.Header.Set("X-Api-Key", "bad-key")
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})(rec, req)
	if called || rec.Code == http.StatusOK {
		t.Fatalf("invalid key on public route: called=%v status=%d", called, rec.Code)
	}
}

func TestTaskMutationsStillRequireAuth(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	req := httptest.NewRequest(http.MethodPost, "/v1/tasks", nil)
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})(rec, req)
	if called || rec.Code == http.StatusOK {
		t.Fatalf("anonymous create: called=%v status=%d", called, rec.Code)
	}
}
