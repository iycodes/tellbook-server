-- migrate:up
CREATE TABLE public_provider_resource_revisions (
    client_id uuid PRIMARY KEY REFERENCES clients(id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT public_provider_resource_revisions_revision_check CHECK (revision > 0)
);

INSERT INTO public_provider_resource_revisions (client_id)
SELECT client_id FROM client_profiles
ON CONFLICT (client_id) DO NOTHING;

CREATE FUNCTION bump_public_provider_resource_revision(target_client_id uuid) RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF target_client_id IS NULL THEN
        RETURN;
    END IF;

    -- Child rows are deleted by cascade after their parent client is already
    -- invisible to this transaction. Do not recreate a revision for a client
    -- that is being removed.
    IF NOT EXISTS (SELECT 1 FROM clients WHERE id = target_client_id) THEN
        RETURN;
    END IF;

    INSERT INTO public_provider_resource_revisions (client_id, revision, updated_at)
    VALUES (target_client_id, 1, NOW())
    ON CONFLICT (client_id) DO UPDATE
    SET revision = public_provider_resource_revisions.revision + 1,
        updated_at = NOW();
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_direct() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
        RETURN NULL;
    END IF;

    PERFORM bump_public_provider_resource_revision(NEW.client_id);
    IF TG_OP = 'UPDATE' AND OLD.client_id IS DISTINCT FROM NEW.client_id THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_service() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    new_client_id uuid;
    old_client_id uuid;
BEGIN
    IF TG_OP <> 'DELETE' THEN
        SELECT client_id INTO new_client_id FROM services WHERE id = NEW.service_id;
        PERFORM bump_public_provider_resource_revision(new_client_id);
    END IF;

    IF TG_OP = 'DELETE' OR (TG_OP = 'UPDATE' AND OLD.service_id IS DISTINCT FROM NEW.service_id) THEN
        SELECT client_id INTO old_client_id FROM services WHERE id = OLD.service_id;
        PERFORM bump_public_provider_resource_revision(old_client_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_category() RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    UPDATE public_provider_resource_revisions revision
    SET revision = revision.revision + 1,
        updated_at = NOW()
    FROM client_profiles profile
    WHERE revision.client_id = profile.client_id
      AND profile.marketplace_category_id = COALESCE(NEW.id, OLD.id);
    RETURN NULL;
END;
$$;

CREATE TRIGGER client_profiles_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON client_profiles
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
CREATE TRIGGER client_profiles_public_resource_update_trigger
AFTER UPDATE OF business_name, handle_slug, category, headline, short_bio,
    public_location_label, timezone, hero_image_url, avatar_url, verified,
    years_experience, currency_code, public_profile_about, booking_page_intro,
    country_code, locale, marketplace_enabled, marketplace_category_id
ON client_profiles
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER client_profile_handles_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON client_profile_handles
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER services_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON services
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
CREATE TRIGGER services_public_resource_update_trigger
AFTER UPDATE OF client_id, title, slug, description, category, icon_name, image_url,
    duration_minutes, price_amount_minor, sort_order, status, prep_time_minutes,
    buffer_time_minutes, availability_mode, minimum_notice_minutes,
    fulfillment_mode, cancellation_policy, lateness_policy, agreement_timing,
    is_hidden, currency_code, provider_location_id, virtual_delivery_label,
    agreement_template_family_id, standalone_signature_required
ON services
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER provider_portfolio_items_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_portfolio_items
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER provider_reviews_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER business_locations_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON business_locations
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
CREATE TRIGGER business_locations_public_resource_update_trigger
AFTER UPDATE OF client_id, formatted_address, resolution_status, is_active
ON business_locations
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER provider_availability_windows_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_availability_windows
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER service_availability_windows_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON service_availability_windows
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_service();

CREATE TRIGGER service_short_notice_rules_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON service_short_notice_rules
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_service();

CREATE TRIGGER bookings_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
CREATE TRIGGER bookings_public_resource_update_trigger
AFTER UPDATE OF client_id, customer_id, service_id, status, end_at
ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER agreement_template_families_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON agreement_template_families
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

CREATE TRIGGER marketplace_categories_public_resource_update_trigger
AFTER UPDATE OF name ON marketplace_categories
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_category();

-- migrate:down
DROP TRIGGER IF EXISTS marketplace_categories_public_resource_update_trigger ON marketplace_categories;
DROP TRIGGER IF EXISTS agreement_template_families_public_resource_trigger ON agreement_template_families;
DROP TRIGGER IF EXISTS bookings_public_resource_update_trigger ON bookings;
DROP TRIGGER IF EXISTS bookings_public_resource_insert_delete_trigger ON bookings;
DROP TRIGGER IF EXISTS service_short_notice_rules_public_resource_trigger ON service_short_notice_rules;
DROP TRIGGER IF EXISTS service_availability_windows_public_resource_trigger ON service_availability_windows;
DROP TRIGGER IF EXISTS provider_availability_windows_public_resource_trigger ON provider_availability_windows;
DROP TRIGGER IF EXISTS business_locations_public_resource_update_trigger ON business_locations;
DROP TRIGGER IF EXISTS business_locations_public_resource_insert_delete_trigger ON business_locations;
DROP TRIGGER IF EXISTS provider_reviews_public_resource_trigger ON provider_reviews;
DROP TRIGGER IF EXISTS provider_portfolio_items_public_resource_trigger ON provider_portfolio_items;
DROP TRIGGER IF EXISTS services_public_resource_update_trigger ON services;
DROP TRIGGER IF EXISTS services_public_resource_insert_delete_trigger ON services;
DROP TRIGGER IF EXISTS client_profile_handles_public_resource_trigger ON client_profile_handles;
DROP TRIGGER IF EXISTS client_profiles_public_resource_update_trigger ON client_profiles;
DROP TRIGGER IF EXISTS client_profiles_public_resource_insert_delete_trigger ON client_profiles;
DROP FUNCTION IF EXISTS bump_public_provider_resource_revision_for_category();
DROP FUNCTION IF EXISTS bump_public_provider_resource_revision_for_service();
DROP FUNCTION IF EXISTS bump_public_provider_resource_revision_direct();
DROP FUNCTION IF EXISTS bump_public_provider_resource_revision(uuid);
DROP TABLE IF EXISTS public_provider_resource_revisions;
