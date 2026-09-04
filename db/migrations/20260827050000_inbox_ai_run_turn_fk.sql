-- migrate:up
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_turn_job_id_fkey;
ALTER TABLE inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_turn_job_id_fkey
    FOREIGN KEY (turn_job_id) REFERENCES inbox_ai_turn_jobs(id) ON DELETE CASCADE;

-- migrate:down
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_turn_job_id_fkey;
ALTER TABLE inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_turn_job_id_fkey
    FOREIGN KEY (turn_job_id) REFERENCES inbox_ai_turn_jobs(id) ON DELETE SET NULL;
