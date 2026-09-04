-- migrate:up
ALTER TABLE inbox_ai_booking_sessions
    DROP CONSTRAINT inbox_ai_booking_sessions_state_check;

ALTER TABLE inbox_ai_booking_sessions
    ADD COLUMN booking_id uuid REFERENCES bookings(id) ON DELETE RESTRICT,
    ADD CONSTRAINT inbox_ai_booking_sessions_state_check CHECK (state IN (
        'collecting_preferences','collecting_customer_details','offering_slots',
        'preparing_proposal','awaiting_before_booking_agreement','awaiting_confirmation',
        'creating_reservation','reservation_created','awaiting_payment',
        'awaiting_after_payment_agreement','awaiting_provider_confirmation','completed',
        'expired','handoff','provider_takeover','failed'
    )),
    ADD CONSTRAINT inbox_ai_booking_sessions_booking_state_check CHECK (
        booking_id IS NULL
        OR state IN (
            'reservation_created','awaiting_payment','awaiting_after_payment_agreement',
            'awaiting_provider_confirmation','completed'
        )
    );

ALTER TABLE inbox_ai_booking_confirmations
    ADD COLUMN booking_id uuid UNIQUE REFERENCES bookings(id) ON DELETE RESTRICT,
    ADD COLUMN reservation_created_at timestamptz,
    ADD CONSTRAINT inbox_ai_booking_confirmations_reservation_check CHECK (
        (booking_id IS NULL AND reservation_created_at IS NULL)
        OR (booking_id IS NOT NULL AND reservation_created_at IS NOT NULL)
    );

CREATE UNIQUE INDEX inbox_ai_booking_sessions_booking_idx
    ON inbox_ai_booking_sessions (booking_id)
    WHERE booking_id IS NOT NULL;

-- migrate:down
DROP INDEX inbox_ai_booking_sessions_booking_idx;

ALTER TABLE inbox_ai_booking_confirmations
    DROP CONSTRAINT inbox_ai_booking_confirmations_reservation_check,
    DROP COLUMN reservation_created_at,
    DROP COLUMN booking_id;

ALTER TABLE inbox_ai_booking_sessions
    DROP CONSTRAINT inbox_ai_booking_sessions_booking_state_check,
    DROP CONSTRAINT inbox_ai_booking_sessions_state_check,
    DROP COLUMN booking_id,
    ADD CONSTRAINT inbox_ai_booking_sessions_state_check CHECK (state IN (
        'collecting_preferences','collecting_customer_details','offering_slots',
        'preparing_proposal','awaiting_before_booking_agreement','awaiting_confirmation',
        'expired','handoff','provider_takeover','failed'
    ));
