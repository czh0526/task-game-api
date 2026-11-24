package shardmigrate

import (
	"context"
	"fmt"
	"strings"

	"taskgame/internal/plane"
)

// EntityTable describes one table that belongs to an aggregate root. Columns
// are copied byte-for-byte: the user/task domains contain timestamps, JSON and
// BLOBs but no auto-increment ordering column, so re-serialising through model
// structs would only create opportunities for drift.
type EntityTable struct {
	Name    string
	Columns []string
	Where   string // WHERE clause, always with exactly one ? argument
	OrderBy string
	PK      []string
	// Cleanup is false for rows that are deliberately duplicated in two
	// shards (friendships). They must be copied to the new owner but the old
	// copy may still be the peer's only local view, so it is never deleted.
	Cleanup bool
	// Args supplies WHERE arguments. Nil means a single key argument.
	Args func(string) []any
}

func (t EntityTable) argsFor(key string) []any {
	if t.Args != nil {
		return t.Args(key)
	}
	return []any{key}
}

// EntitySpec is one migratable aggregate-root family.
type EntitySpec struct {
	Domain    plane.Domain
	Primary   string // table holding one row per aggregate root
	KeyColumn string // primary key column of Primary
	Tables    []EntityTable
}

func (t EntityTable) selectSQL() string {
	return `SELECT ` + strings.Join(t.Columns, ", ") + ` FROM ` + t.Name +
		` WHERE ` + t.Where + ` ORDER BY ` + t.OrderBy
}

func (t EntityTable) insertSQL() string {
	marks := make([]string, len(t.Columns))
	for i := range marks {
		marks[i] = "?"
	}
	return `INSERT INTO ` + t.Name + ` (` + strings.Join(t.Columns, ", ") + `) VALUES (` + strings.Join(marks, ", ") + `)`
}

func (t EntityTable) pkWhere() string {
	parts := make([]string, len(t.PK))
	for i, col := range t.PK {
		parts[i] = col + " = ?"
	}
	return strings.Join(parts, ` AND `)
}

func (t EntityTable) existsSQL() string {
	return `SELECT 1 FROM ` + t.Name + ` WHERE ` + t.pkWhere()
}

func (t EntityTable) selectByPKSQL() string {
	return `SELECT ` + strings.Join(t.Columns, ", ") + ` FROM ` + t.Name + ` WHERE ` + t.pkWhere()
}

type sqlRow struct {
	values []any
}

func (r sqlRow) normalized() string {
	parts := make([]string, len(r.values))
	for i, v := range r.values {
		switch x := v.(type) {
		case nil:
			parts[i] = "\x00NULL"
		case []byte:
			parts[i] = string(x)
		default:
			parts[i] = fmt.Sprint(x)
		}
	}
	return strings.Join(parts, "\x00")
}

func (t EntityTable) pkValues(row sqlRow) ([]any, error) {
	out := make([]any, 0, len(t.PK))
	for _, col := range t.PK {
		idx := -1
		for i, name := range t.Columns {
			if name == col {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("migrate: table %s PK column %s is not selected", t.Name, col)
		}
		out = append(out, row.values[idx])
	}
	return out, nil
}

func readEntityRows(ctx context.Context, q queryer, t EntityTable, key string) ([]sqlRow, error) {
	rows, err := q.QueryContext(ctx, t.selectSQL(), t.argsFor(key)...)
	if err != nil {
		return nil, fmt.Errorf("migrate: select %s: %w", t.Name, err)
	}
	defer rows.Close()
	var out []sqlRow
	for rows.Next() {
		values := make([]any, len(t.Columns))
		ptrs := make([]any, len(values))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("migrate: scan %s: %w", t.Name, err)
		}
		out = append(out, sqlRow{values: values})
	}
	return out, rows.Err()
}

// EntityCopyReport counts inserted (Copied) versus already-present (Skipped)
// rows per table. A retry after a partial failure only inserts what is missing.
type EntityCopyReport struct {
	Copied  map[string]int
	Skipped map[string]int
}

// CopyEntity copies every selected aggregate root and all of its domain rows
// to another physical store. Source rows are preserved until CleanupEntity.
func CopyEntity(ctx context.Context, driver, srcDSN, dstDSN string, spec EntitySpec, toShard int, keys []string) (EntityCopyReport, error) {
	report := EntityCopyReport{Copied: map[string]int{}, Skipped: map[string]int{}}
	_ = toShard // entity tables carry no shard column; the directory does the routing
	src, err := openDB(ctx, driver, srcDSN)
	if err != nil {
		return report, err
	}
	defer src.Close()
	dst, err := openDB(ctx, driver, dstDSN)
	if err != nil {
		return report, err
	}
	defer dst.Close()
	stx, err := src.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer func() { _ = stx.Rollback() }()
	dtx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer func() { _ = dtx.Rollback() }()

	for _, table := range spec.Tables {
		if report.Copied[table.Name] == 0 {
			report.Copied[table.Name] = 0
			report.Skipped[table.Name] = 0
		}
		for _, key := range keys {
			rows, err := readEntityRows(ctx, stx, table, key)
			if err != nil {
				return report, err
			}
			for _, row := range rows {
				pk, err := table.pkValues(row)
				if err != nil {
					return report, err
				}
				exists, err := rowExists(ctx, dtx, table.existsSQL(), pk...)
				if err != nil {
					return report, err
				}
				if exists {
					match, err := rowMatches(ctx, dtx, table, row, pk)
					if err != nil {
						return report, err
					}
					if !match {
						return report, fmt.Errorf("migrate: %s already has a different row with the same key for %s", table.Name, key)
					}
					report.Skipped[table.Name]++
					continue
				}
				if _, err := dtx.ExecContext(ctx, table.insertSQL(), row.values...); err != nil {
					return report, fmt.Errorf("migrate: insert %s for %s: %w", table.Name, key, err)
				}
				report.Copied[table.Name]++
			}
		}
	}
	if err := dtx.Commit(); err != nil {
		return report, fmt.Errorf("migrate: commit target: %w", err)
	}
	if err := stx.Commit(); err != nil {
		return report, fmt.Errorf("migrate: commit source: %w", err)
	}
	return report, nil
}

func rowMatches(ctx context.Context, q queryer, t EntityTable, want sqlRow, pk []any) (bool, error) {
	rows, err := q.QueryContext(ctx, t.selectByPKSQL(), pk...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	cols := make([]any, len(t.Columns))
	ptrs := make([]any, len(cols))
	for i := range cols {
		ptrs[i] = &cols[i]
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := rows.Scan(ptrs...); err != nil {
		return false, err
	}
	return (sqlRow{values: cols}).normalized() == want.normalized(), nil
}

// VerifyEntity compares the exact row multiset of every entity table. Using a
// multiset rather than a positional list is important for double-written rows
// such as friendships: the same pair may legitimately appear once in each
// member's shard.
func VerifyEntity(ctx context.Context, driver, srcDSN, dstDSN string, spec EntitySpec, keys []string) error {
	src, err := openDB(ctx, driver, srcDSN)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := openDB(ctx, driver, dstDSN)
	if err != nil {
		return err
	}
	defer dst.Close()
	for _, table := range spec.Tables {
		for _, key := range keys {
			want, err := readEntityRows(ctx, src, table, key)
			if err != nil {
				return err
			}
			got, err := readEntityRows(ctx, dst, table, key)
			if err != nil {
				return err
			}
			if !equalMultiset(countMultiset(want), countMultiset(got)) {
				return fmt.Errorf("migrate: verify %s for %s: source=%d rows target=%d rows", table.Name, key, len(want), len(got))
			}
		}
	}
	return nil
}

func countMultiset(rows []sqlRow) map[string]int {
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		out[row.normalized()]++
	}
	return out
}

func equalMultiset(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for row, n := range a {
		if b[row] != n {
			return false
		}
	}
	return true
}

// CleanupEntity deletes moved rows from the source. Tables marked Cleanup=false
// (double-written friendships) are intentionally left alone.
func CleanupEntity(ctx context.Context, driver, dsn string, spec EntitySpec, keys []string) (int, error) {
	db, err := openDB(ctx, driver, dsn)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	deleted := 0
	for _, table := range spec.Tables {
		if !table.Cleanup {
			continue
		}
		for _, key := range keys {
			res, err := tx.ExecContext(ctx, `DELETE FROM `+table.Name+` WHERE `+table.Where, table.argsFor(key)...)
			if err != nil {
				return deleted, fmt.Errorf("migrate: cleanup %s: %w", table.Name, err)
			}
			n, _ := res.RowsAffected()
			deleted += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// PlanEntity returns keys that exist in the source store. It never mutates.
func PlanEntity(ctx context.Context, driver, dsn string, spec EntitySpec, keys []string) ([]string, error) {
	db, err := openDB(ctx, driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+spec.Primary+` WHERE `+spec.KeyColumn+` = ?`, key).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fmt.Errorf("migrate: %s %s not found on source", spec.Domain, key)
		}
		out = append(out, key)
	}
	return out, nil
}

// CutoverEntity flips the directory rows of one domain in a single CAS
// transaction and records the new shard's DSN.
func CutoverEntity(ctx context.Context, driver, controlDSN string, domain plane.Domain, keys []string, from, to int, targetDSN string) error {
	dir, err := plane.OpenSQLDirectory(ctx, driver, controlDSN, nil)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.ReassignBatch(ctx, domain, from, to, keys); err != nil {
		return err
	}
	return dir.SyncTopology(ctx, domain, to, targetDSN)
}

// RollbackEntity reverses a completed entity cutover.
func RollbackEntity(ctx context.Context, driver, controlDSN, oldDSN, newDSN string, spec EntitySpec, from, to int, keys []string) error {
	if _, err := CopyEntity(ctx, driver, newDSN, oldDSN, spec, from, keys); err != nil {
		return err
	}
	if err := VerifyEntity(ctx, driver, newDSN, oldDSN, spec, keys); err != nil {
		return err
	}
	if err := CutoverEntity(ctx, driver, controlDSN, spec.Domain, keys, to, from, oldDSN); err != nil {
		return err
	}
	_, err := CleanupEntity(ctx, driver, newDSN, spec, keys)
	return err
}
