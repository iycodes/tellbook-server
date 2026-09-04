-- migrate:up
CREATE INDEX customers_tessa_recent_idx
    ON customers (client_id, updated_at DESC, id);
CREATE INDEX customers_tessa_name_trgm_idx
    ON customers USING gin (LOWER(full_name) gin_trgm_ops);

-- migrate:down
DROP INDEX customers_tessa_name_trgm_idx;
DROP INDEX customers_tessa_recent_idx;
