-- migrate:up
CREATE INDEX inbox_ai_turn_jobs_client_created_idx
    ON inbox_ai_turn_jobs (client_id, created_at DESC);
CREATE INDEX inbox_ai_turn_jobs_customer_created_idx
    ON inbox_ai_turn_jobs (marketplace_customer_id, created_at DESC);

-- migrate:down
DROP INDEX IF EXISTS inbox_ai_turn_jobs_customer_created_idx;
DROP INDEX IF EXISTS inbox_ai_turn_jobs_client_created_idx;
