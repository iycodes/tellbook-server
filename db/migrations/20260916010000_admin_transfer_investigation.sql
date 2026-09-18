-- migrate:up
CREATE INDEX payouts_created_id_idx ON payouts(created_at DESC,id DESC);
CREATE INDEX booking_refund_requests_created_id_idx ON booking_refund_requests(created_at DESC,id DESC);
-- migrate:down
DROP INDEX booking_refund_requests_created_id_idx;
DROP INDEX payouts_created_id_idx;
