// Package archive is the cold-tier skeleton for P3.
//
// Sharding spreads data across databases; it does NOT reduce the total. The
// only things that actually shrink an online database are (1) moving cold rows
// out, (2) moving large columns out, and (3) removing write amplification.
//
// FileBlobStore is runnable, Archiver.Plan produces real plans, and
// Archiver.ExecuteBatch / ExecuteAll perform the copy → checksum-verify →
// delete sequence per shard. Restore reverses it for drills and recovery.
// Large-column offloading (LargeColumn.RefColumn) and emit() de-amplification
// are still staged separately.
package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BlobStore holds large values outside the online database. The ref is opaque
// to callers; a production deployment points this at S3/COS, not local disk.
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte) (ref string, err error)
	Get(ctx context.Context, ref string) ([]byte, error)
	Delete(ctx context.Context, ref string) error
}

// FileBlobStore is the runnable default: content-addressed files under Dir.
type FileBlobStore struct{ Dir string }

func NewFileBlobStore(dir string) (*FileBlobStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("archive: blob dir is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FileBlobStore{Dir: dir}, nil
}

func (f *FileBlobStore) Put(_ context.Context, key string, data []byte) (string, error) {
	sum := sha256.Sum256(append([]byte(key), data...))
	ref := "sha256:" + hex.EncodeToString(sum[:])
	path, err := f.path(ref)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return ref, nil
}

func (f *FileBlobStore) Get(_ context.Context, ref string) ([]byte, error) {
	path, err := f.path(ref)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (f *FileBlobStore) Delete(_ context.Context, ref string) error {
	path, err := f.path(ref)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (f *FileBlobStore) path(ref string) (string, error) {
	if !strings.HasPrefix(ref, "sha256:") {
		return "", fmt.Errorf("archive: unsupported ref %q", ref)
	}
	hexPart := strings.TrimPrefix(ref, "sha256:")
	if len(hexPart) != 64 {
		return "", fmt.Errorf("archive: malformed ref %q", ref)
	}
	if _, err := hex.DecodeString(hexPart); err != nil {
		return "", fmt.Errorf("archive: malformed ref %q", ref)
	}
	return filepath.Join(f.Dir, hexPart[:2], hexPart), nil
}

// LargeColumn is a column that should become a ref. The webhook_deliveries
// body_ref column is now in both dialects' schemas and migrations; the engine
// writes it via WithEventBodyStore and ExternalizeDeliveryBodies back-fills
// legacy inline rows. avatar/payload ref columns remain a future migration.
type LargeColumn struct {
	Table     string
	Column    string
	RefColumn string
	MaxBytes  int64
}

var LargeColumns = []LargeColumn{
	{Table: "users", Column: "avatar", RefColumn: "avatar_ref", MaxBytes: 2 << 20},
	{Table: "submissions", Column: "payload", RefColumn: "payload_ref", MaxBytes: 256 << 10},
	{Table: "webhook_deliveries", Column: "body", RefColumn: "body_ref", MaxBytes: 1 << 20},
}

// RetentionPolicy says how long a row stays in the online database.
type RetentionPolicy struct {
	Table      string
	TimeColumn string
	HotWindow  time.Duration
	Note       string
	// GroupColumn + KeepPerGroup protect the newest N rows of each group from
	// archival (used for messages: keep the newest page of every chat online so
	// the common "load latest history" read never hits the cold tier).
	GroupColumn  string
	KeepPerGroup int
}

// DefaultPolicies covers the append-only tables that grow forever today.
// chat_reads and users are deliberately absent: they are small, hot, and
// needed on every read.
var DefaultPolicies = []RetentionPolicy{
	{Table: "score_events", TimeColumn: "created_at", HotWindow: 90 * 24 * time.Hour, Note: "append-only ledger history"},
	{Table: "webhook_deliveries", TimeColumn: "created_at", HotWindow: 30 * 24 * time.Hour, Note: "delivery attempts and bodies"},
	{Table: "idempotency_keys", TimeColumn: "created_at", HotWindow: 7 * 24 * time.Hour, Note: "needs a created_at index + TTL"},
	{Table: "messages", TimeColumn: "created_at", HotWindow: 180 * 24 * time.Hour,
		GroupColumn: "chat_id", KeepPerGroup: 50, Note: "per shard; keep each chat's newest page online"},
}

// DeliveryBody is the shape of a de-amplified webhook delivery: the body is
// stored once and each delivery keeps only (delivery_id, body_ref, state).
// emit() writes the ref (engine.WithEventBodyStore) and the dispatcher resolves
// it (webhook.Config.BodyStore); ExternalizeDeliveryBodies back-fills legacy
// inline rows. Both paths fall back to inline bodies when no blob tier is set.
type DeliveryBody struct {
	BodyRef string
	Bytes   int64
}

// Batch is one planned archive operation. SQL statements are templates with
// placeholders; they are intentionally not executed by the skeleton.
type Batch struct {
	Table      string
	Before     time.Time
	TimeColumn string // explicit column name; avoids parsing SelectSQL
	SelectSQL  string
	ArchiveSQL string
	DeleteSQL  string
	// KeepPerGroup > 0 means SelectSQL takes a third argument (the keep count)
	// between the cutoff and the limit, protecting each group's newest rows.
	KeepPerGroup int
	RequiresACK  bool
}

type Archiver struct {
	Blobs    BlobStore
	Policies []RetentionPolicy
}

func NewArchiver(b BlobStore, policies []RetentionPolicy) *Archiver {
	if len(policies) == 0 {
		policies = DefaultPolicies
	}
	return &Archiver{Blobs: b, Policies: policies}
}

// Plan returns the batches that would have to run at time now. It is pure:
// no row is read, copied, or deleted.
//
// TODO before enabling: iterate every shard, write the cold copy, verify the
// checksum, then delete online rows inside a per-shard transaction, and keep
// the newest page of each chat for the message policy.
func (a *Archiver) Plan(_ context.Context, now time.Time) ([]Batch, error) {
	out := make([]Batch, 0, len(a.Policies))
	for _, p := range a.Policies {
		if p.Table == "" || p.TimeColumn == "" || p.HotWindow <= 0 {
			return nil, fmt.Errorf("archive: invalid retention policy %+v", p)
		}
		before := now.Add(-p.HotWindow)
		batch := Batch{
			Table:       p.Table,
			Before:      before,
			TimeColumn:  p.TimeColumn,
			ArchiveSQL:  fmt.Sprintf("INSERT INTO %s_cold SELECT * FROM %s WHERE %s < ?", p.Table, p.Table, p.TimeColumn),
			DeleteSQL:   fmt.Sprintf("DELETE FROM %s WHERE %s < ? AND id = ?", p.Table, p.TimeColumn),
			RequiresACK: true,
		}
		if p.KeepPerGroup > 0 && p.GroupColumn != "" {
			// Keep each group's newest N rows online. The correlated NOT EXISTS
			// counts newer rows in the same group; only rows with at least N
			// newer siblings are cold enough to archive. Args: (before, N, limit).
			batch.KeepPerGroup = p.KeepPerGroup
			batch.SelectSQL = fmt.Sprintf(
				"SELECT * FROM %s t WHERE t.%s < ? AND ("+
					"SELECT COUNT(*) FROM %s n WHERE n.%s = t.%s AND "+
					"(n.%s > t.%s OR (n.%s = t.%s AND n.id > t.id))) >= ? LIMIT ?",
				p.Table, p.TimeColumn, p.Table, p.GroupColumn, p.GroupColumn,
				p.TimeColumn, p.TimeColumn, p.TimeColumn, p.TimeColumn)
		} else {
			batch.SelectSQL = fmt.Sprintf("SELECT * FROM %s WHERE %s < ? LIMIT ?", p.Table, p.TimeColumn)
		}
		out = append(out, batch)
	}
	return out, nil
}
