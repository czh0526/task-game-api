package account

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskgame/internal/plane"
	"taskgame/internal/store/sqlite"

	"golang.org/x/crypto/bcrypt"
)

func newIdentityService(t *testing.T, now time.Time) (*Service, plane.UniqueIndex, plane.IDGenerator) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	unique := plane.NewInMemoryUnique()
	ids, err := plane.NewSnowflakeIDGenerator(context.Background(), unique, "api-test")
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, "dev-jwt-secret-change-me-32b", time.Hour,
		WithBcryptCost(bcrypt.MinCost),
		WithClock(func() time.Time { return now }),
		WithIdentity(unique, ids),
	)
	return svc, unique, ids
}

func TestIdentityRegisterLoginConflict(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	svc, unique, _ := newIdentityService(t, now)

	s1, err := svc.Register(ctx, " Publisher@Example.com ", "password123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s1.UserID, "usr_") || len(s1.UserID) <= len("usr_") {
		t.Fatalf("snowflake user id not shaped: %q", s1.UserID)
	}
	owner, ok, err := unique.Owner(ctx, emailNamespace, "publisher@example.com")
	if err != nil || !ok || owner != s1.UserID {
		t.Fatalf("email claim: %q %v %v (owner %q)", owner, ok, err, s1.UserID)
	}

	// Login resolves through the global claim, not a shard scan.
	login, err := svc.Login(ctx, "publisher@example.com", "password123")
	if err != nil || login.UserID != s1.UserID {
		t.Fatalf("login: %+v err=%v", login, err)
	}
	if _, err := svc.Login(ctx, "publisher@example.com", "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	// Duplicate email is a conflict and does not change the owner.
	if _, err := svc.Register(ctx, "publisher@example.com", "password123", nil); err == nil {
		t.Fatal("duplicate email accepted")
	}
	owner2, _, _ := unique.Owner(ctx, emailNamespace, "publisher@example.com")
	if owner2 != s1.UserID {
		t.Fatalf("duplicate changed owner: %q -> %q", s1.UserID, owner2)
	}
}

func TestIdentityLegacyUserFallsBackToEmailScan(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	// Build one service without identity to create a legacy user.
	base, st := newService(t, now)
	legacy, err := base.Register(ctx, "legacy@example.com", "password123", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Now add identity to the same store via a new service.
	unique := plane.NewInMemoryUnique()
	ids, err := plane.NewSnowflakeIDGenerator(ctx, unique, "api-legacy-test")
	if err != nil {
		t.Fatal(err)
	}
	withIdentity := New(st, "dev-jwt-secret-change-me-32b", time.Hour,
		WithBcryptCost(bcrypt.MinCost), WithIdentity(unique, ids))
	session, err := withIdentity.Login(ctx, "legacy@example.com", "password123")
	if err != nil || session.UserID != legacy.UserID {
		t.Fatalf("legacy login: %+v err=%v", session, err)
	}
}
