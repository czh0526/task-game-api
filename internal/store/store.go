// Package store is the persistence boundary.
//
// sqlite.Open implements Store. A later Postgres implementation should satisfy
// the same interface and can live in internal/store/postgres without changing
// handlers. Schema comments in internal/store/sqlite/schema.sql call out the
// dialect-shaped bits (JSON text columns map cleanly onto jsonb).
package store

import (
	"context"
	"errors"
	"time"

	"taskgame/internal/model"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrInsufficient = errors.New("insufficient credits")
	ErrConflict     = errors.New("conflict")
)

// Store is a process-wide database handle.
type Store interface {
	Repos
	InTx(ctx context.Context, fn func(ctx context.Context, r Repos) error) error
	Ping(ctx context.Context) error
	Close() error
}

// Repos is the set of repositories visible inside and outside a transaction.
type Repos interface {
	Users() UserRepo
	Tasks() TaskRepo
	Submissions() SubmissionRepo
	Escrow() EscrowRepo
	Webhooks() WebhookRepo
	Deliveries() DeliveryRepo
	Chats() ChatRepo
	Idem() IdemRepo
}

type UserRepo interface {
	Ensure(ctx context.Context, id string, now time.Time) (model.User, error)
	Get(ctx context.Context, id string) (model.User, error)
	AddBalance(ctx context.Context, id string, delta int64) error
	ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error
	AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error
	ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error)
}

type TaskRepo interface {
	Insert(ctx context.Context, t model.Task) error
	Update(ctx context.Context, t model.Task) error
	Get(ctx context.Context, id string) (model.Task, error)
	List(ctx context.Context, f model.TaskFilter) ([]model.Task, error)
	ListDueReview(ctx context.Context, now time.Time, limit int) ([]string, error)
}

type SubmissionRepo interface {
	Insert(ctx context.Context, s model.Submission) error
	ListByTask(ctx context.Context, taskID string) ([]model.Submission, error)
}

type EscrowRepo interface {
	Insert(ctx context.Context, a model.EscrowAccount) error
	Update(ctx context.Context, a model.EscrowAccount) error
	InsertEntry(ctx context.Context, e model.EscrowEntry) error
	GetByTask(ctx context.Context, taskID string) (model.EscrowAccount, error)
}

type WebhookRepo interface {
	Insert(ctx context.Context, w model.Webhook) error
	ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error)
}

type DeliveryRepo interface {
	Insert(ctx context.Context, d model.Delivery) error
	ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error)
	Update(ctx context.Context, id string, attempts int, status, lastError string) error
}

type ChatRepo interface {
	InsertChat(ctx context.Context, c model.Chat) error
	GetChat(ctx context.Context, id string) (model.Chat, error)
	InsertMessage(ctx context.Context, m model.Message) error
}

type IdemRepo interface {
	Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error)
	Put(ctx context.Context, rec model.IdemRecord) error
}
