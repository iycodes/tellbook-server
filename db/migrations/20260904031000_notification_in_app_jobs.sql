-- migrate:up
CREATE TABLE notification_in_app_jobs (
    id uuid PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    audience_type text NOT NULL,
    reminder_occurrence_at timestamptz NOT NULL,
    scheduled_for timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT notification_in_app_jobs_audience_check CHECK (audience_type IN ('provider','customer')),
    CONSTRAINT notification_in_app_jobs_customer_check CHECK (
        audience_type='provider' OR marketplace_customer_id IS NOT NULL
    ),
    CONSTRAINT notification_in_app_jobs_status_check
        CHECK (status IN ('pending','processing','retry','completed','dead_letter','cancelled')),
    CONSTRAINT notification_in_app_jobs_attempt_check CHECK (attempt_count BETWEEN 0 AND 20),
    CONSTRAINT notification_in_app_jobs_error_check CHECK (char_length(last_error_code)<=80),
    CONSTRAINT notification_in_app_jobs_lease_check CHECK (
        (status='processing' AND btrim(lease_owner)<>'' AND lease_expires_at IS NOT NULL)
        OR (status<>'processing' AND lease_owner='' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT notification_in_app_jobs_completion_check CHECK (
        (status IN ('completed','dead_letter','cancelled') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed','dead_letter','cancelled') AND completed_at IS NULL)
    )
);

CREATE INDEX notification_in_app_jobs_due_idx
    ON notification_in_app_jobs (next_attempt_at,scheduled_for,id)
    WHERE status IN ('pending','retry','processing');

CREATE UNIQUE INDEX notifications_planner_event_key_idx
    ON notifications (client_id,(metadata->>'notification_planner_key'))
    WHERE metadata ? 'notification_planner_key';

-- migrate:down
DROP INDEX IF EXISTS notifications_planner_event_key_idx;
DROP TABLE IF EXISTS notification_in_app_jobs;
