// Package auth parses dev JWTs and stores the caller id on the request context.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"taskgame/internal/biz"
	"taskgame/internal/id"

	"github.com/golang-jwt/jwt/v5"
)

type ctxKey struct{}

// WithUserID returns a context that carries the authenticated user id.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, userID)
}

// UserID returns the authenticated user id, if middleware stored one.
func UserID(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok && v != ""
}

// Require returns the caller id or an unauthorized error.
func Require(ctx context.Context) (string, error) {
	uid, ok := UserID(ctx)
	if !ok {
		return "", biz.Unauthorized("bearer token or X-Api-Key required")
	}
	return uid, nil
}

// SignHS256 mints a dev token. sub is the user id. exp is now+ttl.
func SignHS256(secret, userID string, ttl time.Duration, now time.Time) (string, error) {
	if len(secret) < 16 {
		return "", errors.New("jwt secret must be at least 16 characters")
	}
	if !id.ValidUserID(userID) {
		return "", fmt.Errorf("invalid user id %q", userID)
	}
	if ttl <= 0 {
		return "", errors.New("ttl must be positive")
	}
	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}
	return signed, nil
}

// ParseHS256 validates an HS256 token and returns sub.
func ParseHS256(secret, token string, now time.Time) (string, error) {
	if len(secret) < 16 || token == "" {
		return "", biz.Unauthorized("invalid bearer token")
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	parsed, err := parser.ParseWithClaims(token, &jwt.RegisteredClaims{}, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, biz.Unauthorized("invalid bearer token")
		}
		return []byte(secret), nil
	})
	if err != nil || !parsed.Valid {
		return "", biz.Unauthorized("invalid bearer token")
	}
	claims, ok := parsed.Claims.(*jwt.RegisteredClaims)
	if !ok || !id.ValidUserID(claims.Subject) {
		return "", biz.Unauthorized("invalid bearer token")
	}
	return claims.Subject, nil
}
