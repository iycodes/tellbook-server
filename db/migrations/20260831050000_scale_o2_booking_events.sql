-- migrate:up
CREATE FUNCTION notify_booking_domain_event() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify(
        'tellbook_booking_events',
        NEW.sequence::text || '|' || NEW.client_id::text
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER booking_domain_events_notify
AFTER INSERT ON booking_domain_events
FOR EACH ROW EXECUTE FUNCTION notify_booking_domain_event();

ALTER TABLE financial_jobs DROP CONSTRAINT financial_jobs_status_check;
ALTER TABLE financial_jobs ADD CONSTRAINT financial_jobs_status_check
    CHECK (status IN ('pending','processing','completed','failed','cancelled','dead_letter'));

CREATE TABLE payment_provider_request_budgets (
    provider text PRIMARY KEY,
    next_allowed_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_provider_request_budgets_provider_check CHECK (BTRIM(provider) <> '')
);

CREATE FUNCTION notify_core_worker_queue() RETURNS trigger AS $$
BEGIN
    IF NEW.status IN ('pending','failed','queued') THEN
        PERFORM pg_notify('tellbook_worker_core', TG_TABLE_NAME);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER financial_jobs_wake_core
AFTER INSERT OR UPDATE OF status, available_at ON financial_jobs
FOR EACH ROW EXECUTE FUNCTION notify_core_worker_queue();
CREATE TRIGGER booking_refund_requests_wake_core
AFTER INSERT OR UPDATE OF status ON booking_refund_requests
FOR EACH ROW EXECUTE FUNCTION notify_core_worker_queue();
CREATE TRIGGER agreement_template_generation_jobs_wake_core
AFTER INSERT OR UPDATE OF status, run_at ON agreement_template_generation_jobs
FOR EACH ROW EXECUTE FUNCTION notify_core_worker_queue();
CREATE TRIGGER agreement_jobs_wake_core
AFTER INSERT OR UPDATE OF status, run_at ON agreement_jobs
FOR EACH ROW EXECUTE FUNCTION notify_core_worker_queue();

CREATE FUNCTION notify_ai_worker_queue() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'queued' THEN
        PERFORM pg_notify('tellbook_worker_ai', TG_TABLE_NAME);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tessa_runs_wake_ai
AFTER INSERT OR UPDATE OF status, available_at ON tessa_runs
FOR EACH ROW EXECUTE FUNCTION notify_ai_worker_queue();
CREATE TRIGGER inbox_ai_turn_jobs_wake_ai
AFTER INSERT OR UPDATE OF status, available_at ON inbox_ai_turn_jobs
FOR EACH ROW EXECUTE FUNCTION notify_ai_worker_queue();

ALTER TABLE inbox_ai_runs
    ADD COLUMN input_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT NOW(),
    ADD COLUMN attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN max_attempts integer NOT NULL DEFAULT 3,
    ADD COLUMN lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN lease_expires_at timestamptz;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_status_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_completion_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_status_check
    CHECK (status IN ('queued','processing','running','completed','failed'));
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_completion_check CHECK (
    (status IN ('queued','processing','running') AND completed_at IS NULL
        AND output_draft='' AND error_code='' AND latency_ms IS NULL)
    OR (status='completed' AND completed_at IS NOT NULL
        AND BTRIM(output_draft)<>'' AND error_code='' AND latency_ms IS NOT NULL)
    OR (status='failed' AND completed_at IS NOT NULL
        AND output_draft='' AND BTRIM(error_code)<>'' AND latency_ms IS NOT NULL)
);
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_queue_check CHECK (
    attempt_count BETWEEN 0 AND max_attempts AND max_attempts BETWEEN 1 AND 5
    AND jsonb_typeof(input_snapshot)='object'
    AND ((status='processing' AND BTRIM(lease_owner)<>'' AND lease_expires_at IS NOT NULL)
      OR (status<>'processing' AND lease_owner='' AND lease_expires_at IS NULL))
    AND (mode='manual' OR (status NOT IN ('queued','processing') AND input_snapshot='{}'::jsonb))
);
CREATE INDEX inbox_ai_runs_manual_ready_idx
    ON inbox_ai_runs (available_at,created_at,id)
    WHERE mode='manual' AND status IN ('queued','processing');
CREATE TRIGGER inbox_ai_runs_wake_ai
AFTER INSERT OR UPDATE OF status, available_at ON inbox_ai_runs
FOR EACH ROW EXECUTE FUNCTION notify_ai_worker_queue();

-- migrate:down
DROP TRIGGER booking_domain_events_notify ON booking_domain_events;
DROP FUNCTION notify_booking_domain_event();
DROP TRIGGER inbox_ai_turn_jobs_wake_ai ON inbox_ai_turn_jobs;
DROP TRIGGER tessa_runs_wake_ai ON tessa_runs;
DROP TRIGGER inbox_ai_runs_wake_ai ON inbox_ai_runs;
DROP INDEX inbox_ai_runs_manual_ready_idx;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_queue_check;
UPDATE inbox_ai_runs
SET status='failed',error_code='migration_rollback',completed_at=NOW(),latency_ms=0,
    lease_owner='',lease_expires_at=NULL
WHERE status IN ('queued','processing');
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_completion_check;
ALTER TABLE inbox_ai_runs DROP CONSTRAINT inbox_ai_runs_status_check;
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_status_check
    CHECK (status IN ('running','completed','failed'));
ALTER TABLE inbox_ai_runs ADD CONSTRAINT inbox_ai_runs_completion_check CHECK (
    (status='running' AND completed_at IS NULL AND output_draft='' AND error_code='' AND latency_ms IS NULL)
    OR (status='completed' AND completed_at IS NOT NULL AND BTRIM(output_draft)<>'' AND error_code='' AND latency_ms IS NOT NULL)
    OR (status='failed' AND completed_at IS NOT NULL AND output_draft='' AND BTRIM(error_code)<>'' AND latency_ms IS NOT NULL)
);
ALTER TABLE inbox_ai_runs
    DROP COLUMN input_snapshot,
    DROP COLUMN available_at,
    DROP COLUMN attempt_count,
    DROP COLUMN max_attempts,
    DROP COLUMN lease_owner,
    DROP COLUMN lease_expires_at;
DROP FUNCTION notify_ai_worker_queue();
DROP TRIGGER agreement_jobs_wake_core ON agreement_jobs;
DROP TRIGGER agreement_template_generation_jobs_wake_core ON agreement_template_generation_jobs;
DROP TRIGGER booking_refund_requests_wake_core ON booking_refund_requests;
DROP TRIGGER financial_jobs_wake_core ON financial_jobs;
DROP FUNCTION notify_core_worker_queue();
DROP TABLE payment_provider_request_budgets;
ALTER TABLE financial_jobs DROP CONSTRAINT financial_jobs_status_check;
ALTER TABLE financial_jobs ADD CONSTRAINT financial_jobs_status_check
    CHECK (status IN ('pending','processing','completed','failed','cancelled'));
