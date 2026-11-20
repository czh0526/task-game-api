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

func TestTaskDetailIsPublicWithoutCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/tsk_1", nil)
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("anonymous detail: called=%v status=%d", called, rec.Code)
	}
}

func TestTaskDetailStillValidatesSuppliedCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{
		ApiKeys: []config.ApiKeyConf{{Key: "good-key", UserId: "usr_publisher"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/tasks/tsk_1", nil)
	req.Header.Set("X-Api-Key", "bad-key")
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})(rec, req)
	if called || rec.Code == http.StatusOK {
		t.Fatalf("invalid key on detail route: called=%v status=%d", called, rec.Code)
	}
}

func TestNestedTaskRoutesStillRequireAuth(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	for _, path := range []string{"/v1/tasks/tsk_1/invites", "/v1/tasks/tsk_1/accept"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		called := false
		m.Handle(func(w http.ResponseWriter, r *http.Request) {
			called = true
		})(rec, req)
		if called || rec.Code == http.StatusOK {
			t.Fatalf("anonymous nested route %s: called=%v status=%d", path, called, rec.Code)
		}
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

func TestUserScoresIsPublicWithoutCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	req := httptest.NewRequest(http.MethodGet, "/v1/users/usr_123/scores", nil)
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("anonymous scores: called=%v status=%d", called, rec.Code)
	}
}

func TestUserScoresStillValidatesSuppliedCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{
		ApiKeys: []config.ApiKeyConf{{Key: "good-key", UserId: "usr_publisher"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/users/usr_123/scores", nil)
	req.Header.Set("X-Api-Key", "bad-key")
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})(rec, req)
	if called || rec.Code == http.StatusOK {
		t.Fatalf("invalid key on scores route: called=%v status=%d", called, rec.Code)
	}
}

func TestUserAvatarIsPublicWithoutCredentials(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	for _, path := range []string{"/v1/users/usr_123/avatar", "/v1/users/usr_123/avatar/"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		called := false
		m.Handle(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusOK)
		})(rec, req)
		if !called || rec.Code != http.StatusOK {
			t.Fatalf("anonymous avatar %s: called=%v status=%d", path, called, rec.Code)
		}
	}
}

func TestAvatarUploadRequiresAuth(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	req := httptest.NewRequest(http.MethodPut, "/v1/me/avatar", nil)
	rec := httptest.NewRecorder()
	called := false
	m.Handle(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})(rec, req)
	if called || rec.Code == http.StatusOK {
		t.Fatalf("anonymous upload: called=%v status=%d", called, rec.Code)
	}
}

func TestFriendRoutesStillRequireAuth(t *testing.T) {
	m := NewAuthMiddleware(config.AuthConf{})
	for _, path := range []string{"/v1/friends", "/v1/friends/requests"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		called := false
		m.Handle(func(w http.ResponseWriter, r *http.Request) {
			called = true
		})(rec, req)
		if called || rec.Code == http.StatusOK {
			t.Fatalf("anonymous friend route %s: called=%v status=%d", path, called, rec.Code)
		}
	}
}
