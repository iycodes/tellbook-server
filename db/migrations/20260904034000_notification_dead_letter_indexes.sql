-- migrate:up
CREATE INDEX notification_event_jobs_dead_letter_idx
    ON notification_event_jobs (booking_event_id)
    WHERE status='dead_letter';

CREATE INDEX notification_scope_replan_dead_letter_idx
    ON notification_scope_replan_jobs (client_id,preference_revision)
    WHERE status='dead_letter';

CREATE INDEX notification_in_app_jobs_dead_letter_idx
    ON notification_in_app_jobs (id)
    WHERE status='dead_letter';

-- migrate:down
DROP INDEX IF EXISTS notification_in_app_jobs_dead_letter_idx;
DROP INDEX IF EXISTS notification_scope_replan_dead_letter_idx;
DROP INDEX IF EXISTS notification_event_jobs_dead_letter_idx;
