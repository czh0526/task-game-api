package archive

import (
	"context"
	"database/sql"
	"fmt"
)

// ExternalizeResult reports one pass of body externalization.
type ExternalizeResult struct {
	Scanned int // rows examined
	Moved   int // rows whose inline body was replaced by a blob ref
	Blobs   int // distinct bodies stored (deduplicated by content)
	Bytes   int64
}

// ExternalizeDeliveryBodies moves legacy inline webhook delivery bodies into
// the BlobStore and records a body_ref on each row. Bodies are content-addressed
// and deduplicated in one pass: an event delivered to N subscribers written
// inline today collapses to ONE blob plus N pointers.
//
// It is idempotent: rows with a non-empty body_ref are skipped, so reruns only
// pick up rows written by an older binary. Updates happen in one transaction
// after all blobs are safely stored, so a failure never leaves a dangling ref.
func ExternalizeDeliveryBodies(ctx context.Context, db *sql.DB, blobs BlobStore, limit int) (ExternalizeResult, error) {
	var res ExternalizeResult
	if db == nil || blobs == nil {
		return res, fmt.Errorf("archive: externalize needs a database and a blob store")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, body FROM webhook_deliveries WHERE body_ref = '' AND body <> '' LIMIT ?`, limit)
	if err != nil {
		return res, fmt.Errorf("archive: scan delivery bodies: %w", err)
	}
	type item struct {
		id, body string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.body); err != nil {
			_ = rows.Close()
			return res, err
		}
		items = append(items, it)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.Scanned = len(items)
	if len(items) == 0 {
		return res, nil
	}

	// Deduplicate by body text so one event's N deliveries share one blob.
	refByBody := make(map[string]string)
	for _, it := range items {
		if _, ok := refByBody[it.body]; ok {
			continue
		}
		ref, err := blobs.Put(ctx, "webhook-event:"+it.id, []byte(it.body))
		if err != nil {
			return res, fmt.Errorf("archive: store delivery body: %w", err)
		}
		refByBody[it.body] = ref
		res.Blobs++
		res.Bytes += int64(len(it.body))
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE webhook_deliveries SET body_ref = ?, body = '' WHERE id = ? AND body_ref = ''`)
	if err != nil {
		return res, err
	}
	for _, it := range items {
		r, err := stmt.ExecContext(ctx, refByBody[it.body], it.id)
		if err != nil {
			_ = stmt.Close()
			return res, err
		}
		n, _ := r.RowsAffected()
		res.Moved += int(n)
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}
