-- SQLite 初始化 schema。类型映射说明：
--   uuid        → TEXT（UUID 以字符串形式存储，google/uuid 负责序列化）
--   timestamptz → TIMESTAMP（声明为 TIMESTAMP，modernc 驱动会将存储的
--                   ISO8601 文本解析为 time.Time；若声明为 TEXT 则按字符串
--                   扫描，无法赋值给 Go 的 time.Time 字段）
--   bigint      → INTEGER（SQLite INTEGER 为 64 位）
--   jsonb       → TEXT（JSON 以文本存储，gorm datatypes.JSON 负责编解码）
--   now()       → CURRENT_TIMESTAMP

CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE model_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    secret_kind TEXT NOT NULL CHECK (secret_kind IN ('keychain', 'environment')),
    secret_ref TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    model_profile_id TEXT NOT NULL REFERENCES model_profiles (id) ON DELETE RESTRICT,
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'archived')),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_active_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN (
        'queued', 'running', 'waiting_approval',
        'succeeded', 'failed', 'cancelled', 'interrupted'
    )),
    next_event_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_event_seq > 0),
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP
);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    run_id TEXT REFERENCES runs (id) ON DELETE RESTRICT,
    session_seq INTEGER NOT NULL CHECK (session_seq > 0),
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN (
        'streaming', 'completed', 'failed', 'cancelled', 'interrupted'
    )),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

-- tool_calls.approval_id 前向引用尚未创建的 approvals 表，
-- SQLite 允许在内联 REFERENCES 中引用尚未创建的父表（创建时不校验存在性）。
CREATE TABLE tool_calls (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    approval_id TEXT REFERENCES approvals (id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    arguments_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN (
        'pending', 'waiting_approval', 'running',
        'succeeded', 'failed', 'rejected', 'cancelled'
    )),
    result_json TEXT,
    error_message TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at TIMESTAMP,
    finished_at TIMESTAMP
);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    tool_call_id TEXT NOT NULL REFERENCES tool_calls (id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    requested_payload_json TEXT NOT NULL DEFAULT '{}',
    decision_payload_json TEXT,
    requested_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at TIMESTAMP
);

CREATE TABLE run_events (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    seq INTEGER NOT NULL CHECK (seq > 0),
    type TEXT NOT NULL,
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX sessions_message_seq_idx ON messages(session_id, session_seq);
CREATE UNIQUE INDEX run_events_seq_idx ON run_events(run_id, seq);
CREATE UNIQUE INDEX runs_one_active_idx ON runs(session_id)
    WHERE status IN ('queued', 'running', 'waiting_approval');
CREATE UNIQUE INDEX approvals_tool_call_pending_idx ON approvals(tool_call_id)
    WHERE status = 'pending';
CREATE INDEX sessions_last_active_idx ON sessions(last_active_at DESC);
-- path 唯一约束：替代原 PostgreSQL 的 pg_advisory_xact_lock，
-- 保证同一路径只对应一个 workspace，并发创建由唯一冲突重试处理。
CREATE UNIQUE INDEX workspaces_path_idx ON workspaces(path);
CREATE INDEX messages_run_idx ON messages(run_id);
CREATE INDEX tool_calls_run_idx ON tool_calls(run_id, created_at);
