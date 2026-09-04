-- migrate:up
ALTER TABLE payment_provider_request_budgets
    ADD COLUMN lease_owner text NOT NULL DEFAULT '',
    ADD COLUMN lease_expires_at timestamptz,
    ADD CONSTRAINT payment_provider_request_budgets_lease_check CHECK (
        (lease_owner='' AND lease_expires_at IS NULL)
        OR (BTRIM(lease_owner)<>'' AND lease_expires_at IS NOT NULL)
    );

-- migrate:down
ALTER TABLE payment_provider_request_budgets
    DROP CONSTRAINT payment_provider_request_budgets_lease_check,
    DROP COLUMN lease_owner,
    DROP COLUMN lease_expires_at;
