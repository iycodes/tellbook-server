-- migrate:up
ALTER TABLE marketplace_notifications
    DROP CONSTRAINT marketplace_notifications_booking_id_fkey,
    ADD CONSTRAINT marketplace_notifications_booking_id_fkey
        FOREIGN KEY (booking_id) REFERENCES bookings(id) ON DELETE SET NULL,
    DROP CONSTRAINT marketplace_notifications_conversation_id_fkey,
    ADD CONSTRAINT marketplace_notifications_conversation_id_fkey
        FOREIGN KEY (conversation_id) REFERENCES inbox_conversations(id) ON DELETE SET NULL;

-- migrate:down
ALTER TABLE marketplace_notifications
    DROP CONSTRAINT marketplace_notifications_booking_id_fkey,
    ADD CONSTRAINT marketplace_notifications_booking_id_fkey
        FOREIGN KEY (booking_id) REFERENCES bookings(id) ON DELETE CASCADE,
    DROP CONSTRAINT marketplace_notifications_conversation_id_fkey,
    ADD CONSTRAINT marketplace_notifications_conversation_id_fkey
        FOREIGN KEY (conversation_id) REFERENCES inbox_conversations(id) ON DELETE CASCADE;
