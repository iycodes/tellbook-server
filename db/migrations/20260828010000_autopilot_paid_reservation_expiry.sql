-- migrate:up
ALTER TABLE bookings
    ADD COLUMN reservation_expires_at timestamptz,
    ADD COLUMN reservation_expired_at timestamptz,
    ADD COLUMN reservation_expiry_reason text NOT NULL DEFAULT '',
    ADD CONSTRAINT bookings_reservation_expiry_deadline_check CHECK (
        reservation_expires_at IS NULL OR reservation_expires_at > created_at
    ),
    ADD CONSTRAINT bookings_reservation_expired_state_check CHECK (
        reservation_expired_at IS NULL
        OR (status = 'expired' AND BTRIM(reservation_expiry_reason) <> '')
    );

CREATE INDEX bookings_unpaid_reservation_expiry_idx
    ON bookings (reservation_expires_at, id)
    WHERE reservation_expires_at IS NOT NULL
      AND reservation_expired_at IS NULL
      AND status NOT IN ('cancelled','canceled','declined','expired','completed','no_show')
      AND payment_status NOT IN ('deposit_paid_balance_due','paid_in_full');

DROP INDEX bookings_marketplace_availability_idx;
CREATE INDEX bookings_marketplace_availability_idx
    ON bookings (client_id, occupied_start_at, occupied_end_at)
    WHERE status NOT IN ('cancelled','canceled','declined','expired');

ALTER TABLE inbox_ai_booking_sessions
    DROP CONSTRAINT inbox_ai_booking_sessions_booking_state_check,
    ADD CONSTRAINT inbox_ai_booking_sessions_booking_state_check CHECK (
        booking_id IS NULL
        OR state IN (
            'reservation_created','awaiting_payment','awaiting_after_payment_agreement',
            'awaiting_provider_confirmation','completed','expired'
        )
    );

ALTER TABLE inbox_messages
    DROP CONSTRAINT inbox_messages_presentation_check,
    ADD CONSTRAINT inbox_messages_presentation_check CHECK (
        presentation IS NULL OR (
            jsonb_typeof(presentation)='object'
            AND presentation ? 'kind'
            AND presentation ? 'version'
            AND presentation ? 'data'
            AND jsonb_typeof(presentation->'kind')='string'
            AND presentation->>'kind' IN (
                'booking_link','service_choices','availability_choices','booking_proposal',
                'reservation_created','reservation_expired','booking_next_step'
            )
            AND presentation->'version'='1'::jsonb
            AND jsonb_typeof(presentation->'data')='object'
        )
    );

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
            WHEN 'expired' THEN 'booking_expired'
            ELSE 'booking_status_changed'
        END;
        INSERT INTO booking_domain_events (id, client_id, booking_id, event_type, dedupe_key, payload)
        VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, status_event,
            'booking-status:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'from_status', OLD.status,
                'to_status', NEW.status,
                'reservation_expiry_reason', NEW.reservation_expiry_reason
            )
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
DROP INDEX bookings_unpaid_reservation_expiry_idx;

DROP INDEX bookings_marketplace_availability_idx;
CREATE INDEX bookings_marketplace_availability_idx
    ON bookings (client_id, occupied_start_at, occupied_end_at)
    WHERE status NOT IN ('cancelled','canceled');

ALTER TABLE inbox_ai_booking_sessions
    DROP CONSTRAINT inbox_ai_booking_sessions_booking_state_check,
    ADD CONSTRAINT inbox_ai_booking_sessions_booking_state_check CHECK (
        booking_id IS NULL
        OR state IN (
            'reservation_created','awaiting_payment','awaiting_after_payment_agreement',
            'awaiting_provider_confirmation','completed'
        )
    );

ALTER TABLE inbox_messages
    DROP CONSTRAINT inbox_messages_presentation_check,
    ADD CONSTRAINT inbox_messages_presentation_check CHECK (
        presentation IS NULL OR (
            jsonb_typeof(presentation)='object'
            AND presentation ? 'kind'
            AND presentation ? 'version'
            AND presentation ? 'data'
            AND jsonb_typeof(presentation->'kind')='string'
            AND presentation->>'kind' IN (
                'booking_link','service_choices','availability_choices','booking_proposal',
                'reservation_created','booking_next_step'
            )
            AND presentation->'version'='1'::jsonb
            AND jsonb_typeof(presentation->'data')='object'
        )
    );

ALTER TABLE bookings
    DROP CONSTRAINT bookings_reservation_expired_state_check,
    DROP CONSTRAINT bookings_reservation_expiry_deadline_check,
    DROP COLUMN reservation_expiry_reason,
    DROP COLUMN reservation_expired_at,
    DROP COLUMN reservation_expires_at;

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
