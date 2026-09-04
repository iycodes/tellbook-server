-- migrate:up
ALTER TABLE booking_change_commands
    ADD COLUMN request_fingerprint text NOT NULL DEFAULT repeat('0', 64),
    ADD CONSTRAINT booking_change_commands_fingerprint_check
        CHECK (request_fingerprint ~ '^[a-f0-9]{64}$');

ALTER TABLE booking_change_commands ALTER COLUMN request_fingerprint DROP DEFAULT;

CREATE INDEX booking_refund_attempts_webhook_correlation_idx
    ON booking_refund_attempts (payment_id, provider, amount_minor, created_at DESC)
    WHERE provider_reference IS NOT NULL;

-- migrate:down
DROP INDEX booking_refund_attempts_webhook_correlation_idx;
ALTER TABLE booking_change_commands
    DROP CONSTRAINT booking_change_commands_fingerprint_check,
    DROP COLUMN request_fingerprint;
