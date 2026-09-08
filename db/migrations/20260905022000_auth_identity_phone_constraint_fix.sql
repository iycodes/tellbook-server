-- migrate:up

ALTER TABLE provider_auth_identities
    DROP CONSTRAINT provider_auth_identities_normalized_value_check,
    ADD CONSTRAINT provider_auth_identities_normalized_value_check CHECK (
        (identity_type = 'email' AND normalized_identifier = lower(btrim(normalized_identifier)))
        OR (identity_type = 'phone' AND normalized_identifier ~ '^\+[1-9][0-9]{9,14}$')
    );

ALTER TABLE marketplace_customer_identities
    DROP CONSTRAINT marketplace_customer_identities_normalized_value_check,
    ADD CONSTRAINT marketplace_customer_identities_normalized_value_check CHECK (
        (identifier_type = 'email' AND normalized_identifier = lower(btrim(normalized_identifier)))
        OR (identifier_type = 'phone' AND normalized_identifier ~ '^\+[1-9][0-9]{9,14}$')
    );

-- migrate:down
DO $$
BEGIN
    RAISE EXCEPTION '20260905022000 is intentionally irreversible: the prior phone regex rejected valid E.164 identities';
END
$$;
