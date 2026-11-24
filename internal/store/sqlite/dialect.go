package sqlite

import (
	"strings"

	"taskgame/internal/store/sqlstore"
)

// dialect is the SQLite form of the shared SQL store.
// Row locks are omitted: Open uses one connection, and the DSN write lock
// serializes writers. Message order is the implicit rowid.
type dialect struct{}

func (dialect) Name() string { return "sqlite" }

func (dialect) UserInsertIgnore() string {
	return `
		INSERT INTO users (id, balance, integrity, ability, created_at)
		VALUES (?, ?, 0, 0, ?)
		ON CONFLICT(id) DO NOTHING`
}

func (dialect) ChatReadUpsert() string {
	return `
		INSERT INTO chat_reads (user_id, chat_id, last_read_rowid, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, chat_id) DO UPDATE SET
			last_read_rowid = MAX(chat_reads.last_read_rowid, excluded.last_read_rowid),
			updated_at = CASE
				WHEN excluded.last_read_rowid > chat_reads.last_read_rowid THEN excluded.updated_at
				ELSE chat_reads.updated_at
			END`
}

func (dialect) InviteUpsert() string {
	return `
		INSERT INTO invites (id, task_id, user_id, role_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id, user_id) DO UPDATE SET
			role_id = excluded.role_id, status = excluded.status, updated_at = excluded.updated_at`
}

func (dialect) MembershipUpsert() string {
	return `
		INSERT INTO friend_group_members (owner_id, friend_user_id, group_id) VALUES (?, ?, ?)
		ON CONFLICT(owner_id, friend_user_id) DO UPDATE SET group_id = excluded.group_id`
}

func (dialect) MessageSeq() string { return "rowid" }

func (dialect) LockSuffix(sqlstore.LockMode) string { return "" }

func (dialect) SupportsSkipLocked() bool { return false }

func (dialect) IsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (dialect) IsEmailConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") &&
		(strings.Contains(msg, "users.email") || strings.Contains(msg, "idx_users_email"))
}

func (dialect) KeywordEqualsSQL() string {
	return ` AND EXISTS (SELECT 1 FROM json_each(tasks.keywords) WHERE value = ?)`
}

func (dialect) KeywordSearchSQL() string {
	return `EXISTS (SELECT 1 FROM json_each(tasks.keywords) WHERE instr(lower(value), lower(?)) > 0)`
}

func (dialect) AttrExpr(path string) string {
	switch path {
	case "$.urgency", "$.recurrence", "$.latitude", "$.longitude":
	default:
		panic("sqlite attr path " + path)
	}
	return `json_extract(tasks.attributes, '` + path + `')`
}

func (dialect) ExecutorReqSQL() string {
	return ` AND EXISTS (SELECT 1 FROM json_each(tasks.attributes, '$.executor_requirements') WHERE value = ?)`
}

func (dialect) HaversineSQL() string {
	return `6371.0088 * 2 * asin(min(1.0, sqrt(
	pow(sin(radians(json_extract(tasks.attributes, '$.latitude') - ?) / 2), 2) +
	cos(radians(?)) * cos(radians(json_extract(tasks.attributes, '$.latitude'))) *
	pow(sin(radians(json_extract(tasks.attributes, '$.longitude') - ?) / 2), 2)
)))`
}
