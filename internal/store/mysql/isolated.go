package mysql

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"testing"

	"taskgame/internal/store/sqlstore"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// OpenIsolated creates a fresh database for one test and drops it afterwards.
// TASKGAME_MYSQL_DSN is a go-sql-driver DSN. The account needs CREATE and DROP.
// When the variable is unset the test is skipped, unless TASKGAME_MYSQL_REQUIRED=1.
func OpenIsolated(t *testing.T, initial int64) *sqlstore.DB {
	t.Helper()
	base := os.Getenv("TASKGAME_MYSQL_DSN")
	if base == "" {
		if os.Getenv("TASKGAME_MYSQL_REQUIRED") == "1" {
			t.Fatal("TASKGAME_MYSQL_DSN is required")
		}
		t.Skip("TASKGAME_MYSQL_DSN not set")
	}
	cfg, err := mysqldriver.ParseDSN(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBName == "" {
		cfg.DBName = "mysql"
	}
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	name := "tg_" + hex.EncodeToString(raw[:])
	if _, err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec("DROP DATABASE IF EXISTS `" + name + "`")
		_ = admin.Close()
	})
	cfg.DBName = name
	st, err := Open(cfg.FormatDSN(), initial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
