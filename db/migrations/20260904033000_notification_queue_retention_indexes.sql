-- migrate:up
CREATE INDEX notification_event_jobs_terminal_retention_idx
    ON notification_event_jobs (completed_at,booking_event_id)
    WHERE status IN ('completed','dead_letter');

CREATE INDEX notification_scope_replan_terminal_retention_idx
    ON notification_scope_replan_jobs (completed_at,client_id,preference_revision)
    WHERE status IN ('completed','dead_letter','superseded');

CREATE INDEX notification_in_app_jobs_terminal_retention_idx
    ON notification_in_app_jobs (completed_at,id)
    WHERE status IN ('completed','dead_letter','cancelled');

-- migrate:down
DROP INDEX IF EXISTS notification_in_app_jobs_terminal_retention_idx;
DROP INDEX IF EXISTS notification_scope_replan_terminal_retention_idx;
DROP INDEX IF EXISTS notification_event_jobs_terminal_retention_idx;
