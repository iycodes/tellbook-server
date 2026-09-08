-- migrate:up
ALTER TABLE meta_whatsapp_webhook_receipts
    ADD COLUMN correlation_id text NOT NULL DEFAULT '';

ALTER TABLE meta_whatsapp_webhook_receipts
    ADD CONSTRAINT meta_whatsapp_webhook_receipts_correlation_check
    CHECK (correlation_id='' OR correlation_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$');

CREATE INDEX meta_whatsapp_webhook_receipts_terminal_retention_idx
    ON meta_whatsapp_webhook_receipts (processed_at,id)
    WHERE processing_status IN ('completed','dead_letter');

DROP INDEX IF EXISTS notification_deliveries_dispatch_reconcile_idx;
CREATE INDEX notification_deliveries_dispatch_reconcile_idx
    ON notification_deliveries (reconcile_after,id)
    WHERE status IN ('dispatching','unknown');

-- migrate:down
DROP INDEX IF EXISTS notification_deliveries_dispatch_reconcile_idx;
CREATE INDEX notification_deliveries_dispatch_reconcile_idx
    ON notification_deliveries (reconcile_after,id)
    WHERE status='dispatching';
DROP INDEX IF EXISTS meta_whatsapp_webhook_receipts_terminal_retention_idx;
ALTER TABLE meta_whatsapp_webhook_receipts
    DROP CONSTRAINT IF EXISTS meta_whatsapp_webhook_receipts_correlation_check,
    DROP COLUMN IF EXISTS correlation_id;
