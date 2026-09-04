-- migrate:up
CREATE TABLE inbox_ai_turn_jobs (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES inbox_ai_sessions(id) ON DELETE CASCADE,
    trigger_message_id uuid NOT NULL REFERENCES inbox_messages(id) ON DELETE CASCADE,
    trigger_message_sequence bigint NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    attempt_count integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 3,
    turn_number integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT inbox_ai_turn_jobs_status_check CHECK (
        status IN ('queued','processing','completed','failed','cancelled')
    ),
    CONSTRAINT inbox_ai_turn_jobs_attempt_check CHECK (
        attempt_count BETWEEN 0 AND max_attempts
        AND max_attempts BETWEEN 1 AND 5
        AND turn_number >= 0
        AND trigger_message_sequence > 0
    ),
    CONSTRAINT inbox_ai_turn_jobs_lease_check CHECK (
        (status = 'processing' AND btrim(lease_owner) <> '' AND lease_expires_at IS NOT NULL)
        OR (status <> 'processing' AND lease_owner = '' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT inbox_ai_turn_jobs_completion_check CHECK (
        (status IN ('completed','failed','cancelled') AND completed_at IS NOT NULL)
        OR (status IN ('queued','processing') AND completed_at IS NULL)
    ),
    CONSTRAINT inbox_ai_turn_jobs_error_check CHECK (
        char_length(error_code) <= 80
        AND ((status IN ('failed','cancelled') AND btrim(error_code) <> '')
          OR (status IN ('queued','processing','completed') AND error_code = ''))
    ),
    UNIQUE (trigger_message_id),
    UNIQUE (conversation_id, trigger_message_sequence)
);

CREATE INDEX inbox_ai_turn_jobs_ready_idx
    ON inbox_ai_turn_jobs (available_at, created_at, id)
    WHERE status IN ('queued','processing');
CREATE INDEX inbox_ai_turn_jobs_conversation_idx
    ON inbox_ai_turn_jobs (conversation_id, created_at DESC, id DESC);

ALTER TABLE inbox_ai_runs
    DROP CONSTRAINT inbox_ai_runs_mode_check,
    DROP CONSTRAINT inbox_ai_runs_trigger_check,
    ADD COLUMN turn_job_id uuid REFERENCES inbox_ai_turn_jobs(id) ON DELETE SET NULL,
    ADD COLUMN structured_decision jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN tool_call_count integer NOT NULL DEFAULT 0,
    ADD COLUMN resulting_message_id uuid REFERENCES inbox_messages(id) ON DELETE SET NULL,
    ADD CONSTRAINT inbox_ai_runs_mode_check CHECK (mode IN ('manual','semi_pilot')),
    ADD CONSTRAINT inbox_ai_runs_trigger_check CHECK (
        (mode='manual' AND trigger_type='provider_on_demand')
        OR (mode='semi_pilot' AND trigger_type='customer_message')
    ),
    ADD CONSTRAINT inbox_ai_runs_turn_check CHECK (
        jsonb_typeof(structured_decision)='object'
        AND tool_call_count BETWEEN 0 AND 4
        AND ((mode='manual' AND turn_job_id IS NULL AND resulting_message_id IS NULL)
          OR (mode='semi_pilot' AND turn_job_id IS NOT NULL))
    );

CREATE UNIQUE INDEX inbox_ai_runs_turn_job_idx
    ON inbox_ai_runs (turn_job_id)
    WHERE turn_job_id IS NOT NULL;
CREATE UNIQUE INDEX inbox_ai_runs_resulting_message_idx
    ON inbox_ai_runs (resulting_message_id)
    WHERE resulting_message_id IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS inbox_ai_runs_resulting_message_idx;
DROP INDEX IF EXISTS inbox_ai_runs_turn_job_idx;
ALTER TABLE inbox_ai_runs
    DROP CONSTRAINT inbox_ai_runs_turn_check,
    DROP CONSTRAINT inbox_ai_runs_trigger_check,
    DROP CONSTRAINT inbox_ai_runs_mode_check,
    DROP COLUMN resulting_message_id,
    DROP COLUMN tool_call_count,
    DROP COLUMN structured_decision,
    DROP COLUMN turn_job_id,
    ADD CONSTRAINT inbox_ai_runs_mode_check CHECK (mode = 'manual'),
    ADD CONSTRAINT inbox_ai_runs_trigger_check CHECK (trigger_type = 'provider_on_demand');
DROP TABLE inbox_ai_turn_jobs;
