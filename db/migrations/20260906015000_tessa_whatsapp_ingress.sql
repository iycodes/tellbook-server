-- migrate:up
ALTER TABLE tessa_messages DROP CONSTRAINT tessa_messages_channel_check,
    ADD CONSTRAINT tessa_messages_channel_check CHECK (source_channel IN ('web','whatsapp'));

CREATE TABLE tessa_whatsapp_ingress (
    id uuid PRIMARY KEY,
    sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    source_receipt_id uuid NOT NULL UNIQUE REFERENCES meta_whatsapp_webhook_receipts(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    thread_id uuid,
    phone_number_id text NOT NULL,
    destination text NOT NULL,
    connection_revision bigint NOT NULL,
    security_revision bigint NOT NULL,
    source_message_id text NOT NULL CHECK (char_length(source_message_id) BETWEEN 1 AND 512),
    source_timestamp timestamptz NOT NULL,
    content text,
    status text NOT NULL CHECK (status IN ('pending','admitted','rejected','expired','cancelled')),
    reason text NOT NULL DEFAULT '',
    message_id uuid,
    run_id uuid UNIQUE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    expires_at timestamptz NOT NULL DEFAULT NOW()+INTERVAL '5 minutes',
    completed_at timestamptz,
    UNIQUE(phone_number_id,source_message_id),
    FOREIGN KEY(thread_id,client_id) REFERENCES tessa_threads(id,client_id) ON DELETE CASCADE,
    FOREIGN KEY(message_id,thread_id,client_id) REFERENCES tessa_messages(id,thread_id,client_id) ON DELETE CASCADE,
    FOREIGN KEY(run_id,thread_id,client_id) REFERENCES tessa_runs(id,thread_id,client_id) ON DELETE CASCADE,
    CHECK ((status='pending' AND content IS NOT NULL AND btrim(content)<>'' AND char_length(content)<=4000 AND thread_id IS NOT NULL AND completed_at IS NULL)
      OR (status<>'pending' AND content IS NULL AND completed_at IS NOT NULL)),
    CHECK ((status='admitted' AND message_id IS NOT NULL AND run_id IS NOT NULL) OR (status<>'admitted' AND message_id IS NULL AND run_id IS NULL))
);
CREATE INDEX tessa_whatsapp_ingress_pending_idx ON tessa_whatsapp_ingress(sequence) WHERE status='pending';
CREATE INDEX tessa_whatsapp_ingress_client_pending_idx ON tessa_whatsapp_ingress(client_id,sequence) WHERE status='pending';
CREATE INDEX tessa_whatsapp_ingress_thread_pending_idx ON tessa_whatsapp_ingress(thread_id) WHERE status='pending';
CREATE INDEX tessa_whatsapp_ingress_expiry_idx ON tessa_whatsapp_ingress(expires_at,sequence) WHERE status='pending';

ALTER TABLE tessa_whatsapp_outbox DROP CONSTRAINT tessa_whatsapp_outbox_kind_check,
    ADD CONSTRAINT tessa_whatsapp_outbox_kind_check CHECK (
        (kind IN ('linked','disconnected','mismatch','unavailable','queue_busy','queue_expired','queue_unavailable') AND client_id IS NOT NULL AND onboarding_id IS NULL AND onboarding_revision IS NULL)
        OR (kind IN ('onboarding_menu','onboarding_consent','onboarding_email','onboarding_code','onboarding_invalid','onboarding_signup','onboarding_failed')
            AND client_id IS NULL AND onboarding_id IS NOT NULL AND onboarding_revision IS NOT NULL
            AND challenge_id IS NULL AND connection_revision=0 AND security_revision=0)
    );
CREATE INDEX tessa_whatsapp_queue_notice_budget_idx ON tessa_whatsapp_outbox(client_id,created_at) WHERE kind IN ('queue_busy','queue_expired','queue_unavailable');

-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'WhatsApp conversation history must not be discarded by automatic downgrade'; END $$;
