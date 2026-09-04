-- migrate:up
CREATE TABLE inbox_conversation_moderation_events (
    id uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    action text NOT NULL,
    reason text NOT NULL,
    operator text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT inbox_conversation_moderation_action_check
        CHECK (action IN ('disabled', 'enabled')),
    CONSTRAINT inbox_conversation_moderation_reason_check
        CHECK (btrim(reason) <> '' AND char_length(reason) <= 500),
    CONSTRAINT inbox_conversation_moderation_operator_check
        CHECK (btrim(operator) <> '' AND char_length(operator) <= 160)
);

CREATE INDEX inbox_conversation_moderation_conversation_created_idx
    ON inbox_conversation_moderation_events (conversation_id, created_at DESC, id DESC);

-- migrate:down
DROP TABLE inbox_conversation_moderation_events;
