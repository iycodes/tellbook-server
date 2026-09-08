-- migrate:up
DROP TABLE auth_pending_registrations;
DROP TABLE auth_password_reset_tokens;

-- migrate:down
DO $$
BEGIN
    RAISE EXCEPTION '20260905025000 is intentionally irreversible: removed legacy auth credentials cannot be reconstructed safely';
END
$$;
