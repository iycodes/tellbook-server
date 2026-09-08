-- migrate:up
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM clients
        WHERE email IS NOT NULL AND btrim(email) = ''
    ) THEN
        RAISE EXCEPTION 'provider auth identity migration blocked: blank provider emails remain';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM clients
        WHERE email IS NOT NULL
          AND btrim(email) <> ''
          AND email_verified_at IS NULL
    ) THEN
        RAISE EXCEPTION 'provider auth identity migration blocked: unverified provider emails remain';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM clients
        WHERE email IS NOT NULL AND btrim(email) <> ''
        GROUP BY lower(btrim(email))
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'provider auth identity migration blocked: normalized provider email collision';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (
            SELECT id AS customer_id, phone_e164 AS value
            FROM marketplace_customers
            WHERE phone_e164 IS NOT NULL AND btrim(phone_e164) <> ''
            UNION ALL
            SELECT id, whatsapp_e164
            FROM marketplace_customers
            WHERE whatsapp_e164 IS NOT NULL AND btrim(whatsapp_e164) <> ''
            UNION ALL
            SELECT marketplace_customer_id, normalized_identifier
            FROM marketplace_customer_identities
            WHERE identifier_type IN ('phone', 'whatsapp')
        ) contacts
        GROUP BY value
        HAVING count(DISTINCT customer_id) > 1
    ) THEN
        RAISE EXCEPTION 'marketplace auth identity migration blocked: phone identity has multiple owners';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM (
            SELECT id AS customer_id, phone_e164 AS value
            FROM marketplace_customers
            WHERE phone_e164 IS NOT NULL AND btrim(phone_e164) <> ''
            UNION ALL
            SELECT id, whatsapp_e164
            FROM marketplace_customers
            WHERE whatsapp_e164 IS NOT NULL AND btrim(whatsapp_e164) <> ''
            UNION ALL
            SELECT marketplace_customer_id, normalized_identifier
            FROM marketplace_customer_identities
            WHERE identifier_type IN ('phone', 'whatsapp')
        ) contacts
        GROUP BY customer_id
        HAVING count(DISTINCT value) > 1
    ) THEN
        RAISE EXCEPTION 'marketplace auth identity migration blocked: customer has multiple phone identities';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM marketplace_customers
        WHERE email IS NOT NULL AND btrim(email) <> ''
        GROUP BY lower(btrim(email))
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'marketplace auth identity migration blocked: normalized customer email collision';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM marketplace_customer_identities identity
        JOIN marketplace_customers customer
          ON customer.id <> identity.marketplace_customer_id
         AND customer.email IS NOT NULL
         AND lower(btrim(customer.email)) = identity.normalized_identifier
        WHERE identity.identifier_type = 'email'
    ) THEN
        RAISE EXCEPTION 'marketplace auth identity migration blocked: email identity has multiple owners';
    END IF;
END
$$;

UPDATE clients
SET email = lower(btrim(email))
WHERE email IS NOT NULL;

UPDATE marketplace_customers
SET email = lower(btrim(email))
WHERE email IS NOT NULL;

CREATE TABLE provider_auth_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    identity_type text NOT NULL,
    normalized_identifier text NOT NULL,
    verified_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_auth_identities_type_check
        CHECK (identity_type IN ('email', 'phone')),
    CONSTRAINT provider_auth_identities_value_check
        CHECK (btrim(normalized_identifier) <> ''),
    CONSTRAINT provider_auth_identities_normalized_value_check CHECK (
        (identity_type = 'email' AND normalized_identifier = lower(btrim(normalized_identifier)))
        OR (identity_type = 'phone' AND normalized_identifier ~ '^\\+[1-9][0-9]{9,14}$')
    ),
    CONSTRAINT provider_auth_identities_owner_unique
        UNIQUE (identity_type, normalized_identifier),
    CONSTRAINT provider_auth_identities_client_type_unique
        UNIQUE (client_id, identity_type)
);

INSERT INTO provider_auth_identities (
    client_id, identity_type, normalized_identifier, verified_at
)
SELECT id, 'email', email, email_verified_at
FROM clients
WHERE email IS NOT NULL AND email_verified_at IS NOT NULL;

ALTER TABLE clients
    ALTER COLUMN email DROP NOT NULL,
    ALTER COLUMN password_hash DROP NOT NULL,
    ADD COLUMN security_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT clients_security_revision_check CHECK (security_revision > 0);

ALTER TABLE auth_refresh_sessions
    ADD COLUMN session_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT auth_refresh_sessions_session_revision_check CHECK (session_revision > 0);

UPDATE marketplace_auth_sessions session
SET session_revision = customer.security_revision
FROM marketplace_customers customer
WHERE customer.id = session.marketplace_customer_id
  AND session.session_revision <> customer.security_revision;

DELETE FROM marketplace_customer_identities whatsapp_identity
USING marketplace_customer_identities phone_identity
WHERE whatsapp_identity.marketplace_customer_id = phone_identity.marketplace_customer_id
  AND whatsapp_identity.normalized_identifier = phone_identity.normalized_identifier
  AND whatsapp_identity.identifier_type = 'whatsapp'
  AND phone_identity.identifier_type = 'phone';

UPDATE marketplace_customer_identities
SET identifier_type = 'phone'
WHERE identifier_type = 'whatsapp';

DROP INDEX marketplace_customer_identities_phone_contact_unique;

UPDATE marketplace_customers
SET phone_e164 = COALESCE(phone_e164, whatsapp_e164),
    phone_verified_at = COALESCE(
        GREATEST(phone_verified_at, whatsapp_verified_at),
        phone_verified_at,
        whatsapp_verified_at
    );

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
SELECT id, 'email', email, email_verified_at
FROM marketplace_customers
WHERE email IS NOT NULL AND email_verified_at IS NOT NULL
ON CONFLICT DO NOTHING;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM clients client
        WHERE client.email IS NOT NULL
          AND client.email_verified_at IS NOT NULL
          AND NOT EXISTS (
              SELECT 1
              FROM provider_auth_identities identity
              WHERE identity.client_id = client.id
                AND identity.identity_type = 'email'
                AND identity.normalized_identifier = client.email
          )
    ) THEN
        RAISE EXCEPTION 'provider auth identity migration blocked: verified email lacks canonical identity';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM marketplace_customers customer
        WHERE customer.email IS NOT NULL
          AND customer.email_verified_at IS NOT NULL
          AND NOT EXISTS (
              SELECT 1
              FROM marketplace_customer_identities identity
              WHERE identity.marketplace_customer_id = customer.id
                AND identity.identifier_type = 'email'
                AND identity.normalized_identifier = customer.email
          )
    ) THEN
        RAISE EXCEPTION 'marketplace auth identity migration blocked: verified email lacks canonical identity';
    END IF;
END
$$;

DROP INDEX marketplace_customers_whatsapp_key;
ALTER TABLE marketplace_customers
    DROP CONSTRAINT marketplace_customers_identifier_check,
    DROP COLUMN whatsapp_e164,
    DROP COLUMN whatsapp_verified_at,
    ADD CONSTRAINT marketplace_customers_identifier_check CHECK (
        email IS NOT NULL OR phone_e164 IS NOT NULL
    );

ALTER TABLE marketplace_customer_identities
    DROP CONSTRAINT marketplace_customer_identities_type_check,
    ADD CONSTRAINT marketplace_customer_identities_type_check
        CHECK (identifier_type IN ('email', 'phone')),
    ADD CONSTRAINT marketplace_customer_identities_normalized_value_check CHECK (
        (identifier_type = 'email' AND normalized_identifier = lower(btrim(normalized_identifier)))
        OR (identifier_type = 'phone' AND normalized_identifier ~ '^\\+[1-9][0-9]{9,14}$')
    );

UPDATE marketplace_auth_challenges
SET identifier_type = 'phone'
WHERE identifier_type = 'whatsapp';

-- SMS auth was never a supported delivery path. Its short-lived challenge rows
-- carry no account state and cannot survive the channel cutover.
DELETE FROM marketplace_auth_challenges
WHERE delivery_channel = 'sms';

ALTER TABLE marketplace_auth_challenges
    DROP CONSTRAINT marketplace_auth_challenges_identifier_type_check,
    DROP CONSTRAINT marketplace_auth_challenges_delivery_channel_check,
    DROP CONSTRAINT marketplace_auth_challenges_purpose_check,
    DROP CONSTRAINT marketplace_auth_challenges_target_check,
    ADD COLUMN delivery_deadline timestamptz,
    ADD COLUMN delivery_accepted_at timestamptz,
    ADD COLUMN verify_expires_at timestamptz,
    ADD CONSTRAINT marketplace_auth_challenges_identifier_type_check
        CHECK (identifier_type IN ('email', 'phone')),
    ADD CONSTRAINT marketplace_auth_challenges_delivery_channel_check
        CHECK (delivery_channel IN ('email', 'whatsapp')),
    ADD CONSTRAINT marketplace_auth_challenges_purpose_check
        CHECK (purpose IN ('sign_in', 'link_identity', 'password_reset')),
    ADD CONSTRAINT marketplace_auth_challenges_target_check CHECK (
        (purpose = 'sign_in' AND target_customer_id IS NULL)
        OR (purpose = 'link_identity' AND target_customer_id IS NOT NULL)
        OR purpose = 'password_reset'
    ),
    ADD CONSTRAINT marketplace_auth_challenges_delivery_state_check CHECK (
        (delivery_accepted_at IS NULL AND verify_expires_at IS NULL)
        OR (delivery_accepted_at IS NOT NULL
            AND verify_expires_at IS NOT NULL
            AND verify_expires_at > delivery_accepted_at)
    );

CREATE TABLE provider_auth_challenges (
    id uuid PRIMARY KEY,
    identifier_type text NOT NULL,
    identifier text NOT NULL,
    delivery_channel text NOT NULL,
    purpose text NOT NULL,
    target_client_id uuid REFERENCES clients(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL,
    failed_attempts integer NOT NULL DEFAULT 0,
    delivery_deadline timestamptz NOT NULL,
    delivery_accepted_at timestamptz,
    verify_expires_at timestamptz,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_auth_challenges_identifier_type_check
        CHECK (identifier_type IN ('email', 'phone')),
    CONSTRAINT provider_auth_challenges_delivery_channel_check
        CHECK (delivery_channel IN ('email', 'whatsapp')),
    CONSTRAINT provider_auth_challenges_purpose_check
        CHECK (purpose IN ('sign_in', 'link_identity', 'password_reset')),
    CONSTRAINT provider_auth_challenges_target_check CHECK (
        (purpose = 'sign_in' AND target_client_id IS NULL)
        OR (purpose = 'link_identity' AND target_client_id IS NOT NULL)
        OR purpose = 'password_reset'
    ),
    CONSTRAINT provider_auth_challenges_failed_attempts_check
        CHECK (failed_attempts BETWEEN 0 AND 6),
    CONSTRAINT provider_auth_challenges_delivery_state_check CHECK (
        (delivery_accepted_at IS NULL AND verify_expires_at IS NULL)
        OR (delivery_accepted_at IS NOT NULL
            AND verify_expires_at IS NOT NULL
            AND verify_expires_at > delivery_accepted_at)
    )
);

CREATE INDEX provider_auth_challenges_identifier_idx
    ON provider_auth_challenges (identifier_type, identifier, purpose, created_at DESC);
CREATE INDEX provider_auth_challenges_target_idx
    ON provider_auth_challenges (target_client_id, created_at DESC)
    WHERE target_client_id IS NOT NULL;
CREATE INDEX provider_auth_challenges_delivery_deadline_idx
    ON provider_auth_challenges (delivery_deadline)
    WHERE consumed_at IS NULL;

CREATE TABLE auth_code_delivery_jobs (
    id uuid PRIMARY KEY,
    realm text NOT NULL,
    channel text NOT NULL,
    template_key text NOT NULL,
    locale text NOT NULL DEFAULT 'en',
    provider_challenge_id uuid REFERENCES provider_auth_challenges(id) ON DELETE CASCADE,
    marketplace_challenge_id uuid REFERENCES marketplace_auth_challenges(id) ON DELETE CASCADE,
    payload_ciphertext bytea,
    payload_nonce bytea,
    payload_key_version text,
    destination_fingerprint bytea NOT NULL,
    delivery_deadline timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    provider_message_id text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    accepted_at timestamptz,
    sent_at timestamptz,
    delivered_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT auth_code_delivery_jobs_realm_check
        CHECK (realm IN ('provider', 'marketplace_customer')),
    CONSTRAINT auth_code_delivery_jobs_channel_check
        CHECK (channel IN ('email', 'whatsapp')),
    CONSTRAINT auth_code_delivery_jobs_challenge_check CHECK (
        (realm = 'provider' AND provider_challenge_id IS NOT NULL AND marketplace_challenge_id IS NULL)
        OR (realm = 'marketplace_customer' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NOT NULL)
    ),
    CONSTRAINT auth_code_delivery_jobs_payload_check CHECK (
        octet_length(destination_fingerprint) = 32
        AND (
            (status IN ('pending', 'processing', 'retry')
             AND payload_ciphertext IS NOT NULL
             AND octet_length(payload_ciphertext) > 0
             AND payload_nonce IS NOT NULL
             AND octet_length(payload_nonce) = 12
             AND payload_key_version IS NOT NULL
             AND btrim(payload_key_version) <> '')
            OR
            (status IN ('accepted', 'sent', 'delivered', 'unknown', 'failed', 'expired')
             AND payload_ciphertext IS NULL
             AND payload_nonce IS NULL
             AND payload_key_version IS NULL)
        )
    ),
    CONSTRAINT auth_code_delivery_jobs_status_check CHECK (
        status IN ('pending', 'processing', 'retry', 'accepted', 'sent', 'delivered', 'unknown', 'failed', 'expired')
    ),
    CONSTRAINT auth_code_delivery_jobs_attempt_count_check CHECK (attempt_count >= 0),
    CONSTRAINT auth_code_delivery_jobs_lease_check CHECK (
        status <> 'processing'
        OR (btrim(lease_owner) <> '' AND lease_expires_at IS NOT NULL)
    ),
    CONSTRAINT auth_code_delivery_jobs_provider_challenge_unique UNIQUE (provider_challenge_id),
    CONSTRAINT auth_code_delivery_jobs_marketplace_challenge_unique UNIQUE (marketplace_challenge_id)
);

CREATE INDEX auth_code_delivery_jobs_claim_idx
    ON auth_code_delivery_jobs (next_attempt_at, created_at)
    WHERE status IN ('pending', 'retry');
CREATE INDEX auth_code_delivery_jobs_lease_idx
    ON auth_code_delivery_jobs (lease_expires_at)
    WHERE status = 'processing';
CREATE INDEX auth_code_delivery_jobs_deadline_idx
    ON auth_code_delivery_jobs (delivery_deadline)
    WHERE status IN ('pending', 'processing', 'retry', 'unknown');

-- migrate:down
DO $$
BEGIN
    RAISE EXCEPTION '20260905020000 is intentionally irreversible: canonical identities and passwordless accounts cannot be reconstructed safely';
END
$$;
