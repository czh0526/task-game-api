package sqlite

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/store"
)

func TestAvatarRoundTripAndMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "avatar.db")
	st, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	user, err := st.Users().Ensure(ctx, "usr_publisher", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Users().GetAvatar(ctx, user.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("fresh user avatar: got %v, want ErrNotFound", err)
	}
	if err := st.Users().SetAvatar(ctx, "usr_missing", []byte{1}, "v1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user upload: got %v, want ErrNotFound", err)
	}

	first := bytes.Repeat([]byte{0xff, 0xd8, 0x01}, 40)
	if err := st.Users().SetAvatar(ctx, user.ID, first, "2026-11-20T01:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Users().GetAvatar(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, first) || got.UpdatedAt != "2026-11-20T01:00:00.000000000Z" || got.UserID != user.ID {
		t.Fatalf("avatar round trip mismatch: %+v", got)
	}

	second := []byte{0xff, 0xd8, 0x02}
	if err := st.Users().SetAvatar(ctx, user.ID, second, "2026-11-20T02:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	got, err = st.Users().GetAvatar(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Data, second) || got.UpdatedAt != "2026-11-20T02:00:00.000000000Z" {
		t.Fatalf("avatar replacement failed: %+v", got)
	}

	// Reopening the same file must keep the avatar and its columns.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open("file:"+path+"?mode=rwc", 1000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err = reopened.Users().GetAvatar(ctx, user.ID)
	if err != nil {
		t.Fatalf("avatar lost on reopen: %v", err)
	}
	if !bytes.Equal(got.Data, second) {
		t.Fatal("avatar bytes changed after reopen")
	}
}
