-- migrate:up
CREATE TABLE auth_password_reset_grants (
    id uuid PRIMARY KEY,
    realm text NOT NULL,
    provider_client_id uuid REFERENCES clients(id) ON DELETE CASCADE,
    marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    provider_challenge_id uuid REFERENCES provider_auth_challenges(id) ON DELETE CASCADE,
    marketplace_challenge_id uuid REFERENCES marketplace_auth_challenges(id) ON DELETE CASCADE,
    grant_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT auth_password_reset_grants_realm_check
        CHECK (realm IN ('provider', 'marketplace_customer')),
    CONSTRAINT auth_password_reset_grants_hash_check
        CHECK (octet_length(grant_hash) = 32),
    CONSTRAINT auth_password_reset_grants_scope_check CHECK (
        (realm = 'provider'
         AND provider_client_id IS NOT NULL
         AND provider_challenge_id IS NOT NULL
         AND marketplace_customer_id IS NULL
         AND marketplace_challenge_id IS NULL)
        OR
        (realm = 'marketplace_customer'
         AND provider_client_id IS NULL
         AND provider_challenge_id IS NULL
         AND marketplace_customer_id IS NOT NULL
         AND marketplace_challenge_id IS NOT NULL)
    ),
    CONSTRAINT auth_password_reset_grants_provider_challenge_unique
        UNIQUE (provider_challenge_id),
    CONSTRAINT auth_password_reset_grants_marketplace_challenge_unique
        UNIQUE (marketplace_challenge_id)
);

CREATE INDEX auth_password_reset_grants_expiry_idx
    ON auth_password_reset_grants (expires_at)
    WHERE consumed_at IS NULL;

-- migrate:down
DROP TABLE auth_password_reset_grants;
