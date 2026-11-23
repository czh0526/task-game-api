package middleware

import (
	"net/http"
	"strings"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/config"
	"taskgame/internal/store/cell"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// AuthMiddleware accepts Authorization: Bearer <HS256 JWT> or X-Api-Key.
type AuthMiddleware struct {
	secret string
	keys   map[string]string
	// loc is nil for a single-database deployment. In cell mode it pins each
	// request to the database that owns the aggregate root in the URL (or the
	// authenticated user when there is no id).
	loc *cell.Locator
}

// NewAuthMiddleware indexes the configured API keys. An optional cell Locator
// enables horizontal routing; single-database callers omit it.
func NewAuthMiddleware(cfg config.AuthConf, loc ...*cell.Locator) *AuthMiddleware {
	keys := make(map[string]string, len(cfg.ApiKeys))
	for _, item := range cfg.ApiKeys {
		keys[item.Key] = item.UserId
	}
	m := &AuthMiddleware{secret: cfg.JwtSecret, keys: keys}
	if len(loc) > 0 {
		m.loc = loc[0]
	}
	return m
}

// cellRoute pins the request context to one cell. It runs after the user id is
// placed on the context, so both authenticated and anonymous public reads can
// be routed. A request with no key at all stays unpinned; list reads fan out.
func (m *AuthMiddleware) cellRoute(next http.HandlerFunc) http.HandlerFunc {
	if m.loc == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		key, kind := cell.KeyFromPath(r.URL.Path)
		if key == "" {
			if uid, ok := auth.UserID(ctx); ok {
				key, kind = uid, cell.KindUser
			}
		}
		if key != "" {
			ctx = cell.WithHint(ctx, key, kind)
			ctx = cell.WithCell(ctx, m.loc.CellFor(ctx, key, kind))
		}
		next(w, r.WithContext(ctx))
	}
}

// Handle rejects unauthenticated calls before the handler runs.
// The task market listing, individual task details, a user's public scores,
// and stored avatars are readable without sign-in; credentials, when supplied,
// are still validated.
func (m *AuthMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	routed := m.cellRoute(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if isPublicRead(r) && !hasCredentials(r) {
			routed(w, r)
			return
		}
		userID, err := m.authenticate(r)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		routed(w, r.WithContext(auth.WithUserID(r.Context(), userID)))
	}
}

// isPublicRead reports GET requests that only read public data: the market
// listing (GET /v1/tasks), a single task detail (GET /v1/tasks/{taskId}), a
// user's public scores (GET /v1/users/{userId}/scores), and that user's stored
// avatar (GET /v1/users/{userId}/avatar). Mutations and all other routes stay
// protected.
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
		// Exactly /v1/users/{userId}/scores and /v1/users/{userId}/avatar are
		// public; other user routes and trailing sub-paths stay protected.
		parts := strings.Split(strings.TrimPrefix(path, usersPrefix), "/")
		return len(parts) == 2 && parts[0] != "" && (parts[1] == "scores" || parts[1] == "avatar")
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
