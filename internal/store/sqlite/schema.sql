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
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tasks_created ON tasks (created_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_review ON tasks (status, review_deadline);

CREATE TABLE IF NOT EXISTS submissions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    milestone_id TEXT NOT NULL DEFAULT '',
    executor_id TEXT NOT NULL,
    payload TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
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
