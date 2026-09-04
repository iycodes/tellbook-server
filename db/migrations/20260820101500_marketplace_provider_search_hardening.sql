-- migrate:up
CREATE INDEX IF NOT EXISTS services_marketplace_travel_extent_idx
    ON public.services (max_travel_distance_meters DESC)
    WHERE status = 'published' AND is_active AND NOT is_hidden
      AND fulfillment_mode = 'customer_location'
      AND max_travel_distance_meters IS NOT NULL;

-- migrate:down
DROP INDEX IF EXISTS public.services_marketplace_travel_extent_idx;
