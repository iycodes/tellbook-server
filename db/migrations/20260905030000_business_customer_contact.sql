-- migrate:up
ALTER TABLE client_profiles
    ADD COLUMN customer_contact_phone text,
    ADD COLUMN customer_contact_verified_at timestamptz,
    ADD COLUMN allow_booking_contact boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN show_contact_on_public_profile boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN customer_contact_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT customer_contact_phone_check CHECK (
        customer_contact_phone IS NULL OR customer_contact_phone ~ '^\+[1-9][0-9]{7,14}$'
    ),
    ADD CONSTRAINT customer_contact_verified_check CHECK (
        customer_contact_verified_at IS NULL OR customer_contact_phone IS NOT NULL
    ),
    ADD CONSTRAINT customer_contact_visibility_check CHECK (
        NOT (allow_booking_contact OR show_contact_on_public_profile)
        OR customer_contact_verified_at IS NOT NULL
    ),
    ADD CONSTRAINT customer_contact_revision_check CHECK (customer_contact_revision > 0);

ALTER TABLE client_profiles
    ADD COLUMN public_contact_phone text GENERATED ALWAYS AS (
        CASE WHEN show_contact_on_public_profile AND customer_contact_verified_at IS NOT NULL
             THEN customer_contact_phone END
    ) STORED,
    ADD COLUMN booking_contact_phone text GENERATED ALWAYS AS (
        CASE WHEN allow_booking_contact AND customer_contact_verified_at IS NOT NULL
             THEN customer_contact_phone END
    ) STORED;

CREATE TABLE provider_customer_contact_challenges (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES client_profiles(client_id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash)=32),
    destination_hmac bytea NOT NULL CHECK (octet_length(destination_hmac)=32),
    contact_revision bigint NOT NULL CHECK (contact_revision > 0),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 5),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CHECK (expires_at > created_at)
);
CREATE UNIQUE INDEX provider_customer_contact_challenge_active_idx
    ON provider_customer_contact_challenges(client_id) WHERE consumed_at IS NULL;
CREATE INDEX provider_customer_contact_challenge_expiry_idx
    ON provider_customer_contact_challenges(expires_at);

CREATE TRIGGER client_profiles_contact_public_revision_trigger
AFTER UPDATE OF customer_contact_phone, customer_contact_verified_at, show_contact_on_public_profile
ON client_profiles FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

-- migrate:down
DROP TRIGGER client_profiles_contact_public_revision_trigger ON client_profiles;
DROP TABLE provider_customer_contact_challenges;
ALTER TABLE client_profiles
    DROP COLUMN public_contact_phone, DROP COLUMN booking_contact_phone,
    DROP COLUMN customer_contact_phone, DROP COLUMN customer_contact_verified_at,
    DROP COLUMN allow_booking_contact, DROP COLUMN show_contact_on_public_profile,
    DROP COLUMN customer_contact_revision;
