-- migrate:up
DROP INDEX IF EXISTS notification_scope_replan_claim_idx;
CREATE INDEX notification_scope_replan_claim_idx
    ON notification_scope_replan_jobs (
        (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
        created_at,client_id,preference_revision
    ) WHERE status IN ('pending','retry','processing');

DROP INDEX IF EXISTS notification_deliveries_due_idx;
CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries (
        channel,
        (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
        scheduled_for,id
    ) WHERE status IN ('pending','retry','processing');

DROP INDEX IF EXISTS notification_in_app_jobs_due_idx;
CREATE INDEX notification_in_app_jobs_due_idx
    ON notification_in_app_jobs (
        (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END),
        scheduled_for,id
    ) WHERE status IN ('pending','retry','processing');

-- migrate:down
DROP INDEX IF EXISTS notification_in_app_jobs_due_idx;
CREATE INDEX notification_in_app_jobs_due_idx
    ON notification_in_app_jobs (next_attempt_at,scheduled_for,id)
    WHERE status IN ('pending','retry','processing');

DROP INDEX IF EXISTS notification_deliveries_due_idx;
CREATE INDEX notification_deliveries_due_idx
    ON notification_deliveries (channel,next_attempt_at,scheduled_for,id)
    WHERE status IN ('pending','retry','processing');

DROP INDEX IF EXISTS notification_scope_replan_claim_idx;
CREATE INDEX notification_scope_replan_claim_idx
    ON notification_scope_replan_jobs (next_attempt_at,created_at,client_id,preference_revision)
    WHERE status IN ('pending','retry','processing');
