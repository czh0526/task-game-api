// Package store is the persistence boundary.
//
// sqlite.Open is the default Store. mysql.Open is the shared-database Store
// used when several API processes sit in front of one MySQL 8 database.
// Handlers depend only on this interface.
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
	// ErrBusy means another transaction holds the row (MySQL SKIP LOCKED).
	ErrBusy = errors.New("busy")
)

// Store is a process-wide database handle.
type Store interface {
	Repos
	InTx(ctx context.Context, fn func(ctx context.Context, r Repos) error) error
	Ping(ctx context.Context) error
	Close() error
}

// SQLTx is an OPTIONAL capability implemented by repositories that run inside
// a real SQL transaction. It lets a caller (the domain store) write a control-
// plane directory row in the SAME transaction as the business row, closing the
// eventually-consistent window when the control plane is co-located. Stores
// that do not expose a raw transaction simply do not implement this interface.
type SQLTx interface {
	// SQLTxHandle returns the active transaction and its SQL dialect
	// ("sqlite" or "mysql"). It is only valid while the transaction is open.
	SQLTxHandle() (tx any, dialect string)
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
	// SetNickname stores a display name. Empty is allowed and means unset. ErrNotFound when the user row is missing.
	SetNickname(ctx context.Context, id, nickname string) error
	// SetAvatar stores a normalized 720x720 JPEG and its upload timestamp.
	// ErrNotFound when the user row is missing.
	SetAvatar(ctx context.Context, id string, jpeg []byte, updatedAt string) error
	// GetAvatar returns the stored JPEG and its version. ErrNotFound when the
	// user does not exist or has never uploaded an avatar.
	GetAvatar(ctx context.Context, id string) (model.UserAvatar, error)
	AddBalance(ctx context.Context, id string, delta int64) error
	ApplyScore(ctx context.Context, id string, deltaIntegrity, deltaAbility int64) error
	AddScoreEvent(ctx context.Context, ev model.ScoreEvent) error
	ListScoreEvents(ctx context.Context, userID string, limit int) ([]model.ScoreEvent, error)
}

type TaskRepo interface {
	Insert(ctx context.Context, t model.Task) error
	Update(ctx context.Context, t model.Task) error
	Get(ctx context.Context, id string) (model.Task, error)
	// Lock reads the task and holds the row until the surrounding transaction ends.
	// SQLite serializes writers without a row lock. MySQL uses SELECT ... FOR UPDATE.
	Lock(ctx context.Context, id string) (model.Task, error)
	// TryLock is Lock, except a row already locked by another transaction returns ErrBusy.
	TryLock(ctx context.Context, id string) (model.Task, error)
	// ClaimDueReview locks one in_review task whose review_deadline has passed.
	// exclude omits ids already considered in this pass. An empty id means none remain.
	// The caller must be inside a transaction so the MySQL lock is held through settlement.
	ClaimDueReview(ctx context.Context, now time.Time, exclude []string) (string, error)
	// ListSweep pages tasks in open, active, or in_review. The cursor is (created_at, id)
	// in ascending order. Empty afterCreated starts at the beginning.
	ListSweep(ctx context.Context, afterCreated, afterID string, limit int) ([]model.Task, error)
	List(ctx context.Context, f model.TaskFilter) ([]model.Task, error)
	Count(ctx context.Context, f model.TaskFilter) (int64, error)
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
	// LockByTask is GetByTask plus a row lock held until the transaction ends.
	LockByTask(ctx context.Context, taskID string) (model.EscrowAccount, error)
}

type WebhookRepo interface {
	Insert(ctx context.Context, w model.Webhook) error
	ListByOwners(ctx context.Context, ownerIDs []string) ([]model.Webhook, error)
}

type DeliveryRepo interface {
	Insert(ctx context.Context, d model.Delivery) error
	ListPending(ctx context.Context, maxAttempts, limit int) ([]model.Delivery, error)
	// ClaimPending marks up to limit rows as delivering, increments attempts, and
	// returns the claimed rows. Expired delivering leases are eligible again.
	// Each claim commits before HTTP so two workers cannot both POST the same row
	// while the lease is held.
	ClaimPending(ctx context.Context, maxAttempts, limit int, now, until time.Time) ([]model.Delivery, error)
	// Finish writes the outcome only when claimToken still owns the row.
	Finish(ctx context.Context, id, claimToken, status, lastError string) error
}

type ChatRepo interface {
	InsertChat(ctx context.Context, c model.Chat) error
	UpdateChat(ctx context.Context, c model.Chat) error
	GetChat(ctx context.Context, id string) (model.Chat, error)
	FindDirect(ctx context.Context, a, b string) (model.Chat, error)
	ListByTask(ctx context.Context, taskID string) ([]model.Chat, error)
	InsertMessage(ctx context.Context, m model.Message) error
	ListMessages(ctx context.Context, chatID string, limit, offset int) ([]model.Message, error)
	// ListDirectUnreads is every direct chat this user is in. UnreadCount is
	// messages from the peer after the user's cursor. Task and group chats are omitted.
	ListDirectUnreads(ctx context.Context, userID string) ([]model.DirectUnread, error)
	// MessageRowID is the insertion-order rowid of one message in the chat.
	MessageRowID(ctx context.Context, chatID, messageID string) (int64, error)
	// LatestMessageRow is the newest inserted message. ok is false when the chat is empty.
	LatestMessageRow(ctx context.Context, chatID string) (rowid int64, messageID string, ok bool, err error)
	// CountUnread is messages from other senders inserted after the user's cursor.
	CountUnread(ctx context.Context, userID, chatID string) (int64, error)
	// AdvanceRead moves the cursor forward to rowid. It does not move backward.
	AdvanceRead(ctx context.Context, userID, chatID string, rowid int64, now time.Time) error
}

type InviteRepo interface {
	Upsert(ctx context.Context, inv model.Invite) error
	Update(ctx context.Context, inv model.Invite) error
	Get(ctx context.Context, id string) (model.Invite, error)
	Find(ctx context.Context, taskID, userID string) (model.Invite, error)
	ListByTask(ctx context.Context, taskID string) ([]model.Invite, error)
	ListByUser(ctx context.Context, userID, status string) ([]model.Invite, error)
	// DeleteByRole removes every invite bound to a collaborative seat. Used when
	// the publisher removes that seat. Returns nil when no rows match.
	DeleteByRole(ctx context.Context, taskID, roleID string) error
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
	// GetLocked is Get plus a row lock when the driver supports it.
	// Use it inside the transaction that may insert the same key.
	GetLocked(ctx context.Context, userID, method, path, key string) (*model.IdemRecord, error)
	Put(ctx context.Context, rec model.IdemRecord) error
}
