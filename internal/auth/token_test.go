package auth

import (
	"testing"
	"time"
)

func TestSignAndParse(t *testing.T) {
	secret := "dev-jwt-secret-change-me-32b"
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	tok, err := SignHS256(secret, "usr_executor", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseHS256(secret, tok, now.Add(30*time.Minute))
	if err != nil || got != "usr_executor" {
		t.Fatalf("parse %q err=%v", got, err)
	}
	if _, err := ParseHS256(secret, tok, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expired token to fail")
	}
	if _, err := ParseHS256("other-secret-value-32bxxxx", tok, now); err == nil {
		t.Fatal("expected wrong secret to fail")
	}
}
