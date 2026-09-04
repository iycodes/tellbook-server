-- migrate:up
ALTER TABLE bookings
    ADD COLUMN customer_email_snapshot text,
    ADD COLUMN email_reminder_consent boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN email_reminder_consent_at timestamptz,
    ADD COLUMN email_reminder_consent_source text NOT NULL DEFAULT '',
    ADD COLUMN customer_whatsapp_e164_snapshot text,
    ADD COLUMN whatsapp_consent_at timestamptz,
    ADD COLUMN whatsapp_consent_source text NOT NULL DEFAULT '',
    ADD COLUMN notification_consent_policy_revision integer NOT NULL DEFAULT 0;

ALTER TABLE inbox_ai_booking_confirmations
    ADD COLUMN email_reminder_consent boolean NOT NULL DEFAULT FALSE;

-- Legacy bookings did not bind WhatsApp consent to an immutable normalized
-- destination. They must not become externally deliverable by inference.
UPDATE bookings SET whatsapp_consent = FALSE WHERE whatsapp_consent;

ALTER TABLE bookings
    ADD CONSTRAINT bookings_customer_email_snapshot_check CHECK (
        customer_email_snapshot IS NULL
        OR (
            customer_email_snapshot = lower(btrim(customer_email_snapshot))
            AND customer_email_snapshot !~ '[[:space:]]'
            AND position('@' IN customer_email_snapshot) > 1
            AND char_length(customer_email_snapshot) <= 320
        )
    ),
    ADD CONSTRAINT bookings_customer_whatsapp_snapshot_check CHECK (
        customer_whatsapp_e164_snapshot IS NULL
        OR customer_whatsapp_e164_snapshot ~ '^\+[1-9][0-9]{7,14}$'
    ),
    ADD CONSTRAINT bookings_notification_consent_policy_check CHECK (
        notification_consent_policy_revision IN (0, 1)
    ),
    ADD CONSTRAINT bookings_email_reminder_consent_shape_check CHECK (
        (
            email_reminder_consent
            AND customer_email_snapshot IS NOT NULL
            AND email_reminder_consent_at IS NOT NULL
            AND email_reminder_consent_source IN (
                'public_checkout', 'marketplace_checkout', 'inbox_autopilot'
            )
        )
        OR (
            NOT email_reminder_consent
            AND email_reminder_consent_at IS NULL
            AND email_reminder_consent_source = ''
        )
    ),
    ADD CONSTRAINT bookings_whatsapp_consent_shape_check CHECK (
        (
            whatsapp_consent
            AND customer_whatsapp_e164_snapshot IS NOT NULL
            AND whatsapp_consent_at IS NOT NULL
            AND whatsapp_consent_source IN (
                'public_checkout', 'marketplace_checkout', 'inbox_autopilot'
            )
        )
        OR (
            NOT whatsapp_consent
            AND whatsapp_consent_at IS NULL
            AND whatsapp_consent_source = ''
        )
    ),
    ADD CONSTRAINT bookings_notification_snapshot_revision_shape_check CHECK (
        notification_consent_policy_revision = 0
        OR (notification_consent_policy_revision = 1 AND customer_email_snapshot IS NOT NULL)
    );

CREATE TABLE provider_notification_preferences (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    booking_email boolean NOT NULL DEFAULT TRUE,
    booking_whatsapp boolean NOT NULL DEFAULT FALSE,
    appointment_reminder_enabled boolean NOT NULL DEFAULT TRUE,
    appointment_reminder_minutes integer NOT NULL DEFAULT 1440,
    whatsapp_e164 text,
    whatsapp_verified_at timestamptz,
    whatsapp_verification_method text NOT NULL DEFAULT '',
    whatsapp_verification_revision bigint NOT NULL DEFAULT 0,
    preference_revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_notification_preferences_reminder_check
        CHECK (appointment_reminder_minutes = 1440),
    CONSTRAINT provider_notification_preferences_phone_check
        CHECK (whatsapp_e164 IS NULL OR whatsapp_e164 ~ '^\+[1-9][0-9]{7,14}$'),
    CONSTRAINT provider_notification_preferences_verification_check CHECK (
        (
            whatsapp_verified_at IS NULL
            AND whatsapp_verification_method = ''
        )
        OR (
            whatsapp_e164 IS NOT NULL
            AND whatsapp_verified_at IS NOT NULL
            AND whatsapp_verification_method = 'inbound_challenge'
        )
    ),
    CONSTRAINT provider_notification_preferences_enabled_check
        CHECK (NOT booking_whatsapp OR whatsapp_verified_at IS NOT NULL),
    CONSTRAINT provider_notification_preferences_revision_check
        CHECK (preference_revision > 0 AND whatsapp_verification_revision >= 0)
);

CREATE TABLE provider_whatsapp_verification_challenges (
    id uuid PRIMARY KEY,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    destination_hmac bytea NOT NULL,
    verification_revision bigint NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT provider_whatsapp_challenge_hash_check CHECK (
        octet_length(token_hash) = 32 AND octet_length(destination_hmac) = 32
    ),
    CONSTRAINT provider_whatsapp_challenge_attempt_check
        CHECK (attempt_count BETWEEN 0 AND 5),
    CONSTRAINT provider_whatsapp_challenge_expiry_check
        CHECK (expires_at > created_at),
    CONSTRAINT provider_whatsapp_challenge_revision_check
        CHECK (verification_revision > 0)
);

CREATE UNIQUE INDEX provider_whatsapp_challenge_active_client_idx
    ON provider_whatsapp_verification_challenges (client_id)
    WHERE consumed_at IS NULL;
CREATE INDEX provider_whatsapp_challenge_expiry_idx
    ON provider_whatsapp_verification_challenges (expires_at)
    WHERE consumed_at IS NULL;

CREATE TABLE notification_contact_suppressions (
    channel text NOT NULL,
    destination_hmac bytea NOT NULL,
    reason text NOT NULL,
    source text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel, destination_hmac),
    CONSTRAINT notification_contact_suppressions_channel_check
        CHECK (channel IN ('email', 'whatsapp')),
    CONSTRAINT notification_contact_suppressions_hmac_check
        CHECK (octet_length(destination_hmac) = 32),
    CONSTRAINT notification_contact_suppressions_reason_check
        CHECK (reason IN ('user_opt_out', 'invalid_address', 'hard_bounce', 'manual')),
    CONSTRAINT notification_contact_suppressions_source_check
        CHECK (source IN ('inbound_control', 'customer_preference', 'provider_response', 'admin'))
);

CREATE TABLE notification_scope_replan_jobs (
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    preference_revision bigint NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT NOW(),
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    booking_cursor_start_at timestamptz,
    booking_cursor_id uuid,
    last_error_code text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    completed_at timestamptz,
    PRIMARY KEY (client_id, preference_revision),
    CONSTRAINT notification_scope_replan_status_check
        CHECK (status IN ('pending', 'processing', 'retry', 'completed', 'dead_letter', 'superseded')),
    CONSTRAINT notification_scope_replan_attempt_check
        CHECK (attempt_count BETWEEN 0 AND 20),
    CONSTRAINT notification_scope_replan_revision_check
        CHECK (preference_revision > 0),
    CONSTRAINT notification_scope_replan_error_check
        CHECK (char_length(last_error_code) <= 80),
    CONSTRAINT notification_scope_replan_lease_check CHECK (
        (status = 'processing' AND btrim(lease_owner) <> '' AND lease_expires_at IS NOT NULL)
        OR (status <> 'processing' AND lease_owner = '' AND lease_expires_at IS NULL)
    ),
    CONSTRAINT notification_scope_replan_cursor_check CHECK (
        (booking_cursor_start_at IS NULL AND booking_cursor_id IS NULL)
        OR (booking_cursor_start_at IS NOT NULL AND booking_cursor_id IS NOT NULL)
    ),
    CONSTRAINT notification_scope_replan_completion_check CHECK (
        (status IN ('completed', 'dead_letter', 'superseded') AND completed_at IS NOT NULL)
        OR (status NOT IN ('completed', 'dead_letter', 'superseded') AND completed_at IS NULL)
    )
);

CREATE INDEX notification_scope_replan_claim_idx
    ON notification_scope_replan_jobs (next_attempt_at, created_at, client_id, preference_revision)
    WHERE status IN ('pending', 'retry', 'processing');

-- migrate:down
DROP TABLE IF EXISTS notification_scope_replan_jobs;
DROP TABLE IF EXISTS notification_contact_suppressions;
DROP TABLE IF EXISTS provider_whatsapp_verification_challenges;
DROP TABLE IF EXISTS provider_notification_preferences;

ALTER TABLE bookings
    DROP CONSTRAINT IF EXISTS bookings_notification_snapshot_revision_shape_check,
    DROP CONSTRAINT IF EXISTS bookings_whatsapp_consent_shape_check,
    DROP CONSTRAINT IF EXISTS bookings_email_reminder_consent_shape_check,
    DROP CONSTRAINT IF EXISTS bookings_notification_consent_policy_check,
    DROP CONSTRAINT IF EXISTS bookings_customer_whatsapp_snapshot_check,
    DROP CONSTRAINT IF EXISTS bookings_customer_email_snapshot_check,
    DROP COLUMN IF EXISTS notification_consent_policy_revision,
    DROP COLUMN IF EXISTS whatsapp_consent_source,
    DROP COLUMN IF EXISTS whatsapp_consent_at,
    DROP COLUMN IF EXISTS customer_whatsapp_e164_snapshot,
    DROP COLUMN IF EXISTS email_reminder_consent_source,
    DROP COLUMN IF EXISTS email_reminder_consent_at,
    DROP COLUMN IF EXISTS email_reminder_consent,
    DROP COLUMN IF EXISTS customer_email_snapshot;

ALTER TABLE inbox_ai_booking_confirmations
    DROP COLUMN IF EXISTS email_reminder_consent;
