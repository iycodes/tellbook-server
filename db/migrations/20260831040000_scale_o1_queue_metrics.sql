-- migrate:up
CREATE INDEX agreement_jobs_active_metrics_idx
    ON agreement_jobs (created_at) INCLUDE (attempt_count)
    WHERE status IN ('queued', 'processing');
CREATE INDEX agreement_jobs_recent_completed_metrics_idx
    ON agreement_jobs (completed_at) WHERE completed_at IS NOT NULL;

CREATE INDEX agreement_template_generation_jobs_active_metrics_idx
    ON agreement_template_generation_jobs (created_at) INCLUDE (attempt_count)
    WHERE status IN ('queued', 'processing');
CREATE INDEX agreement_template_generation_jobs_recent_completed_metrics_idx
    ON agreement_template_generation_jobs (completed_at) WHERE completed_at IS NOT NULL;

CREATE INDEX financial_jobs_active_metrics_idx
    ON financial_jobs (created_at) INCLUDE (attempts)
    WHERE status IN ('pending', 'failed', 'processing');
CREATE INDEX financial_jobs_recent_completed_metrics_idx
    ON financial_jobs (completed_at) WHERE completed_at IS NOT NULL;

CREATE INDEX inbox_ai_turn_jobs_active_metrics_idx
    ON inbox_ai_turn_jobs (created_at) INCLUDE (attempt_count)
    WHERE status IN ('queued', 'processing');
CREATE INDEX inbox_ai_turn_jobs_recent_completed_metrics_idx
    ON inbox_ai_turn_jobs (completed_at) WHERE completed_at IS NOT NULL;

CREATE INDEX tessa_runs_active_metrics_idx
    ON tessa_runs (created_at) INCLUDE (attempt_count)
    WHERE status IN ('queued', 'processing');
CREATE INDEX tessa_runs_recent_completed_metrics_idx
    ON tessa_runs (completed_at) WHERE completed_at IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS tessa_runs_recent_completed_metrics_idx;
DROP INDEX IF EXISTS tessa_runs_active_metrics_idx;
DROP INDEX IF EXISTS inbox_ai_turn_jobs_recent_completed_metrics_idx;
DROP INDEX IF EXISTS inbox_ai_turn_jobs_active_metrics_idx;
DROP INDEX IF EXISTS financial_jobs_recent_completed_metrics_idx;
DROP INDEX IF EXISTS financial_jobs_active_metrics_idx;
DROP INDEX IF EXISTS agreement_template_generation_jobs_recent_completed_metrics_idx;
DROP INDEX IF EXISTS agreement_template_generation_jobs_active_metrics_idx;
DROP INDEX IF EXISTS agreement_jobs_recent_completed_metrics_idx;
DROP INDEX IF EXISTS agreement_jobs_active_metrics_idx;
