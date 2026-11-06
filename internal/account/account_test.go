package account

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskgame/internal/auth"
	"taskgame/internal/biz"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"

	"golang.org/x/crypto/bcrypt"
)

func newService(t *testing.T, now time.Time) (*Service, store.Store) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := New(st, "dev-jwt-secret-change-me-32b", time.Hour, WithBcryptCost(bcrypt.MinCost), WithClock(func() time.Time {
		return now
	}))
	return svc, st
}

func TestRegisterLoginAndTokenShape(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	svc, st := newService(t, now)

	session, err := svc.Register(ctx, "  Publisher@Example.com ", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if session.Email != "publisher@example.com" || !strings.HasPrefix(session.UserID, "usr_") {
		t.Fatalf("session %+v", session)
	}
	if session.ExpiresIn != 3600 {
		t.Fatalf("expires_in %d", session.ExpiresIn)
	}
	sub, err := auth.ParseHS256("dev-jwt-secret-change-me-32b", session.AccessToken, now.Add(time.Minute))
	if err != nil || sub != session.UserID {
		t.Fatalf("parse sub %q err %v", sub, err)
	}
	if _, err := auth.ParseHS256("dev-jwt-secret-change-me-32b", session.AccessToken, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expired token to fail")
	}

	stored, err := st.Users().GetByEmail(ctx, "publisher@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == "" || stored.PasswordHash == "password123" || !strings.HasPrefix(stored.PasswordHash, "$2") {
		t.Fatalf("password hash was not bcrypt: %q", stored.PasswordHash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("password123")); err != nil {
		t.Fatal(err)
	}
	visible, err := st.Users().Get(ctx, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if visible.PasswordHash != "" {
		t.Fatal("Get leaked the password hash")
	}
	if visible.Balance != 1_000_000 || visible.Email != "publisher@example.com" {
		t.Fatalf("user %+v", visible)
	}

	if _, err := svc.Register(ctx, "publisher@example.com", "password123"); codeOf(err) != "conflict" {
		t.Fatalf("duplicate err %v", err)
	}
	if _, err := svc.Login(ctx, "publisher@example.com", "nope-nope"); codeOf(err) != "unauthorized" {
		t.Fatalf("bad password %v", err)
	}
	if _, err := svc.Login(ctx, "missing@example.com", "password123"); codeOf(err) != "unauthorized" {
		t.Fatalf("missing email %v", err)
	}
	logged, err := svc.Login(ctx, "PUBLISHER@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if logged.UserID != session.UserID {
		t.Fatalf("login id %s want %s", logged.UserID, session.UserID)
	}
	sub, err = auth.ParseHS256("dev-jwt-secret-change-me-32b", logged.AccessToken, now.Add(time.Minute))
	if err != nil || sub != session.UserID {
		t.Fatalf("login token %q %v", sub, err)
	}

	me, err := svc.Current(ctx, session.UserID)
	if err != nil || me.Email != "publisher@example.com" || me.ID != session.UserID {
		t.Fatalf("me %+v %v", me, err)
	}
	dev, err := st.Users().Ensure(ctx, "usr_publisher", now)
	if err != nil || dev.Email != "" {
		t.Fatalf("dev user %+v %v", dev, err)
	}
	if _, err := st.Users().Ensure(ctx, "usr_executor", now); err != nil {
		t.Fatal(err)
	}
	ghost, err := svc.Current(ctx, "usr_executor")
	if err != nil || ghost.ID != "usr_executor" || ghost.Email != "" {
		t.Fatalf("api key profile %+v %v", ghost, err)
	}
}

func TestRegisterValidation(t *testing.T) {
	ctx := context.Background()
	svc, _ := newService(t, time.Now().UTC())
	cases := []struct {
		email, password, code string
	}{
		{"not-an-email", "password123", "invalid_argument"},
		{"a@b", "password123", "invalid_argument"},
		{"ok@example.com", "short", "invalid_argument"},
		{"ok@example.com", "        ", "invalid_argument"},
		{"ok@example.com", strings.Repeat("a", 73), "invalid_argument"},
	}
	for _, tc := range cases {
		_, err := svc.Register(ctx, tc.email, tc.password)
		if codeOf(err) != tc.code {
			t.Fatalf("register %q %q => %v", tc.email, tc.password, err)
		}
	}
	if _, err := svc.Login(ctx, "not-an-email", "password123"); codeOf(err) != "invalid_argument" {
		t.Fatalf("login email %v", err)
	}
	if _, err := svc.Login(ctx, "ok@example.com", ""); codeOf(err) != "invalid_argument" {
		t.Fatalf("login empty password %v", err)
	}
}

func codeOf(err error) string {
	var be *biz.Error
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}
