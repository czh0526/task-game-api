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
func (m *AuthMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := m.authenticate(r)
		if err != nil {
			httpx.ErrorCtx(r.Context(), w, err)
			return
		}
		next(w, r.WithContext(auth.WithUserID(r.Context(), userID)))
	}
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
