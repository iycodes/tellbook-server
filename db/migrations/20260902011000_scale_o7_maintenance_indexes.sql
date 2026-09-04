-- migrate:up
CREATE INDEX booking_quotes_expired_unconsumed_idx
ON booking_quotes (expires_at, id)
WHERE consumed_at IS NULL AND booking_id IS NULL;

CREATE INDEX booking_change_quotes_expired_unconsumed_idx
ON booking_change_quotes (expires_at, id)
WHERE consumed_at IS NULL;

CREATE INDEX marketplace_auth_challenges_cleanup_idx
ON marketplace_auth_challenges (expires_at, id);

ALTER TABLE resolved_locations SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_threshold = 500
);
ALTER TABLE marketplace_auth_challenges SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_threshold = 500
);
ALTER TABLE marketplace_auth_sessions SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_threshold = 500
);
ALTER TABLE inbox_events SET (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_threshold = 1000,
    autovacuum_analyze_threshold = 1000
);
ALTER TABLE tessa_events SET (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_threshold = 1000,
    autovacuum_analyze_threshold = 1000
);
ALTER TABLE financial_jobs SET (
    autovacuum_vacuum_scale_factor = 0.05,
    autovacuum_analyze_scale_factor = 0.02,
    autovacuum_vacuum_threshold = 500,
    autovacuum_analyze_threshold = 500
);

-- migrate:down
ALTER TABLE financial_jobs RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
ALTER TABLE tessa_events RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
ALTER TABLE inbox_events RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
ALTER TABLE marketplace_auth_sessions RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
ALTER TABLE marketplace_auth_challenges RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
ALTER TABLE resolved_locations RESET (
    autovacuum_vacuum_scale_factor,
    autovacuum_analyze_scale_factor,
    autovacuum_vacuum_threshold,
    autovacuum_analyze_threshold
);
DROP INDEX marketplace_auth_challenges_cleanup_idx;
DROP INDEX booking_change_quotes_expired_unconsumed_idx;
DROP INDEX booking_quotes_expired_unconsumed_idx;
