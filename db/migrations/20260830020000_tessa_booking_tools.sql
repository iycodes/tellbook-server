-- migrate:up
ALTER TABLE tessa_runs DROP CONSTRAINT tessa_runs_stage_check;
ALTER TABLE tessa_runs ADD CONSTRAINT tessa_runs_stage_check CHECK (stage IN (
    'queued', 'planning', 'checking_help', 'checking_business', 'checking_bookings',
    'checking_schedule', 'checking_availability', 'answering', 'completed'
));

-- migrate:down
UPDATE tessa_runs SET stage = 'checking_business'
WHERE stage IN ('checking_bookings', 'checking_schedule', 'checking_availability');
ALTER TABLE tessa_runs DROP CONSTRAINT tessa_runs_stage_check;
ALTER TABLE tessa_runs ADD CONSTRAINT tessa_runs_stage_check CHECK (stage IN (
    'queued', 'planning', 'checking_help', 'checking_business', 'answering', 'completed'
));
