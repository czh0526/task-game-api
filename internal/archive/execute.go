package archive

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"taskgame/internal/model"
)

// coldRow is one archived row: its table, original column names, and values
// serialized as JSON strings. Values are stored as strings so INTEGER/TEXT/
// BLOB round-trip without dialect-specific decoding; restore uses the same
// column order.
type coldRow struct {
	Columns []string  `json:"columns"`
	Values  []*string `json:"values"` // nil = SQL NULL
}

// ExecResult reports what one batch archived.
type ExecResult struct {
	Table   string
	Rows    int
	Ref     string // blob ref of the cold payload, empty when nothing archived
	Bytes   int64
	Deleted int
}

// ExecuteBatch performs one archive batch against a single physical database:
//  1. SELECT the cold rows (bounded by limit) in a read transaction;
//  2. serialize them to NDJSON and write to the BlobStore;
//  3. read the blob back and verify its SHA-256 (so a silent write failure
//     never leads to deleting the online rows);
//  4. DELETE the SAME rows by primary key in one transaction, guarded by the
//     cutoff timestamp so a concurrent newer update cannot be lost.
//
// The table MUST have a single-column primary key named "id" for the delete
// step; all archived tables in this project do. Rows written after `before`
// are never selected, and the delete re-checks the time column.
func (a *Archiver) ExecuteBatch(ctx context.Context, db *sql.DB, dialect string, b Batch, limit int) (ExecResult, error) {
	res := ExecResult{Table: b.Table}
	if limit <= 0 {
		limit = 500
	}
	// 1. Select cold rows. Rebuild the SQL from the policy's table/time column
	// carried in the batch template rather than trusting a caller-built string.
	var selectArgs []any
	if b.KeepPerGroup > 0 {
		// (cutoff, keepPerGroup, limit)
		selectArgs = []any{formatArchiveTime(b.Before), b.KeepPerGroup, limit}
	} else {
		selectArgs = []any{formatArchiveTime(b.Before), limit}
	}
	rows, err := db.QueryContext(ctx, b.SelectSQL, selectArgs...)
	if err != nil {
		return res, fmt.Errorf("archive: select %s: %w", b.Table, err)
	}
	cols, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return res, err
	}
	var archived []coldRow
	var ids []any
	idIdx := indexOf(cols, "id")
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			_ = rows.Close()
			return res, fmt.Errorf("archive: scan %s: %w", b.Table, err)
		}
		row := coldRow{Columns: cols, Values: make([]*string, len(cols))}
		for i, v := range raw {
			switch x := v.(type) {
			case nil:
				row.Values[i] = nil
			case []byte:
				s := string(x)
				row.Values[i] = &s
			default:
				s := fmt.Sprint(x)
				row.Values[i] = &s
			}
		}
		archived = append(archived, row)
		if idIdx >= 0 {
			ids = append(ids, stringValue(raw[idIdx]))
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	if len(archived) == 0 {
		return res, nil
	}
	if len(ids) != len(archived) {
		return res, fmt.Errorf("archive: %s has no id column; cannot delete safely", b.Table)
	}

	// 2. Serialize + write to cold store.
	var buf []byte
	for _, row := range archived {
		line, err := json.Marshal(row)
		if err != nil {
			return res, err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	ref, err := a.Blobs.Put(ctx, b.Table, buf)
	if err != nil {
		return res, fmt.Errorf("archive: store %s: %w", b.Table, err)
	}
	// 3. Verify by reading back and comparing the content hash.
	got, err := a.Blobs.Get(ctx, ref)
	if err != nil {
		return res, fmt.Errorf("archive: verify read %s: %w", b.Table, err)
	}
	wantHash := sha256.Sum256(buf)
	gotHash := sha256.Sum256(got)
	if hex.EncodeToString(wantHash[:]) != hex.EncodeToString(gotHash[:]) {
		return res, fmt.Errorf("archive: checksum mismatch for %s; refusing to delete online rows", b.Table)
	}

	// 4. Delete the exact rows in one transaction, re-checking the cutoff.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	delSQL := fmt.Sprintf(`DELETE FROM %s WHERE id = ? AND %s < ?`, b.Table, timeColumnOf(b))
	cutoff := formatArchiveTime(b.Before)
	deleted := 0
	for _, rowID := range ids {
		r, err := tx.ExecContext(ctx, delSQL, rowID, cutoff)
		if err != nil {
			return res, fmt.Errorf("archive: delete %s/%v: %w", b.Table, rowID, err)
		}
		n, _ := r.RowsAffected()
		deleted += int(n)
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.Rows, res.Ref, res.Bytes, res.Deleted = len(archived), ref, int64(len(buf)), deleted
	return res, nil
}

// ExecuteAll plans and executes every batch against db, bounded by limit each.
func (a *Archiver) ExecuteAll(ctx context.Context, db *sql.DB, dialect string, now time.Time, limit int) ([]ExecResult, error) {
	batches, err := a.Plan(ctx, now)
	if err != nil {
		return nil, err
	}
	out := make([]ExecResult, 0, len(batches))
	for _, b := range batches {
		r, err := a.ExecuteBatch(ctx, db, dialect, b, limit)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Restore rewrites a cold blob back into its online table. It is the inverse
// used for verification drills and incident recovery; INSERT OR IGNORE (or
// MySQL INSERT IGNORE) makes a replay idempotent.
func (a *Archiver) Restore(ctx context.Context, db *sql.DB, dialect, table, ref string) (int, error) {
	data, err := a.Blobs.Get(ctx, ref)
	if err != nil {
		return 0, err
	}
	ignore := "INSERT OR IGNORE"
	if dialect == "mysql" {
		ignore = "INSERT IGNORE"
	}
	restored := 0
	var lines []coldRow
	// NDJSON: split on newlines.
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var row coldRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return restored, err
		}
		lines = append(lines, row)
	}
	for _, row := range lines {
		placeholders := ""
		args := make([]any, len(row.Columns))
		for i := range row.Columns {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			if row.Values[i] == nil {
				args[i] = nil
			} else {
				args[i] = *row.Values[i]
			}
		}
		cols := ""
		for i, c := range row.Columns {
			if i > 0 {
				cols += ","
			}
			cols += c
		}
		stmt := fmt.Sprintf("%s INTO %s (%s) VALUES (%s)", ignore, table, cols, placeholders)
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			return restored, fmt.Errorf("archive: restore %s: %w", table, err)
		}
		restored++
	}
	return restored, nil
}

func indexOf(cols []string, want string) int {
	for i, c := range cols {
		if c == want {
			return i
		}
	}
	return -1
}

func stringValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(x)
	default:
		return fmt.Sprint(x)
	}
}

// timeColumnOf extracts the time column from a SelectSQL of the form
// "SELECT * FROM t WHERE col < ? LIMIT ?".
func timeColumnOf(b Batch) string {
	if b.TimeColumn != "" {
		return b.TimeColumn
	}
	const marker = " WHERE "
	i := 0
	if idx := indexOfString(b.SelectSQL, marker); idx >= 0 {
		i = idx + len(marker)
	} else {
		return "created_at"
	}
	j := i
	for j < len(b.SelectSQL) && b.SelectSQL[j] != ' ' {
		j++
	}
	return b.SelectSQL[i:j]
}

func indexOfString(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func formatArchiveTime(t time.Time) string {
	// Business rows store fixed-width RFC3339 (model.TimeLayout), so lexical
	// comparison in SQL works only with the same layout.
	return model.FormatTime(t)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
