-- migrate:up
CREATE INDEX inbox_ai_turn_jobs_terminal_completed_idx
    ON inbox_ai_turn_jobs (completed_at, id)
    WHERE status IN ('completed','failed','cancelled');

-- migrate:down
DROP INDEX IF EXISTS inbox_ai_turn_jobs_terminal_completed_idx;
