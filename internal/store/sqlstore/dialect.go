package sqlstore

// LockMode selects the row lock appended to a SELECT.
// SQLite ignores it: one connection and an immediate write transaction
// already serialize writers. MySQL uses InnoDB row locks.
type LockMode int

const (
	LockNone LockMode = iota
	LockUpdate
	LockSkipLocked
)

// Dialect is the SQL that differs between SQLite and MySQL.
// Identifiers it returns are fixed literals, never user input.
type Dialect interface {
	Name() string
	UserInsertIgnore() string
	ChatReadUpsert() string
	InviteUpsert() string
	MembershipUpsert() string
	// MessageSeq is the insertion-order column: SQLite rowid or MySQL seq.
	MessageSeq() string
	LockSuffix(mode LockMode) string
	SupportsSkipLocked() bool
	IsUnique(err error) bool
	IsEmailConflict(err error) bool
	KeywordEqualsSQL() string
	KeywordSearchSQL() string
	// AttrExpr is the scalar JSON value at path. path is one of the fixed
	// attribute paths the filter uses, not user input.
	AttrExpr(path string) string
	ExecutorReqSQL() string
	HaversineSQL() string
}
