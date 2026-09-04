-- migrate:up
DROP INDEX inbox_conversations_provider_list_idx;
DROP INDEX inbox_conversations_marketplace_list_idx;

CREATE INDEX inbox_conversations_provider_list_idx
    ON inbox_conversations (
        client_id,
        last_message_at DESC NULLS LAST,
        created_at DESC,
        id DESC
    );
CREATE INDEX inbox_conversations_marketplace_list_idx
    ON inbox_conversations (
        marketplace_customer_id,
        last_message_at DESC NULLS LAST,
        created_at DESC,
        id DESC
    );

CREATE INDEX marketplace_customers_inbox_name_trgm_idx
    ON marketplace_customers USING gin (lower(full_name) gin_trgm_ops);
CREATE INDEX client_profiles_inbox_business_name_trgm_idx
    ON client_profiles USING gin (lower(business_name) gin_trgm_ops);
CREATE INDEX clients_inbox_full_name_trgm_idx
    ON clients USING gin (lower(full_name) gin_trgm_ops);

-- migrate:down
DROP INDEX clients_inbox_full_name_trgm_idx;
DROP INDEX client_profiles_inbox_business_name_trgm_idx;
DROP INDEX marketplace_customers_inbox_name_trgm_idx;

DROP INDEX inbox_conversations_marketplace_list_idx;
DROP INDEX inbox_conversations_provider_list_idx;

CREATE INDEX inbox_conversations_provider_list_idx
    ON inbox_conversations (client_id, last_message_at DESC NULLS LAST, id DESC);
CREATE INDEX inbox_conversations_marketplace_list_idx
    ON inbox_conversations (marketplace_customer_id, last_message_at DESC NULLS LAST, id DESC);
