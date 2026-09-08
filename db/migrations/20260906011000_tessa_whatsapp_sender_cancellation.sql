-- migrate:up
CREATE INDEX tessa_whatsapp_pending_sender_idx ON tessa_whatsapp_link_challenges(phone_number_id,destination,client_id)
    WHERE consumed_at IS NULL;
CREATE INDEX tessa_whatsapp_outbox_sender_idx ON tessa_whatsapp_outbox(phone_number_id,destination,client_id)
    WHERE status IN ('pending','retry','processing');

-- migrate:down
DROP INDEX tessa_whatsapp_outbox_sender_idx;
DROP INDEX tessa_whatsapp_pending_sender_idx;
