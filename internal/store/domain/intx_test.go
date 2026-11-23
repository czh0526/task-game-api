package domain

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"taskgame/internal/plane"
	"taskgame/internal/store"
	"taskgame/internal/store/sqlite"
)

// TestSQLTxHandleExposesLiveTx verifies that txRepos exposes its live SQL
// transaction and dialect, which AssignInTx needs for the co-located write.
func TestSQLTxHandleExposesLiveTx(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	// Open a minimal sqlstore DB through the domain pool builder.
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Co-located control plane: apply the same schema the directory uses.
	dir, err := plane.OpenSQLDirectory(ctx, "sqlite", dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()

	// Outside a transaction, plain DB repos do not implement SQLTx.
	if _, ok := any(st).(store.SQLTx); ok {
		t.Fatal("plain store should not expose a transaction handle")
	}
	called := false
	err = st.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		cap, ok := r.(store.SQLTx)
		if !ok {
			return errors.New("tx repos do not expose SQLTx")
		}
		handle, dialect := cap.SQLTxHandle()
		tx, ok := handle.(*sql.Tx)
		if !ok || tx == nil || dialect == "" {
			return errors.New("invalid tx handle")
		}
		called = true
		// A directory row written in this tx is visible to AssignInTx.
		return plane.AssignInTx(ctx, tx, dialect, plane.DomainUser, "usr_intx", 0, time.Now().UTC())
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("transaction callback did not run")
	}
}

// TestAssignInTxRollsBackWithBusiness verifies the core guarantee: when the
// business callback fails, the directory row written in the same tx vanishes.
func TestAssignInTxRollsBackWithBusiness(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?mode=rwc&_pragma=busy_timeout(5000)"
	st, err := sqlite.Open(dsn, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := plane.OpenSQLDirectory(ctx, "sqlite", dsn, nil); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("business failure")
	err = st.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		handle, dialect := r.(store.SQLTx).SQLTxHandle()
		if e := plane.AssignInTx(ctx, handle.(*sql.Tx), dialect, plane.DomainUser, "usr_rollback", 0, time.Now().UTC()); e != nil {
			return e
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want business error, got %v", err)
	}
	// Re-open a directory on the same file and confirm the row is gone.
	dir, err := plane.OpenSQLDirectory(ctx, "sqlite", dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if _, ok, _ := dir.Lookup(ctx, plane.DomainUser, "usr_rollback"); ok {
		t.Fatal("directory row survived a rolled-back business transaction")
	}
}
