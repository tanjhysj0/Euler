CREATE TABLE workspaces (
    id uuid PRIMARY KEY,
    path text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model_profiles (
    id uuid PRIMARY KEY,
    name text NOT NULL,
    base_url text NOT NULL,
    model text NOT NULL,
    secret_kind text NOT NULL CHECK (secret_kind IN ('keychain', 'environment')),
    secret_ref text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE RESTRICT,
    model_profile_id uuid NOT NULL REFERENCES model_profiles (id) ON DELETE RESTRICT,
    title text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'archived')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_active_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE runs (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN (
        'queued', 'running', 'waiting_approval',
        'succeeded', 'failed', 'cancelled', 'interrupted'
    )),
    next_event_seq bigint NOT NULL DEFAULT 1 CHECK (next_event_seq > 0),
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);

CREATE TABLE messages (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES sessions (id) ON DELETE RESTRICT,
    run_id uuid REFERENCES runs (id) ON DELETE RESTRICT,
    session_seq bigint NOT NULL CHECK (session_seq > 0),
    role text NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN (
        'streaming', 'completed', 'failed', 'cancelled', 'interrupted'
    )),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE TABLE tool_calls (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    approval_id uuid,
    name text NOT NULL,
    arguments_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN (
        'pending', 'waiting_approval', 'running',
        'succeeded', 'failed', 'rejected', 'cancelled'
    )),
    result_json jsonb,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz
);

CREATE TABLE approvals (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    tool_call_id uuid NOT NULL REFERENCES tool_calls (id) ON DELETE RESTRICT,
    status text NOT NULL CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled')),
    requested_payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    decision_payload_json jsonb,
    requested_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);

ALTER TABLE tool_calls
    ADD CONSTRAINT tool_calls_approval_id_fkey
    FOREIGN KEY (approval_id) REFERENCES approvals (id) ON DELETE RESTRICT;

CREATE TABLE run_events (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES runs (id) ON DELETE RESTRICT,
    seq bigint NOT NULL CHECK (seq > 0),
    type text NOT NULL,
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX sessions_message_seq_idx ON messages(session_id, session_seq);
CREATE UNIQUE INDEX run_events_seq_idx ON run_events(run_id, seq);
CREATE UNIQUE INDEX runs_one_active_idx ON runs(session_id)
    WHERE status IN ('queued', 'running', 'waiting_approval');
CREATE UNIQUE INDEX approvals_tool_call_pending_idx ON approvals(tool_call_id)
    WHERE status = 'pending';
CREATE INDEX sessions_last_active_idx ON sessions(last_active_at DESC);
CREATE INDEX workspaces_path_idx ON workspaces(path);
CREATE INDEX messages_run_idx ON messages(run_id);
CREATE INDEX tool_calls_run_idx ON tool_calls(run_id, created_at);
