-- migrate:up
ALTER TABLE inbox_conversations ALTER COLUMN customer_id DROP NOT NULL;

ALTER TABLE inbox_messages ADD COLUMN presentation jsonb;

ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_sender_id_check;
ALTER TABLE inbox_messages ADD CONSTRAINT inbox_messages_sender_id_check CHECK (
    (sender_type = 'system' AND sender_id IS NULL)
    OR (sender_type IN ('provider', 'marketplace_customer', 'ai') AND sender_id IS NOT NULL)
);

ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_sender_type_check;
ALTER TABLE inbox_messages ADD CONSTRAINT inbox_messages_sender_type_check CHECK (
    sender_type IN ('provider', 'marketplace_customer', 'ai', 'system')
);

ALTER TABLE inbox_messages ADD CONSTRAINT inbox_messages_presentation_check CHECK (
    presentation IS NULL
    OR (
        jsonb_typeof(presentation) = 'object'
        AND presentation ? 'kind'
        AND presentation ? 'version'
        AND presentation ? 'data'
        AND jsonb_typeof(presentation->'kind') = 'string'
        AND presentation->>'kind' IN (
            'booking_link',
            'service_choices',
            'availability_choices',
            'booking_proposal',
            'reservation_created',
            'booking_next_step'
        )
        AND presentation->'version' = '1'::jsonb
        AND jsonb_typeof(presentation->'data') = 'object'
    )
);

-- migrate:down
ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_presentation_check;
ALTER TABLE inbox_messages DROP COLUMN presentation;

-- Preserve the accessible fallback text while mapping internal AI messages back
-- to the provider-side actor supported by the previous schema.
UPDATE inbox_messages SET sender_type = 'provider' WHERE sender_type = 'ai';

ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_sender_id_check;
ALTER TABLE inbox_messages ADD CONSTRAINT inbox_messages_sender_id_check CHECK (
    (sender_type = 'system' AND sender_id IS NULL)
    OR (sender_type IN ('provider', 'marketplace_customer') AND sender_id IS NOT NULL)
);

ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_sender_type_check;
ALTER TABLE inbox_messages ADD CONSTRAINT inbox_messages_sender_type_check CHECK (
    sender_type IN ('provider', 'marketplace_customer', 'system')
);

-- The previous schema cannot represent a conversation before a provider
-- customer exists. Remove only those pre-booking-only threads on rollback.
DELETE FROM inbox_conversations WHERE customer_id IS NULL;

ALTER TABLE inbox_conversations ALTER COLUMN customer_id SET NOT NULL;
