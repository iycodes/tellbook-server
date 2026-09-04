-- migrate:up
CREATE INDEX bookings_provider_window_cursor_idx
    ON bookings (client_id, start_at, id);

CREATE INDEX bookings_provider_customer_summary_idx
    ON bookings (client_id, customer_id, start_at)
    INCLUDE (end_at, status);

CREATE INDEX customers_provider_page_cursor_idx
    ON customers (client_id, (COALESCE(last_seen_at, created_at)) DESC, id DESC);

CREATE INDEX notifications_provider_page_cursor_idx
    ON notifications (client_id, created_at DESC, id DESC);

CREATE INDEX notifications_provider_unread_cursor_idx
    ON notifications (client_id, created_at DESC, id DESC)
    WHERE read_at IS NULL;

CREATE INDEX notifications_provider_urgent_cursor_idx
    ON notifications (client_id, created_at DESC, id DESC)
    WHERE severity='urgent';

-- migrate:down
DROP INDEX notifications_provider_urgent_cursor_idx;
DROP INDEX notifications_provider_unread_cursor_idx;
DROP INDEX notifications_provider_page_cursor_idx;
DROP INDEX customers_provider_page_cursor_idx;
DROP INDEX bookings_provider_customer_summary_idx;
DROP INDEX bookings_provider_window_cursor_idx;
