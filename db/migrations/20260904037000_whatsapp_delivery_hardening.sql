-- migrate:up
DROP INDEX IF EXISTS meta_whatsapp_webhook_receipts_pending_idx;
CREATE INDEX meta_whatsapp_webhook_receipts_pending_idx
    ON meta_whatsapp_webhook_receipts (
        (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),
        created_at,id
    ) WHERE event_kind='status'
      AND processing_status IN ('pending','retry','processing');

-- migrate:down
DROP INDEX IF EXISTS meta_whatsapp_webhook_receipts_pending_idx;
CREATE INDEX meta_whatsapp_webhook_receipts_pending_idx
    ON meta_whatsapp_webhook_receipts (available_at,created_at,id)
    WHERE processing_status IN ('pending','retry','processing');
