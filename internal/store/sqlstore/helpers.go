package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// LargeValueStore holds big field values (avatar JPEGs, submission payloads)
// outside the online database. The archive.BlobStore satisfies it; this small
// interface keeps the store layer independent of the archive package.
//
// A value written here gets an opaque ref that is stored in the *_ref column.
// Reads prefer the ref and fall back to the inline column for legacy rows, so
// enabling the blob tier never invalidates existing data.
type LargeValueStore interface {
	Put(ctx context.Context, key string, data []byte) (ref string, err error)
	Get(ctx context.Context, ref string) ([]byte, error)
}

// LargePayloadThreshold is the minimum inline payload size that is worth
// moving to the blob tier. Small JSON payloads stay inline: a blob round trip
// would cost more than the bytes it saves.
const LargePayloadThreshold = 4 << 10 // 4KB

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "[]", nil
	}
	return string(b), nil
}

func jsonOrNull(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, nil
	}
	return string(b), nil
}

func unmarshalSlice[T any](s string) ([]T, error) {
	if s == "" || s == "null" {
		return []T{}, nil
	}
	var out []T
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return []T{}, nil
	}
	return out, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
