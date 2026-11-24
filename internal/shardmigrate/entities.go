package shardmigrate

import "taskgame/internal/plane"

// UserSpec is the user aggregate: everything addressed by user_id in the
// user domain. Friendships are copied but never deleted on the source — each
// member's shard keeps its own copy, so the source row may be the peer's only
// local view of the relationship.
var UserSpec = EntitySpec{
	Domain:    plane.DomainUser,
	Primary:   "users",
	KeyColumn: "id",
	Tables: []EntityTable{
		{
			Name: "users", Columns: []string{
				"id", "balance", "integrity", "ability", "created_at", "email",
				"password_hash", "nickname", "avatar", "avatar_updated_at",
			},
			Where: "id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "score_events", Columns: []string{
				"id", "user_id", "delta_integrity", "delta_ability", "reason", "task_id", "created_at",
			},
			Where: "user_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "friend_requests", Columns: []string{
				"id", "from_user_id", "to_user_id", "pair_low", "pair_high",
				"status", "created_at", "updated_at",
			},
			// Requests are stored in the SENDER's shard. A recipient's incoming
			// list is a scatter read, so only from_user_id owns the row.
			Where: "from_user_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "friendships", Columns: []string{"user_low", "user_high", "created_at"},
			Where: "user_low = ? OR user_high = ?", OrderBy: "user_low, user_high",
			PK: []string{"user_low", "user_high"}, Cleanup: false,
			Args: func(key string) []any { return []any{key, key} },
		},
		{
			Name: "friend_groups", Columns: []string{
				"id", "owner_id", "name", "sort_order", "created_at", "updated_at",
			},
			Where: "owner_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "friend_group_members", Columns: []string{"owner_id", "friend_user_id", "group_id"},
			Where: "owner_id = ?", OrderBy: "owner_id, friend_user_id",
			PK: []string{"owner_id", "friend_user_id"}, Cleanup: true,
		},
		{
			Name: "idempotency_keys", Columns: []string{
				"user_id", "method", "path", "idem_key", "request_hash", "response_body", "created_at",
			},
			Where: "user_id = ?", OrderBy: "user_id, method, path, idem_key",
			PK: []string{"user_id", "method", "path", "idem_key"}, Cleanup: true,
		},
	},
}

// TaskSpec is the task aggregate: everything addressed by task_id in the
// task domain. webhooks and webhook_deliveries are intentionally absent: they
// are routed by owner id / delivery id, not by a task id, so they do not move
// with a task. They stay on their own shards and are reached by scatter reads.
var TaskSpec = EntitySpec{
	Domain:    plane.DomainTask,
	Primary:   "tasks",
	KeyColumn: "id",
	Tables: []EntityTable{
		{
			Name: "tasks", Columns: []string{
				"id", "kind", "title", "description", "keywords", "attributes", "status",
				"publisher_id", "payment_mode", "payment_amount", "payment_conditions",
				"milestones", "acceptance_deadline", "acceptance_quality",
				"acceptance_constraints", "review_hours", "assignee_policy",
				"competitive", "collaborative", "executor_ids", "winning_submission_id",
				"review_deadline", "escrow_id", "dispute_reason", "cancel_reason",
				"cancel_code", "cancelled_at", "chat_id", "funded_at", "start_deadline",
				"winner_select_deadline", "dispute_role_id", "resolve_json",
				"max_revisions", "created_at", "updated_at",
			},
			Where: "id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "submissions", Columns: []string{
				"id", "task_id", "milestone_id", "executor_id", "payload", "note",
				"role_id", "status", "created_at",
			},
			Where: "task_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "escrow_accounts", Columns: []string{"id", "task_id", "held", "frozen", "currency", "version"},
			Where: "task_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "escrow_entries", Columns: []string{
				"id", "escrow_id", "type", "amount", "to_user_id", "created_at",
			},
			// Escrow entries belong to the task that owns their account. The
			// subquery is evaluated on the source before cleanup and on the
			// target after accounts were copied, so both see the same set.
			Where:   "escrow_id IN (SELECT id FROM escrow_accounts WHERE task_id = ?)",
			OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
		{
			Name: "invites", Columns: []string{
				"id", "task_id", "user_id", "status", "created_at", "updated_at",
			},
			Where: "task_id = ?", OrderBy: "id", PK: []string{"id"}, Cleanup: true,
		},
	},
}

// EntitySpecFor resolves the CLI's -entity value.
func EntitySpecFor(name string) (EntitySpec, bool) {
	switch name {
	case "user":
		return UserSpec, true
	case "task":
		return TaskSpec, true
	default:
		return EntitySpec{}, false
	}
}
