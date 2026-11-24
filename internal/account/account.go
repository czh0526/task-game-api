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
	"unicode"
	"unicode/utf8"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/id"
	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store"

	"golang.org/x/crypto/bcrypt"
)

// emailNamespace is the global_unique namespace used for the email -> user_id
// constraint when a control plane is wired in.
const emailNamespace = "email"

const (
	minPasswordChars = 8
	maxPasswordBytes = 72
	maxNicknameRunes = 32
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
	Nickname    string
}

// Profile is the caller summary returned by GET /v1/me.
type Profile struct {
	ID       string
	Email    string
	Nickname string
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

	// identity is the optional control-plane-backed global constraint. When
	// set, registration reserves email -> user_id before writing the user and
	// login resolves through it, instead of scanning every shard.
	identity *identityBackend
}

// identityBackend bundles the two control-plane pieces registration needs.
type identityBackend struct {
	unique plane.UniqueIndex
	ids    plane.IDGenerator
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

// WithIdentity wires the control plane's global unique index and Snowflake ID
// generator into registration/login. Without it, registration falls back to
// the legacy id.New + store-local email uniqueness path (byte-for-byte the
// original behavior), which keeps every existing deployment and test intact.
func WithIdentity(unique plane.UniqueIndex, ids plane.IDGenerator) Option {
	return func(s *Service) {
		if unique != nil && ids != nil {
			s.identity = &identityBackend{unique: unique, ids: ids}
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
// nickname is optional. Nil leaves it unset. A non-nil value is trimmed and must be 1–32 characters.
func (s *Service) Register(ctx context.Context, email, password string, nickname *string) (Session, error) {
	norm, err := normalizeEmail(email)
	if err != nil {
		return Session{}, err
	}
	if err := validateNewPassword(password); err != nil {
		return Session{}, err
	}
	nick := ""
	if nickname != nil {
		nick, err = NormalizeNickname(*nickname)
		if err != nil {
			return Session{}, err
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.cost)
	if err != nil {
		return Session{}, err
	}
	now := s.now().UTC()

	// Control-plane path: reserve the global email claim and use a Snowflake
	// id before writing the user row. A failed insert releases the claim so a
	// retry can succeed; ErrConflict maps to the same 409 as the legacy path.
	if s.identity != nil {
		userID := s.identity.ids.New("usr_")
		if err := s.identity.unique.Reserve(ctx, emailNamespace, norm, userID); err != nil {
			if errors.Is(err, plane.ErrConflict) {
				return Session{}, biz.Conflict("email already registered")
			}
			return Session{}, err
		}
		user, err := s.store.Users().InsertRegistered(ctx, userID, norm, string(hash), now)
		if err != nil {
			// Best-effort rollback: Release is guarded by owner id, so this
			// never frees another process's claim. Domain routing errors are
			// unreachable here because the row was never committed.
			if rel, ok := s.identity.unique.(interface {
				Release(context.Context, string, string, string) error
			}); ok {
				_ = rel.Release(ctx, emailNamespace, norm, userID)
			}
			if errors.Is(err, store.ErrConflict) {
				return Session{}, biz.Conflict("email already registered")
			}
			return Session{}, err
		}
		if nick != "" {
			if err := s.store.Users().SetNickname(ctx, user.ID, nick); err != nil {
				return Session{}, err
			}
			user.Nickname = nick
		}
		return s.sign(user.ID, user.Email, user.Nickname, now)
	}

	// Legacy path (unchanged).
	userID := id.New("usr_")
	user, err := s.store.Users().InsertRegistered(ctx, userID, norm, string(hash), now)
	if errors.Is(err, store.ErrConflict) {
		return Session{}, biz.Conflict("email already registered")
	}
	if err != nil {
		return Session{}, err
	}
	if nick != "" {
		if err := s.store.Users().SetNickname(ctx, user.ID, nick); err != nil {
			return Session{}, err
		}
		user.Nickname = nick
	}
	return s.sign(user.ID, user.Email, user.Nickname, now)
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
	var user model.User
	if s.identity != nil {
		if owner, ok, oerr := s.identity.unique.Owner(ctx, emailNamespace, norm); oerr != nil {
			return Session{}, oerr
		} else if ok {
			// ResolveExisting through the store routes by user_id and, in
			// domain mode, reads the directory for the authoritative shard.
			users := s.store.Users()
			if cred, supported := users.(interface {
				GetCredentials(context.Context, string) (model.User, error)
			}); supported {
				user, err = cred.GetCredentials(ctx, owner)
			} else {
				user, err = users.Get(ctx, owner)
			}
		} else {
			// Legacy user registered before the global index existed.
			user, err = s.store.Users().GetByEmail(ctx, norm)
		}
	} else {
		user, err = s.store.Users().GetByEmail(ctx, norm)
	}
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
	return s.sign(user.ID, user.Email, user.Nickname, s.now().UTC())
}

// Current returns the caller's id, email, and nickname.
// API-key users with no row have an empty email and nickname.
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
	return Profile{ID: user.ID, Email: user.Email, Nickname: user.Nickname}, nil
}

// UpdateNickname sets the caller's display name. It creates a row for API-key users who do not have one yet.
func (s *Service) UpdateNickname(ctx context.Context, userID, raw string) (Profile, error) {
	if !id.ValidUserID(userID) {
		return Profile{}, biz.Unauthorized("invalid bearer token")
	}
	nick, err := NormalizeNickname(raw)
	if err != nil {
		return Profile{}, err
	}
	if _, err := s.store.Users().Ensure(ctx, userID, s.now().UTC()); err != nil {
		return Profile{}, err
	}
	if err := s.store.Users().SetNickname(ctx, userID, nick); err != nil {
		return Profile{}, err
	}
	return s.Current(ctx, userID)
}

func (s *Service) sign(userID, email, nickname string, now time.Time) (Session, error) {
	token, err := auth.SignHS256(s.secret, userID, s.ttl, now)
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken: token,
		ExpiresIn:   int64(s.ttl / time.Second),
		UserID:      userID,
		Email:       email,
		Nickname:    nickname,
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

// NormalizeNickname trims a display name. It is not unique.
// Empty after trim, control characters, and more than 32 Unicode characters are rejected.
func NormalizeNickname(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxNicknameRunes {
		return "", biz.Invalid("nickname must be 1 to 32 characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", biz.Invalid("nickname must be 1 to 32 characters")
		}
	}
	return name, nil
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
