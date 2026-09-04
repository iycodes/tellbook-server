-- migrate:up
CREATE UNIQUE INDEX marketplace_customer_identities_phone_contact_unique
    ON marketplace_customer_identities (normalized_identifier)
    WHERE identifier_type IN ('phone', 'whatsapp');

-- migrate:down
DROP INDEX IF EXISTS marketplace_customer_identities_phone_contact_unique;
