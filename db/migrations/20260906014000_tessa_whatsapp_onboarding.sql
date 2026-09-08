-- migrate:up
CREATE TABLE tessa_whatsapp_onboarding (
    id uuid PRIMARY KEY,
    phone_number_id text NOT NULL CHECK (phone_number_id ~ '^[0-9]{1,32}$'),
    destination text NOT NULL CHECK (destination ~ '^\+[1-9][0-9]{7,14}$'),
    stage text NOT NULL CHECK (stage IN ('menu','consent','email','code','closed')),
    revision bigint NOT NULL DEFAULT 1,
    client_id uuid REFERENCES clients(id) ON DELETE CASCADE,
    challenge_id uuid REFERENCES tessa_whatsapp_email_challenges(id) ON DELETE SET NULL,
    notice_revision text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    last_inbound_at timestamptz NOT NULL,
    last_reply_at timestamptz NOT NULL DEFAULT NOW(),
    reply_window_started_at timestamptz NOT NULL DEFAULT NOW(),
    reply_count integer NOT NULL DEFAULT 1 CHECK (reply_count BETWEEN 0 AND 30),
    UNIQUE(phone_number_id,destination)
);
CREATE INDEX tessa_whatsapp_onboarding_client_idx ON tessa_whatsapp_onboarding(client_id) WHERE stage<>'closed';
CREATE INDEX tessa_whatsapp_onboarding_expiry_idx ON tessa_whatsapp_onboarding(expires_at,id);

ALTER TABLE tessa_whatsapp_outbox
    ALTER COLUMN client_id DROP NOT NULL,
    ADD COLUMN onboarding_id uuid REFERENCES tessa_whatsapp_onboarding(id) ON DELETE CASCADE,
    ADD COLUMN onboarding_revision bigint,
    DROP CONSTRAINT tessa_whatsapp_outbox_kind_check,
    ADD CONSTRAINT tessa_whatsapp_outbox_kind_check CHECK (
        (kind IN ('linked','disconnected','mismatch','unavailable') AND client_id IS NOT NULL AND onboarding_id IS NULL AND onboarding_revision IS NULL)
        OR (kind IN ('onboarding_menu','onboarding_consent','onboarding_email','onboarding_code','onboarding_invalid','onboarding_signup','onboarding_failed')
            AND client_id IS NULL AND onboarding_id IS NOT NULL AND onboarding_revision IS NOT NULL
            AND challenge_id IS NULL AND connection_revision=0 AND security_revision=0)
    );
CREATE INDEX tessa_whatsapp_onboarding_outbox_idx ON tessa_whatsapp_outbox(onboarding_id) WHERE status IN ('pending','retry','processing');
CREATE INDEX tessa_whatsapp_onboarding_budget_idx ON tessa_whatsapp_outbox(created_at) WHERE onboarding_id IS NOT NULL;

-- migrate:down
DO $$ BEGIN
    RAISE EXCEPTION 'Pending WhatsApp onboarding must not be discarded by an automatic downgrade';
END $$;
