package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"taskgame/internal/config"
	domainpkg "taskgame/internal/store/domain"
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

func TestDomainRouteIsAttachedForAuthenticatedRequests(t *testing.T) {
	m := NewAuthMiddlewareWithDomain(config.AuthConf{
		ApiKeys: []config.ApiKeyConf{{Key: "domain-key", UserId: "usr_domain"}},
	}, true)
	cases := []struct {
		path   string
		domain string
		key    string
	}{
		{"/v1/tasks/tsk_1", "task", "tsk_1"},
		{"/v1/chats/cht_1/messages", "message", "cht_1"},
		{"/v1/tasks", "task", "usr_domain"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		req.Header.Set("X-Api-Key", "domain-key")
		rec := httptest.NewRecorder()
		var gotDomain, gotKey string
		m.Handle(func(w http.ResponseWriter, r *http.Request) {
			route, ok := domainpkg.RouteFrom(r.Context())
			if ok {
				gotDomain, gotKey = string(route.Domain), route.Key
			}
		})(rec, req)
		if gotDomain != tc.domain || gotKey != tc.key {
			t.Fatalf("%s: route = %s/%s, want %s/%s", tc.path, gotDomain, gotKey, tc.domain, tc.key)
		}
	}
}
