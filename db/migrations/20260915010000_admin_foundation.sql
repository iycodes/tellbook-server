-- migrate:up
CREATE TABLE admin_staff (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text NOT NULL UNIQUE CHECK(email=lower(btrim(email))),
 full_name text NOT NULL, role text NOT NULL CHECK(role IN ('super_admin','operations','support','finance','analyst')),
 status text NOT NULL DEFAULT 'invited' CHECK(status IN ('invited','enrolling','active','suspended')),
 password_hash text NOT NULL DEFAULT '', mfa_cipher jsonb, last_totp_step bigint NOT NULL DEFAULT -1,
 revision bigint NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE admin_sessions (
 id uuid PRIMARY KEY, staff_id uuid NOT NULL REFERENCES admin_staff(id), token_hash bytea NOT NULL UNIQUE,
 stage text NOT NULL CHECK(stage IN ('login','enroll','full')), staff_revision bigint NOT NULL,
 failed_attempts integer NOT NULL DEFAULT 0, user_agent text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL
);
CREATE INDEX admin_sessions_staff_idx ON admin_sessions(staff_id);
CREATE INDEX admin_sessions_expiry_idx ON admin_sessions(expires_at);
CREATE TABLE admin_recovery_codes (staff_id uuid NOT NULL REFERENCES admin_staff(id), code_hash bytea NOT NULL, PRIMARY KEY(staff_id,code_hash));
CREATE TABLE admin_invitations (
 id uuid PRIMARY KEY, staff_id uuid NOT NULL REFERENCES admin_staff(id), token_hash bytea NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL, consumed_at timestamptz, revoked_at timestamptz,
 delivery_state text NOT NULL DEFAULT 'not_sent' CHECK(delivery_state IN ('not_sent','sent','failed','unknown')),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_invitation_staff_idx ON admin_invitations(staff_id,created_at DESC);
CREATE TABLE admin_password_resets (
 token_hash bytea PRIMARY KEY, staff_id uuid NOT NULL REFERENCES admin_staff(id), expires_at timestamptz NOT NULL, consumed_at timestamptz
);
CREATE TABLE admin_auth_limits (key_hash bytea PRIMARY KEY, attempts integer NOT NULL, window_end timestamptz NOT NULL);
CREATE INDEX admin_auth_limits_expiry_idx ON admin_auth_limits(window_end);
CREATE TABLE admin_audit_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), actor_id uuid REFERENCES admin_staff(id), action text NOT NULL,
 entity_type text NOT NULL, entity_id uuid, reason text NOT NULL DEFAULT '', details jsonb NOT NULL DEFAULT '{}',
 request_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX admin_audit_time_idx ON admin_audit_events(created_at DESC,id DESC);
CREATE INDEX admin_audit_entity_idx ON admin_audit_events(entity_type,entity_id,created_at DESC);
CREATE TABLE admin_business_notes (
 id uuid PRIMARY KEY, business_id uuid NOT NULL REFERENCES clients(id), author_id uuid NOT NULL REFERENCES admin_staff(id),
 body text NOT NULL CHECK(length(btrim(body)) BETWEEN 1 AND 4000), request_key uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(author_id,request_key)
);
CREATE INDEX admin_business_notes_business_idx ON admin_business_notes(business_id,created_at DESC,id DESC);
-- migrate:down
DROP TABLE admin_business_notes;
DROP TABLE admin_audit_events;
DROP TABLE admin_auth_limits;
DROP TABLE admin_password_resets;
DROP TABLE admin_invitations;
DROP TABLE admin_recovery_codes;
DROP TABLE admin_sessions;
DROP TABLE admin_staff;
