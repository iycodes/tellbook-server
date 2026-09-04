-- migrate:up
ALTER TABLE tessa_runs DROP CONSTRAINT tessa_runs_trigger_fk;
ALTER TABLE tessa_runs ADD CONSTRAINT tessa_runs_trigger_fk
    FOREIGN KEY (trigger_message_id, thread_id, client_id)
    REFERENCES tessa_messages(id, thread_id, client_id) ON DELETE CASCADE;

ALTER TABLE tessa_messages DROP CONSTRAINT tessa_messages_result_run_fk;
ALTER TABLE tessa_messages ADD CONSTRAINT tessa_messages_result_run_fk
    FOREIGN KEY (run_id, thread_id, client_id)
    REFERENCES tessa_runs(id, thread_id, client_id) ON DELETE CASCADE;

DROP INDEX tessa_events_created_at_idx;
CREATE INDEX tessa_events_created_sequence_idx ON tessa_events (created_at, sequence);
CREATE INDEX tessa_threads_archived_retention_idx ON tessa_threads (archived_at, id)
    WHERE status = 'archived';

-- migrate:down
DROP INDEX tessa_threads_archived_retention_idx;
DROP INDEX tessa_events_created_sequence_idx;
CREATE INDEX tessa_events_created_at_idx ON tessa_events (created_at);

ALTER TABLE tessa_messages DROP CONSTRAINT tessa_messages_result_run_fk;
ALTER TABLE tessa_messages ADD CONSTRAINT tessa_messages_result_run_fk
    FOREIGN KEY (run_id, thread_id, client_id)
    REFERENCES tessa_runs(id, thread_id, client_id) ON DELETE RESTRICT;

ALTER TABLE tessa_runs DROP CONSTRAINT tessa_runs_trigger_fk;
ALTER TABLE tessa_runs ADD CONSTRAINT tessa_runs_trigger_fk
    FOREIGN KEY (trigger_message_id, thread_id, client_id)
    REFERENCES tessa_messages(id, thread_id, client_id) ON DELETE RESTRICT;
