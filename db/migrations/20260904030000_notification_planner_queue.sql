-- migrate:up
CREATE TABLE notification_event_jobs (
    booking_event_id uuid PRIMARY KEY REFERENCES booking_domain_events(id) ON DELETE CASCADE,
    event_sequence bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT notification_event_jobs_status_check
        CHECK (status IN ('pending','processing','retry','completed','dead_letter')),
    CONSTRAINT notification_event_jobs_attempt_check CHECK (attempt_count BETWEEN 0 AND 20),
    CONSTRAINT notification_event_jobs_sequence_check CHECK (event_sequence > 0),
    CONSTRAINT notification_event_jobs_error_check CHECK (char_length(last_error_code) <= 80),
    CONSTRAINT notification_event_jobs_lease_check CHECK (
        (status='processing' AND btrim(lease_owner)<>'' AND lease_expires_at IS NOT NULL)
        OR (status<>'processing' AND lease_owner='' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT notification_event_jobs_completion_check CHECK (
        (status IN ('completed','dead_letter') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed','dead_letter') AND completed_at IS NULL)
    )
);

CREATE INDEX notification_event_jobs_claim_idx
    ON notification_event_jobs (
        (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
        event_sequence, booking_event_id
    ) WHERE status IN ('pending','retry','processing');

CREATE TABLE notification_deliveries (
    id uuid PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE SET NULL,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    booking_event_id uuid REFERENCES booking_domain_events(id) ON DELETE SET NULL,
    booking_event_sequence bigint NOT NULL,
    audience_type text NOT NULL,
    channel text NOT NULL,
    notification_type text NOT NULL,
    template_key text NOT NULL DEFAULT '',
    reminder_occurrence_at timestamptz,
    reminder_offset_minutes integer,
    scheduled_for timestamptz NOT NULL,
    destination_hmac bytea NOT NULL,
    preference_revision bigint NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    dispatch_authorized_at timestamptz,
    authorized_booking_event_sequence bigint,
    authorized_preference_revision bigint,
    reconcile_after timestamptz,
    provider_message_id text NOT NULL DEFAULT '',
    provider_status text NOT NULL DEFAULT '',
    provider_error_code text NOT NULL DEFAULT '',
    provider_status_at timestamptz,
    accepted_at timestamptz,
    sent_at timestamptz,
    delivered_at timestamptz,
    read_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT notification_deliveries_idempotency_check
        CHECK (btrim(idempotency_key)<>'' AND char_length(idempotency_key)<=240),
    CONSTRAINT notification_deliveries_audience_check
        CHECK (audience_type IN ('provider','customer')),
    CONSTRAINT notification_deliveries_channel_check CHECK (channel IN ('email','whatsapp')),
    CONSTRAINT notification_deliveries_type_check
        CHECK (btrim(notification_type)<>'' AND char_length(notification_type)<=80),
    CONSTRAINT notification_deliveries_template_check CHECK (char_length(template_key)<=80),
    CONSTRAINT notification_deliveries_hmac_check CHECK (octet_length(destination_hmac)=32),
    CONSTRAINT notification_deliveries_status_check CHECK (status IN (
        'pending','processing','dispatching','accepted','sent','delivered','read','retry',
        'unknown','manual_review','failed','deleted','cancelled'
    )),
    CONSTRAINT notification_deliveries_attempt_check CHECK (attempt_count BETWEEN 0 AND 20),
    CONSTRAINT notification_deliveries_error_check CHECK (
        char_length(last_error_code)<=80 AND char_length(provider_error_code)<=120
    ),
    CONSTRAINT notification_deliveries_reminder_check CHECK (
        (notification_type='appointment_reminder' AND reminder_occurrence_at IS NOT NULL
            AND reminder_offset_minutes=1440)
        OR (notification_type<>'appointment_reminder' AND reminder_occurrence_at IS NULL
            AND reminder_offset_minutes IS NULL)
    ),
    CONSTRAINT notification_deliveries_lease_check CHECK (
        (status='processing' AND btrim(lease_owner)<>'' AND lease_expires_at IS NOT NULL)
        OR (status<>'processing' AND lease_owner='' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT notification_deliveries_dispatch_check CHECK (
        (dispatch_authorized_at IS NULL AND authorized_booking_event_sequence IS NULL
            AND authorized_preference_revision IS NULL)
        OR (dispatch_authorized_at IS NOT NULL AND authorized_booking_event_sequence IS NOT NULL
            AND authorized_preference_revision IS NOT NULL)
    )
);

CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries (channel, next_attempt_at, scheduled_for, id)
    WHERE status IN ('pending','retry','processing');
CREATE INDEX notification_deliveries_booking_active_idx
    ON notification_deliveries (booking_id, notification_type, status, scheduled_for, id)
    WHERE status IN ('pending','retry','processing','dispatching','unknown','manual_review');
CREATE UNIQUE INDEX notification_deliveries_provider_message_idx
    ON notification_deliveries (provider_message_id)
    WHERE provider_message_id<>'';
CREATE INDEX notification_deliveries_terminal_retention_idx
    ON notification_deliveries (completed_at, id)
    WHERE status IN ('failed','deleted','cancelled','read','delivered');

CREATE FUNCTION enqueue_notification_event_job() RETURNS trigger AS $$
BEGIN
    INSERT INTO notification_event_jobs (booking_event_id,event_sequence)
    VALUES (NEW.id,NEW.sequence)
    ON CONFLICT (booking_event_id) DO NOTHING;
    PERFORM pg_notify('tellbook_worker_core','notification_event');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER booking_domain_events_enqueue_notification
AFTER INSERT ON booking_domain_events
FOR EACH ROW EXECUTE FUNCTION enqueue_notification_event_job();

CREATE OR REPLACE FUNCTION append_booking_update_event() RETURNS trigger AS $$
BEGIN
    IF ROW(OLD.status,OLD.payment_status,OLD.agreement_status,OLD.start_at,OLD.end_at)
       IS DISTINCT FROM
       ROW(NEW.status,NEW.payment_status,NEW.agreement_status,NEW.start_at,NEW.end_at) THEN
        INSERT INTO booking_domain_events (
            id,client_id,booking_id,event_type,dedupe_key,payload,created_at
        ) VALUES (
            gen_random_uuid(),NEW.client_id,NEW.id,'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id',NEW.id::text,
                'status',NEW.status,'previous_status',OLD.status,
                'payment_status',NEW.payment_status,'previous_payment_status',OLD.payment_status,
                'agreement_status',NEW.agreement_status,'previous_agreement_status',OLD.agreement_status,
                'starts_at',NEW.start_at,'previous_starts_at',OLD.start_at,
                'ends_at',NEW.end_at,'previous_ends_at',OLD.end_at
            ),NOW()
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

INSERT INTO notification_event_jobs (booking_event_id,event_sequence)
SELECT DISTINCT ON (event.booking_id) event.id,event.sequence
FROM booking_domain_events event
JOIN bookings booking ON booking.id=event.booking_id
WHERE booking.start_at>NOW()
  AND booking.status NOT IN ('cancelled','canceled','declined','completed','no_show','expired')
ORDER BY event.booking_id,event.sequence DESC
ON CONFLICT (booking_event_id) DO NOTHING;

-- migrate:down
DROP TRIGGER IF EXISTS booking_domain_events_enqueue_notification ON booking_domain_events;
DROP FUNCTION IF EXISTS enqueue_notification_event_job();
CREATE OR REPLACE FUNCTION append_booking_update_event() RETURNS trigger AS $$
BEGIN
    IF ROW(OLD.status, OLD.payment_status, OLD.agreement_status, OLD.start_at, OLD.end_at)
       IS DISTINCT FROM
       ROW(NEW.status, NEW.payment_status, NEW.agreement_status, NEW.start_at, NEW.end_at) THEN
        INSERT INTO booking_domain_events (
            id, client_id, booking_id, event_type, dedupe_key, payload, created_at
        ) VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, 'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'status', NEW.status,
                'payment_status', NEW.payment_status,
                'agreement_status', NEW.agreement_status,
                'starts_at', NEW.start_at
            ),NOW()
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS notification_event_jobs;
