-- migrate:up
CREATE INDEX payments_created_id_idx ON payments(created_at DESC,id DESC);
CREATE INDEX payment_exceptions_payment_created_idx ON payment_exceptions(payment_id,created_at DESC,id DESC);
-- migrate:down
DROP INDEX payment_exceptions_payment_created_idx;
DROP INDEX payments_created_id_idx;
