-- migrate:up
-- Platform windows cannot use the provider-leading index. Keep existing indexes
-- and domain tables; no parallel booking or customer persistence.
CREATE INDEX bookings_platform_window_idx ON bookings(start_at,id);
CREATE INDEX bookings_contact_window_idx ON bookings(customer_id,start_at,id);
-- migrate:down
DROP INDEX bookings_contact_window_idx;
DROP INDEX bookings_platform_window_idx;
