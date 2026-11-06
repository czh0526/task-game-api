// Package account registers and logs in email/password users.
// Tokens are the same HS256 JWTs as `make token`: sub is the user id.
package account

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/store"

	"golang.org/x/crypto/bcrypt"
)

const (
	minPasswordChars = 8
	maxPasswordBytes = 72
	defaultTTL       = 24 * time.Hour
)

// emailPattern is a practical ASCII address: one @, a dotted domain, no spaces.
var emailPattern = regexp.MustCompile(`^[a-z0-9._%+\-]+@[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9\-]*[a-z0-9])?)+$`)

// Session is a newly signed access token plus the public user summary.
type Session struct {
	AccessToken string
	ExpiresIn   int64
	UserID      string
	Email       string
}

// Profile is the caller summary returned by GET /v1/me.
type Profile struct {
	ID    string
	Email string
}

// Service persists password accounts and signs access tokens.
type Service struct {
	store     store.Store
	secret    string
	ttl       time.Duration
	now       func() time.Time
	cost      int
	dummyOnce sync.Once
	dummyHash []byte
}

// Option adjusts a Service. Production uses bcrypt.DefaultCost and time.Now.
type Option func(*Service)

// WithBcryptCost overrides the hash cost. Tests use bcrypt.MinCost.
func WithBcryptCost(cost int) Option {
	return func(s *Service) {
		if cost > 0 {
			s.cost = cost
		}
	}
}

// WithClock overrides the clock used for iat/exp and created_at.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// New builds a Service. ttl <= 0 means 24h, matching `make token`.
func New(st store.Store, secret string, ttl time.Duration, opts ...Option) *Service {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	s := &Service{
		store:  st,
		secret: secret,
		ttl:    ttl,
		now:    time.Now,
		cost:   bcrypt.DefaultCost,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register creates a user and returns a bearer token. Duplicate email is 409 conflict.
func (s *Service) Register(ctx context.Context, email, password string) (Session, error) {
	norm, err := normalizeEmail(email)
	if err != nil {
		return Session{}, err
	}
	if err := validateNewPassword(password); err != nil {
		return Session{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()
	userID := id.New("usr_")
	user, err := s.store.Users().InsertRegistered(ctx, userID, norm, string(hash), now)
	if errors.Is(err, store.ErrConflict) {
		return Session{}, biz.Conflict("email already registered")
	}
	if err != nil {
		return Session{}, err
	}
	return s.sign(user.ID, user.Email, now)
}

// Login checks the password and returns a new bearer token.
// Unknown email and wrong password are both 401, with the same message.
func (s *Service) Login(ctx context.Context, email, password string) (Session, error) {
	norm, err := normalizeEmail(email)
	if err != nil {
		return Session{}, err
	}
	if password == "" {
		return Session{}, biz.Invalid("password is required")
	}
	if len(password) > maxPasswordBytes {
		return Session{}, biz.Invalid("password must be at most 72 bytes")
	}
	user, err := s.store.Users().GetByEmail(ctx, norm)
	if errors.Is(err, store.ErrNotFound) {
		s.dummyCompare(password)
		return Session{}, biz.Unauthorized("invalid email or password")
	}
	if err != nil {
		return Session{}, err
	}
	if user.PasswordHash == "" {
		s.dummyCompare(password)
		return Session{}, biz.Unauthorized("invalid email or password")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return Session{}, biz.Unauthorized("invalid email or password")
	}
	return s.sign(user.ID, user.Email, s.now().UTC())
}

// Current returns the caller's id and email. API-key users with no row have an empty email.
func (s *Service) Current(ctx context.Context, userID string) (Profile, error) {
	if !id.ValidUserID(userID) {
		return Profile{}, biz.Unauthorized("invalid bearer token")
	}
	user, err := s.store.Users().Get(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return Profile{ID: userID}, nil
	}
	if err != nil {
		return Profile{}, err
	}
	return Profile{ID: user.ID, Email: user.Email}, nil
}

func (s *Service) sign(userID, email string, now time.Time) (Session, error) {
	token, err := auth.SignHS256(s.secret, userID, s.ttl, now)
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken: token,
		ExpiresIn:   int64(s.ttl / time.Second),
		UserID:      userID,
		Email:       email,
	}, nil
}

func (s *Service) dummyCompare(password string) {
	s.dummyOnce.Do(func() {
		hash, err := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), s.cost)
		if err != nil {
			panic(err)
		}
		s.dummyHash = hash
	})
	_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
}

func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 || strings.Contains(email, "..") || !emailPattern.MatchString(email) {
		return "", biz.Invalid("invalid email")
	}
	return email, nil
}

func validateNewPassword(password string) error {
	if strings.TrimSpace(password) == "" {
		return biz.Invalid("password is required")
	}
	if utf8.RuneCountInString(password) < minPasswordChars {
		return biz.Invalid("password must be at least 8 characters")
	}
	if len(password) > maxPasswordBytes {
		return biz.Invalid("password must be at most 72 bytes")
	}
	return nil
}
