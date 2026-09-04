-- migrate:up
CREATE TABLE marketplace_customer_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    identifier_type text NOT NULL,
    normalized_identifier text NOT NULL,
    verified_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_customer_identities_type_check
        CHECK (identifier_type IN ('email', 'phone', 'whatsapp')),
    CONSTRAINT marketplace_customer_identities_value_check
        CHECK (btrim(normalized_identifier) <> ''),
    CONSTRAINT marketplace_customer_identities_unique UNIQUE (identifier_type, normalized_identifier),
    CONSTRAINT marketplace_customer_identities_customer_type_unique
        UNIQUE (marketplace_customer_id, identifier_type)
);

CREATE INDEX marketplace_customer_identities_customer_idx
    ON marketplace_customer_identities (marketplace_customer_id, created_at);

INSERT INTO marketplace_customer_identities (
    marketplace_customer_id, identifier_type, normalized_identifier, verified_at
)
SELECT id, 'email', lower(email), email_verified_at
FROM marketplace_customers
WHERE email IS NOT NULL AND email_verified_at IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO marketplace_customer_identities (
    marketplace_customer_id, identifier_type, normalized_identifier, verified_at
)
SELECT id, 'phone', phone_e164, phone_verified_at
FROM marketplace_customers
WHERE phone_e164 IS NOT NULL AND phone_verified_at IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO marketplace_customer_identities (
    marketplace_customer_id, identifier_type, normalized_identifier, verified_at
)
SELECT id, 'whatsapp', whatsapp_e164, whatsapp_verified_at
FROM marketplace_customers
WHERE whatsapp_e164 IS NOT NULL AND whatsapp_verified_at IS NOT NULL
ON CONFLICT DO NOTHING;

ALTER TABLE marketplace_auth_challenges
    ADD COLUMN purpose text NOT NULL DEFAULT 'sign_in',
    ADD COLUMN target_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    ADD CONSTRAINT marketplace_auth_challenges_purpose_check
        CHECK (purpose IN ('sign_in', 'link_identity')),
    ADD CONSTRAINT marketplace_auth_challenges_target_check CHECK (
        (purpose = 'sign_in' AND target_customer_id IS NULL)
        OR (purpose = 'link_identity' AND target_customer_id IS NOT NULL)
    );

CREATE INDEX marketplace_auth_challenges_target_idx
    ON marketplace_auth_challenges (target_customer_id, created_at DESC)
    WHERE target_customer_id IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS marketplace_auth_challenges_target_idx;
ALTER TABLE marketplace_auth_challenges
    DROP CONSTRAINT IF EXISTS marketplace_auth_challenges_target_check,
    DROP CONSTRAINT IF EXISTS marketplace_auth_challenges_purpose_check,
    DROP COLUMN IF EXISTS target_customer_id,
    DROP COLUMN IF EXISTS purpose;
DROP TABLE IF EXISTS marketplace_customer_identities;
