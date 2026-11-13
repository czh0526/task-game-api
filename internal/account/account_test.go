package account

import (
	"context"
	"errors"
	"fmt"
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

	session, err := svc.Register(ctx, "  Publisher@Example.com ", "password123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if session.Email != "publisher@example.com" || session.Nickname != "" || !strings.HasPrefix(session.UserID, "usr_") {
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

	if _, err := svc.Register(ctx, "publisher@example.com", "password123", nil); codeOf(err) != "conflict" {
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
	if err != nil || me.Email != "publisher@example.com" || me.ID != session.UserID || me.Nickname != "" {
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
		_, err := svc.Register(ctx, tc.email, tc.password, nil)
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

func TestNicknameRegisterAndUpdate(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	svc, st := newService(t, now)

	shared := "同名"
	first, err := svc.Register(ctx, "one@example.com", "password123", &shared)
	if err != nil || first.Nickname != "同名" {
		t.Fatalf("register nickname %+v %v", first, err)
	}
	second, err := svc.Register(ctx, "two@example.com", "password123", &shared)
	if err != nil || second.Nickname != "同名" || second.UserID == first.UserID {
		t.Fatalf("duplicate display name should be allowed %+v %v", second, err)
	}
	trimmed, err := svc.Register(ctx, "three@example.com", "password123", strPtr("  阿宁  "))
	if err != nil || trimmed.Nickname != "阿宁" {
		t.Fatalf("trim %+v %v", trimmed, err)
	}
	plain, err := svc.Register(ctx, "four@example.com", "password123", nil)
	if err != nil || plain.Nickname != "" {
		t.Fatalf("omitted nickname %+v %v", plain, err)
	}

	updated, err := svc.UpdateNickname(ctx, plain.UserID, "  阿宁同学  ")
	if err != nil || updated.Nickname != "阿宁同学" || updated.Email != "four@example.com" {
		t.Fatalf("update %+v %v", updated, err)
	}
	again, err := svc.UpdateNickname(ctx, plain.UserID, "小林")
	if err != nil || again.Nickname != "小林" {
		t.Fatalf("second update %+v %v", again, err)
	}
	me, err := svc.Current(ctx, plain.UserID)
	if err != nil || me.Nickname != "小林" {
		t.Fatalf("current %+v %v", me, err)
	}
	logged, err := svc.Login(ctx, "four@example.com", "password123")
	if err != nil || logged.Nickname != "小林" {
		t.Fatalf("login nickname %+v %v", logged, err)
	}

	dev, err := svc.UpdateNickname(ctx, "usr_publisher", "发布者")
	if err != nil || dev.Nickname != "发布者" || dev.Email != "" || dev.ID != "usr_publisher" {
		t.Fatalf("api key nickname %+v %v", dev, err)
	}
	kept, err := st.Users().Ensure(ctx, "usr_publisher", now)
	if err != nil || kept.Nickname != "发布者" {
		t.Fatalf("ensure cleared nickname %+v %v", kept, err)
	}

	bad := []string{"", "   ", strings.Repeat("名", 33), "a\nb", "ok\u0001"}
	for i, nick := range bad {
		if _, err := svc.UpdateNickname(ctx, plain.UserID, nick); codeOf(err) != "invalid_argument" {
			t.Fatalf("update %q => %v", nick, err)
		}
		email := fmt.Sprintf("bad-nick-%d@example.com", i)
		if _, err := svc.Register(ctx, email, "password123", &nick); codeOf(err) != "invalid_argument" {
			t.Fatalf("register %q => %v", nick, err)
		}
		if _, err := st.Users().GetByEmail(ctx, email); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("invalid nickname %q still registered: %v", nick, err)
		}
	}
	still, err := svc.Current(ctx, plain.UserID)
	if err != nil || still.Nickname != "小林" {
		t.Fatalf("invalid update changed nickname %+v %v", still, err)
	}
}

func strPtr(s string) *string { return &s }

func codeOf(err error) string {
	var be *biz.Error
	if errors.As(err, &be) {
		return be.Code
	}
	return ""
}
