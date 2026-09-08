-- migrate:up
CREATE TABLE tessa_whatsapp_email_challenges (
    id uuid PRIMARY KEY,
    source_receipt_id uuid NOT NULL UNIQUE REFERENCES meta_whatsapp_webhook_receipts(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    purpose text NOT NULL DEFAULT 'tessa_whatsapp_link' CHECK (purpose='tessa_whatsapp_link'),
    phone_number_id text NOT NULL CHECK (phone_number_id ~ '^[0-9]{1,32}$'),
    destination text NOT NULL CHECK (destination ~ '^\+[1-9][0-9]{7,14}$'),
    security_revision bigint NOT NULL,
    notice_revision text NOT NULL,
    code_hash bytea NOT NULL CHECK (octet_length(code_hash)=32),
    failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts BETWEEN 0 AND 5),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    expires_at timestamptz NOT NULL DEFAULT NOW()+INTERVAL '10 minutes',
    delivery_accepted_at timestamptz,
    consumed_at timestamptz
);
CREATE UNIQUE INDEX tessa_email_link_active_sender_idx ON tessa_whatsapp_email_challenges(phone_number_id,destination) WHERE consumed_at IS NULL;
CREATE UNIQUE INDEX tessa_email_link_active_client_idx ON tessa_whatsapp_email_challenges(client_id) WHERE consumed_at IS NULL;
CREATE INDEX tessa_email_link_sender_budget_idx ON tessa_whatsapp_email_challenges(phone_number_id,destination,created_at);
CREATE INDEX tessa_email_link_client_budget_idx ON tessa_whatsapp_email_challenges(client_id,created_at);
CREATE INDEX tessa_email_link_created_idx ON tessa_whatsapp_email_challenges(created_at,id);

ALTER TABLE auth_code_delivery_jobs
    ADD COLUMN tessa_link_challenge_id uuid UNIQUE REFERENCES tessa_whatsapp_email_challenges(id) ON DELETE CASCADE,
    DROP CONSTRAINT auth_code_delivery_jobs_challenge_check,
    DROP CONSTRAINT auth_code_delivery_jobs_template_contract_check,
    ADD CONSTRAINT auth_code_delivery_jobs_challenge_check CHECK (
        (realm='provider' AND provider_challenge_id IS NOT NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
        OR (realm='marketplace_customer' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NOT NULL AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
        OR (realm='provider' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NOT NULL AND tessa_link_challenge_id IS NULL)
        OR (realm='provider' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NOT NULL)
    ),
    ADD CONSTRAINT auth_code_delivery_jobs_template_contract_check CHECK (
        (channel='email' AND template_key='auth_code_email' AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
        OR (channel='whatsapp' AND template_key='v_c_x' AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
        OR (channel='email' AND template_key='tessa_security_email' AND tessa_security_event_id IS NOT NULL AND tessa_link_challenge_id IS NULL)
        OR (channel='email' AND template_key='tessa_link_email' AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NOT NULL)
    );

-- migrate:down
DO $$ BEGIN
    RAISE EXCEPTION 'Pending Tessa linking challenges must not be discarded by an automatic downgrade';
END $$;
