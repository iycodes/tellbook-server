-- migrate:up
CREATE TABLE tessa_whatsapp_security_events (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    connection_revision bigint NOT NULL,
    kind text NOT NULL CHECK (kind IN ('linked','replaced','disconnected')),
    destination text NOT NULL CHECK (destination ~ '^\+[1-9][0-9]{7,14}$'),
    recipient_email text NOT NULL CHECK (char_length(recipient_email)<=320),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    email_deadline timestamptz NOT NULL DEFAULT NOW()+INTERVAL '24 hours',
    email_queued_at timestamptz,
    email_accepted_at timestamptz,
    skip_reason text NOT NULL DEFAULT '' CHECK (skip_reason IN ('','no_verified_email','expired','invalid_recipient')),
    UNIQUE(client_id,connection_revision,kind)
);
CREATE INDEX tessa_security_email_pending_idx ON tessa_whatsapp_security_events(created_at,id)
    WHERE email_queued_at IS NULL AND skip_reason='';
CREATE INDEX tessa_security_event_retention_idx ON tessa_whatsapp_security_events(created_at,id);

ALTER TABLE auth_code_delivery_jobs
    ADD COLUMN tessa_security_event_id uuid UNIQUE REFERENCES tessa_whatsapp_security_events(id) ON DELETE CASCADE,
    DROP CONSTRAINT auth_code_delivery_jobs_challenge_check,
    DROP CONSTRAINT auth_code_delivery_jobs_template_contract_check,
    ADD CONSTRAINT auth_code_delivery_jobs_challenge_check CHECK (
        (realm='provider' AND provider_challenge_id IS NOT NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NULL)
        OR (realm='marketplace_customer' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NOT NULL AND tessa_security_event_id IS NULL)
        OR (realm='provider' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NOT NULL)
    ),
    ADD CONSTRAINT auth_code_delivery_jobs_template_contract_check CHECK (
        (channel='email' AND template_key='auth_code_email' AND tessa_security_event_id IS NULL)
        OR (channel='whatsapp' AND template_key='v_c_x' AND tessa_security_event_id IS NULL)
        OR (channel='email' AND template_key='tessa_security_email' AND tessa_security_event_id IS NOT NULL)
    );

-- Security notices must not push 90-second login deliveries behind a mail backlog.
CREATE INDEX auth_email_delivery_priority_idx ON auth_code_delivery_jobs(channel,
    (CASE WHEN template_key='tessa_security_email' THEN 1 ELSE 0 END),next_attempt_at,created_at,id)
    WHERE status IN ('pending','retry');

-- migrate:down
DO $$ BEGIN
    RAISE EXCEPTION 'Tessa security email intents must not be discarded by an automatic downgrade';
END $$;
