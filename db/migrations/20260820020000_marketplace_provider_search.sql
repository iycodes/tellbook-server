-- migrate:up
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE INDEX client_profiles_marketplace_business_name_trgm_idx
    ON public.client_profiles USING gin (lower(business_name) public.gin_trgm_ops)
    WHERE marketplace_enabled;

CREATE INDEX client_profiles_marketplace_headline_trgm_idx
    ON public.client_profiles USING gin (lower(headline) public.gin_trgm_ops)
    WHERE marketplace_enabled;

CREATE INDEX services_marketplace_title_trgm_idx
    ON public.services USING gin (lower(title) public.gin_trgm_ops)
    WHERE status = 'published' AND is_active AND NOT is_hidden;

CREATE INDEX services_marketplace_description_trgm_idx
    ON public.services USING gin (lower(description) public.gin_trgm_ops)
    WHERE status = 'published' AND is_active AND NOT is_hidden;

CREATE INDEX services_marketplace_discovery_idx
    ON public.services (client_id, price_amount_minor, sort_order, id)
    WHERE status = 'published' AND is_active AND NOT is_hidden;

CREATE INDEX bookings_marketplace_availability_idx
    ON public.bookings (client_id, occupied_start_at, occupied_end_at)
    WHERE status NOT IN ('cancelled', 'canceled');

-- migrate:down
DROP INDEX IF EXISTS public.bookings_marketplace_availability_idx;
DROP INDEX IF EXISTS public.services_marketplace_discovery_idx;
DROP INDEX IF EXISTS public.services_marketplace_description_trgm_idx;
DROP INDEX IF EXISTS public.services_marketplace_title_trgm_idx;
DROP INDEX IF EXISTS public.client_profiles_marketplace_headline_trgm_idx;
DROP INDEX IF EXISTS public.client_profiles_marketplace_business_name_trgm_idx;
