-- migrate:up
ALTER TABLE inbox_ai_actions
    ADD COLUMN turn_job_id uuid REFERENCES inbox_ai_turn_jobs(id) ON DELETE SET NULL;
CREATE INDEX inbox_ai_actions_turn_job_idx
    ON inbox_ai_actions (turn_job_id, started_at, id)
    WHERE turn_job_id IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS inbox_ai_actions_turn_job_idx;
ALTER TABLE inbox_ai_actions DROP COLUMN turn_job_id;
