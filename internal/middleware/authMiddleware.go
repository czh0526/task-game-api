package middleware

import (
	"net/http"
	"strings"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/config"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// AuthMiddleware accepts Authorization: Bearer <HS256 JWT> or X-Api-Key.
type AuthMiddleware struct {
	secret string
	keys   map[string]string
}

// NewAuthMiddleware indexes the configured API keys.
func NewAuthMiddleware(cfg config.AuthConf) *AuthMiddleware {
	keys := make(map[string]string, len(cfg.ApiKeys))
	for _, item := range cfg.ApiKeys {
		keys[item.Key] = item.UserId
	}
	return &AuthMiddleware{secret: cfg.JwtSecret, keys: keys}
}

// Handle rejects unauthenticated calls before the handler runs.
// The task market listing, individual task details, and a user's public scores
// are readable without sign-in; credentials, when supplied, are still validated.
func (m *AuthMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if isPublicRead(r) && !hasCredentials(r) {
			next(w, r)
			return
		}
		userID, err := m.authenticate(r)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		next(w, r.WithContext(auth.WithUserID(r.Context(), userID)))
	}
}

// isPublicRead reports GET requests that only read public data: the market
// listing (GET /v1/tasks), a single task detail (GET /v1/tasks/{taskId}), and
// a user's public scores (GET /v1/users/{userId}/scores). Mutations and all
// other routes stay protected.
func isPublicRead(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/v1/tasks" {
		return true
	}
	const tasksPrefix = "/v1/tasks/"
	if strings.HasPrefix(path, tasksPrefix) {
		// Only a single segment after /v1/tasks/ is the detail route; deeper
		// paths (e.g. /v1/tasks/{id}/invites) are not public.
		rest := strings.TrimPrefix(path, tasksPrefix)
		return rest != "" && !strings.Contains(rest, "/")
	}
	const usersPrefix = "/v1/users/"
	if strings.HasPrefix(path, usersPrefix) {
		// Exactly /v1/users/{userId}/scores is public; other user routes and
		// trailing sub-paths stay protected.
		parts := strings.Split(strings.TrimPrefix(path, usersPrefix), "/")
		return len(parts) == 2 && parts[0] != "" && parts[1] == "scores"
	}
	return false
}

func hasCredentials(r *http.Request) bool {
	return strings.TrimSpace(r.Header.Get("X-Api-Key")) != "" ||
		strings.TrimSpace(r.Header.Get("Authorization")) != ""
}

func (m *AuthMiddleware) authenticate(r *http.Request) (string, error) {
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		userID, ok := m.keys[key]
		if !ok {
			return "", biz.Unauthorized("invalid api key")
		}
		return userID, nil
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return "", biz.Unauthorized("bearer token or X-Api-Key required")
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", biz.Unauthorized("invalid bearer token")
	}
	return auth.ParseHS256(m.secret, strings.TrimSpace(token), time.Now())
}
