-- migrate:up
CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE public.marketplace_categories (
    id uuid PRIMARY KEY,
    slug text NOT NULL UNIQUE,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    image_url text NOT NULL DEFAULT '',
    image_alt text NOT NULL DEFAULT '',
    sort_order integer NOT NULL DEFAULT 0,
    is_active boolean NOT NULL DEFAULT TRUE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT marketplace_categories_slug_check CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    CONSTRAINT marketplace_categories_name_check CHECK (btrim(name) <> '')
);

INSERT INTO public.marketplace_categories (
    id, slug, name, description, image_url, image_alt, sort_order
) VALUES
    ('10000000-0000-4000-8000-000000000001', 'beauty', 'Beauty',
     'Hair, nails, makeup + more',
     'https://images.unsplash.com/photo-1522337360788-8b13dee7a37e?auto=format&fit=crop&w=900&q=86',
     'Beauty professional styling a client', 10),
    ('10000000-0000-4000-8000-000000000002', 'home', 'Home',
     'Cleaning, repairs, moving',
     'https://images.unsplash.com/photo-1503387762-592deb58ef4e?auto=format&fit=crop&w=900&q=86',
     'Home improvement professional at work', 20),
    ('10000000-0000-4000-8000-000000000003', 'events', 'Events',
     'Planners, decorators, catering',
     'https://images.unsplash.com/photo-1507501336603-6e31db2be093?auto=format&fit=crop&w=900&q=86',
     'Elegant outdoor event table setting', 30),
    ('10000000-0000-4000-8000-000000000004', 'creative', 'Creative',
     'Photo, video, design',
     'https://images.unsplash.com/photo-1452780212940-6f5c0d14d848?auto=format&fit=crop&w=900&q=86',
     'Professional photographer holding a camera', 40);

CREATE TABLE public.administrative_regions (
    id uuid PRIMARY KEY,
    parent_id uuid REFERENCES public.administrative_regions(id) ON DELETE RESTRICT,
    country_code text NOT NULL,
    level text NOT NULL,
    code text NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    boundary geometry(MultiPolygon, 4326),
    source text NOT NULL DEFAULT '',
    source_version text NOT NULL DEFAULT '',
    source_feature_id text NOT NULL DEFAULT '',
    source_license text NOT NULL DEFAULT '',
    is_active boolean NOT NULL DEFAULT TRUE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT administrative_regions_country_code_check CHECK (country_code ~ '^[A-Z]{2}$'),
    CONSTRAINT administrative_regions_level_check CHECK (level IN ('country', 'state', 'lga')),
    CONSTRAINT administrative_regions_code_check CHECK (code ~ '^[A-Z0-9]+(?:-[A-Z0-9]+)*$'),
    CONSTRAINT administrative_regions_slug_check CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    CONSTRAINT administrative_regions_name_check CHECK (btrim(name) <> ''),
    CONSTRAINT administrative_regions_parent_check CHECK (
        (level = 'country' AND parent_id IS NULL AND boundary IS NULL)
        OR (level IN ('state', 'lga') AND parent_id IS NOT NULL AND boundary IS NOT NULL)
    ),
    UNIQUE (country_code, code),
    UNIQUE (parent_id, slug)
);

CREATE INDEX administrative_regions_parent_list_idx
    ON public.administrative_regions (parent_id, level, is_active, name);
CREATE INDEX administrative_regions_boundary_gist_idx
    ON public.administrative_regions USING gist (boundary)
    WHERE boundary IS NOT NULL AND is_active;

ALTER TABLE public.client_profiles
    ADD COLUMN marketplace_enabled boolean NOT NULL DEFAULT FALSE,
    ADD COLUMN marketplace_category_id uuid REFERENCES public.marketplace_categories(id) ON DELETE SET NULL,
    ADD COLUMN marketplace_location_visibility text NOT NULL DEFAULT 'approximate',
    ADD CONSTRAINT client_profiles_marketplace_location_visibility_check
        CHECK (marketplace_location_visibility IN ('approximate', 'exact'));

UPDATE public.client_profiles cp
SET marketplace_category_id = category.id
FROM public.marketplace_categories category
WHERE category.slug = CASE
    WHEN lower(cp.category) ~ '(beauty|hair|salon|barber|makeup|nail|spa|wellness)' THEN 'beauty'
    WHEN lower(cp.category) ~ '(home|clean|repair|moving|plumb|electric)' THEN 'home'
    WHEN lower(cp.category) ~ '(event|cater|decor|wedding|party)' THEN 'events'
    WHEN lower(cp.category) ~ '(photo|video|creative|design|brand)' THEN 'creative'
    ELSE ''
END;

CREATE INDEX client_profiles_marketplace_discovery_idx
    ON public.client_profiles (marketplace_category_id, client_id)
    WHERE marketplace_enabled;

ALTER TABLE public.business_locations
    ADD COLUMN country_code text,
    ADD COLUMN state_region_id uuid REFERENCES public.administrative_regions(id) ON DELETE SET NULL,
    ADD COLUMN lga_region_id uuid REFERENCES public.administrative_regions(id) ON DELETE SET NULL,
    ADD COLUMN locality text NOT NULL DEFAULT '',
    ADD COLUMN geog geography(Point, 4326) GENERATED ALWAYS AS (
        CASE
            WHEN latitude IS NULL OR longitude IS NULL THEN NULL
            ELSE ST_SetSRID(ST_MakePoint(longitude::double precision, latitude::double precision), 4326)::geography
        END
    ) STORED,
    ADD CONSTRAINT business_locations_country_code_check
        CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$');

CREATE INDEX business_locations_geog_gist_idx
    ON public.business_locations USING gist (geog)
    WHERE geog IS NOT NULL AND is_active;
CREATE INDEX business_locations_region_discovery_idx
    ON public.business_locations (state_region_id, lga_region_id, client_id)
    WHERE is_active;

ALTER TABLE public.resolved_locations
    ADD COLUMN country_code text,
    ADD COLUMN state_region_id uuid REFERENCES public.administrative_regions(id) ON DELETE SET NULL,
    ADD COLUMN lga_region_id uuid REFERENCES public.administrative_regions(id) ON DELETE SET NULL,
    ADD COLUMN locality text NOT NULL DEFAULT '',
    ADD COLUMN geog geography(Point, 4326) GENERATED ALWAYS AS (
        CASE
            WHEN latitude IS NULL OR longitude IS NULL THEN NULL
            ELSE ST_SetSRID(ST_MakePoint(longitude::double precision, latitude::double precision), 4326)::geography
        END
    ) STORED,
    ADD CONSTRAINT resolved_locations_country_code_check
        CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$');

CREATE INDEX resolved_locations_geog_gist_idx
    ON public.resolved_locations USING gist (geog)
    WHERE geog IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS public.resolved_locations_geog_gist_idx;
ALTER TABLE public.resolved_locations
    DROP CONSTRAINT IF EXISTS resolved_locations_country_code_check,
    DROP COLUMN IF EXISTS geog,
    DROP COLUMN IF EXISTS locality,
    DROP COLUMN IF EXISTS lga_region_id,
    DROP COLUMN IF EXISTS state_region_id,
    DROP COLUMN IF EXISTS country_code;

DROP INDEX IF EXISTS public.business_locations_region_discovery_idx;
DROP INDEX IF EXISTS public.business_locations_geog_gist_idx;
ALTER TABLE public.business_locations
    DROP CONSTRAINT IF EXISTS business_locations_country_code_check,
    DROP COLUMN IF EXISTS geog,
    DROP COLUMN IF EXISTS locality,
    DROP COLUMN IF EXISTS lga_region_id,
    DROP COLUMN IF EXISTS state_region_id,
    DROP COLUMN IF EXISTS country_code;

DROP INDEX IF EXISTS public.client_profiles_marketplace_discovery_idx;
ALTER TABLE public.client_profiles
    DROP CONSTRAINT IF EXISTS client_profiles_marketplace_location_visibility_check,
    DROP COLUMN IF EXISTS marketplace_location_visibility,
    DROP COLUMN IF EXISTS marketplace_category_id,
    DROP COLUMN IF EXISTS marketplace_enabled;

DROP TABLE IF EXISTS public.administrative_regions;
DROP TABLE IF EXISTS public.marketplace_categories;
