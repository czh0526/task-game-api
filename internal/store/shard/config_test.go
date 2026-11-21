package shard

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"taskgame/internal/model"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		n       int
		wantErr string
	}{
		{"empty driver defaults to sqlite", Config{DSNs: []string{"file:a.db", "file:b.db"}}, 2, ""},
		{"sqlite list", Config{Driver: DriverSQLite, DSNs: []string{"file:a.db", "file:b.db"}}, 2, ""},
		{"mysql list", Config{Driver: DriverMySQL, DSNs: []string{"u:p@tcp(h0:3306)/s0", "u:p@tcp(h1:3306)/s1"}}, 2, ""},
		{"unsupported driver", Config{Driver: "postgres", DSNs: []string{"a", "b"}}, 2, "unsupported driver"},
		{"single shard rejected", Config{DSNs: []string{"file:a.db"}}, 1, "at least 2 shards"},
		{"too few DSNs", Config{DSNs: []string{"file:a.db"}}, 2, "need 2 shard DSNs"},
		{"too many DSNs", Config{DSNs: []string{"a", "b", "c"}}, 2, "need 2 shard DSNs"},
		{"empty DSN", Config{DSNs: []string{"file:a.db", "  "}}, 2, "DSN 1 is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate(tc.n)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestExpandTemplate(t *testing.T) {
	got, err := ExpandTemplate("file:data/shard-%d.db", 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"file:data/shard-0.db", "file:data/shard-1.db", "file:data/shard-2.db"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dsn %d = %q, want %q", i, got[i], want[i])
		}
	}
	if _, err := ExpandTemplate("file:data/shard.db", 2); err == nil {
		t.Fatal("template without a shard index placeholder should be rejected")
	}
	if _, err := ExpandTemplate("file:data/shard-%d.db", 1); err == nil {
		t.Fatal("single shard should be rejected")
	}
}

// TestOpenConfigWithDSNList proves the per-shard DSN list path works without a
// template. Locally it uses two SQLite files; the same code path is what a
// MySQL DSN list uses (see mysql_test.go).
func TestOpenConfigWithDSNList(t *testing.T) {
	dir := t.TempDir()
	dsns := []string{
		"file:" + filepath.Join(dir, "first.db"),
		"file:" + filepath.Join(dir, "second.db"),
	}
	s, err := OpenConfig(NewRouter(2), Config{DSNs: dsns})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Driver() != DriverSQLite || s.Len() != 2 {
		t.Fatalf("driver=%s len=%d", s.Driver(), s.Len())
	}
	ctx := context.Background()
	now := time.Now().UTC()
	m := model.Message{ID: "msg_list", ChatID: "cht_list", SenderID: "usr_a", Type: "text", Text: "list dsn", CreatedAt: now}
	if err := s.Insert(ctx, 1, m); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, 1, m.ChatID, 50, 0)
	if err != nil || len(got) != 1 || got[0].Text != "list dsn" {
		t.Fatalf("list = %+v, %v", got, err)
	}
	other, err := s.List(ctx, 0, m.ChatID, 50, 0)
	if err != nil || len(other) != 0 {
		t.Fatalf("shard 0 must be empty: %d, %v", len(other), err)
	}
}

func TestOpenConfigRejectsMismatchedList(t *testing.T) {
	_, err := OpenConfig(NewRouter(2), Config{Driver: DriverMySQL, DSNs: []string{"root@tcp(h:3306)/s0"}})
	if err == nil || !strings.Contains(err.Error(), "need 2 shard DSNs") {
		t.Fatalf("want DSN count error, got %v", err)
	}
}
