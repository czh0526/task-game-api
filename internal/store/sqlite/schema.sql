-- SQLite schema for Task Game Open Protocol v0.1.
-- JSON columns are TEXT. A Postgres port can use jsonb and keep the same names.
-- INTEGER amounts are simulated credits. Timestamps are UTC, fixed-width RFC3339.

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    balance INTEGER NOT NULL,
    integrity INTEGER NOT NULL,
    ability INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    -- Password accounts only. API-key users (usr_publisher, usr_executor) leave both NULL.
    -- password_hash is bcrypt. Never store the plaintext password.
    -- Uniqueness is a partial index created in Open, so older files can ADD COLUMN first.
    email TEXT,
    password_hash TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL,
    status TEXT NOT NULL,
    publisher_id TEXT NOT NULL,
    payment_mode TEXT NOT NULL,
    payment_amount INTEGER NOT NULL,
    payment_conditions TEXT NOT NULL,
    milestones TEXT NOT NULL,
    acceptance_deadline TEXT NOT NULL,
    acceptance_quality TEXT NOT NULL,
    acceptance_constraints TEXT NOT NULL,
    review_hours INTEGER NOT NULL,
    assignee_policy TEXT NOT NULL,
    competitive TEXT,
    collaborative TEXT,
    executor_ids TEXT NOT NULL,
    winning_submission_id TEXT,
    review_deadline TEXT,
    escrow_id TEXT NOT NULL DEFAULT '',
    dispute_reason TEXT NOT NULL DEFAULT '',
    cancel_reason TEXT NOT NULL DEFAULT '',
    cancel_code TEXT NOT NULL DEFAULT '',
    cancelled_at TEXT,
    chat_id TEXT NOT NULL DEFAULT '',
    funded_at TEXT,
    start_deadline TEXT,
    winner_select_deadline TEXT,
    dispute_role_id TEXT NOT NULL DEFAULT '',
    resolve_json TEXT NOT NULL DEFAULT '',
    max_revisions INTEGER NOT NULL DEFAULT 3,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks (created_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_review ON tasks (status, review_deadline);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks (status, created_at, id);

CREATE TABLE IF NOT EXISTS submissions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    milestone_id TEXT NOT NULL DEFAULT '',
    executor_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    role_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'current',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_submissions_task ON submissions (task_id, created_at, id);

CREATE TABLE IF NOT EXISTS escrow_accounts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL UNIQUE,
    held INTEGER NOT NULL,
    frozen INTEGER NOT NULL,
    currency TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS escrow_entries (
    id TEXT PRIMARY KEY,
    escrow_id TEXT NOT NULL,
    type TEXT NOT NULL,
    amount INTEGER NOT NULL,
    to_user_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_escrow_entries ON escrow_entries (escrow_id, created_at, id);

CREATE TABLE IF NOT EXISTS webhooks (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    url TEXT NOT NULL,
    secret TEXT NOT NULL,
    events TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_webhooks_owner ON webhooks (owner_id);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id TEXT PRIMARY KEY,
    webhook_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    body TEXT NOT NULL,
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deliveries_status ON webhook_deliveries (status, created_at);

CREATE TABLE IF NOT EXISTS chats (
    id TEXT PRIMARY KEY,
    task_id TEXT,
    member_ids TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_chats_task ON chats (task_id);

CREATE TABLE IF NOT EXISTS invites (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_invites_task ON invites (task_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_invites_user ON invites (user_id, status, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_invites_task_user ON invites (task_id, user_id);

CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    type TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    attachment_ref TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS score_events (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    delta_integrity INTEGER NOT NULL,
    delta_ability INTEGER NOT NULL,
    reason TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_score_events_user ON score_events (user_id, created_at, id);

-- Friendships are mutual and stored once per pair (user_low < user_high).
-- friend_requests.pair_low / pair_high use the same order so only one pending
-- request can exist for a pair. Accepted, declined, and cancelled rows stay
-- so a later request can be created. Groups are personal folders: the other
-- person does not see them. Caps live in the engine (50 groups, 500 friends,
-- 100 pending outgoing requests, group names of 1–32 characters).

CREATE TABLE IF NOT EXISTS friend_requests (
    id TEXT PRIMARY KEY,
    from_user_id TEXT NOT NULL,
    to_user_id TEXT NOT NULL,
    pair_low TEXT NOT NULL,
    pair_high TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (pair_low < pair_high),
    CHECK (status IN ('pending', 'accepted', 'declined', 'cancelled'))
);

CREATE INDEX IF NOT EXISTS idx_friend_req_to ON friend_requests (to_user_id, status, created_at, id);
CREATE INDEX IF NOT EXISTS idx_friend_req_from ON friend_requests (from_user_id, status, created_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_req_pending_pair
    ON friend_requests (pair_low, pair_high) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS friendships (
    user_low TEXT NOT NULL,
    user_high TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_low, user_high),
    CHECK (user_low < user_high)
);

CREATE INDEX IF NOT EXISTS idx_friendships_high ON friendships (user_high, user_low);

CREATE TABLE IF NOT EXISTS friend_groups (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    name TEXT NOT NULL,
    sort_order INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (length(name) > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_friend_groups_owner_name ON friend_groups (owner_id, name);
CREATE INDEX IF NOT EXISTS idx_friend_groups_owner_sort ON friend_groups (owner_id, sort_order, created_at, id);

CREATE TABLE IF NOT EXISTS friend_group_members (
    owner_id TEXT NOT NULL,
    friend_user_id TEXT NOT NULL,
    group_id TEXT NOT NULL,
    PRIMARY KEY (owner_id, friend_user_id)
);

CREATE INDEX IF NOT EXISTS idx_friend_group_members_group ON friend_group_members (group_id);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    user_id TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    idem_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_body TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (user_id, method, path, idem_key)
);
