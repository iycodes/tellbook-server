-- migrate:up
CREATE TABLE inbox_ai_runs (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    requested_by uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    mode text NOT NULL DEFAULT 'manual',
    trigger_type text NOT NULL DEFAULT 'provider_on_demand',
    status text NOT NULL,
    model_provider text NOT NULL,
    model_name text NOT NULL,
    context_version integer NOT NULL,
    context_hash text NOT NULL,
    latest_message_sequence bigint NOT NULL DEFAULT 0,
    input_character_count integer NOT NULL DEFAULT 0,
    output_draft text NOT NULL DEFAULT '',
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
    error_code text NOT NULL DEFAULT '',
    latency_ms integer,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT inbox_ai_runs_mode_check CHECK (mode = 'manual'),
    CONSTRAINT inbox_ai_runs_trigger_check CHECK (trigger_type = 'provider_on_demand'),
    CONSTRAINT inbox_ai_runs_status_check CHECK (status IN ('running', 'completed', 'failed')),
    CONSTRAINT inbox_ai_runs_model_check CHECK (
        btrim(model_provider) <> '' AND char_length(model_provider) <= 80
        AND btrim(model_name) <> '' AND char_length(model_name) <= 240
    ),
    CONSTRAINT inbox_ai_runs_context_check CHECK (
        context_version > 0
        AND context_hash ~ '^[a-f0-9]{64}$'
        AND latest_message_sequence >= 0
        AND input_character_count >= 0
    ),
    CONSTRAINT inbox_ai_runs_output_check CHECK (char_length(output_draft) <= 4000),
    CONSTRAINT inbox_ai_runs_warnings_check CHECK (jsonb_typeof(warnings) = 'array'),
    CONSTRAINT inbox_ai_runs_latency_check CHECK (latency_ms IS NULL OR latency_ms >= 0),
    CONSTRAINT inbox_ai_runs_completion_check CHECK (
        (status = 'running' AND completed_at IS NULL AND output_draft = '' AND error_code = '' AND latency_ms IS NULL)
        OR (status = 'completed' AND completed_at IS NOT NULL AND btrim(output_draft) <> '' AND error_code = '' AND latency_ms IS NOT NULL)
        OR (status = 'failed' AND completed_at IS NOT NULL AND output_draft = '' AND btrim(error_code) <> '' AND latency_ms IS NOT NULL)
    )
);

CREATE INDEX inbox_ai_runs_conversation_created_idx
    ON inbox_ai_runs (conversation_id, created_at DESC, id DESC);
CREATE INDEX inbox_ai_runs_client_created_idx
    ON inbox_ai_runs (client_id, created_at DESC, id DESC);

-- migrate:down
DROP TABLE inbox_ai_runs;
