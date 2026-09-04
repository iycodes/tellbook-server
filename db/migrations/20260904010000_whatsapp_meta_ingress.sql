-- migrate:up
CREATE TABLE meta_whatsapp_webhook_receipts (
    id uuid PRIMARY KEY,
    dedupe_key text NOT NULL UNIQUE,
    waba_id text NOT NULL,
    phone_number_id text NOT NULL,
    event_kind text NOT NULL,
    wamid text NOT NULL,
    message_status text NOT NULL DEFAULT '',
    provider_timestamp timestamptz,
    provider_error_code text NOT NULL DEFAULT '',
    processing_status text NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_token uuid,
    lease_expires_at timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    processed_at timestamptz,
    CONSTRAINT meta_whatsapp_webhook_receipts_dedupe_check
        CHECK (dedupe_key ~ '^[a-f0-9]{64}$'),
    CONSTRAINT meta_whatsapp_webhook_receipts_waba_check
        CHECK (waba_id ~ '^[0-9]{1,32}$'),
    CONSTRAINT meta_whatsapp_webhook_receipts_phone_check
        CHECK (phone_number_id ~ '^[0-9]{1,32}$'),
    CONSTRAINT meta_whatsapp_webhook_receipts_kind_check
        CHECK (event_kind IN ('status', 'inbound_message')),
    CONSTRAINT meta_whatsapp_webhook_receipts_wamid_check
        CHECK (char_length(wamid) BETWEEN 1 AND 512),
    CONSTRAINT meta_whatsapp_webhook_receipts_status_length_check
        CHECK (char_length(message_status) <= 40),
    CONSTRAINT meta_whatsapp_webhook_receipts_error_length_check
        CHECK (
            char_length(provider_error_code) <= 80
            AND char_length(last_error_code) <= 80
        ),
    CONSTRAINT meta_whatsapp_webhook_receipts_processing_check
        CHECK (processing_status IN ('pending', 'processing', 'retry', 'completed', 'dead_letter')),
    CONSTRAINT meta_whatsapp_webhook_receipts_attempt_check
        CHECK (attempt_count >= 0 AND attempt_count <= 20),
    CONSTRAINT meta_whatsapp_webhook_receipts_lease_check CHECK (
        (processing_status = 'processing' AND btrim(lease_owner) <> ''
            AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
        OR (processing_status <> 'processing' AND lease_owner = ''
            AND lease_token IS NULL AND lease_expires_at IS NULL)
    ),
    CONSTRAINT meta_whatsapp_webhook_receipts_processed_check CHECK (
        (processing_status IN ('completed', 'dead_letter') AND processed_at IS NOT NULL)
        OR (processing_status NOT IN ('completed', 'dead_letter') AND processed_at IS NULL)
    )
);

CREATE INDEX meta_whatsapp_webhook_receipts_pending_idx
    ON meta_whatsapp_webhook_receipts (available_at, created_at, id)
    WHERE processing_status IN ('pending', 'retry', 'processing');

-- migrate:down
DROP TABLE IF EXISTS meta_whatsapp_webhook_receipts;
