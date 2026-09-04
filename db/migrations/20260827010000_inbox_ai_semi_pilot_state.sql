-- migrate:up
CREATE TABLE inbox_ai_policies (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    default_mode text NOT NULL DEFAULT 'manual',
    enabled_service_ids uuid[] NOT NULL DEFAULT '{}'::uuid[],
    paused boolean NOT NULL DEFAULT false,
    max_turns integer NOT NULL DEFAULT 12,
    inactivity_timeout_minutes integer NOT NULL DEFAULT 60,
    revision bigint NOT NULL DEFAULT 1,
    updated_by uuid NOT NULL REFERENCES clients(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_ai_policies_mode_check CHECK (default_mode IN ('manual','semi_pilot','autopilot')),
    CONSTRAINT inbox_ai_policies_services_check CHECK (cardinality(enabled_service_ids) <= 100),
    CONSTRAINT inbox_ai_policies_limits_check CHECK (
        max_turns BETWEEN 1 AND 30
        AND inactivity_timeout_minutes BETWEEN 5 AND 1440
        AND revision > 0
    )
);

CREATE TABLE inbox_ai_conversation_controls (
    conversation_id uuid PRIMARY KEY REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    mode_override text,
    state text NOT NULL DEFAULT 'active',
    reason text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1,
    updated_by_actor text NOT NULL,
    updated_by_id uuid,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_ai_conversation_controls_mode_check CHECK (
        mode_override IS NULL OR mode_override IN ('manual','semi_pilot','autopilot')
    ),
    CONSTRAINT inbox_ai_conversation_controls_state_check CHECK (
        state IN ('active','paused','provider_takeover','handoff')
    ),
    CONSTRAINT inbox_ai_conversation_controls_actor_check CHECK (
        updated_by_actor IN ('provider','marketplace_customer','system')
    ),
    CONSTRAINT inbox_ai_conversation_controls_reason_check CHECK (
        char_length(reason) <= 500
        AND ((state = 'active' AND reason = '') OR (state <> 'active' AND btrim(reason) <> ''))
    ),
    CONSTRAINT inbox_ai_conversation_controls_revision_check CHECK (revision > 0),
    UNIQUE (conversation_id, client_id)
);

CREATE INDEX inbox_ai_conversation_controls_client_idx
    ON inbox_ai_conversation_controls (client_id, updated_at DESC);

CREATE TABLE inbox_ai_sessions (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL UNIQUE REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    mode text NOT NULL,
    state text NOT NULL,
    selected_service_id uuid REFERENCES services(id) ON DELETE SET NULL,
    last_processed_message_sequence bigint NOT NULL DEFAULT 0,
    last_ai_message_id uuid REFERENCES inbox_messages(id) ON DELETE SET NULL,
    booking_link_url text NOT NULL DEFAULT '',
    booking_link_revision bigint NOT NULL DEFAULT 0,
    policy_revision bigint NOT NULL,
    control_revision bigint NOT NULL,
    turn_count integer NOT NULL DEFAULT 0,
    max_turns_snapshot integer NOT NULL,
    handoff_reason text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 1,
    expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_ai_sessions_mode_check CHECK (mode IN ('semi_pilot','autopilot')),
    CONSTRAINT inbox_ai_sessions_state_check CHECK (state IN (
        'qualifying','service_identified','link_ready','link_sent',
        'collecting_preferences','collecting_customer_details','offering_slots',
        'preparing_proposal','awaiting_before_booking_agreement','awaiting_confirmation',
        'creating_reservation','reservation_created','awaiting_payment',
        'awaiting_after_payment_agreement','awaiting_provider_confirmation','completed',
        'handoff','provider_takeover','expired','failed'
    )),
    CONSTRAINT inbox_ai_sessions_counters_check CHECK (
        last_processed_message_sequence >= 0
        AND booking_link_revision >= 0
        AND policy_revision > 0
        AND control_revision >= 0
        AND turn_count >= 0
        AND max_turns_snapshot BETWEEN 1 AND 30
        AND revision > 0
    ),
    CONSTRAINT inbox_ai_sessions_text_check CHECK (
        char_length(booking_link_url) <= 1000 AND char_length(handoff_reason) <= 500
    )
);

CREATE INDEX inbox_ai_sessions_client_state_idx
    ON inbox_ai_sessions (client_id, state, updated_at DESC);

CREATE TABLE inbox_ai_actions (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES inbox_ai_sessions(id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    idempotency_key uuid NOT NULL,
    action_name text NOT NULL,
    status text NOT NULL,
    safe_input jsonb NOT NULL DEFAULT '{}'::jsonb,
    safe_result jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    CONSTRAINT inbox_ai_actions_name_check CHECK (action_name IN (
        'list_relevant_services','get_service_details','get_booking_link',
        'search_availability','get_booking_requirements','refresh_booking_proposal',
        'confirm_booking_proposal','customer_handoff'
    )),
    CONSTRAINT inbox_ai_actions_status_check CHECK (status IN ('running','succeeded','failed')),
    CONSTRAINT inbox_ai_actions_json_check CHECK (
        jsonb_typeof(safe_input) = 'object' AND jsonb_typeof(safe_result) = 'object'
    ),
    CONSTRAINT inbox_ai_actions_completion_check CHECK (
        (status = 'running' AND completed_at IS NULL AND error_code = '')
        OR (status = 'succeeded' AND completed_at IS NOT NULL AND error_code = '')
        OR (status = 'failed' AND completed_at IS NOT NULL AND btrim(error_code) <> '')
    ),
    UNIQUE (session_id, idempotency_key)
);

CREATE INDEX inbox_ai_actions_conversation_started_idx
    ON inbox_ai_actions (conversation_id, started_at DESC, id DESC);

ALTER TABLE inbox_events DROP CONSTRAINT inbox_events_type_check;
ALTER TABLE inbox_events ADD CONSTRAINT inbox_events_type_check CHECK (
    event_type IN (
        'conversation.created','conversation.updated','message.created','read.updated',
        'participant.archive_updated','conversation.disabled','ai.control_updated','ai.session_updated'
    )
);

-- migrate:down
ALTER TABLE inbox_events DROP CONSTRAINT inbox_events_type_check;
ALTER TABLE inbox_events ADD CONSTRAINT inbox_events_type_check CHECK (
    event_type IN (
        'conversation.created','conversation.updated','message.created','read.updated',
        'participant.archive_updated','conversation.disabled'
    )
);

DROP TABLE inbox_ai_actions;
DROP TABLE inbox_ai_sessions;
DROP TABLE inbox_ai_conversation_controls;
DROP TABLE inbox_ai_policies;
