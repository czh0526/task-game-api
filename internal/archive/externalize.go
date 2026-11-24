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

// LargePayloadThreshold mirrors sqlstore.LargePayloadThreshold. Duplicated as
// a literal so the archive package stays independent of the store layer; the
// shardctl command passes this value explicitly.
const LargePayloadThreshold = 4 << 10 // 4KB

// ExternalizeAvatars moves legacy inline avatar JPEGs into the BlobStore and
// records an avatar_ref, clearing the inline column. It is idempotent: rows
// with a non-empty ref are skipped. Blobs are stored before the SQL update, so
// a failure never leaves a ref pointing at missing data.
//
// Run this ONLY in a maintenance window after every API process has been
// upgraded to the ref-aware binary: an older binary only reads the inline
// column and would see "no avatar" once it is cleared.
func ExternalizeAvatars(ctx context.Context, db *sql.DB, blobs BlobStore, limit int) (ExternalizeResult, error) {
	var res ExternalizeResult
	if db == nil || blobs == nil {
		return res, fmt.Errorf("archive: externalize needs a database and a blob store")
	}
	if limit <= 0 {
		limit = 500
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, avatar FROM users
		WHERE avatar_ref = '' AND avatar IS NOT NULL AND length(avatar) > 0 LIMIT ?`, limit)
	if err != nil {
		return res, fmt.Errorf("archive: scan avatars: %w", err)
	}
	type item struct {
		id   string
		data []byte
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.data); err != nil {
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

	refs := make([]string, len(items))
	for i, it := range items {
		ref, err := blobs.Put(ctx, "avatar:"+it.id, it.data)
		if err != nil {
			return res, fmt.Errorf("archive: store avatar: %w", err)
		}
		refs[i] = ref
		res.Blobs++
		res.Bytes += int64(len(it.data))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE users SET avatar_ref = ?, avatar = NULL WHERE id = ? AND avatar_ref = ''`)
	if err != nil {
		return res, err
	}
	for i, it := range items {
		r, err := stmt.ExecContext(ctx, refs[i], it.id)
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

// ExternalizeSubmissionPayloads moves legacy inline payloads larger than the
// threshold into the BlobStore, leaving '{}' inline so older readers still see
// valid JSON. Idempotent like ExternalizeAvatars; also a maintenance-window
// operation for the same binary-mix reason.
func ExternalizeSubmissionPayloads(ctx context.Context, db *sql.DB, blobs BlobStore, limit int, threshold int) (ExternalizeResult, error) {
	var res ExternalizeResult
	if db == nil || blobs == nil {
		return res, fmt.Errorf("archive: externalize needs a database and a blob store")
	}
	if limit <= 0 {
		limit = 500
	}
	if threshold <= 0 {
		threshold = LargePayloadThreshold
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, payload FROM submissions
		WHERE payload_ref = '' AND length(payload) > ? LIMIT ?`, threshold, limit)
	if err != nil {
		return res, fmt.Errorf("archive: scan submission payloads: %w", err)
	}
	type item struct{ id, payload string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.payload); err != nil {
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

	refs := make([]string, len(items))
	for i, it := range items {
		ref, err := blobs.Put(ctx, "submission:"+it.id, []byte(it.payload))
		if err != nil {
			return res, fmt.Errorf("archive: store submission payload: %w", err)
		}
		refs[i] = ref
		res.Blobs++
		res.Bytes += int64(len(it.payload))
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx,
		`UPDATE submissions SET payload_ref = ?, payload = '{}' WHERE id = ? AND payload_ref = ''`)
	if err != nil {
		return res, err
	}
	for i, it := range items {
		r, err := stmt.ExecContext(ctx, refs[i], it.id)
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
