// Package model holds the persistence and domain records behind the HTTP DTOs.
// A Postgres store can reuse these types without changing the HTTP layer.
package model

import (
	"encoding/json"
	"time"
)

const TimeLayout = "2006-01-02T15:04:05.000000000Z"

func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

func ParseTime(s string) (time.Time, error) {
	if t, err := time.Parse(TimeLayout, s); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

const (
	KindExclusive     = "exclusive"
	KindCompetitive   = "competitive"
	KindCollaborative = "collaborative"

	StatusDraft     = "draft"
	StatusOpen      = "open"
	StatusActive    = "active"
	StatusInReview  = "in_review"
	StatusDisputed  = "disputed"
	StatusSettled   = "settled"
	StatusCancelled = "cancelled"

	PayOneShot   = "one_shot"
	PayMilestone = "milestone"

	PolicySpecific = "specific"
	PolicyCriteria = "criteria"
	PolicyOpen     = "open"

	EscrowHold     = "hold"
	EscrowRelease  = "release"
	EscrowRefund   = "refund"
	EscrowFreeze   = "freeze"
	EscrowUnfreeze = "unfreeze"

	RoleOpen      = "open"
	RoleFilled    = "filled"
	RoleDropped   = "dropped"
	RoleSubmitted = "submitted"
	RoleSettled   = "settled"
	RoleRefunded  = "refunded"
	RoleDisputed  = "disputed"

	SubmissionCurrent    = "current"
	SubmissionSuperseded = "superseded"

	InvitePending  = "pending"
	InviteAccepted = "accepted"
	InviteDeclined = "declined"

	FriendPending   = "pending"
	FriendAccepted  = "accepted"
	FriendDeclined  = "declined"
	FriendCancelled = "cancelled"
	FriendIncoming  = "incoming"
	FriendOutgoing  = "outgoing"

	OutcomePayExecutor     = "pay_executor"
	OutcomeRefundPublisher = "refund_publisher"
	OutcomeSplit           = "split"

	CancelOpenTimeout         = "open_timeout"
	CancelStartTimeout        = "start_timeout"
	CancelWinnerSelectTimeout = "winner_select_timeout"
	CancelSeatTimeout         = "seat_timeout"

	MilestonePending   = "pending"
	MilestoneSubmitted = "submitted"
	MilestoneReleased  = "released"
	MilestoneDisputed  = "disputed"

	SettlementWinnerTakeAll = "winner_take_all"
	SettlementPerSeat       = "per_seat"
	SettlementUnified       = "unified"
	DropoutReplace          = "replace"
	CurrencyCredits         = "credits"

	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

const (
	EventTaskFunded            = "task.funded"
	EventTaskAccepted          = "task.accepted"
	EventTaskStarted           = "task.started"
	EventTaskSubmitted         = "task.submitted"
	EventTaskInReview          = "task.in_review"
	EventTaskConfirmed         = "task.confirmed"
	EventTaskAutoSettled       = "task.auto_settled"
	EventTaskDisputed          = "task.disputed"
	EventTaskSettled           = "task.settled"
	EventTaskCancelled         = "task.cancelled"
	EventTaskExecutorReplaced  = "task.executor_replaced"
	EventTaskRevisionRequested = "task.revision_requested"
	EventTaskResolveProposed   = "task.resolve_proposed"
	EventTaskArbitrated        = "task.arbitrated"
	EventTaskInvited           = "task.invited"
	EventTaskInviteDeclined    = "task.invite_declined"
	EventEscrowHold            = "escrow.hold"
	EventEscrowRelease         = "escrow.release"
	EventEscrowRefund          = "escrow.refund"
	EventEscrowUnfreeze        = "escrow.unfreeze"
	EventChatCreated           = "chat.created"
	EventChatMessageCreated    = "chat.message.created"
	EventScoreUpdated          = "score.updated"
	EventFriendRequested       = "friend.requested"
	EventFriendAccepted        = "friend.accepted"
	EventFriendDeclined        = "friend.declined"
	EventFriendRemoved         = "friend.removed"
)

// WebhookEventTypes is the closed set from the OpenAPI enum.
var WebhookEventTypes = map[string]struct{}{
	EventTaskFunded:            {},
	EventTaskAccepted:          {},
	EventTaskStarted:           {},
	EventTaskSubmitted:         {},
	EventTaskInReview:          {},
	EventTaskConfirmed:         {},
	EventTaskAutoSettled:       {},
	EventTaskDisputed:          {},
	EventTaskSettled:           {},
	EventTaskCancelled:         {},
	EventTaskExecutorReplaced:  {},
	EventTaskRevisionRequested: {},
	EventTaskResolveProposed:   {},
	EventTaskArbitrated:        {},
	EventTaskInvited:           {},
	EventTaskInviteDeclined:    {},
	EventEscrowHold:            {},
	EventEscrowRelease:         {},
	EventEscrowRefund:          {},
	EventEscrowUnfreeze:        {},
	EventChatCreated:           {},
	EventChatMessageCreated:    {},
	EventScoreUpdated:          {},
	EventFriendRequested:       {},
	EventFriendAccepted:        {},
	EventFriendDeclined:        {},
	EventFriendRemoved:         {},
}

type Payment struct {
	Mode       string
	Amount     int64
	Milestones []Milestone
	Conditions []string
}

type Milestone struct {
	ID        string `json:"id"`
	Amount    int64  `json:"amount"`
	Condition string `json:"condition"`
	Status    string `json:"status"`
}

type Acceptance struct {
	Deadline     time.Time
	Quality      string
	Constraints  []string
	ReviewHours  int
	MaxRevisions int
}

type AssigneePolicy struct {
	Type       string   `json:"type"`
	UserIDs    []string `json:"user_ids,omitempty"`
	MinAbility *int64   `json:"min_ability,omitempty"`
}

type Competitive struct {
	Slots          int    `json:"slots"`
	SettlementRule string `json:"settlement_rule"`
}

type Role struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ShareBps       int64      `json:"share_bps"`
	TemplateID     string     `json:"template_id,omitempty"`
	AssigneeUserID string     `json:"assignee_user_id,omitempty"`
	Status         string     `json:"status"`
	OpenedAt       *time.Time `json:"opened_at,omitempty"`
}

type Collaborative struct {
	Roles          []Role `json:"roles"`
	OnDropout      string `json:"on_dropout"`
	SettlementMode string `json:"settlement_mode,omitempty"`
}

type Task struct {
	ID                   string
	Kind                 string
	Title                string
	Description          string
	Status               string
	PublisherID          string
	Payment              Payment
	Acceptance           Acceptance
	AssigneePolicy       AssigneePolicy
	Competitive          *Competitive
	Collaborative        *Collaborative
	ExecutorIDs          []string
	WinningSubmissionID  *string
	ReviewDeadline       *time.Time
	EscrowID             string
	DisputeReason        string
	DisputeRoleID        string
	CancelReason         string
	CancelCode           string
	CancelledAt          *time.Time
	ChatID               string
	FundedAt             *time.Time
	StartDeadline        *time.Time
	WinnerSelectDeadline *time.Time
	Resolve              ResolveState
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Split is one line of a resolve or arbitrate split.
// Amount and ShareBps are mutually exclusive across the whole proposal.
type Split struct {
	UserID   string `json:"user_id"`
	Amount   *int64 `json:"amount,omitempty"`
	ShareBps *int64 `json:"share_bps,omitempty"`
}

// ResolveProposal is one side's offer. The other side must post the same outcome and split.
type ResolveProposal struct {
	UserID       string    `json:"user_id"`
	Outcome      string    `json:"outcome"`
	Reason       string    `json:"reason,omitempty"`
	RoleID       string    `json:"role_id,omitempty"`
	SubmissionID string    `json:"submission_id,omitempty"`
	Splits       []Split   `json:"splits,omitempty"`
	At           time.Time `json:"at"`
}

// ResolveState holds at most one publisher proposal and one executor proposal.
type ResolveState struct {
	Publisher *ResolveProposal `json:"publisher,omitempty"`
	Executor  *ResolveProposal `json:"executor,omitempty"`
}

type TaskFilter struct {
	Status   string
	Statuses []string
	Kind     string
	Limit    int
	Offset   int
}

type User struct {
	ID        string
	Balance   int64
	Integrity int64
	Ability   int64
	Email     string
	// PasswordHash is bcrypt and is filled only by GetByEmail. It is not an API field.
	PasswordHash string
	CreatedAt    time.Time
}

type ScoreEvent struct {
	ID             string
	UserID         string
	DeltaIntegrity int64
	DeltaAbility   int64
	Reason         string
	TaskID         string
	CreatedAt      time.Time
}

type Submission struct {
	ID          string
	TaskID      string
	MilestoneID string
	ExecutorID  string
	RoleID      string
	Payload     json.RawMessage
	Note        string
	Status      string
	CreatedAt   time.Time
}

type EscrowAccount struct {
	ID       string
	TaskID   string
	Held     int64
	Frozen   int64
	Currency string
	Entries  []EscrowEntry
}

type EscrowEntry struct {
	ID        string
	EscrowID  string
	Type      string
	Amount    int64
	ToUserID  string
	CreatedAt time.Time
}

type Webhook struct {
	ID        string
	OwnerID   string
	URL       string
	Secret    string
	Events    []string
	CreatedAt time.Time
}

type Delivery struct {
	ID        string
	WebhookID string
	EventID   string
	EventType string
	Body      string
	Status    string
	Attempts  int
	LastError string
	URL       string
	Secret    string
	CreatedAt time.Time
}

const (
	ChatTask   = "task"
	ChatDirect = "direct"
	ChatGroup  = "group"
)

type Chat struct {
	ID        string
	TaskID    string
	Type      string
	PairLow   string
	PairHigh  string
	MemberIDs []string
	CreatedAt time.Time
}

type Invite struct {
	ID        string
	TaskID    string
	UserID    string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Message struct {
	ID            string
	ChatID        string
	SenderID      string
	Type          string
	Text          string
	AttachmentRef string
	CreatedAt     time.Time
}

type IdemRecord struct {
	UserID       string
	Method       string
	Path         string
	Key          string
	RequestHash  string
	ResponseBody string
	CreatedAt    time.Time
}

// OrderedPair returns user ids in ascending byte order. Friendship and the
// pending-request uniqueness key both use this so a pair is stored once.
func OrderedPair(a, b string) (low, high string) {
	if a <= b {
		return a, b
	}
	return b, a
}

// FriendRequest is one directed request. Pending rows are unique per unordered pair.
type FriendRequest struct {
	ID         string
	FromUserID string
	ToUserID   string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Friendship is the mutual relationship. UserLow < UserHigh.
type Friendship struct {
	UserLow   string
	UserHigh  string
	CreatedAt time.Time
}

// FriendGroup is a personal folder owned by one user. Friends of that user
// see their own folders, not this one.
type FriendGroup struct {
	ID        string
	OwnerID   string
	Name      string
	SortOrder int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// FriendEdge is one friend of a user, with the public scores and the owner's group.
// GroupID is empty when the friend is in the ungrouped bucket.
type FriendEdge struct {
	UserID    string
	Integrity int64
	Ability   int64
	Since     time.Time
	GroupID   string
}
