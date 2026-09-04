-- migrate:up
ALTER TABLE inbox_ai_runs
    ADD COLUMN request_id uuid,
    ADD COLUMN prompt_input_hash text,
    ADD COLUMN prompt_version integer,
    ADD COLUMN model_config_hash text,
    ADD COLUMN needs_provider_input boolean,
    ADD COLUMN provider_outcome text NOT NULL DEFAULT 'pending',
    ADD COLUMN provider_message_id uuid REFERENCES inbox_messages(id) ON DELETE SET NULL,
    ADD COLUMN provider_final_content_hash text,
    ADD COLUMN provider_outcome_at timestamptz;

UPDATE inbox_ai_runs
SET request_id = id,
    prompt_input_hash = context_hash,
    prompt_version = 1,
    model_config_hash = repeat('0', 64),
    needs_provider_input = CASE WHEN status = 'completed' THEN false ELSE NULL END,
    provider_outcome = CASE WHEN status = 'completed' THEN 'pending' ELSE 'unavailable' END;

ALTER TABLE inbox_ai_runs
    ALTER COLUMN request_id SET NOT NULL,
    ALTER COLUMN prompt_input_hash SET NOT NULL,
    ALTER COLUMN prompt_version SET NOT NULL,
    ALTER COLUMN model_config_hash SET NOT NULL,
    ADD CONSTRAINT inbox_ai_runs_prompt_hash_check CHECK (
        prompt_input_hash ~ '^[a-f0-9]{64}$'
        AND model_config_hash ~ '^[a-f0-9]{64}$'
        AND prompt_version > 0
    ),
    ADD CONSTRAINT inbox_ai_runs_provider_outcome_check CHECK (
        provider_outcome IN ('pending', 'sent_unchanged', 'sent_edited', 'discarded', 'stale', 'unavailable')
    ),
    ADD CONSTRAINT inbox_ai_runs_provider_result_check CHECK (
        (provider_outcome IN ('sent_unchanged', 'sent_edited')
            AND provider_message_id IS NOT NULL
            AND provider_final_content_hash ~ '^[a-f0-9]{64}$'
            AND provider_outcome_at IS NOT NULL)
        OR (provider_outcome IN ('discarded', 'stale')
            AND provider_message_id IS NULL
            AND provider_final_content_hash IS NULL
            AND provider_outcome_at IS NOT NULL)
        OR (provider_outcome IN ('pending', 'unavailable')
            AND provider_message_id IS NULL
            AND provider_final_content_hash IS NULL
            AND provider_outcome_at IS NULL)
    );

CREATE UNIQUE INDEX inbox_ai_runs_request_idempotency_idx
    ON inbox_ai_runs (client_id, conversation_id, request_id);
CREATE UNIQUE INDEX inbox_ai_runs_provider_message_idx
    ON inbox_ai_runs (provider_message_id)
    WHERE provider_message_id IS NOT NULL;
CREATE INDEX inbox_ai_runs_running_created_idx
    ON inbox_ai_runs (created_at)
    WHERE status = 'running';

-- migrate:down
DROP INDEX IF EXISTS inbox_ai_runs_running_created_idx;
DROP INDEX IF EXISTS inbox_ai_runs_provider_message_idx;
DROP INDEX IF EXISTS inbox_ai_runs_request_idempotency_idx;
ALTER TABLE inbox_ai_runs
    DROP CONSTRAINT IF EXISTS inbox_ai_runs_provider_result_check,
    DROP CONSTRAINT IF EXISTS inbox_ai_runs_provider_outcome_check,
    DROP CONSTRAINT IF EXISTS inbox_ai_runs_prompt_hash_check,
    DROP COLUMN IF EXISTS provider_outcome_at,
    DROP COLUMN IF EXISTS provider_final_content_hash,
    DROP COLUMN IF EXISTS provider_message_id,
    DROP COLUMN IF EXISTS provider_outcome,
    DROP COLUMN IF EXISTS needs_provider_input,
    DROP COLUMN IF EXISTS model_config_hash,
    DROP COLUMN IF EXISTS prompt_version,
    DROP COLUMN IF EXISTS prompt_input_hash,
    DROP COLUMN IF EXISTS request_id;
