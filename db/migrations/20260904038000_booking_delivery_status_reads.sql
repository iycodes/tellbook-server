-- migrate:up
CREATE INDEX notification_deliveries_booking_channel_status_idx
    ON notification_deliveries (booking_id,audience_type,channel,updated_at DESC,id DESC);

-- migrate:down
DROP INDEX IF EXISTS notification_deliveries_booking_channel_status_idx;
