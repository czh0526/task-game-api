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

	EscrowHold    = "hold"
	EscrowRelease = "release"
	EscrowRefund  = "refund"
	EscrowFreeze  = "freeze"

	RoleOpen    = "open"
	RoleFilled  = "filled"
	RoleDropped = "dropped"

	MilestonePending   = "pending"
	MilestoneSubmitted = "submitted"
	MilestoneReleased  = "released"
	MilestoneDisputed  = "disputed"

	SettlementWinnerTakeAll = "winner_take_all"
	DropoutReplace          = "replace"
	CurrencyCredits         = "credits"

	DeliveryPending   = "pending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

const (
	EventTaskFunded           = "task.funded"
	EventTaskAccepted         = "task.accepted"
	EventTaskSubmitted        = "task.submitted"
	EventTaskInReview         = "task.in_review"
	EventTaskConfirmed        = "task.confirmed"
	EventTaskAutoSettled      = "task.auto_settled"
	EventTaskDisputed         = "task.disputed"
	EventTaskSettled          = "task.settled"
	EventTaskCancelled        = "task.cancelled"
	EventTaskExecutorReplaced = "task.executor_replaced"
	EventEscrowHold           = "escrow.hold"
	EventEscrowRelease        = "escrow.release"
	EventEscrowRefund         = "escrow.refund"
	EventChatMessageCreated   = "chat.message.created"
	EventScoreUpdated         = "score.updated"
)

// WebhookEventTypes is the closed set from the OpenAPI enum.
var WebhookEventTypes = map[string]struct{}{
	EventTaskFunded:           {},
	EventTaskAccepted:         {},
	EventTaskSubmitted:        {},
	EventTaskInReview:         {},
	EventTaskConfirmed:        {},
	EventTaskAutoSettled:      {},
	EventTaskDisputed:         {},
	EventTaskSettled:          {},
	EventTaskCancelled:        {},
	EventTaskExecutorReplaced: {},
	EventEscrowHold:           {},
	EventEscrowRelease:        {},
	EventEscrowRefund:         {},
	EventChatMessageCreated:   {},
	EventScoreUpdated:         {},
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
	Deadline    time.Time
	Quality     string
	Constraints []string
	ReviewHours int
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
	ID             string `json:"id"`
	Name           string `json:"name"`
	ShareBps       int64  `json:"share_bps"`
	AssigneeUserID string `json:"assignee_user_id,omitempty"`
	Status         string `json:"status"`
}

type Collaborative struct {
	Roles     []Role `json:"roles"`
	OnDropout string `json:"on_dropout"`
}

type Task struct {
	ID                  string
	Kind                string
	Title               string
	Description         string
	Status              string
	PublisherID         string
	Payment             Payment
	Acceptance          Acceptance
	AssigneePolicy      AssigneePolicy
	Competitive         *Competitive
	Collaborative       *Collaborative
	ExecutorIDs         []string
	WinningSubmissionID *string
	ReviewDeadline      *time.Time
	EscrowID            string
	DisputeReason       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type TaskFilter struct {
	Status string
	Kind   string
	Limit  int
	Offset int
}

type User struct {
	ID        string
	Balance   int64
	Integrity int64
	Ability   int64
	CreatedAt time.Time
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
	Payload     json.RawMessage
	Note        string
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

type Chat struct {
	ID        string
	TaskID    string
	MemberIDs []string
	CreatedAt time.Time
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
