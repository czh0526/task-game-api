package compensation

// Schema is added to the control-plane database (the same pool that holds
// shard_directory / global_unique). A saga log is small: one row per
// cross-domain operation until it reaches a terminal state.
const Schema = `
CREATE TABLE IF NOT EXISTS compensation_sagas (
    id            VARCHAR(191) NOT NULL,
    kind          VARCHAR(32)  NOT NULL,
    user_id       VARCHAR(191) NOT NULL DEFAULT '',
    task_id       VARCHAR(191) NOT NULL DEFAULT '',
    payload       TEXT         NOT NULL DEFAULT '{}',
    state         VARCHAR(16)  NOT NULL,
    attempts      INT          NOT NULL DEFAULT 0,
    lease_owner   VARCHAR(191) NOT NULL DEFAULT '',
    lease_until   DATETIME     NOT NULL,
    last_error    TEXT         NOT NULL DEFAULT '',
    created_at    DATETIME     NOT NULL,
    updated_at    DATETIME     NOT NULL,
    PRIMARY KEY (id)
);
CREATE INDEX IF NOT EXISTS idx_compensation_state ON compensation_sagas (state, lease_until, created_at);
`

// Saga states. Transitions are monotone; a terminal saga is never rewritten.
const (
	StatePending      = "pending"      // forward steps not yet confirmed
	StateCompensating = "compensating" // forward failed; undo in progress
	StateDone         = "done"
	StateCompensated  = "compensated"
)

// KindFundHold is the cross user/task saga: deduct the publisher's balance
// (user domain) and create the escrow hold (task domain).
const KindFundHold = "fund_hold"
