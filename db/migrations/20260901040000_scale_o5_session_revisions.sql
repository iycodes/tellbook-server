-- migrate:up
ALTER TABLE marketplace_customers
    ADD COLUMN security_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT marketplace_customers_security_revision_check
        CHECK (security_revision > 0);

ALTER TABLE marketplace_auth_sessions
    ADD COLUMN session_revision bigint NOT NULL DEFAULT 1,
    ADD CONSTRAINT marketplace_auth_sessions_session_revision_check
        CHECK (session_revision > 0);

-- migrate:down
ALTER TABLE marketplace_auth_sessions
    DROP CONSTRAINT IF EXISTS marketplace_auth_sessions_session_revision_check,
    DROP COLUMN IF EXISTS session_revision;

ALTER TABLE marketplace_customers
    DROP CONSTRAINT IF EXISTS marketplace_customers_security_revision_check,
    DROP COLUMN IF EXISTS security_revision;
