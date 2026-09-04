-- migrate:up
CREATE TABLE inbox_ai_booking_sessions (
    id uuid PRIMARY KEY,
    ai_session_id uuid NOT NULL UNIQUE REFERENCES inbox_ai_sessions(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    state text NOT NULL DEFAULT 'collecting_preferences',
    selected_service_id uuid REFERENCES services(id) ON DELETE RESTRICT,
    requested_from date,
    requested_days integer,
    timezone text NOT NULL DEFAULT '',
    selected_start_at timestamptz,
    selected_end_at timestamptz,
    quote_id uuid REFERENCES booking_quotes(id) ON DELETE RESTRICT,
    quote_token text NOT NULL DEFAULT '',
    quote_expires_at timestamptz,
    currency_code text NOT NULL DEFAULT '',
    total_amount_minor bigint,
    proposal_id uuid,
    proposal_revision bigint NOT NULL DEFAULT 0,
    proposal_hash text NOT NULL DEFAULT '',
    proposal_customer_details_revision timestamptz,
    agreement_instance_id uuid REFERENCES agreement_instances(id) ON DELETE RESTRICT,
    confirmation_id uuid,
    current_action_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT 1,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_ai_booking_sessions_state_check CHECK (state IN (
        'collecting_preferences','collecting_customer_details','offering_slots',
        'preparing_proposal','awaiting_before_booking_agreement','awaiting_confirmation',
        'expired','handoff','provider_takeover','failed'
    )),
    CONSTRAINT inbox_ai_booking_sessions_request_check CHECK (
        (requested_from IS NULL AND requested_days IS NULL)
        OR (requested_from IS NOT NULL AND requested_days BETWEEN 1 AND 14)
    ),
    CONSTRAINT inbox_ai_booking_sessions_slot_check CHECK (
        (selected_start_at IS NULL AND selected_end_at IS NULL)
        OR (selected_start_at IS NOT NULL AND selected_end_at > selected_start_at)
    ),
    CONSTRAINT inbox_ai_booking_sessions_quote_check CHECK (
        (quote_id IS NULL AND quote_token = '' AND quote_expires_at IS NULL
            AND currency_code = '' AND total_amount_minor IS NULL)
        OR (quote_id IS NOT NULL AND btrim(quote_token) <> '' AND quote_expires_at IS NOT NULL
            AND currency_code ~ '^[A-Z]{3}$' AND total_amount_minor >= 0)
    ),
    CONSTRAINT inbox_ai_booking_sessions_proposal_check CHECK (
        (proposal_id IS NULL AND proposal_revision = 0 AND proposal_hash = ''
            AND proposal_customer_details_revision IS NULL)
        OR (proposal_id IS NOT NULL AND proposal_revision > 0
            AND proposal_hash ~ '^[a-f0-9]{64}$' AND quote_id IS NOT NULL
            AND proposal_customer_details_revision IS NOT NULL)
    ),
    CONSTRAINT inbox_ai_booking_sessions_revision_check CHECK (revision > 0),
    UNIQUE (conversation_id, client_id, marketplace_customer_id)
);

CREATE INDEX inbox_ai_booking_sessions_expiry_idx
    ON inbox_ai_booking_sessions (expires_at)
    WHERE state NOT IN ('expired','handoff','provider_takeover','failed');

CREATE TABLE inbox_ai_booking_confirmations (
    id uuid PRIMARY KEY,
    booking_session_id uuid NOT NULL REFERENCES inbox_ai_booking_sessions(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    proposal_id uuid NOT NULL,
    proposal_revision bigint NOT NULL,
    proposal_hash text NOT NULL,
    idempotency_key uuid NOT NULL,
    contact_details_confirmed boolean NOT NULL,
    customer_details_revision timestamptz NOT NULL,
    whatsapp_consent boolean NOT NULL,
    sms_consent boolean NOT NULL,
    agreement_instance_id uuid REFERENCES agreement_instances(id) ON DELETE RESTRICT,
    confirmed_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_ai_booking_confirmations_revision_check CHECK (proposal_revision > 0),
    CONSTRAINT inbox_ai_booking_confirmations_hash_check CHECK (proposal_hash ~ '^[a-f0-9]{64}$'),
    CONSTRAINT inbox_ai_booking_confirmations_contact_check CHECK (contact_details_confirmed),
    UNIQUE (booking_session_id, idempotency_key),
    UNIQUE (booking_session_id, proposal_id, proposal_revision)
);

ALTER TABLE inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_confirmation_fkey
    FOREIGN KEY (confirmation_id) REFERENCES inbox_ai_booking_confirmations(id) ON DELETE SET NULL;

-- migrate:down
ALTER TABLE inbox_ai_booking_sessions DROP CONSTRAINT inbox_ai_booking_sessions_confirmation_fkey;
DROP TABLE inbox_ai_booking_confirmations;
DROP TABLE inbox_ai_booking_sessions;
