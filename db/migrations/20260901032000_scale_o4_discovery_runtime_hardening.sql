-- migrate:up
ALTER TABLE marketplace_discovery_jobs
    ADD COLUMN lease_token uuid,
    ADD COLUMN lease_expires_at timestamptz;

ALTER TABLE marketplace_service_availability_days
    ADD COLUMN refresh_after timestamptz NOT NULL DEFAULT NOW();

UPDATE marketplace_service_availability_days day
SET refresh_after = CASE
    WHEN day.first_available_at IS NULL THEN 'infinity'::timestamptz
    ELSE GREATEST(
        NOW(),
        day.first_available_at - make_interval(mins => service.minimum_notice_minutes) + INTERVAL '1 second'
    )
END
FROM services service
WHERE service.id = day.service_id;

CREATE INDEX marketplace_service_availability_days_provider_refresh_idx
    ON marketplace_service_availability_days (provider_id, refresh_after, local_date);

ALTER TABLE marketplace_provider_documents
    ADD COLUMN availability_refresh_after timestamptz,
    ADD COLUMN availability_refresh_date date;

UPDATE marketplace_provider_documents
SET (availability_refresh_after, availability_refresh_date) = (
    SELECT day.refresh_after, day.local_date
    FROM marketplace_service_availability_days day
    WHERE day.provider_id = marketplace_provider_documents.client_id
    ORDER BY day.refresh_after, day.local_date, day.service_id
    LIMIT 1
);

CREATE INDEX marketplace_provider_documents_availability_refresh_idx
    ON marketplace_provider_documents (availability_refresh_after, client_id)
    WHERE availability_refresh_after IS NOT NULL;

CREATE TABLE marketplace_discovery_count_jobs (
    singleton boolean PRIMARY KEY DEFAULT true,
    requested_revision bigint NOT NULL DEFAULT 1,
    applied_revision bigint NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_discovery_count_jobs_singleton_check CHECK (singleton),
    CONSTRAINT marketplace_discovery_count_jobs_revision_check CHECK (
        requested_revision > 0 AND applied_revision >= 0 AND applied_revision <= requested_revision
    )
);

INSERT INTO marketplace_discovery_count_jobs (singleton) VALUES (true);

-- migrate:down
DROP TABLE IF EXISTS marketplace_discovery_count_jobs;
DROP INDEX IF EXISTS marketplace_provider_documents_availability_refresh_idx;
ALTER TABLE marketplace_provider_documents
    DROP COLUMN IF EXISTS availability_refresh_date,
    DROP COLUMN IF EXISTS availability_refresh_after;
DROP INDEX IF EXISTS marketplace_service_availability_days_provider_refresh_idx;
ALTER TABLE marketplace_service_availability_days DROP COLUMN IF EXISTS refresh_after;
ALTER TABLE marketplace_discovery_jobs
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS lease_token;
