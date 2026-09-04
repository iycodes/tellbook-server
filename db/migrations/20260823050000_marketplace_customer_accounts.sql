-- migrate:up
CREATE TABLE marketplace_customers (
    id uuid PRIMARY KEY,
    full_name text NOT NULL DEFAULT '',
    email text,
    phone_e164 text,
    whatsapp_e164 text,
    email_verified_at timestamptz,
    phone_verified_at timestamptz,
    whatsapp_verified_at timestamptz,
    password_hash text,
    birthday date,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_customers_identifier_check CHECK (
        email IS NOT NULL OR phone_e164 IS NOT NULL OR whatsapp_e164 IS NOT NULL
    ),
    CONSTRAINT marketplace_customers_full_name_check CHECK (char_length(full_name) <= 160)
);

CREATE UNIQUE INDEX marketplace_customers_email_key
    ON marketplace_customers (lower(email)) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX marketplace_customers_phone_key
    ON marketplace_customers (phone_e164) WHERE phone_e164 IS NOT NULL;
CREATE UNIQUE INDEX marketplace_customers_whatsapp_key
    ON marketplace_customers (whatsapp_e164) WHERE whatsapp_e164 IS NOT NULL;

CREATE TABLE marketplace_auth_challenges (
    id uuid PRIMARY KEY,
    identifier_type text NOT NULL,
    identifier text NOT NULL,
    delivery_channel text NOT NULL,
    code_hash bytea NOT NULL,
    failed_attempts integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_auth_challenges_identifier_type_check
        CHECK (identifier_type IN ('email', 'phone', 'whatsapp')),
    CONSTRAINT marketplace_auth_challenges_delivery_channel_check
        CHECK (delivery_channel IN ('email', 'sms', 'whatsapp')),
    CONSTRAINT marketplace_auth_challenges_failed_attempts_check
        CHECK (failed_attempts BETWEEN 0 AND 6)
);

CREATE INDEX marketplace_auth_challenges_identifier_idx
    ON marketplace_auth_challenges (identifier_type, identifier, created_at DESC);
CREATE INDEX marketplace_auth_challenges_expiry_idx
    ON marketplace_auth_challenges (expires_at) WHERE consumed_at IS NULL;

CREATE TABLE marketplace_auth_sessions (
    id uuid PRIMARY KEY,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    user_agent text NOT NULL DEFAULT '',
    ip_address text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW()
);

CREATE INDEX marketplace_auth_sessions_customer_idx
    ON marketplace_auth_sessions (marketplace_customer_id, expires_at DESC);
CREATE INDEX marketplace_auth_sessions_expiry_idx
    ON marketplace_auth_sessions (expires_at);

CREATE TABLE marketplace_customer_addresses (
    id uuid PRIMARY KEY,
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    label text NOT NULL,
    address_line_1 text NOT NULL,
    address_line_2 text NOT NULL DEFAULT '',
    locality text NOT NULL DEFAULT '',
    state_region_id uuid REFERENCES administrative_regions(id) ON DELETE SET NULL,
    lga_region_id uuid REFERENCES administrative_regions(id) ON DELETE SET NULL,
    country_code text NOT NULL DEFAULT 'NG',
    postal_code text NOT NULL DEFAULT '',
    latitude numeric(9,6),
    longitude numeric(9,6),
    is_default boolean NOT NULL DEFAULT FALSE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_customer_addresses_label_check CHECK (btrim(label) <> ''),
    CONSTRAINT marketplace_customer_addresses_line_1_check CHECK (btrim(address_line_1) <> ''),
    CONSTRAINT marketplace_customer_addresses_country_check CHECK (country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT marketplace_customer_addresses_coordinates_check CHECK (
        (latitude IS NULL AND longitude IS NULL)
        OR (latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180)
    )
);

CREATE INDEX marketplace_customer_addresses_customer_idx
    ON marketplace_customer_addresses (marketplace_customer_id, is_default DESC, created_at);
CREATE UNIQUE INDEX marketplace_customer_addresses_one_default
    ON marketplace_customer_addresses (marketplace_customer_id) WHERE is_default;

CREATE TABLE marketplace_notification_preferences (
    marketplace_customer_id uuid PRIMARY KEY REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    booking_email boolean NOT NULL DEFAULT TRUE,
    booking_sms boolean NOT NULL DEFAULT FALSE,
    booking_whatsapp boolean NOT NULL DEFAULT FALSE,
    marketing_email boolean NOT NULL DEFAULT FALSE,
    updated_at timestamptz NOT NULL DEFAULT NOW()
);

ALTER TABLE bookings
    ADD COLUMN marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE SET NULL;

CREATE INDEX bookings_marketplace_customer_idx
    ON bookings (marketplace_customer_id, start_at DESC)
    WHERE marketplace_customer_id IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS bookings_marketplace_customer_idx;
ALTER TABLE bookings DROP COLUMN IF EXISTS marketplace_customer_id;
DROP TABLE IF EXISTS marketplace_notification_preferences;
DROP TABLE IF EXISTS marketplace_customer_addresses;
DROP TABLE IF EXISTS marketplace_auth_sessions;
DROP TABLE IF EXISTS marketplace_auth_challenges;
DROP TABLE IF EXISTS marketplace_customers;
