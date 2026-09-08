-- migrate:up
ALTER TABLE notification_event_jobs
    ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'live';

ALTER TABLE notification_event_jobs
    DROP CONSTRAINT IF EXISTS notification_event_jobs_origin_check;
ALTER TABLE notification_event_jobs
    ADD CONSTRAINT notification_event_jobs_origin_check
    CHECK (origin IN ('live','backfill'));

-- Rows inserted by the original one-time migration share a much later job
-- timestamp than their source event. Live trigger jobs are created in the same
-- transaction as their event, so this upgrades already-applied databases
-- without classifying subsequent live work as historical backfill.
UPDATE notification_event_jobs job
SET origin='backfill'
FROM booking_domain_events event
WHERE event.id=job.booking_event_id
  AND event.created_at < job.created_at-INTERVAL '5 minutes';

CREATE INDEX IF NOT EXISTS booking_domain_events_booking_sequence_idx
    ON booking_domain_events (booking_id,sequence DESC) INCLUDE (id);

DROP INDEX IF EXISTS notification_deliveries_terminal_retention_idx;
CREATE INDEX notification_deliveries_terminal_retention_idx
    ON notification_deliveries (completed_at,id)
    WHERE status IN ('failed','deleted','cancelled','read','delivered')
       OR (channel='email' AND status='accepted');

CREATE INDEX notification_deliveries_dispatch_reconcile_idx
    ON notification_deliveries (reconcile_after,id)
    WHERE status='dispatching';

-- migrate:down
DROP INDEX IF EXISTS notification_deliveries_dispatch_reconcile_idx;
DROP INDEX IF EXISTS notification_deliveries_terminal_retention_idx;
CREATE INDEX notification_deliveries_terminal_retention_idx
    ON notification_deliveries (completed_at,id)
    WHERE status IN ('failed','deleted','cancelled','read','delivered');
DROP INDEX IF EXISTS booking_domain_events_booking_sequence_idx;
ALTER TABLE notification_event_jobs
    DROP CONSTRAINT IF EXISTS notification_event_jobs_origin_check,
    DROP COLUMN IF EXISTS origin;
