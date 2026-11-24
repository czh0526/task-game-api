-- MySQL 8.0+ schema for Task Game Open Protocol v0.1.
-- InnoDB, utf8mb4. JSON and timestamps stay TEXT, matching the SQLite port.
-- Amounts are BIGINT (MySQL INTEGER is 32-bit). Message order is seq, not rowid.
-- Partial unique indexes are generated columns that are NULL when they do not apply.

CREATE TABLE IF NOT EXISTS users (
    id VARCHAR(128) NOT NULL,
    balance BIGINT NOT NULL,
    integrity BIGINT NOT NULL,
    ability BIGINT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    email VARCHAR(320) NULL,
    password_hash VARCHAR(255) NULL,
    nickname VARCHAR(64) NOT NULL DEFAULT '',
    -- 720x720 JPEG from PUT /v1/me/avatar. NULL means no avatar yet.
    avatar MEDIUMBLOB NULL,
    avatar_updated_at VARCHAR(40) NOT NULL DEFAULT '',
    -- When set, the JPEG lives in the BlobStore and avatar is empty.
    avatar_ref VARCHAR(128) NOT NULL DEFAULT '',
    PRIMARY KEY (id),
    UNIQUE KEY idx_users_email (email)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS tasks (
    id VARCHAR(64) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    title TEXT NOT NULL,
    description MEDIUMTEXT NOT NULL,
    keywords TEXT NOT NULL,
    attributes TEXT NOT NULL,
    status VARCHAR(32) NOT NULL,
    publisher_id VARCHAR(128) NOT NULL,
    payment_mode VARCHAR(32) NOT NULL,
    payment_amount BIGINT NOT NULL,
    payment_conditions TEXT NOT NULL,
    milestones MEDIUMTEXT NOT NULL,
    acceptance_deadline VARCHAR(40) NOT NULL,
    acceptance_quality TEXT NOT NULL,
    acceptance_constraints TEXT NOT NULL,
    review_hours BIGINT NOT NULL,
    assignee_policy TEXT NOT NULL,
    competitive MEDIUMTEXT NULL,
    collaborative MEDIUMTEXT NULL,
    executor_ids TEXT NOT NULL,
    winning_submission_id VARCHAR(64) NULL,
    review_deadline VARCHAR(40) NULL,
    escrow_id VARCHAR(64) NOT NULL DEFAULT '',
    dispute_reason TEXT NOT NULL,
    cancel_reason TEXT NOT NULL,
    cancel_code VARCHAR(64) NOT NULL DEFAULT '',
    cancelled_at VARCHAR(40) NULL,
    chat_id VARCHAR(64) NOT NULL DEFAULT '',
    funded_at VARCHAR(40) NULL,
    start_deadline VARCHAR(40) NULL,
    winner_select_deadline VARCHAR(40) NULL,
    dispute_role_id VARCHAR(64) NOT NULL DEFAULT '',
    resolve_json MEDIUMTEXT NOT NULL,
    max_revisions BIGINT NOT NULL DEFAULT 3,
    created_at VARCHAR(40) NOT NULL,
    updated_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_tasks_created (created_at, id),
    KEY idx_tasks_review (status, review_deadline),
    KEY idx_tasks_status (status, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS submissions (
    id VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NOT NULL,
    milestone_id VARCHAR(64) NOT NULL DEFAULT '',
    executor_id VARCHAR(128) NOT NULL,
    payload MEDIUMTEXT NOT NULL,
    -- Large payloads (>4KB) live in the BlobStore, payload keeps '{}' for
    -- legacy readers. Reads prefer the ref.
    payload_ref VARCHAR(128) NOT NULL DEFAULT '',
    note TEXT NOT NULL,
    role_id VARCHAR(64) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL DEFAULT 'current',
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_submissions_task (task_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS escrow_accounts (
    id VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NOT NULL,
    held BIGINT NOT NULL,
    frozen BIGINT NOT NULL,
    currency VARCHAR(16) NOT NULL,
    version BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    UNIQUE KEY uq_escrow_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS escrow_entries (
    id VARCHAR(64) NOT NULL,
    escrow_id VARCHAR(64) NOT NULL,
    type VARCHAR(32) NOT NULL,
    amount BIGINT NOT NULL,
    to_user_id VARCHAR(128) NOT NULL DEFAULT '',
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_escrow_entries (escrow_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS webhooks (
    id VARCHAR(64) NOT NULL,
    owner_id VARCHAR(128) NOT NULL,
    url TEXT NOT NULL,
    secret VARCHAR(255) NOT NULL,
    events TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_webhooks_owner (owner_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id VARCHAR(64) NOT NULL,
    webhook_id VARCHAR(64) NOT NULL,
    event_id VARCHAR(64) NOT NULL,
    event_type VARCHAR(64) NOT NULL,
    body MEDIUMTEXT NOT NULL,
    body_ref VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    attempts BIGINT NOT NULL,
    last_error VARCHAR(512) NOT NULL DEFAULT '',
    created_at VARCHAR(40) NOT NULL,
    claim_token VARCHAR(64) NOT NULL DEFAULT '',
    claimed_until VARCHAR(40) NULL,
    PRIMARY KEY (id),
    KEY idx_deliveries_status (status, created_at),
    KEY idx_deliveries_claim (status, claimed_until, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS chats (
    id VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NULL,
    member_ids TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    type VARCHAR(16) NOT NULL DEFAULT 'group',
    pair_low VARCHAR(128) NOT NULL DEFAULT '',
    pair_high VARCHAR(128) NOT NULL DEFAULT '',
    direct_pair VARCHAR(280) GENERATED ALWAYS AS (
        CASE WHEN type = 'direct' AND pair_low <> '' THEN CONCAT(pair_low, '#', pair_high) ELSE NULL END
    ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY idx_chats_direct_pair (direct_pair),
    KEY idx_chats_task (task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS invites (
    id VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NOT NULL,
    user_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    updated_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_invites_task_user (task_id, user_id),
    KEY idx_invites_task (task_id, created_at, id),
    KEY idx_invites_user (user_id, status, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS messages (
    id VARCHAR(64) NOT NULL,
    seq BIGINT NOT NULL AUTO_INCREMENT,
    chat_id VARCHAR(64) NOT NULL,
    sender_id VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,
    text MEDIUMTEXT NOT NULL,
    attachment_ref TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_messages_seq (seq),
    KEY idx_messages_chat (chat_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS chat_reads (
    user_id VARCHAR(128) NOT NULL,
    chat_id VARCHAR(64) NOT NULL,
    last_read_rowid BIGINT NOT NULL,
    updated_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (user_id, chat_id),
    CONSTRAINT chk_chat_reads_rowid CHECK (last_read_rowid >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Message sharding prototype: global directory and transactional outbox.
CREATE TABLE IF NOT EXISTS chat_shards (
    chat_id VARCHAR(64) NOT NULL,
    shard INT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (chat_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS message_outbox (
    id VARCHAR(64) NOT NULL,
    chat_id VARCHAR(64) NOT NULL,
    shard INT NOT NULL,
    sender_id VARCHAR(128) NOT NULL,
    type VARCHAR(32) NOT NULL,
    text MEDIUMTEXT NOT NULL,
    attachment_ref TEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    projected TINYINT NOT NULL DEFAULT 0,
    PRIMARY KEY (id),
    KEY idx_outbox_projected (projected)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS score_events (
    id VARCHAR(64) NOT NULL,
    user_id VARCHAR(128) NOT NULL,
    delta_integrity BIGINT NOT NULL,
    delta_ability BIGINT NOT NULL,
    reason VARCHAR(64) NOT NULL,
    task_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_score_events_user (user_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS friend_requests (
    id VARCHAR(64) NOT NULL,
    from_user_id VARCHAR(128) NOT NULL,
    to_user_id VARCHAR(128) NOT NULL,
    pair_low VARCHAR(128) NOT NULL,
    pair_high VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    updated_at VARCHAR(40) NOT NULL,
    pending_pair VARCHAR(280) GENERATED ALWAYS AS (
        CASE WHEN status = 'pending' THEN CONCAT(pair_low, '#', pair_high) ELSE NULL END
    ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY idx_friend_req_pending_pair (pending_pair),
    KEY idx_friend_req_to (to_user_id, status, created_at, id),
    KEY idx_friend_req_from (from_user_id, status, created_at, id),
    CONSTRAINT chk_friend_req_pair CHECK (pair_low < pair_high),
    CONSTRAINT chk_friend_req_status CHECK (status IN ('pending', 'accepted', 'declined', 'cancelled'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS friendships (
    user_low VARCHAR(128) NOT NULL,
    user_high VARCHAR(128) NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (user_low, user_high),
    KEY idx_friendships_high (user_high, user_low),
    CONSTRAINT chk_friendships_pair CHECK (user_low < user_high)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS friend_groups (
    id VARCHAR(64) NOT NULL,
    owner_id VARCHAR(128) NOT NULL,
    name VARCHAR(64) NOT NULL,
    sort_order BIGINT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    updated_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY idx_friend_groups_owner_name (owner_id, name),
    KEY idx_friend_groups_owner_sort (owner_id, sort_order, created_at, id),
    CONSTRAINT chk_friend_groups_name CHECK (CHAR_LENGTH(name) > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS friend_group_members (
    owner_id VARCHAR(128) NOT NULL,
    friend_user_id VARCHAR(128) NOT NULL,
    group_id VARCHAR(64) NOT NULL,
    PRIMARY KEY (owner_id, friend_user_id),
    KEY idx_friend_group_members_group (group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS idempotency_keys (
    user_id VARCHAR(128) NOT NULL,
    method VARCHAR(16) NOT NULL,
    path VARCHAR(255) NOT NULL,
    idem_key VARCHAR(200) NOT NULL,
    request_hash VARCHAR(64) NOT NULL,
    response_body MEDIUMTEXT NOT NULL,
    created_at VARCHAR(40) NOT NULL,
    PRIMARY KEY (user_id, method, path, idem_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
