package mysql

import (
	"errors"
	"strings"

	"taskgame/internal/store/sqlstore"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// dialect is the MySQL 8 form of the shared SQL store.
type dialect struct{}

func (dialect) Name() string { return "mysql" }

func (dialect) UserInsertIgnore() string {
	return `
		INSERT INTO users (id, balance, integrity, ability, created_at)
		VALUES (?, ?, 0, 0, ?) AS new
		ON DUPLICATE KEY UPDATE id = users.id`
}

func (dialect) ChatReadUpsert() string {
	return `
		INSERT INTO chat_reads (user_id, chat_id, last_read_rowid, updated_at)
		VALUES (?, ?, ?, ?) AS new
		ON DUPLICATE KEY UPDATE
			updated_at = IF(new.last_read_rowid > chat_reads.last_read_rowid, new.updated_at, chat_reads.updated_at),
			last_read_rowid = GREATEST(chat_reads.last_read_rowid, new.last_read_rowid)`
}

func (dialect) InviteUpsert() string {
	return `
		INSERT INTO invites (id, task_id, user_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) AS new
		ON DUPLICATE KEY UPDATE status = new.status, updated_at = new.updated_at`
}

func (dialect) MembershipUpsert() string {
	return `
		INSERT INTO friend_group_members (owner_id, friend_user_id, group_id)
		VALUES (?, ?, ?) AS new
		ON DUPLICATE KEY UPDATE group_id = new.group_id`
}

func (dialect) MessageSeq() string { return "seq" }

func (dialect) LockSuffix(mode sqlstore.LockMode) string {
	switch mode {
	case sqlstore.LockSkipLocked:
		return " FOR UPDATE SKIP LOCKED"
	case sqlstore.LockUpdate:
		return " FOR UPDATE"
	default:
		return ""
	}
}

func (dialect) SupportsSkipLocked() bool { return true }

func (dialect) IsUnique(err error) bool {
	var me *mysqldriver.MySQLError
	return errors.As(err, &me) && me.Number == 1062
}

func (d dialect) IsEmailConflict(err error) bool {
	if !d.IsUnique(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "email")
}

func (dialect) KeywordEqualsSQL() string {
	return ` AND JSON_CONTAINS(tasks.keywords, JSON_QUOTE(?))`
}

func (dialect) KeywordSearchSQL() string {
	return `EXISTS (SELECT 1 FROM JSON_TABLE(tasks.keywords, '$[*]' COLUMNS (value VARCHAR(512) PATH '$')) jt WHERE instr(lower(jt.value), lower(?)) > 0)`
}

func (dialect) AttrExpr(path string) string {
	switch path {
	case "$.urgency", "$.recurrence", "$.latitude", "$.longitude":
	default:
		panic("mysql attr path " + path)
	}
	return `JSON_UNQUOTE(JSON_EXTRACT(tasks.attributes, '` + path + `'))`
}

func (dialect) ExecutorReqSQL() string {
	return ` AND JSON_CONTAINS(JSON_EXTRACT(tasks.attributes, '$.executor_requirements'), JSON_QUOTE(?))`
}

func (dialect) HaversineSQL() string {
	lat := `CAST(JSON_UNQUOTE(JSON_EXTRACT(tasks.attributes, '$.latitude')) AS DECIMAL(12,8))`
	lon := `CAST(JSON_UNQUOTE(JSON_EXTRACT(tasks.attributes, '$.longitude')) AS DECIMAL(12,8))`
	return `6371.0088 * 2 * asin(least(1.0, sqrt(
	pow(sin(radians(` + lat + ` - ?) / 2), 2) +
	cos(radians(?)) * cos(radians(` + lat + `)) *
	pow(sin(radians(` + lon + ` - ?) / 2), 2)
)))`
}
