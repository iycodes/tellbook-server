-- migrate:up
DROP TABLE inbox_messages;
DROP TABLE inbox_conversations;

CREATE TABLE inbox_conversations (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    customer_id uuid NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    channel text NOT NULL DEFAULT 'tellbook',
    preview text NOT NULL DEFAULT '',
    last_message_sequence bigint,
    last_message_at timestamptz,
    disabled_at timestamptz,
    disabled_reason text NOT NULL DEFAULT '',
    disabled_by text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_conversations_channel_check CHECK (channel = 'tellbook'),
    CONSTRAINT inbox_conversations_preview_check CHECK (char_length(preview) <= 240),
    CONSTRAINT inbox_conversations_disabled_reason_check CHECK (char_length(disabled_reason) <= 500),
    CONSTRAINT inbox_conversations_disabled_state_check CHECK (
        (disabled_at IS NULL AND disabled_reason = '' AND disabled_by = '')
        OR (disabled_at IS NOT NULL AND btrim(disabled_reason) <> '' AND btrim(disabled_by) <> '')
    )
);

CREATE UNIQUE INDEX inbox_conversations_provider_marketplace_customer_key
    ON inbox_conversations (client_id, marketplace_customer_id);
CREATE INDEX inbox_conversations_provider_list_idx
    ON inbox_conversations (client_id, last_message_at DESC NULLS LAST, id DESC);
CREATE INDEX inbox_conversations_marketplace_list_idx
    ON inbox_conversations (marketplace_customer_id, last_message_at DESC NULLS LAST, id DESC);

CREATE TABLE inbox_conversation_bookings (
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    linked_at timestamptz NOT NULL DEFAULT NOW(),
    linked_by_actor text NOT NULL,
    PRIMARY KEY (conversation_id, booking_id),
    CONSTRAINT inbox_conversation_bookings_actor_check
        CHECK (linked_by_actor IN ('customer', 'provider', 'system'))
);

CREATE UNIQUE INDEX inbox_conversation_bookings_booking_key
    ON inbox_conversation_bookings (booking_id);

CREATE FUNCTION enforce_inbox_booking_ownership() RETURNS trigger
    LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM inbox_conversations conversation
        INNER JOIN bookings booking ON booking.id = NEW.booking_id
        WHERE conversation.id = NEW.conversation_id
          AND conversation.client_id = booking.client_id
          AND conversation.customer_id = booking.customer_id
          AND conversation.marketplace_customer_id = booking.marketplace_customer_id
    ) THEN
        RAISE EXCEPTION 'inbox conversation and booking ownership do not match'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER inbox_conversation_bookings_enforce_ownership
    BEFORE INSERT OR UPDATE ON inbox_conversation_bookings
    FOR EACH ROW EXECUTE FUNCTION enforce_inbox_booking_ownership();

CREATE TABLE inbox_messages (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    sender_type text NOT NULL,
    sender_id uuid,
    client_message_id uuid,
    request_fingerprint text,
    booking_id uuid REFERENCES bookings(id) ON DELETE SET NULL,
    content text NOT NULL,
    message_type text NOT NULL DEFAULT 'text',
    sent_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_messages_sender_type_check
        CHECK (sender_type IN ('provider', 'marketplace_customer', 'system')),
    CONSTRAINT inbox_messages_sender_id_check CHECK (
        (sender_type = 'system' AND sender_id IS NULL)
        OR (sender_type IN ('provider', 'marketplace_customer') AND sender_id IS NOT NULL)
    ),
    CONSTRAINT inbox_messages_idempotency_check CHECK (
        (client_message_id IS NULL AND request_fingerprint IS NULL)
        OR (client_message_id IS NOT NULL AND request_fingerprint ~ '^[a-f0-9]{64}$')
    ),
    CONSTRAINT inbox_messages_content_check
        CHECK (btrim(content) <> '' AND char_length(content) <= 4000),
    CONSTRAINT inbox_messages_type_check CHECK (message_type = 'text')
);

CREATE INDEX inbox_messages_conversation_sequence_idx
    ON inbox_messages (conversation_id, sequence DESC);
CREATE UNIQUE INDEX inbox_messages_client_idempotency_key
    ON inbox_messages (conversation_id, sender_type, sender_id, client_message_id)
    WHERE client_message_id IS NOT NULL;

CREATE TABLE inbox_participant_states (
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    participant_type text NOT NULL,
    participant_id uuid NOT NULL,
    last_read_sequence bigint NOT NULL DEFAULT 0,
    last_read_at timestamptz,
    archived_at timestamptz,
    PRIMARY KEY (conversation_id, participant_type, participant_id),
    CONSTRAINT inbox_participant_states_type_check
        CHECK (participant_type IN ('provider', 'marketplace_customer')),
    CONSTRAINT inbox_participant_states_read_sequence_check CHECK (last_read_sequence >= 0)
);

CREATE INDEX inbox_participant_states_actor_archive_idx
    ON inbox_participant_states (participant_type, participant_id, archived_at, conversation_id);

CREATE TABLE inbox_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_events_type_check CHECK (
        event_type IN (
            'conversation.created',
            'conversation.updated',
            'message.created',
            'read.updated',
            'participant.archive_updated',
            'conversation.disabled'
        )
    ),
    CONSTRAINT inbox_events_payload_check CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX inbox_events_provider_sequence_idx
    ON inbox_events (client_id, sequence);
CREATE INDEX inbox_events_marketplace_sequence_idx
    ON inbox_events (marketplace_customer_id, sequence);
CREATE INDEX inbox_events_created_at_idx
    ON inbox_events (created_at);

-- migrate:down
DROP TABLE inbox_events;
DROP TABLE inbox_participant_states;
DROP TABLE inbox_messages;
DROP TRIGGER inbox_conversation_bookings_enforce_ownership ON inbox_conversation_bookings;
DROP FUNCTION enforce_inbox_booking_ownership();
DROP TABLE inbox_conversation_bookings;
DROP TABLE inbox_conversations;

CREATE TABLE inbox_conversations (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    customer_id uuid REFERENCES customers(id) ON DELETE SET NULL,
    source text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    preview text NOT NULL DEFAULT '',
    avatar_url text,
    last_message_at timestamptz NOT NULL DEFAULT NOW(),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    lead_name text NOT NULL DEFAULT '',
    lead_contact text NOT NULL DEFAULT '',
    external_lead_id text NOT NULL DEFAULT '',
    autopilot_mode text NOT NULL DEFAULT 'manual',
    agent_state text NOT NULL DEFAULT 'new_lead',
    human_takeover boolean NOT NULL DEFAULT FALSE,
    last_ai_reply_at timestamptz,
    human_composing boolean NOT NULL DEFAULT FALSE,
    human_composing_started_at timestamptz,
    human_composing_expires_at timestamptz
);

CREATE INDEX inbox_conversations_client_id_external_lead_id_idx
    ON inbox_conversations (client_id, external_lead_id, last_message_at DESC);
CREATE INDEX inbox_conversations_client_id_idx ON inbox_conversations (client_id);
CREATE INDEX inbox_conversations_client_id_last_message_at_idx
    ON inbox_conversations (client_id, last_message_at DESC);
CREATE INDEX inbox_conversations_client_id_status_idx
    ON inbox_conversations (client_id, status);

CREATE TABLE inbox_messages (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    sender_role text NOT NULL DEFAULT '',
    content text NOT NULL DEFAULT '',
    message_type text NOT NULL DEFAULT 'text',
    sent_at timestamptz NOT NULL DEFAULT NOW(),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    action_type text NOT NULL DEFAULT ''
);

CREATE INDEX inbox_messages_conversation_id_idx
    ON inbox_messages (conversation_id, sent_at);
