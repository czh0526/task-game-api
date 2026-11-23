// Package shardmigrate implements the maintenance operations needed for a
// PHYSICAL domain cutover: backfill the routing directory for legacy rows,
// copy selected chat data to a new shard, verify it, flip the directory rows
// and clean up the source. Nothing here runs on the online request path.
package shardmigrate

import (
	"context"
	"fmt"

	"taskgame/internal/model"
	"taskgame/internal/plane"
	"taskgame/internal/store/shard"
)

// BackfillReport counts the directory rows written for each domain/shard.
type BackfillReport struct {
	Counts     map[plane.Domain]map[int]int
	Mismatches []string
}

// Backfill scans every physical store and pins every legacy aggregate root in
// the control-plane directory at the shard where its row actually lives.
//
// Assign is idempotent, so re-running is safe. A row that already points at a
// DIFFERENT shard is reported as a mismatch instead of being overwritten: that
// means the data and the directory disagree and needs a human decision.
func Backfill(ctx context.Context, driver, controlDSN string, dsns []string, domains []plane.Domain) (BackfillReport, error) {
	if len(dsns) == 0 {
		return BackfillReport{}, fmt.Errorf("migrate: no physical DSNs to scan")
	}
	dir, err := plane.OpenSQLDirectory(ctx, driver, controlDSN, nil)
	if err != nil {
		return BackfillReport{}, err
	}
	defer dir.Close()

	report := BackfillReport{Counts: make(map[plane.Domain]map[int]int)}
	for _, domain := range domains {
		if !domain.Valid() {
			return report, fmt.Errorf("migrate: invalid domain %q", domain)
		}
		report.Counts[domain] = make(map[int]int)
		for index, dsn := range dsns {
			db, err := openDB(ctx, driver, dsn)
			if err != nil {
				return report, err
			}
			keys, err := keysInStore(ctx, domain, db)
			_ = db.Close()
			if err != nil {
				return report, err
			}
			for _, key := range keys {
				if old, ok, err := dir.Lookup(ctx, domain, key); err != nil {
					return report, err
				} else if ok && old != index {
					report.Mismatches = append(report.Mismatches,
						fmt.Sprintf("%s/%s: data on shard %d but directory says %d", domain, key, index, old))
					continue
				}
				if err := dir.Assign(ctx, domain, key, index); err != nil {
					return report, err
				}
				report.Counts[domain][index]++
			}
		}
	}
	return report, nil
}

// keysInStore returns every directory key owned by one physical store for a
// domain. Direct chats contribute TWO keys: the chat id and the ordered user
// pair, so both members resolve to the same physical database.
func keysInStore(ctx context.Context, domain plane.Domain, db queryer) ([]string, error) {
	var table string
	switch domain {
	case plane.DomainUser:
		table = "users"
	case plane.DomainTask:
		table = "tasks"
	case plane.DomainMessage:
		table = "chats"
	default:
		return nil, fmt.Errorf("migrate: invalid domain %q", domain)
	}
	rows, err := db.QueryContext(ctx, "SELECT id FROM "+table)
	if err != nil {
		return nil, fmt.Errorf("migrate: scan %s: %w", table, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// Close before the per-chat point queries: a SQLite store allows only one
	// open connection, so a live rows cursor would deadlock them.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if domain != plane.DomainMessage {
		return ids, nil
	}

	// Message keys need the chat type and pair columns as well.
	out := make([]string, 0, len(ids)*2)
	for _, id := range ids {
		var c model.Chat
		err := db.QueryRowContext(ctx,
			`SELECT id, type, pair_low, pair_high FROM chats WHERE id = ?`, id).
			Scan(&c.ID, &c.Type, &c.PairLow, &c.PairHigh)
		if err != nil {
			return nil, fmt.Errorf("migrate: chat %s: %w", id, err)
		}
		out = append(out, c.ID, shard.ChatKey(c))
	}
	return out, nil
}
