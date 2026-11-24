package svc

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"taskgame/internal/plane"
)

// cryptoRandUserID matches id.New("usr_"): "usr_" + 24 lowercase hex chars.
var cryptoRandUserID = regexp.MustCompile(`^usr_[0-9a-f]{24}$`)

// TestRegistrationIDsWithoutPersistentControlPlane reproduces the bug where
// registration inherited the in-memory control plane's LocalIDGenerator and
// produced "usr_api-1", "usr_api-2", ... Two processes with the same
// InstanceID (and one process restarted) would collide. Without a persistent
// Directory, registration must fall back to crypto/rand ids instead.
func TestRegistrationIDsWithoutPersistentControlPlane(t *testing.T) {
	ctx := context.Background()
	var ids []string
	// Two independent service contexts emulate two API processes sharing the
	// same configured InstanceID. Before the fix both produced usr_api-1.
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		c := directoryTestConfig(dir, "")
		c.Scale.Directory.Enabled = false
		c.Scale.InstanceID = "api-1" // deliberately identical in both "processes"
		sc, err := NewServiceContext(c)
		if err != nil {
			t.Fatalf("process %d: NewServiceContext: %v", i, err)
		}
		if sc.Scale.PersistentIdentity() {
			t.Fatalf("process %d: in-memory control plane must not report persistent identity", i)
		}
		sess, err := sc.Accounts.Register(ctx, "user@example.com", "password123", nil)
		if err != nil {
			t.Fatalf("process %d: register: %v", i, err)
		}
		if !cryptoRandUserID.MatchString(sess.UserID) {
			t.Fatalf("process %d: user id = %q, want crypto/rand usr_<24hex> (got LocalIDGenerator form?)", i, sess.UserID)
		}
		ids = append(ids, sess.UserID)
		sc.Close()
	}
	if ids[0] == ids[1] {
		t.Fatalf("two processes produced the same user id %q", ids[0])
	}
}

// TestRegistrationIDsWithPersistentControlPlane confirms that enabling the
// SQL Directory switches registration to Snowflake ids (sorted, worker-id
// aware) and keeps the process-local email claim + global_unique path.
func TestRegistrationIDsWithPersistentControlPlane(t *testing.T) {
	dir := t.TempDir()
	controlDSN := "file:" + filepath.Join(dir, "control.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	c := directoryTestConfig(dir, controlDSN)
	sc, err := NewServiceContext(c)
	if err != nil {
		t.Fatalf("NewServiceContext: %v", err)
	}
	defer sc.Close()
	if !sc.Scale.PersistentIdentity() {
		t.Fatal("persistent Directory must report persistent identity")
	}
	if _, ok := sc.Scale.ControlPlane.IDs.(*plane.SnowflakeIDGenerator); !ok {
		t.Fatalf("ids = %T, want *plane.SnowflakeIDGenerator", sc.Scale.ControlPlane.IDs)
	}
	ctx := context.Background()
	sess, err := sc.Accounts.Register(ctx, "user@example.com", "password123", nil)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if cryptoRandUserID.MatchString(sess.UserID) {
		t.Fatalf("user id = %q, want Snowflake form, not crypto/rand 24-hex", sess.UserID)
	}
	if len(sess.UserID) <= len("usr_") {
		t.Fatalf("user id = %q, too short", sess.UserID)
	}
	if sess.UserID[:4] != "usr_" {
		t.Fatalf("user id = %q, missing usr_ prefix", sess.UserID)
	}
}
