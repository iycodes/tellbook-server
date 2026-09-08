-- migrate:up

-- Pre-A2 marketplace challenges were delivered synchronously and cannot be
-- safely interpreted using the acceptance-anchored lifecycle.
DELETE FROM auth_code_delivery_jobs;
DELETE FROM marketplace_auth_challenges;

ALTER TABLE marketplace_auth_challenges
    DROP COLUMN expires_at,
    ALTER COLUMN delivery_deadline SET NOT NULL;

ALTER TABLE auth_code_delivery_jobs
    ADD CONSTRAINT auth_code_delivery_jobs_attempt_limit_check
        CHECK (attempt_count BETWEEN 0 AND 8),
    ADD CONSTRAINT auth_code_delivery_jobs_error_code_check
        CHECK (char_length(error_code) <= 100);

CREATE FUNCTION notify_auth_code_delivery_job() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('tellbook_worker_core','auth_code_delivery');
    RETURN NEW;
END;
$$;

CREATE TRIGGER auth_code_delivery_jobs_wake
    AFTER INSERT ON auth_code_delivery_jobs
    FOR EACH STATEMENT EXECUTE FUNCTION notify_auth_code_delivery_job();

-- migrate:down
DO $$
BEGIN
    RAISE EXCEPTION '20260905021000 is intentionally irreversible: acceptance-anchored auth challenges cannot be downgraded safely';
END
$$;
