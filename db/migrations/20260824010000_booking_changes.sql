-- migrate:up
ALTER TABLE bookings
    ADD COLUMN cancellation_notice_minutes_snapshot integer NOT NULL DEFAULT 0,
    ADD COLUMN cancellation_refund_bps_snapshot integer NOT NULL DEFAULT 0,
    ADD COLUMN reschedule_notice_minutes_snapshot integer NOT NULL DEFAULT 0,
    ADD COLUMN reschedule_fee_minor_snapshot bigint NOT NULL DEFAULT 0,
    ADD COLUMN automated_reschedule_snapshot boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT bookings_change_policy_snapshot_check CHECK (
        cancellation_notice_minutes_snapshot >= 0
        AND cancellation_refund_bps_snapshot BETWEEN 0 AND 10000
        AND reschedule_notice_minutes_snapshot >= 0
        AND reschedule_fee_minor_snapshot >= 0
    );

UPDATE bookings
SET cancellation_notice_minutes_snapshot = CASE
        WHEN LOWER(cancellation_policy_snapshot) LIKE '%48h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%48 hour%' THEN 2880
        WHEN LOWER(cancellation_policy_snapshot) LIKE '%24h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%24 hour%' THEN 1440
        ELSE 0
    END,
    cancellation_refund_bps_snapshot = CASE
        WHEN LOWER(cancellation_policy_snapshot) LIKE '%no refund%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%non-refundable%' THEN 0
        WHEN LOWER(cancellation_policy_snapshot) = 'no cancellation fee'
          OR LOWER(cancellation_policy_snapshot) LIKE '%24h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%24 hour%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%48h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%48 hour%' THEN 10000
        ELSE 0
    END,
    reschedule_notice_minutes_snapshot = CASE
        WHEN LOWER(cancellation_policy_snapshot) LIKE '%48h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%48 hour%' THEN 2880
        WHEN LOWER(cancellation_policy_snapshot) LIKE '%24h%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%24 hour%'
          OR LOWER(cancellation_policy_snapshot) LIKE '%no refund%' THEN 1440
        ELSE 0
    END,
    automated_reschedule_snapshot = (
        LOWER(cancellation_policy_snapshot) = 'no cancellation fee'
        OR LOWER(cancellation_policy_snapshot) LIKE '%24h%'
        OR LOWER(cancellation_policy_snapshot) LIKE '%24 hour%'
        OR LOWER(cancellation_policy_snapshot) LIKE '%48h%'
        OR LOWER(cancellation_policy_snapshot) LIKE '%48 hour%'
        OR LOWER(cancellation_policy_snapshot) LIKE '%no refund%'
        OR LOWER(cancellation_policy_snapshot) LIKE '%non-refundable%'
    );

CREATE TABLE booking_change_quotes (
    id uuid PRIMARY KEY,
    public_token text NOT NULL UNIQUE,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    kind text NOT NULL,
    idempotency_key uuid NOT NULL,
    request_fingerprint text NOT NULL,
    booking_updated_at timestamptz NOT NULL,
    current_start_at timestamptz NOT NULL,
    current_end_at timestamptz NOT NULL,
    proposed_start_at timestamptz,
    proposed_end_at timestamptz,
    proposed_occupied_start_at timestamptz,
    proposed_occupied_end_at timestamptz,
    refund_amount_minor bigint NOT NULL DEFAULT 0,
    retained_amount_minor bigint NOT NULL DEFAULT 0,
    fee_amount_minor bigint NOT NULL DEFAULT 0,
    currency_code text NOT NULL,
    policy_message text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT booking_change_quotes_kind_check CHECK (kind IN ('cancellation', 'reschedule')),
    CONSTRAINT booking_change_quotes_amount_check CHECK (
        refund_amount_minor >= 0 AND retained_amount_minor >= 0 AND fee_amount_minor >= 0
    ),
    CONSTRAINT booking_change_quotes_currency_check CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT booking_change_quotes_fingerprint_check CHECK (request_fingerprint ~ '^[a-f0-9]{64}$'),
    CONSTRAINT booking_change_quotes_expiry_check CHECK (expires_at > created_at),
    CONSTRAINT booking_change_quotes_reschedule_shape_check CHECK (
        (kind = 'cancellation' AND proposed_start_at IS NULL AND proposed_end_at IS NULL
            AND proposed_occupied_start_at IS NULL AND proposed_occupied_end_at IS NULL)
        OR
        (kind = 'reschedule' AND proposed_start_at IS NOT NULL AND proposed_end_at IS NOT NULL
            AND proposed_occupied_start_at IS NOT NULL AND proposed_occupied_end_at IS NOT NULL
            AND proposed_occupied_start_at <= proposed_start_at
            AND proposed_start_at < proposed_end_at
            AND proposed_end_at <= proposed_occupied_end_at)
    ),
    UNIQUE (marketplace_customer_id, kind, idempotency_key)
);

CREATE INDEX booking_change_quotes_booking_idx ON booking_change_quotes (booking_id, created_at DESC);

CREATE TABLE booking_change_commands (
    id uuid PRIMARY KEY,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    command text NOT NULL,
    idempotency_key uuid NOT NULL,
    quote_id uuid REFERENCES booking_change_quotes(id) ON DELETE RESTRICT,
    reason text NOT NULL DEFAULT '',
    response_snapshot jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT booking_change_commands_actor_check CHECK (actor_type IN ('customer', 'provider')),
    CONSTRAINT booking_change_commands_command_check CHECK (
        command IN ('cancel', 'reschedule', 'confirm', 'decline', 'complete', 'mark_no_show')
    ),
    CONSTRAINT booking_change_commands_response_check CHECK (jsonb_typeof(response_snapshot) = 'object'),
    UNIQUE (actor_type, actor_id, idempotency_key)
);

CREATE INDEX booking_change_commands_booking_idx ON booking_change_commands (booking_id, created_at DESC);

CREATE TABLE booking_refund_requests (
    id uuid PRIMARY KEY,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    command_id uuid NOT NULL UNIQUE REFERENCES booking_change_commands(id) ON DELETE RESTRICT,
    amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text NOT NULL DEFAULT 'queued',
    reason text NOT NULL DEFAULT '',
    failure_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT booking_refund_requests_amount_check CHECK (amount_minor > 0),
    CONSTRAINT booking_refund_requests_currency_check CHECK (currency_code ~ '^[A-Z]{3}$'),
    CONSTRAINT booking_refund_requests_status_check CHECK (
        status IN ('queued', 'processing', 'successful', 'failed', 'cancelled')
    )
);

CREATE INDEX booking_refund_requests_status_idx
    ON booking_refund_requests (status, created_at) WHERE status IN ('queued', 'processing');

CREATE OR REPLACE FUNCTION append_booking_update_event() RETURNS trigger AS $$
DECLARE
    status_event text;
BEGIN
    IF OLD.status IS DISTINCT FROM NEW.status THEN
        status_event := CASE NEW.status
            WHEN 'cancelled' THEN 'booking_cancelled'
            WHEN 'declined' THEN 'booking_declined'
            WHEN 'confirmed' THEN 'booking_confirmed'
            WHEN 'completed' THEN 'booking_completed'
            WHEN 'no_show' THEN 'booking_marked_no_show'
            ELSE 'booking_status_changed'
        END;
        INSERT INTO booking_domain_events (id, client_id, booking_id, event_type, dedupe_key, payload)
        VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, status_event,
            'booking-status:' || gen_random_uuid()::text,
            jsonb_build_object('booking_id', NEW.id::text, 'from_status', OLD.status, 'to_status', NEW.status)
        );
    END IF;
    IF ROW(OLD.start_at, OLD.end_at) IS DISTINCT FROM ROW(NEW.start_at, NEW.end_at) THEN
        INSERT INTO booking_domain_events (id, client_id, booking_id, event_type, dedupe_key, payload)
        VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, 'booking_rescheduled',
            'booking-rescheduled:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'old_starts_at', OLD.start_at,
                'new_starts_at', NEW.start_at,
                'old_ends_at', OLD.end_at,
                'new_ends_at', NEW.end_at
            )
        );
    END IF;
    IF ROW(OLD.payment_status, OLD.agreement_status)
       IS DISTINCT FROM ROW(NEW.payment_status, NEW.agreement_status) THEN
        INSERT INTO booking_domain_events (id, client_id, booking_id, event_type, dedupe_key, payload)
        VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, 'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'payment_status', NEW.payment_status,
                'agreement_status', NEW.agreement_status
            )
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- migrate:down
CREATE OR REPLACE FUNCTION append_booking_update_event() RETURNS trigger AS $$
BEGIN
    IF ROW(OLD.status, OLD.payment_status, OLD.agreement_status, OLD.start_at, OLD.end_at)
       IS DISTINCT FROM
       ROW(NEW.status, NEW.payment_status, NEW.agreement_status, NEW.start_at, NEW.end_at) THEN
        INSERT INTO booking_domain_events (id, client_id, booking_id, event_type, dedupe_key, payload, created_at)
        VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, 'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'status', NEW.status,
                'payment_status', NEW.payment_status,
                'agreement_status', NEW.agreement_status,
                'starts_at', NEW.start_at
            ),
            NOW()
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TABLE booking_refund_requests;
DROP TABLE booking_change_commands;
DROP TABLE booking_change_quotes;
ALTER TABLE bookings
    DROP CONSTRAINT bookings_change_policy_snapshot_check,
    DROP COLUMN automated_reschedule_snapshot,
    DROP COLUMN reschedule_fee_minor_snapshot,
    DROP COLUMN reschedule_notice_minutes_snapshot,
    DROP COLUMN cancellation_refund_bps_snapshot,
    DROP COLUMN cancellation_notice_minutes_snapshot;
