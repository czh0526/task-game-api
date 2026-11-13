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
	Invites() InviteRepo
	Friends() FriendRepo
	Idem() IdemRepo
}

type UserRepo interface {
	Ensure(ctx context.Context, id string, now time.Time) (model.User, error)
	Get(ctx context.Context, id string) (model.User, error)
	// InsertRegistered creates a password account. Email is unique. Balance is the store initial balance.
	InsertRegistered(ctx context.Context, id, email, passwordHash string, now time.Time) (model.User, error)
	// GetByEmail returns the user and its password hash. ErrNotFound when the email is absent.
	GetByEmail(ctx context.Context, email string) (model.User, error)
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
	MarkStatus(ctx context.Context, id, status string) error
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
	UpdateChat(ctx context.Context, c model.Chat) error
	GetChat(ctx context.Context, id string) (model.Chat, error)
	FindDirect(ctx context.Context, a, b string) (model.Chat, error)
	ListByTask(ctx context.Context, taskID string) ([]model.Chat, error)
	InsertMessage(ctx context.Context, m model.Message) error
	ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error)
}

type InviteRepo interface {
	Upsert(ctx context.Context, inv model.Invite) error
	Update(ctx context.Context, inv model.Invite) error
	Get(ctx context.Context, id string) (model.Invite, error)
	Find(ctx context.Context, taskID, userID string) (model.Invite, error)
	ListByTask(ctx context.Context, taskID string) ([]model.Invite, error)
	ListByUser(ctx context.Context, userID, status string) ([]model.Invite, error)
}

// FriendRepo stores mutual friendships, directed requests, and personal groups.
// A friendship is one row per unordered pair. At most one request between a
// pair may be pending. Group membership is visible only to the owner.
type FriendRepo interface {
	InsertRequest(ctx context.Context, row model.FriendRequest) error
	UpdateRequest(ctx context.Context, row model.FriendRequest) error
	GetRequest(ctx context.Context, id string) (model.FriendRequest, error)
	FindPendingBetween(ctx context.Context, a, b string) (model.FriendRequest, error)
	ListPending(ctx context.Context, userID, direction string) ([]model.FriendRequest, error)
	CountPendingOutgoing(ctx context.Context, userID string) (int, error)

	InsertFriendship(ctx context.Context, row model.Friendship) error
	DeleteFriendship(ctx context.Context, a, b string) error
	GetFriendship(ctx context.Context, a, b string) (model.Friendship, error)
	CountFriendships(ctx context.Context, userID string) (int, error)
	ListFriendEdges(ctx context.Context, userID string) ([]model.FriendEdge, error)

	InsertGroup(ctx context.Context, row model.FriendGroup) error
	UpdateGroup(ctx context.Context, row model.FriendGroup) error
	DeleteGroup(ctx context.Context, ownerID, id string) error
	GetGroup(ctx context.Context, id string) (model.FriendGroup, error)
	ListGroups(ctx context.Context, ownerID string) ([]model.FriendGroup, error)
	CountGroups(ctx context.Context, ownerID string) (int, error)

	SetMembership(ctx context.Context, ownerID, friendUserID, groupID string) error
	ClearMembership(ctx context.Context, ownerID, friendUserID string) error
	ClearMembershipsForPair(ctx context.Context, a, b string) error
	ClearMembershipsForGroup(ctx context.Context, groupID string) error
}

type IdemRepo interface {
	Get(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error)
	Put(ctx context.Context, rec model.IdemRecord) error
}
