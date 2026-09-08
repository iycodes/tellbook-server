-- migrate:up
CREATE TABLE tessa_whatsapp_connections (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    phone_number_id text NOT NULL,
    destination text NOT NULL CHECK (destination ~ '^\+[1-9][0-9]{7,14}$'),
    status text NOT NULL CHECK (status IN ('active','revoked')),
    revision bigint NOT NULL DEFAULT 1,
    security_revision bigint NOT NULL,
    notice_revision text NOT NULL,
    linked_at timestamptz NOT NULL DEFAULT now(),
    last_active_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);
CREATE UNIQUE INDEX tessa_whatsapp_active_sender_idx ON tessa_whatsapp_connections(phone_number_id,destination) WHERE status='active';

CREATE TABLE tessa_whatsapp_link_challenges (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    phone_number_id text NOT NULL,
    destination text NOT NULL,
    security_revision bigint NOT NULL,
    notice_revision text NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);
CREATE UNIQUE INDEX tessa_whatsapp_one_challenge_idx ON tessa_whatsapp_link_challenges(client_id) WHERE consumed_at IS NULL;
CREATE INDEX tessa_whatsapp_challenge_retention_idx ON tessa_whatsapp_link_challenges(expires_at,id);
CREATE INDEX tessa_whatsapp_challenge_cooldown_idx ON tessa_whatsapp_link_challenges(client_id,created_at);

CREATE TABLE tessa_whatsapp_outbox (
    id uuid PRIMARY KEY,
    source_receipt_id uuid NOT NULL UNIQUE REFERENCES meta_whatsapp_webhook_receipts(id) ON DELETE CASCADE,
    challenge_id uuid REFERENCES tessa_whatsapp_link_challenges(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    phone_number_id text NOT NULL,
    destination text NOT NULL,
    connection_revision bigint NOT NULL,
    security_revision bigint NOT NULL,
    kind text NOT NULL CHECK (kind IN ('linked','disconnected','mismatch','unavailable')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','dispatching','retry','accepted','sent','delivered','read','failed','cancelled','expired','unknown','manual_review')),
    window_expires_at timestamptz NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    lease_expires_at timestamptz,
    provider_message_id text NOT NULL DEFAULT '',
    status_at timestamptz,
    reconcile_after timestamptz,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX tessa_whatsapp_outbox_due_idx ON tessa_whatsapp_outbox(available_at,id) WHERE status IN ('pending','retry','processing');
CREATE UNIQUE INDEX tessa_whatsapp_outbox_wamid_idx ON tessa_whatsapp_outbox(provider_message_id) WHERE provider_message_id<>'';
CREATE INDEX tessa_whatsapp_outbox_client_idx ON tessa_whatsapp_outbox(client_id) WHERE status IN ('pending','retry','processing');
CREATE INDEX tessa_whatsapp_outbox_reconcile_idx ON tessa_whatsapp_outbox(reconcile_after,id) WHERE status IN ('dispatching','unknown');
CREATE INDEX tessa_whatsapp_outbox_retention_idx ON tessa_whatsapp_outbox(completed_at,id) WHERE completed_at IS NOT NULL;

ALTER TABLE meta_whatsapp_webhook_receipts ADD COLUMN processing_owner text NOT NULL DEFAULT 'unassigned'
    CHECK (processing_owner IN ('unassigned','auth','notification','tessa','quarantined'));
CREATE INDEX meta_whatsapp_unassigned_idx ON meta_whatsapp_webhook_receipts(
    (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),created_at,id)
    WHERE event_kind='status' AND processing_owner='unassigned' AND processing_status IN ('pending','retry','processing');
CREATE INDEX meta_whatsapp_owned_due_idx ON meta_whatsapp_webhook_receipts(processing_owner,
    (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),created_at,id)
    WHERE event_kind='status' AND processing_status IN ('pending','retry','processing');

-- migrate:down
DROP INDEX meta_whatsapp_owned_due_idx;
DROP INDEX meta_whatsapp_unassigned_idx;
ALTER TABLE meta_whatsapp_webhook_receipts DROP COLUMN processing_owner;
DROP TABLE tessa_whatsapp_outbox;
DROP TABLE tessa_whatsapp_link_challenges;
DROP TABLE tessa_whatsapp_connections;
