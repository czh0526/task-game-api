package shardmigrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store/mysql"
	"taskgame/internal/store/shard"
	"taskgame/internal/store/sqlite"

	_ "github.com/go-sql-driver/mysql" // driver name: mysql
	_ "modernc.org/sqlite"             // driver name: sqlite
)

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// openDB opens a physical store without applying schema; EnsureSchema does
// that once on a brand-new shard before any copy starts.
func openDB(ctx context.Context, driver, dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("migrate: empty DSN")
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: open %s: %w", dsn, err)
	}
	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: ping %s: %w", dsn, err)
	}
	return db, nil
}

// EnsureSchema applies the full business schema to a newly added physical
// database. Reusing the store openers keeps the migration DDL identical to a
// normal cell, including all additive migrations.
func EnsureSchema(ctx context.Context, driver, dsn string) error {
	switch driver {
	case "sqlite":
		st, err := sqlite.Open(dsn, 0)
		if err != nil {
			return err
		}
		return st.Close()
	case "mysql":
		st, err := mysql.Open(dsn, 0)
		if err != nil {
			return err
		}
		return st.Close()
	default:
		return fmt.Errorf("migrate: unsupported driver %q", driver)
	}
}

// ChatPlan is one chat selected for migration, with the directory keys that
// must be flipped together.
type ChatPlan struct {
	ID       string
	Key      string // ordered pair for direct chats, id otherwise
	Messages int
	Reads    int
}

// Plan describes exactly what a migration would move. Nothing is mutated.
func PlanChats(ctx context.Context, driver, dsn string, chatIDs []string) ([]ChatPlan, error) {
	db, err := openDB(ctx, driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	plans := make([]ChatPlan, 0, len(chatIDs))
	for _, id := range chatIDs {
		var c model.Chat
		err := db.QueryRowContext(ctx,
			`SELECT id, type, pair_low, pair_high FROM chats WHERE id = ?`, id).
			Scan(&c.ID, &c.Type, &c.PairLow, &c.PairHigh)
		if err != nil {
			return nil, fmt.Errorf("migrate: plan chat %s: %w", id, err)
		}
		p := ChatPlan{ID: id, Key: shard.ChatKey(c)}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE chat_id = ?`, id).Scan(&p.Messages); err != nil {
			return nil, err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_reads WHERE chat_id = ?`, id).Scan(&p.Reads); err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// DirectoryKeys returns both routing keys of a chat: its id (used by id-only
// reads such as ListMessages) and its aggregate key (used by direct chat
// symmetry). Both must move in one directory transaction.
func DirectoryKeys(plans []ChatPlan) []string {
	keys := make([]string, 0, len(plans)*2)
	for _, p := range plans {
		keys = append(keys, p.ID)
		if p.Key != p.ID {
			keys = append(keys, p.Key)
		}
	}
	return keys
}

// CopyReport records what was copied and how message order numbers changed.
type CopyReport struct {
	Chats     int
	Messages  int
	Reads     int
	ShardRows int
	IDMap     map[int64]int64 // source rowid/seq -> target rowid/seq
}

// Copy moves the selected chats and all of their message-domain rows from one
// physical store to another. Source rows are kept: Cutover + Cleanup happen
// only after Verify succeeds. Re-running is idempotent, so a failed attempt
// can be retried.
//
// Message order numbers are AUTO_INCREMENT / rowid and therefore change when
// rows are copied. IDMap preserves the old -> new relationship so read cursors
// (chat_reads.last_read_rowid) can be rewritten to the new sequence.
func Copy(ctx context.Context, driver, srcDSN, dstDSN string, toShard int, chatIDs []string) (CopyReport, error) {
	report := CopyReport{IDMap: make(map[int64]int64)}
	if srcDSN == dstDSN {
		return report, fmt.Errorf("migrate: source and target are the same database")
	}
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

	seqCol := messageSeqColumn(driver)
	for _, id := range chatIDs {
		// 1. chat row.
		var c model.Chat
		var taskID sql.NullString
		var memberIDs, created string
		err := stx.QueryRowContext(ctx,
			`SELECT id, task_id, member_ids, created_at, type, pair_low, pair_high FROM chats WHERE id = ?`, id).
			Scan(&c.ID, &taskID, &memberIDs, &created, &c.Type, &c.PairLow, &c.PairHigh)
		if err != nil {
			return report, fmt.Errorf("migrate: copy chat %s: %w", id, err)
		}
		c.TaskID = taskID.String
		c.CreatedAt, err = model.ParseTime(created)
		if err != nil {
			return report, fmt.Errorf("migrate: chat %s created_at: %w", id, err)
		}
		if err := json.Unmarshal([]byte(memberIDs), &c.MemberIDs); err != nil {
			return report, fmt.Errorf("migrate: chat %s members: %w", id, err)
		}
		existing, err := rowExists(ctx, dtx, `SELECT created_at FROM chats WHERE id = ?`, id)
		if err != nil {
			return report, err
		}
		if !existing {
			members, err := json.Marshal(c.MemberIDs)
			if err != nil {
				return report, err
			}
			if _, err := dtx.ExecContext(ctx,
				`INSERT INTO chats (id, task_id, member_ids, created_at, type, pair_low, pair_high) VALUES (?,?,?,?,?,?,?)`,
				c.ID, nullString(c.TaskID), string(members), model.FormatTime(c.CreatedAt), c.Type, c.PairLow, c.PairHigh); err != nil {
				return report, fmt.Errorf("migrate: insert chat %s: %w", id, err)
			}
			report.Chats++
		}

		// 2. messages, in insertion order, remembering sequence numbers.
		rows, err := stx.QueryContext(ctx,
			`SELECT `+seqCol+`, id, sender_id, type, text, attachment_ref, created_at FROM messages WHERE chat_id = ? ORDER BY `+seqCol, id)
		if err != nil {
			return report, err
		}
		type msgRow struct {
			seq                                     int64
			id, sender, kind, text, attach, created string
		}
		var msgs []msgRow
		for rows.Next() {
			var m msgRow
			if err := rows.Scan(&m.seq, &m.id, &m.sender, &m.kind, &m.text, &m.attach, &m.created); err != nil {
				_ = rows.Close()
				return report, err
			}
			msgs = append(msgs, m)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return report, err
		}
		_ = rows.Close()
		for _, m := range msgs {
			var targetSeq int64
			err := dtx.QueryRowContext(ctx,
				`SELECT `+seqCol+` FROM messages WHERE id = ?`, m.id).Scan(&targetSeq)
			switch {
			case err == nil:
				// Already copied by an earlier attempt: verify content matches.
				var gotSender, gotKind, gotText, gotAttach, gotCreated string
				if err := dtx.QueryRowContext(ctx,
					`SELECT sender_id, type, text, attachment_ref, created_at FROM messages WHERE id = ?`, m.id).
					Scan(&gotSender, &gotKind, &gotText, &gotAttach, &gotCreated); err != nil {
					return report, err
				}
				if gotSender != m.sender || gotKind != m.kind || gotText != m.text || gotAttach != m.attach || gotCreated != m.created {
					return report, fmt.Errorf("migrate: message %s already exists on target with different content", m.id)
				}
			case err == sql.ErrNoRows:
				res, err := dtx.ExecContext(ctx,
					`INSERT INTO messages (id, chat_id, sender_id, type, text, attachment_ref, created_at) VALUES (?,?,?,?,?,?,?)`,
					m.id, id, m.sender, m.kind, m.text, m.attach, m.created)
				if err != nil {
					return report, fmt.Errorf("migrate: insert message %s: %w", m.id, err)
				}
				targetSeq, err = res.LastInsertId()
				if err != nil {
					return report, err
				}
				report.Messages++
			default:
				return report, err
			}
			report.IDMap[m.seq] = targetSeq
		}

		// 3. read cursors, with their rowid rewritten to the target sequence.
		readRows, err := stx.QueryContext(ctx,
			`SELECT user_id, last_read_rowid, updated_at FROM chat_reads WHERE chat_id = ? ORDER BY user_id`, id)
		if err != nil {
			return report, err
		}
		type readRow struct {
			user, updated    string
			last, mappedLast int64
		}
		var reads []readRow
		for readRows.Next() {
			var r readRow
			if err := readRows.Scan(&r.user, &r.last, &r.updated); err != nil {
				_ = readRows.Close()
				return report, err
			}
			mapped, ok := report.IDMap[r.last]
			if !ok {
				// A cursor may point at a message deleted long ago; 0 means
				// "nothing read" and is safe, but log it in the report by
				// keeping the original value as a sentinel for Verify.
				mapped = 0
			}
			r.mappedLast = mapped
			reads = append(reads, r)
		}
		if err := readRows.Err(); err != nil {
			_ = readRows.Close()
			return report, err
		}
		_ = readRows.Close()
		for _, r := range reads {
			existing, err := rowExists(ctx, dtx,
				`SELECT updated_at FROM chat_reads WHERE user_id = ? AND chat_id = ?`, r.user, id)
			if err != nil {
				return report, err
			}
			if !existing {
				if _, err := dtx.ExecContext(ctx,
					`INSERT INTO chat_reads (user_id, chat_id, last_read_rowid, updated_at) VALUES (?,?,?,?)`,
					r.user, id, r.mappedLast, r.updated); err != nil {
					return report, fmt.Errorf("migrate: insert read cursor: %w", err)
				}
				report.Reads++
			}
		}

		// 4. legacy chat_shards directory; keep its shard number consistent.
		var shardCreated string
		err = stx.QueryRowContext(ctx, `SELECT created_at FROM chat_shards WHERE chat_id = ?`, id).Scan(&shardCreated)
		switch {
		case err == nil:
			existing, err := rowExists(ctx, dtx, `SELECT created_at FROM chat_shards WHERE chat_id = ?`, id)
			if err != nil {
				return report, err
			}
			if !existing {
				if _, err := dtx.ExecContext(ctx,
					`INSERT INTO chat_shards (chat_id, shard, created_at) VALUES (?,?,?)`, id, toShard, shardCreated); err != nil {
					return report, err
				}
				report.ShardRows++
			}
		case err == sql.ErrNoRows:
			// Nothing to migrate for this legacy table.
		default:
			return report, err
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

// Verify compares every copied row. Sequence numbers are intentionally NOT
// compared directly: Copy rewrites them. Read cursors are compared AFTER the
// old -> new mapping is applied, so an off-by-one in that mapping fails here.
func Verify(ctx context.Context, driver, srcDSN, dstDSN string, toShard int, chatIDs []string) error {
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
	seqCol := messageSeqColumn(driver)

	for _, id := range chatIDs {
		srcChat, err := readChatFields(ctx, src, id)
		if err != nil {
			return err
		}
		dstChat, err := readChatFields(ctx, dst, id)
		if err != nil {
			return err
		}
		if srcChat != dstChat {
			return fmt.Errorf("migrate: verify chat %s: row differs", id)
		}

		srcMsgs, err := readMessages(ctx, src, seqCol, id)
		if err != nil {
			return err
		}
		dstMsgs, err := readMessages(ctx, dst, seqCol, id)
		if err != nil {
			return err
		}
		if len(srcMsgs) != len(dstMsgs) {
			return fmt.Errorf("migrate: verify chat %s: %d messages on source, %d on target", id, len(srcMsgs), len(dstMsgs))
		}
		mapping := make(map[int64]int64, len(srcMsgs))
		for i := range srcMsgs {
			if srcMsgs[i].content != dstMsgs[i].content {
				return fmt.Errorf("migrate: verify chat %s: message %d differs", id, i)
			}
			mapping[srcMsgs[i].seq] = dstMsgs[i].seq
		}

		srcLast, srcUpdated, err := readReads(ctx, src, id)
		if err != nil {
			return err
		}
		dstLast, dstUpdated, err := readReads(ctx, dst, id)
		if err != nil {
			return err
		}
		if len(srcLast) != len(dstLast) {
			return fmt.Errorf("migrate: verify chat %s: %d read cursors on source, %d on target", id, len(srcLast), len(dstLast))
		}
		for user, last := range srcLast {
			want, ok := mapping[last]
			if !ok {
				// A stale cursor outside the copied message set stays at 0.
				want = 0
			}
			got, ok := dstLast[user]
			if !ok || got != want || dstUpdated[user+"#updated"] != srcUpdated[user+"#updated"] {
				return fmt.Errorf("migrate: verify chat %s: cursor %s = %d, want %d", id, user, got, want)
			}
		}

		legacyRow, err := rowExists(ctx, src, `SELECT created_at FROM chat_shards WHERE chat_id = ?`, id)
		if err != nil {
			return err
		}
		if legacyRow {
			var shardRow int
			err := dst.QueryRowContext(ctx, `SELECT shard FROM chat_shards WHERE chat_id = ?`, id).Scan(&shardRow)
			if err == sql.ErrNoRows {
				return fmt.Errorf("migrate: verify chat %s: legacy chat_shards row missing on target", id)
			}
			if err != nil {
				return err
			}
			if shardRow != toShard {
				return fmt.Errorf("migrate: verify chat %s: chat_shards says %d, want %d", id, shardRow, toShard)
			}
		}
	}
	return nil
}

// Cutover flips both directory keys of every chat in one control-plane
// transaction, then records the new DSN in shard_topology. Data must already
// be verified on the target.
func Cutover(ctx context.Context, driver, controlDSN string, plans []ChatPlan, from, to int, targetDSN string) error {
	dir, err := plane.OpenSQLDirectory(ctx, driver, controlDSN, nil)
	if err != nil {
		return err
	}
	defer dir.Close()
	keys := DirectoryKeys(plans)
	if err := dir.ReassignBatch(ctx, plane.DomainMessage, from, to, keys); err != nil {
		return err
	}
	return dir.SyncTopology(ctx, plane.DomainMessage, to, targetDSN)
}

// Cleanup deletes source rows after a successful cutover. It is a separate
// step so an operator can inspect both databases before dropping data.
func Cleanup(ctx context.Context, driver, dsn string, chatIDs []string) (int, error) {
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
	for _, id := range chatIDs {
		for _, stmt := range []string{
			`DELETE FROM chat_reads WHERE chat_id = ?`,
			`DELETE FROM messages WHERE chat_id = ?`,
			`DELETE FROM chat_shards WHERE chat_id = ?`,
			`DELETE FROM chats WHERE id = ?`,
		} {
			res, err := tx.ExecContext(ctx, stmt, id)
			if err != nil {
				return deleted, fmt.Errorf("migrate: cleanup %s: %w", stmt, err)
			}
			if stmt == `DELETE FROM chats WHERE id = ?` {
				n, _ := res.RowsAffected()
				deleted += int(n)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// Rollback reverses a completed cutover: copy back, verify, flip the directory
// rows, then delete the moved rows from the new shard.
func Rollback(ctx context.Context, driver, controlDSN, oldDSN, newDSN string, from, to int, chatIDs []string) error {
	plans, err := PlanChats(ctx, driver, newDSN, chatIDs)
	if err != nil {
		return err
	}
	if _, err := Copy(ctx, driver, newDSN, oldDSN, from, chatIDs); err != nil {
		return err
	}
	if err := Verify(ctx, driver, newDSN, oldDSN, from, chatIDs); err != nil {
		return err
	}
	dir, err := plane.OpenSQLDirectory(ctx, driver, controlDSN, nil)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.ReassignBatch(ctx, plane.DomainMessage, to, from, DirectoryKeys(plans)); err != nil {
		return err
	}
	if err := dir.SyncTopology(ctx, plane.DomainMessage, from, oldDSN); err != nil {
		return err
	}
	_, err = Cleanup(ctx, driver, newDSN, chatIDs)
	return err
}

func messageSeqColumn(driver string) string {
	if driver == "mysql" {
		return "seq"
	}
	return "rowid"
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func rowExists(ctx context.Context, q queryer, query string, args ...any) (bool, error) {
	var probe string
	err := q.QueryRowContext(ctx, query, args...).Scan(&probe)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

type messageContent struct {
	seq     int64
	content string
}

func readMessages(ctx context.Context, db queryer, seqCol, chatID string) ([]messageContent, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+seqCol+`, sender_id, type, text, attachment_ref, created_at FROM messages WHERE chat_id = ? ORDER BY `+seqCol, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []messageContent
	for rows.Next() {
		var m messageContent
		var sender, kind, text, attach, created string
		if err := rows.Scan(&m.seq, &sender, &kind, &text, &attach, &created); err != nil {
			return nil, err
		}
		// Message id is intentionally excluded: it is part of the row key and
		// already compared by Copy's idempotency check. Content + order is what
		// matters for a cutover.
		m.content = strings.Join([]string{sender, kind, text, attach, created}, "\x00")
		out = append(out, m)
	}
	return out, rows.Err()
}

func readChatFields(ctx context.Context, db queryer, id string) (string, error) {
	var taskID, memberIDs, created, kind, low, high sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT task_id, member_ids, created_at, type, pair_low, pair_high FROM chats WHERE id = ?`, id).
		Scan(&taskID, &memberIDs, &created, &kind, &low, &high)
	if err != nil {
		return "", fmt.Errorf("migrate: read chat %s: %w", id, err)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		taskID.String, memberIDs.String, created.String, kind.String, low.String, high.String,
	}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

// readReads encodes cursors as user -> last rowid, plus a "#updated" companion
// key so the timestamp is verified too.
func readReads(ctx context.Context, db queryer, chatID string) (map[string]int64, map[string]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT user_id, last_read_rowid, updated_at FROM chat_reads WHERE chat_id = ?`, chatID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	last := make(map[string]int64)
	updated := make(map[string]string)
	for rows.Next() {
		var user, at string
		var rowid int64
		if err := rows.Scan(&user, &rowid, &at); err != nil {
			return nil, nil, err
		}
		last[user] = rowid
		updated[user+"#updated"] = at
	}
	return last, updated, rows.Err()
}
