-- migrate:up
CREATE TABLE marketplace_provider_documents (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    handle_slug text NOT NULL,
    business_name text NOT NULL,
    headline text NOT NULL DEFAULT '',
    category_id uuid NOT NULL REFERENCES marketplace_categories(id) ON DELETE RESTRICT,
    category_name text NOT NULL,
    avatar_url text NOT NULL DEFAULT '',
    hero_image_url text NOT NULL DEFAULT '',
    verified boolean NOT NULL DEFAULT false,
    review_rating double precision NOT NULL DEFAULT 0,
    review_count integer NOT NULL DEFAULT 0,
    completed_bookings integer NOT NULL DEFAULT 0,
    location_visibility text NOT NULL,
    timezone text NOT NULL,
    public_location_label text NOT NULL DEFAULT '',
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            COALESCE(business_name, '') || ' ' ||
            COALESCE(headline, '') || ' ' ||
            COALESCE(category_name, '')
        )
    ) STORED,
    document_revision bigint NOT NULL,
    source_updated_at timestamptz NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_provider_documents_visibility_check
        CHECK (location_visibility IN ('approximate', 'exact')),
    CONSTRAINT marketplace_provider_documents_counts_check
        CHECK (review_count >= 0 AND completed_bookings >= 0),
    CONSTRAINT marketplace_provider_documents_rating_check
        CHECK (review_rating >= 0 AND review_rating <= 5)
);

CREATE UNIQUE INDEX marketplace_provider_documents_handle_idx
    ON marketplace_provider_documents (handle_slug);
CREATE INDEX marketplace_provider_documents_category_client_idx
    ON marketplace_provider_documents (category_id, client_id);
CREATE INDEX marketplace_provider_documents_search_idx
    ON marketplace_provider_documents USING GIN (search_vector);

CREATE TABLE marketplace_service_documents (
    service_id uuid PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    provider_id uuid NOT NULL REFERENCES marketplace_provider_documents(client_id) ON DELETE CASCADE,
    slug text NOT NULL,
    title text NOT NULL,
    description text NOT NULL DEFAULT '',
    image_url text NOT NULL DEFAULT '',
    duration_minutes integer NOT NULL,
    price_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    fulfillment_mode text NOT NULL,
    provider_location_id uuid REFERENCES business_locations(id) ON DELETE CASCADE,
    location_label text NOT NULL,
    state_region_id uuid REFERENCES administrative_regions(id) ON DELETE SET NULL,
    lga_region_id uuid REFERENCES administrative_regions(id) ON DELETE SET NULL,
    internal_geog geography(Point,4326),
    public_geog geography(Point,4326),
    public_latitude numeric(9,6),
    public_longitude numeric(9,6),
    map_precision text NOT NULL,
    max_travel_distance_meters integer,
    sort_order integer NOT NULL DEFAULT 0,
    search_vector tsvector GENERATED ALWAYS AS (
        to_tsvector('simple',
            COALESCE(title, '') || ' ' || COALESCE(description, '')
        )
    ) STORED,
    document_revision bigint NOT NULL,
    source_updated_at timestamptz NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_service_documents_fulfillment_check
        CHECK (fulfillment_mode IN ('provider_location', 'customer_location', 'virtual')),
    CONSTRAINT marketplace_service_documents_map_precision_check
        CHECK (map_precision IN ('exact', 'approximate', 'none')),
    CONSTRAINT marketplace_service_documents_price_duration_check
        CHECK (price_amount_minor >= 0 AND duration_minutes > 0),
    CONSTRAINT marketplace_service_documents_map_shape_check CHECK (
        (map_precision = 'none' AND public_geog IS NULL AND public_latitude IS NULL AND public_longitude IS NULL) OR
        (map_precision IN ('exact', 'approximate') AND public_geog IS NOT NULL AND public_latitude IS NOT NULL AND public_longitude IS NOT NULL)
    )
);

CREATE INDEX marketplace_service_documents_provider_sort_idx
    ON marketplace_service_documents (provider_id, sort_order, service_id);
CREATE INDEX marketplace_service_documents_state_provider_idx
    ON marketplace_service_documents (state_region_id, provider_id)
    WHERE state_region_id IS NOT NULL;
CREATE INDEX marketplace_service_documents_lga_provider_idx
    ON marketplace_service_documents (lga_region_id, provider_id)
    WHERE lga_region_id IS NOT NULL;
CREATE INDEX marketplace_service_documents_price_provider_idx
    ON marketplace_service_documents (price_amount_minor, provider_id);
CREATE INDEX marketplace_service_documents_fulfillment_provider_idx
    ON marketplace_service_documents (fulfillment_mode, provider_id);
CREATE INDEX marketplace_service_documents_public_geog_idx
    ON marketplace_service_documents USING GIST (public_geog)
    WHERE public_geog IS NOT NULL;
CREATE INDEX marketplace_service_documents_internal_geog_idx
    ON marketplace_service_documents USING GIST (internal_geog)
    WHERE internal_geog IS NOT NULL;
CREATE INDEX marketplace_service_documents_search_idx
    ON marketplace_service_documents USING GIN (search_vector);

CREATE TABLE marketplace_service_availability_days (
    service_id uuid NOT NULL REFERENCES marketplace_service_documents(service_id) ON DELETE CASCADE,
    provider_id uuid NOT NULL REFERENCES marketplace_provider_documents(client_id) ON DELETE CASCADE,
    local_date date NOT NULL,
    has_available_slot boolean NOT NULL,
    first_available_at timestamptz,
    document_revision bigint NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (service_id, local_date),
    CONSTRAINT marketplace_service_availability_days_slot_shape_check CHECK (
        has_available_slot = (first_available_at IS NOT NULL)
    )
);

CREATE INDEX marketplace_service_availability_days_available_idx
    ON marketplace_service_availability_days (local_date, provider_id, first_available_at, service_id)
    WHERE has_available_slot;
CREATE INDEX marketplace_service_availability_days_provider_date_idx
    ON marketplace_service_availability_days (provider_id, local_date, first_available_at);

CREATE TABLE marketplace_discovery_counts (
    dimension text NOT NULL,
    dimension_id uuid NOT NULL,
    provider_count integer NOT NULL,
    refreshed_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (dimension, dimension_id),
    CONSTRAINT marketplace_discovery_counts_dimension_check
        CHECK (dimension IN ('category', 'state', 'lga')),
    CONSTRAINT marketplace_discovery_counts_value_check CHECK (provider_count >= 0)
);

CREATE TABLE marketplace_discovery_jobs (
    client_id uuid PRIMARY KEY,
    requested_revision bigint NOT NULL DEFAULT 1,
    applied_revision bigint NOT NULL DEFAULT 0,
    refresh_counts boolean NOT NULL DEFAULT false,
    refresh_services boolean NOT NULL DEFAULT true,
    refresh_availability boolean NOT NULL DEFAULT true,
    availability_from date,
    availability_to date,
    available_at timestamptz NOT NULL DEFAULT NOW(),
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_discovery_jobs_revision_check
        CHECK (requested_revision > 0 AND applied_revision >= 0 AND applied_revision <= requested_revision),
    CONSTRAINT marketplace_discovery_jobs_availability_range_check CHECK (
        (availability_from IS NULL AND availability_to IS NULL) OR
        (refresh_availability AND availability_from IS NOT NULL AND availability_to >= availability_from)
    )
);

CREATE INDEX marketplace_discovery_jobs_available_idx
    ON marketplace_discovery_jobs (available_at, updated_at, client_id)
    WHERE requested_revision > applied_revision;

CREATE FUNCTION enqueue_marketplace_discovery_provider(
    target_client_id uuid,
    counts_changed boolean DEFAULT false,
    services_changed boolean DEFAULT false,
    availability_changed boolean DEFAULT false,
    changed_from date DEFAULT NULL,
    changed_to date DEFAULT NULL
) RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF target_client_id IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO marketplace_discovery_jobs (
        client_id, requested_revision, refresh_counts, refresh_services, refresh_availability,
        availability_from, availability_to
    ) VALUES (
        target_client_id, 1, counts_changed, services_changed, availability_changed,
        changed_from, changed_to
    )
    ON CONFLICT (client_id) DO UPDATE
    SET requested_revision = marketplace_discovery_jobs.requested_revision + 1,
        refresh_counts = marketplace_discovery_jobs.refresh_counts OR EXCLUDED.refresh_counts,
        refresh_services = marketplace_discovery_jobs.refresh_services OR EXCLUDED.refresh_services,
        refresh_availability = marketplace_discovery_jobs.refresh_availability OR EXCLUDED.refresh_availability,
        availability_from = CASE
            WHEN NOT (marketplace_discovery_jobs.refresh_availability OR EXCLUDED.refresh_availability) THEN NULL
            WHEN (marketplace_discovery_jobs.refresh_availability AND marketplace_discovery_jobs.availability_from IS NULL)
              OR (EXCLUDED.refresh_availability AND EXCLUDED.availability_from IS NULL) THEN NULL
            ELSE LEAST(marketplace_discovery_jobs.availability_from, EXCLUDED.availability_from)
        END,
        availability_to = CASE
            WHEN NOT (marketplace_discovery_jobs.refresh_availability OR EXCLUDED.refresh_availability) THEN NULL
            WHEN (marketplace_discovery_jobs.refresh_availability AND marketplace_discovery_jobs.availability_to IS NULL)
              OR (EXCLUDED.refresh_availability AND EXCLUDED.availability_to IS NULL) THEN NULL
            ELSE GREATEST(marketplace_discovery_jobs.availability_to, EXCLUDED.availability_to)
        END,
        available_at = NOW(),
        updated_at = NOW();

    PERFORM pg_notify('tellbook_worker_core', 'marketplace_discovery');
END;
$$;

CREATE FUNCTION enqueue_marketplace_discovery_direct() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_client_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_client_id := OLD.client_id;
    ELSE
        target_client_id := NEW.client_id;
    END IF;
    PERFORM enqueue_marketplace_discovery_provider(
        target_client_id,
        TG_ARGV[0]::boolean,
        TG_ARGV[1]::boolean,
        TG_ARGV[2]::boolean,
        NULL,
        NULL
    );
    IF TG_OP = 'UPDATE' AND OLD.client_id <> NEW.client_id THEN
        PERFORM enqueue_marketplace_discovery_provider(
            OLD.client_id,
            TG_ARGV[0]::boolean,
            TG_ARGV[1]::boolean,
            TG_ARGV[2]::boolean,
            NULL,
            NULL
        );
    END IF;

    RETURN NULL;
END;
$$;

CREATE FUNCTION enqueue_marketplace_discovery_service_window() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_service_id uuid;
    target_client_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_service_id := OLD.service_id;
    ELSE
        target_service_id := NEW.service_id;
    END IF;
    SELECT client_id INTO target_client_id FROM services WHERE id = target_service_id;
    PERFORM enqueue_marketplace_discovery_provider(target_client_id, true, true, true, NULL, NULL);
    RETURN NULL;
END;
$$;

CREATE FUNCTION enqueue_marketplace_discovery_category() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO marketplace_discovery_jobs (
        client_id, requested_revision, refresh_counts, refresh_services, refresh_availability
    )
    SELECT profile.client_id, 1, true, true, true
    FROM client_profiles profile
    WHERE profile.marketplace_category_id = COALESCE(NEW.id, OLD.id)
    ON CONFLICT (client_id) DO UPDATE
    SET requested_revision = marketplace_discovery_jobs.requested_revision + 1,
        refresh_counts = true,
        refresh_services = true,
        refresh_availability = true,
        availability_from = NULL,
        availability_to = NULL,
        available_at = NOW(),
        updated_at = NOW();
    PERFORM pg_notify('tellbook_worker_core', 'marketplace_discovery');
    RETURN NULL;
END;
$$;

CREATE FUNCTION enqueue_marketplace_discovery_booking() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_client_id uuid;
    provider_timezone text;
    changed_from date;
    changed_to date;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_client_id := OLD.client_id;
    ELSE
        target_client_id := NEW.client_id;
    END IF;
    SELECT timezone INTO provider_timezone FROM client_profiles WHERE client_id = target_client_id;
    provider_timezone := COALESCE(NULLIF(provider_timezone, ''), 'Africa/Lagos');

    IF TG_OP = 'INSERT' THEN
        changed_from := (NEW.start_at AT TIME ZONE provider_timezone)::date;
        changed_to := changed_from;
    ELSIF TG_OP = 'DELETE' THEN
        changed_from := (OLD.start_at AT TIME ZONE provider_timezone)::date;
        changed_to := changed_from;
    ELSE
        changed_from := LEAST(
            (OLD.start_at AT TIME ZONE provider_timezone)::date,
            (NEW.start_at AT TIME ZONE provider_timezone)::date
        );
        changed_to := GREATEST(
            (OLD.start_at AT TIME ZONE provider_timezone)::date,
            (NEW.start_at AT TIME ZONE provider_timezone)::date
        );
    END IF;

    PERFORM enqueue_marketplace_discovery_provider(
        target_client_id, false, false, true, changed_from, changed_to
    );
    IF TG_OP = 'UPDATE' AND OLD.client_id <> NEW.client_id THEN
        SELECT timezone INTO provider_timezone FROM client_profiles WHERE client_id = OLD.client_id;
        provider_timezone := COALESCE(NULLIF(provider_timezone, ''), 'Africa/Lagos');
        changed_from := (OLD.start_at AT TIME ZONE provider_timezone)::date;
        changed_to := changed_from;
        PERFORM enqueue_marketplace_discovery_provider(
            OLD.client_id, false, false, true, changed_from, changed_to
        );
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER client_profiles_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON client_profiles
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

CREATE TRIGGER services_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON services
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

CREATE TRIGGER business_locations_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON business_locations
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

CREATE TRIGGER provider_availability_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_availability_windows
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

CREATE TRIGGER service_availability_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON service_availability_windows
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_service_window();

CREATE TRIGGER provider_reviews_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('false', 'false', 'false');

CREATE TRIGGER bookings_marketplace_discovery_insert_delete_trigger
AFTER INSERT OR DELETE ON bookings
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_booking();

CREATE TRIGGER bookings_marketplace_discovery_update_trigger
AFTER UPDATE OF client_id, service_id, status, start_at, end_at, occupied_start_at, occupied_end_at ON bookings
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_booking();

CREATE TRIGGER marketplace_categories_discovery_trigger
AFTER UPDATE ON marketplace_categories
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_category();

INSERT INTO marketplace_discovery_jobs (
    client_id, refresh_counts, refresh_services, refresh_availability
)
SELECT client_id, true, true, true FROM client_profiles
ON CONFLICT (client_id) DO NOTHING;

-- migrate:down
DROP TRIGGER IF EXISTS marketplace_categories_discovery_trigger ON marketplace_categories;
DROP TRIGGER IF EXISTS bookings_marketplace_discovery_update_trigger ON bookings;
DROP TRIGGER IF EXISTS bookings_marketplace_discovery_insert_delete_trigger ON bookings;
DROP TRIGGER IF EXISTS provider_reviews_marketplace_discovery_trigger ON provider_reviews;
DROP TRIGGER IF EXISTS service_availability_marketplace_discovery_trigger ON service_availability_windows;
DROP TRIGGER IF EXISTS provider_availability_marketplace_discovery_trigger ON provider_availability_windows;
DROP TRIGGER IF EXISTS business_locations_marketplace_discovery_trigger ON business_locations;
DROP TRIGGER IF EXISTS services_marketplace_discovery_trigger ON services;
DROP TRIGGER IF EXISTS client_profiles_marketplace_discovery_trigger ON client_profiles;
DROP FUNCTION IF EXISTS enqueue_marketplace_discovery_category();
DROP FUNCTION IF EXISTS enqueue_marketplace_discovery_booking();
DROP FUNCTION IF EXISTS enqueue_marketplace_discovery_service_window();
DROP FUNCTION IF EXISTS enqueue_marketplace_discovery_direct();
DROP FUNCTION IF EXISTS enqueue_marketplace_discovery_provider(uuid, boolean, boolean, boolean, date, date);
DROP TABLE IF EXISTS marketplace_discovery_jobs;
DROP TABLE IF EXISTS marketplace_discovery_counts;
DROP TABLE IF EXISTS marketplace_service_availability_days;
DROP TABLE IF EXISTS marketplace_service_documents;
DROP TABLE IF EXISTS marketplace_provider_documents;
