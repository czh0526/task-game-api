package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode"

	"taskgame/internal/biz"
	"taskgame/internal/model"
	"taskgame/internal/store"
)

type replayError struct {
	body []byte
}

func (e *replayError) Error() string { return "idempotent replay" }

func validateIdemKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 200 {
		return biz.Invalid("Idempotency-Key is too long")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return biz.Invalid("Idempotency-Key has invalid characters")
		}
	}
	return nil
}

func hashReq(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// call runs fn inside a transaction. A repeated Idempotency-Key with the same
// request returns the stored response and does not run fn again.
func call[T any](e *Engine, ctx context.Context, userID, method, path, key string, fp any, fn func(context.Context, store.Repos) (T, error)) (T, error) {
	var zero T
	if err := validateIdemKey(key); err != nil {
		return zero, err
	}
	if key == "" {
		var out T
		err := e.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
			v, err := fn(ctx, r)
			if err != nil {
				return err
			}
			out = v
			return nil
		})
		return out, err
	}
	sum, err := hashReq(fp)
	if err != nil {
		return zero, err
	}
	if rec, err := e.store.Idem().Get(ctx, userID, method, path, key); err != nil {
		return zero, err
	} else if rec != nil {
		return replay[T](rec, sum)
	}

	var out T
	err = e.store.InTx(ctx, func(ctx context.Context, r store.Repos) error {
		rec, err := r.Idem().Get(ctx, userID, method, path, key)
		if err != nil {
			return err
		}
		if rec != nil {
			if rec.RequestHash != sum {
				return biz.IdempotencyConflict()
			}
			return &replayError{body: []byte(rec.ResponseBody)}
		}
		v, err := fn(ctx, r)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if err := r.Idem().Put(ctx, model.IdemRecord{
			UserID:       userID,
			Method:       method,
			Path:         path,
			Key:          key,
			RequestHash:  sum,
			ResponseBody: string(raw),
			CreatedAt:    e.now(),
		}); err != nil {
			if errors.Is(err, store.ErrConflict) {
				rec, gerr := r.Idem().Get(ctx, userID, method, path, key)
				if gerr != nil {
					return gerr
				}
				if rec == nil {
					return err
				}
				if rec.RequestHash != sum {
					return biz.IdempotencyConflict()
				}
				return &replayError{body: []byte(rec.ResponseBody)}
			}
			return err
		}
		out = v
		return nil
	})
	var rp *replayError
	if errors.As(err, &rp) {
		var decoded T
		if uerr := json.Unmarshal(rp.body, &decoded); uerr != nil {
			return zero, uerr
		}
		return decoded, nil
	}
	return out, err
}

func replay[T any](rec *model.IdemRecord, sum string) (T, error) {
	var zero T
	if rec.RequestHash != sum {
		return zero, biz.IdempotencyConflict()
	}
	var out T
	if err := json.Unmarshal([]byte(rec.ResponseBody), &out); err != nil {
		return zero, err
	}
	return out, nil
}
