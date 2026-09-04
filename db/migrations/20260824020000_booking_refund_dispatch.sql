-- migrate:up
ALTER TABLE booking_refund_requests DROP CONSTRAINT booking_refund_requests_status_check;
ALTER TABLE booking_refund_requests ADD CONSTRAINT booking_refund_requests_status_check CHECK (
    status IN ('queued', 'processing', 'successful', 'failed', 'cancelled', 'manual_review')
);

CREATE TABLE booking_refund_attempts (
    id uuid PRIMARY KEY,
    request_id uuid NOT NULL REFERENCES booking_refund_requests(id) ON DELETE CASCADE,
    payment_id uuid NOT NULL REFERENCES payments(id) ON DELETE RESTRICT,
    provider text NOT NULL,
    transaction_reference text NOT NULL,
    provider_reference text,
    amount_minor bigint NOT NULL,
    payment_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    currency_exponent smallint NOT NULL,
    status text NOT NULL DEFAULT 'prepared',
    provider_status text NOT NULL DEFAULT '',
    failure_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT booking_refund_attempts_provider_check CHECK (provider IN ('paystack', 'payaza')),
    CONSTRAINT booking_refund_attempts_amount_check CHECK (
        amount_minor > 0 AND payment_amount_minor > 0 AND amount_minor <= payment_amount_minor
    ),
    CONSTRAINT booking_refund_attempts_currency_check CHECK (
        currency_code ~ '^[A-Z]{3}$' AND currency_exponent BETWEEN 0 AND 6
    ),
    CONSTRAINT booking_refund_attempts_status_check CHECK (
        status IN ('prepared', 'initiating', 'pending', 'successful', 'failed', 'unknown')
    ),
    UNIQUE (request_id, payment_id)
);

CREATE UNIQUE INDEX booking_refund_attempts_provider_reference_idx
    ON booking_refund_attempts (provider, provider_reference) WHERE provider_reference IS NOT NULL;
CREATE INDEX booking_refund_attempts_status_idx
    ON booking_refund_attempts (status, created_at) WHERE status IN ('prepared', 'initiating', 'pending');

-- migrate:down
DROP TABLE booking_refund_attempts;
ALTER TABLE booking_refund_requests DROP CONSTRAINT booking_refund_requests_status_check;
ALTER TABLE booking_refund_requests ADD CONSTRAINT booking_refund_requests_status_check CHECK (
    status IN ('queued', 'processing', 'successful', 'failed', 'cancelled')
);
