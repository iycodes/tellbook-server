-- migrate:up
ALTER TABLE booking_quotes
    ADD COLUMN idempotency_key uuid,
    ADD COLUMN request_fingerprint text,
    ADD COLUMN response_snapshot jsonb;

ALTER TABLE booking_quotes
    ADD CONSTRAINT booking_quotes_idempotency_shape_check CHECK (
        (idempotency_key IS NULL AND request_fingerprint IS NULL AND response_snapshot IS NULL)
        OR
        (idempotency_key IS NOT NULL
            AND request_fingerprint ~ '^[a-f0-9]{64}$'
            AND jsonb_typeof(response_snapshot) = 'object')
    );

CREATE UNIQUE INDEX booking_quotes_client_idempotency_key_idx
    ON booking_quotes (client_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS booking_quotes_client_idempotency_key_idx;
ALTER TABLE booking_quotes DROP CONSTRAINT IF EXISTS booking_quotes_idempotency_shape_check;
ALTER TABLE booking_quotes
    DROP COLUMN IF EXISTS response_snapshot,
    DROP COLUMN IF EXISTS request_fingerprint,
    DROP COLUMN IF EXISTS idempotency_key;
