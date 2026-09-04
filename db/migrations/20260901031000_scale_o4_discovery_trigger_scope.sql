-- migrate:up
CREATE OR REPLACE FUNCTION enqueue_marketplace_discovery_direct() RETURNS trigger
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

DROP TRIGGER client_profiles_marketplace_discovery_trigger ON client_profiles;
CREATE TRIGGER client_profiles_marketplace_discovery_insert_delete_trigger
AFTER INSERT OR DELETE ON client_profiles
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');
CREATE TRIGGER client_profiles_marketplace_discovery_update_trigger
AFTER UPDATE OF business_name, handle_slug, headline, public_location_label, timezone,
    hero_image_url, avatar_url, verified, country_code, currency_code,
    market_configured_at, marketplace_enabled, marketplace_category_id,
    marketplace_location_visibility, concurrent_booking_capacity
ON client_profiles
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

DROP TRIGGER services_marketplace_discovery_trigger ON services;
CREATE TRIGGER services_marketplace_discovery_insert_delete_trigger
AFTER INSERT OR DELETE ON services
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');
CREATE TRIGGER services_marketplace_discovery_update_trigger
AFTER UPDATE OF client_id, title, slug, description, image_url, duration_minutes,
    price_amount_minor, is_active, sort_order, status, prep_time_minutes,
    buffer_time_minutes, availability_mode, minimum_notice_minutes,
    max_bookings_per_day, fulfillment_mode, max_travel_distance_meters,
    is_hidden, currency_code, provider_location_id
ON services
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

DROP TRIGGER business_locations_marketplace_discovery_trigger ON business_locations;
CREATE TRIGGER business_locations_marketplace_discovery_insert_delete_trigger
AFTER INSERT OR DELETE ON business_locations
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');
CREATE TRIGGER business_locations_marketplace_discovery_update_trigger
AFTER UPDATE OF client_id, formatted_address, latitude, longitude, resolution_status,
    timezone, is_active, country_code, state_region_id, lga_region_id, locality
ON business_locations
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

DROP TRIGGER provider_reviews_marketplace_discovery_trigger ON provider_reviews;
CREATE TRIGGER provider_reviews_marketplace_discovery_insert_delete_trigger
AFTER INSERT OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('false', 'false', 'false');
CREATE TRIGGER provider_reviews_marketplace_discovery_update_trigger
AFTER UPDATE OF client_id, rating, status ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('false', 'false', 'false');

-- migrate:down
DROP TRIGGER provider_reviews_marketplace_discovery_update_trigger ON provider_reviews;
DROP TRIGGER provider_reviews_marketplace_discovery_insert_delete_trigger ON provider_reviews;
CREATE TRIGGER provider_reviews_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('false', 'false', 'false');

DROP TRIGGER business_locations_marketplace_discovery_update_trigger ON business_locations;
DROP TRIGGER business_locations_marketplace_discovery_insert_delete_trigger ON business_locations;
CREATE TRIGGER business_locations_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON business_locations
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

DROP TRIGGER services_marketplace_discovery_update_trigger ON services;
DROP TRIGGER services_marketplace_discovery_insert_delete_trigger ON services;
CREATE TRIGGER services_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON services
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');

DROP TRIGGER client_profiles_marketplace_discovery_update_trigger ON client_profiles;
DROP TRIGGER client_profiles_marketplace_discovery_insert_delete_trigger ON client_profiles;
CREATE TRIGGER client_profiles_marketplace_discovery_trigger
AFTER INSERT OR UPDATE OR DELETE ON client_profiles
FOR EACH ROW EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true', 'true', 'true');
