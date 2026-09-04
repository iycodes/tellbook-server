-- migrate:up
CREATE TABLE tessa_preferences (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    introduction_completed_at timestamptz,
    acknowledged_notice_revision text NOT NULL DEFAULT '',
    notice_acknowledged_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT tessa_preferences_notice_check CHECK (
        (acknowledged_notice_revision = '' AND notice_acknowledged_at IS NULL)
        OR (btrim(acknowledged_notice_revision) <> '' AND notice_acknowledged_at IS NOT NULL)
    ),
    CONSTRAINT tessa_preferences_notice_revision_length_check
        CHECK (char_length(acknowledged_notice_revision) <= 80)
);

CREATE TABLE tessa_threads (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    creation_request_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'active',
    summary text NOT NULL DEFAULT '',
    summary_through_sequence bigint NOT NULL DEFAULT 0,
    summary_revision integer NOT NULL DEFAULT 1,
    last_message_sequence bigint NOT NULL DEFAULT 0,
    last_activity_at timestamptz NOT NULL DEFAULT NOW(),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    archived_at timestamptz,
    CONSTRAINT tessa_threads_id_client_key UNIQUE (id, client_id),
    CONSTRAINT tessa_threads_creation_request_key UNIQUE (client_id, creation_request_id),
    CONSTRAINT tessa_threads_status_check CHECK (status IN ('active', 'archived')),
    CONSTRAINT tessa_threads_summary_check CHECK (char_length(summary) <= 12000),
    CONSTRAINT tessa_threads_sequence_check CHECK (
        summary_through_sequence >= 0
        AND last_message_sequence >= summary_through_sequence
        AND summary_revision >= 1
    ),
    CONSTRAINT tessa_threads_archive_check CHECK (
        (status = 'active' AND archived_at IS NULL)
        OR (status = 'archived' AND archived_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX tessa_threads_one_active_per_client
    ON tessa_threads (client_id) WHERE status = 'active';
CREATE INDEX tessa_threads_client_activity_idx
    ON tessa_threads (client_id, last_activity_at DESC, id DESC);

CREATE TABLE tessa_messages (
    id uuid PRIMARY KEY,
    thread_id uuid NOT NULL,
    client_id uuid NOT NULL,
    sequence bigint NOT NULL,
    sender_type text NOT NULL,
    source_channel text NOT NULL DEFAULT 'web',
    client_message_id uuid,
    request_fingerprint text,
    content text NOT NULL,
    presentation jsonb NOT NULL DEFAULT '{}'::jsonb,
    entity_references jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT tessa_messages_thread_fk FOREIGN KEY (thread_id, client_id)
        REFERENCES tessa_threads(id, client_id) ON DELETE CASCADE,
    CONSTRAINT tessa_messages_id_thread_client_key UNIQUE (id, thread_id, client_id),
    CONSTRAINT tessa_messages_thread_sequence_key UNIQUE (thread_id, sequence),
    CONSTRAINT tessa_messages_sender_check CHECK (sender_type IN ('provider', 'tessa', 'system')),
    CONSTRAINT tessa_messages_channel_check CHECK (source_channel = 'web'),
    CONSTRAINT tessa_messages_content_check CHECK (
        btrim(content) <> '' AND char_length(content) <= 4000
    ),
    CONSTRAINT tessa_messages_sequence_check CHECK (sequence > 0),
    CONSTRAINT tessa_messages_idempotency_check CHECK (
        (sender_type = 'provider' AND client_message_id IS NOT NULL
            AND request_fingerprint ~ '^[a-f0-9]{64}$')
        OR (sender_type <> 'provider' AND client_message_id IS NULL
            AND request_fingerprint IS NULL)
    ),
    CONSTRAINT tessa_messages_presentation_check CHECK (
        jsonb_typeof(presentation) = 'object'
        AND jsonb_typeof(entity_references) = 'array'
        AND octet_length(presentation::text) <= 4096
        AND octet_length(entity_references::text) <= 8192
    )
);

CREATE UNIQUE INDEX tessa_messages_provider_idempotency_key
    ON tessa_messages (thread_id, client_message_id)
    WHERE sender_type = 'provider';
CREATE INDEX tessa_messages_thread_sequence_idx
    ON tessa_messages (thread_id, sequence DESC);

CREATE TABLE tessa_runs (
    id uuid PRIMARY KEY,
    thread_id uuid NOT NULL,
    client_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    stage text NOT NULL DEFAULT 'queued',
    lease_owner text NOT NULL DEFAULT '',
    lease_token uuid,
    lease_expires_at timestamptz,
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    input_hash text NOT NULL,
    context_hash text NOT NULL DEFAULT '',
    schema_revision text NOT NULL,
    config_hash text NOT NULL,
    primary_provider text NOT NULL,
    primary_model text NOT NULL,
    final_provider text NOT NULL DEFAULT '',
    final_model text NOT NULL DEFAULT '',
    fallback_used boolean NOT NULL DEFAULT FALSE,
    fallback_reason text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    queue_latency_ms integer,
    generation_latency_ms integer,
    total_latency_ms integer,
    input_tokens integer,
    output_tokens integer,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    started_at timestamptz,
    completed_at timestamptz,
    cancelled_at timestamptz,
    CONSTRAINT tessa_runs_thread_fk FOREIGN KEY (thread_id, client_id)
        REFERENCES tessa_threads(id, client_id) ON DELETE CASCADE,
    CONSTRAINT tessa_runs_trigger_fk FOREIGN KEY (trigger_message_id, thread_id, client_id)
        REFERENCES tessa_messages(id, thread_id, client_id) ON DELETE RESTRICT,
    CONSTRAINT tessa_runs_id_thread_client_key UNIQUE (id, thread_id, client_id),
    CONSTRAINT tessa_runs_trigger_key UNIQUE (trigger_message_id),
    CONSTRAINT tessa_runs_status_check
        CHECK (status IN ('queued', 'processing', 'completed', 'failed', 'cancelled')),
    CONSTRAINT tessa_runs_stage_check
        CHECK (stage IN ('queued', 'planning', 'checking_help', 'checking_business', 'answering', 'completed')),
    CONSTRAINT tessa_runs_attempt_check CHECK (
        attempt_count >= 0 AND max_attempts BETWEEN 1 AND 5 AND attempt_count <= max_attempts
    ),
    CONSTRAINT tessa_runs_hash_check CHECK (
        input_hash ~ '^[a-f0-9]{64}$'
        AND (context_hash = '' OR context_hash ~ '^[a-f0-9]{64}$')
        AND config_hash ~ '^[a-f0-9]{64}$'
    ),
    CONSTRAINT tessa_runs_identity_length_check CHECK (
        char_length(schema_revision) BETWEEN 1 AND 80
        AND char_length(primary_provider) BETWEEN 1 AND 40
        AND char_length(primary_model) BETWEEN 1 AND 160
        AND char_length(final_provider) <= 40
        AND char_length(final_model) <= 160
        AND char_length(fallback_reason) <= 80
        AND char_length(error_code) <= 80
    ),
    CONSTRAINT tessa_runs_lease_check CHECK (
        (status = 'processing' AND btrim(lease_owner) <> ''
            AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (status <> 'processing' AND lease_owner = ''
            AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CONSTRAINT tessa_runs_terminal_check CHECK (
        (status IN ('queued', 'processing') AND completed_at IS NULL AND cancelled_at IS NULL)
        OR (status IN ('completed', 'failed') AND completed_at IS NOT NULL AND cancelled_at IS NULL)
        OR (status = 'cancelled' AND completed_at IS NULL AND cancelled_at IS NOT NULL)
    )
);

CREATE UNIQUE INDEX tessa_runs_one_active_per_thread
    ON tessa_runs (thread_id) WHERE status IN ('queued', 'processing');
CREATE INDEX tessa_runs_queue_idx
    ON tessa_runs (available_at, created_at, id)
    WHERE status IN ('queued', 'processing');
CREATE INDEX tessa_runs_client_created_idx
    ON tessa_runs (client_id, created_at DESC, id DESC);

ALTER TABLE tessa_messages ADD COLUMN run_id uuid;
ALTER TABLE tessa_messages ADD CONSTRAINT tessa_messages_result_run_fk
    FOREIGN KEY (run_id, thread_id, client_id)
    REFERENCES tessa_runs(id, thread_id, client_id) ON DELETE RESTRICT;
ALTER TABLE tessa_messages ADD CONSTRAINT tessa_messages_result_run_check CHECK (
    (sender_type = 'tessa' AND run_id IS NOT NULL)
    OR (sender_type <> 'tessa' AND run_id IS NULL)
);
CREATE UNIQUE INDEX tessa_messages_one_result_per_run
    ON tessa_messages (run_id) WHERE sender_type = 'tessa';

CREATE TABLE tessa_run_steps (
    id uuid PRIMARY KEY,
    run_id uuid NOT NULL REFERENCES tessa_runs(id) ON DELETE CASCADE,
    sequence integer NOT NULL,
    stage text NOT NULL,
    idempotency_key text,
    provider text NOT NULL DEFAULT '',
    model text NOT NULL DEFAULT '',
    tool_name text NOT NULL DEFAULT '',
    safe_argument_hash text NOT NULL DEFAULT '',
    status text NOT NULL,
    safe_result jsonb NOT NULL DEFAULT '{}'::jsonb,
    safe_result_count integer NOT NULL DEFAULT 0,
    duration_ms integer NOT NULL DEFAULT 0,
    input_tokens integer,
    output_tokens integer,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT tessa_run_steps_run_sequence_key UNIQUE (run_id, sequence),
    CONSTRAINT tessa_run_steps_sequence_check CHECK (sequence > 0),
    CONSTRAINT tessa_run_steps_stage_check CHECK (stage IN ('planning', 'tool', 'synthesis')),
    CONSTRAINT tessa_run_steps_status_check CHECK (status IN ('running', 'succeeded', 'failed')),
    CONSTRAINT tessa_run_steps_result_check CHECK (
        jsonb_typeof(safe_result) = 'object'
        AND octet_length(safe_result::text) <= 4096
        AND safe_result_count >= 0
        AND duration_ms >= 0
    ),
    CONSTRAINT tessa_run_steps_tool_key_check CHECK (
        (stage = 'tool' AND btrim(COALESCE(idempotency_key, '')) <> '' AND btrim(tool_name) <> '')
        OR (stage <> 'tool' AND idempotency_key IS NULL AND tool_name = '')
    ),
    CONSTRAINT tessa_run_steps_hash_check CHECK (
        safe_argument_hash = '' OR safe_argument_hash ~ '^[a-f0-9]{64}$'
    ),
    CONSTRAINT tessa_run_steps_lengths_check CHECK (
        char_length(COALESCE(idempotency_key, '')) <= 160
        AND char_length(provider) <= 40
        AND char_length(model) <= 160
        AND char_length(tool_name) <= 80
        AND char_length(error_code) <= 80
    )
);

CREATE UNIQUE INDEX tessa_run_steps_action_key
    ON tessa_run_steps (run_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE tessa_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    thread_id uuid NOT NULL,
    message_id uuid,
    run_id uuid,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT tessa_events_thread_fk FOREIGN KEY (thread_id, client_id)
        REFERENCES tessa_threads(id, client_id) ON DELETE CASCADE,
    CONSTRAINT tessa_events_message_fk FOREIGN KEY (message_id, thread_id, client_id)
        REFERENCES tessa_messages(id, thread_id, client_id) ON DELETE CASCADE,
    CONSTRAINT tessa_events_run_fk FOREIGN KEY (run_id, thread_id, client_id)
        REFERENCES tessa_runs(id, thread_id, client_id) ON DELETE CASCADE,
    CONSTRAINT tessa_events_type_check CHECK (event_type IN (
        'thread.created', 'message.created', 'run.started', 'run.stage_changed',
        'run.completed', 'run.failed', 'run.cancelled'
    )),
    CONSTRAINT tessa_events_payload_check CHECK (
        jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 4096
    ),
    CONSTRAINT tessa_events_reference_check CHECK (
        (event_type = 'thread.created' AND message_id IS NULL AND run_id IS NULL)
        OR (event_type = 'message.created' AND message_id IS NOT NULL)
        OR (event_type LIKE 'run.%' AND run_id IS NOT NULL)
    )
);

CREATE INDEX tessa_events_client_sequence_idx ON tessa_events (client_id, sequence);
CREATE INDEX tessa_events_created_at_idx ON tessa_events (created_at);

-- migrate:down
DROP TABLE tessa_events;
DROP TABLE tessa_run_steps;
ALTER TABLE tessa_messages DROP CONSTRAINT tessa_messages_result_run_fk;
DROP TABLE tessa_runs;
DROP TABLE tessa_messages;
DROP TABLE tessa_threads;
DROP TABLE tessa_preferences;
