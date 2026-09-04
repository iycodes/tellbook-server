-- migrate:up
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_provider_result_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_provider_result_check CHECK (
    (provider_outcome IN ('sent_unchanged', 'sent_edited')
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

-- migrate:down
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_provider_result_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_provider_result_check CHECK (
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
