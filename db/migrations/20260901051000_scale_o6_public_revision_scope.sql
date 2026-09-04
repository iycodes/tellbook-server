-- migrate:up
CREATE OR REPLACE FUNCTION bump_public_provider_resource_revision_for_service() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
	    new_client_id uuid;
	    old_client_id uuid;
BEGIN
	    IF TG_OP <> 'DELETE' THEN
	        SELECT client_id INTO new_client_id
	        FROM services
	        WHERE id = NEW.service_id
	          AND status IN ('published', 'paused')
	          AND COALESCE(is_hidden, FALSE) = FALSE;
	        PERFORM bump_public_provider_resource_revision(new_client_id);
	    END IF;
	    IF TG_OP = 'DELETE' OR (TG_OP = 'UPDATE' AND OLD.service_id IS DISTINCT FROM NEW.service_id) THEN
	        SELECT client_id INTO old_client_id
	        FROM services
	        WHERE id = OLD.service_id
	          AND status IN ('published', 'paused')
	          AND COALESCE(is_hidden, FALSE) = FALSE;
	        PERFORM bump_public_provider_resource_revision(old_client_id);
	    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_visible_service() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    old_is_public boolean := FALSE;
    new_is_public boolean := FALSE;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_is_public := OLD.status IN ('published', 'paused') AND COALESCE(OLD.is_hidden, FALSE) = FALSE;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_is_public := NEW.status IN ('published', 'paused') AND COALESCE(NEW.is_hidden, FALSE) = FALSE;
    END IF;

    IF old_is_public THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
    END IF;
    IF new_is_public AND (NOT old_is_public OR TG_OP = 'INSERT' OR OLD.client_id IS DISTINCT FROM NEW.client_id) THEN
        PERFORM bump_public_provider_resource_revision(NEW.client_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_public_review() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    old_is_public boolean := FALSE;
    new_is_public boolean := FALSE;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_is_public := OLD.status = 'approved';
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_is_public := NEW.status = 'approved';
    END IF;

    IF old_is_public THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
    END IF;
    IF new_is_public AND (NOT old_is_public OR TG_OP = 'INSERT' OR OLD.client_id IS DISTINCT FROM NEW.client_id) THEN
        PERFORM bump_public_provider_resource_revision(NEW.client_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_completed_booking() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    old_is_public boolean := FALSE;
    new_is_public boolean := FALSE;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_is_public := OLD.status = 'completed';
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_is_public := NEW.status = 'completed';
    END IF;

    IF old_is_public THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
    END IF;
    IF new_is_public AND (NOT old_is_public OR TG_OP = 'INSERT' OR OLD.client_id IS DISTINCT FROM NEW.client_id) THEN
        PERFORM bump_public_provider_resource_revision(NEW.client_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION bump_public_provider_resource_revision_for_published_agreement() RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    old_is_public boolean := FALSE;
    new_is_public boolean := FALSE;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        old_is_public := OLD.status = 'published' AND OLD.client_id IS NOT NULL;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_is_public := NEW.status = 'published' AND NEW.client_id IS NOT NULL;
    END IF;

    IF old_is_public THEN
        PERFORM bump_public_provider_resource_revision(OLD.client_id);
    END IF;
    IF new_is_public AND (NOT old_is_public OR TG_OP = 'INSERT' OR OLD.client_id IS DISTINCT FROM NEW.client_id) THEN
        PERFORM bump_public_provider_resource_revision(NEW.client_id);
    END IF;
    RETURN NULL;
END;
$$;

DROP TRIGGER services_public_resource_insert_delete_trigger ON services;
DROP TRIGGER services_public_resource_update_trigger ON services;
CREATE TRIGGER services_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON services
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_visible_service();
CREATE TRIGGER services_public_resource_update_trigger
AFTER UPDATE OF client_id, title, slug, description, category, icon_name, image_url,
    duration_minutes, price_amount_minor, sort_order, status, prep_time_minutes,
    buffer_time_minutes, availability_mode, minimum_notice_minutes,
    fulfillment_mode, cancellation_policy, lateness_policy, agreement_timing,
    is_hidden, currency_code, provider_location_id, virtual_delivery_label,
    agreement_template_family_id, standalone_signature_required
ON services
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_visible_service();

DROP TRIGGER provider_reviews_public_resource_trigger ON provider_reviews;
CREATE TRIGGER provider_reviews_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_public_review();
CREATE TRIGGER provider_reviews_public_resource_update_trigger
AFTER UPDATE OF client_id, customer_id, author_name, rating, review_text, image_url,
    booking_id, service_id, status, created_at ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_public_review();

DROP TRIGGER bookings_public_resource_insert_delete_trigger ON bookings;
DROP TRIGGER bookings_public_resource_update_trigger ON bookings;
CREATE TRIGGER bookings_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_completed_booking();
CREATE TRIGGER bookings_public_resource_update_trigger
AFTER UPDATE OF client_id, customer_id, service_id, status ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_completed_booking();

DROP TRIGGER agreement_template_families_public_resource_trigger ON agreement_template_families;
CREATE TRIGGER agreement_template_families_public_resource_trigger
AFTER INSERT OR DELETE OR UPDATE OF client_id, title, confirmation_method, status
ON agreement_template_families
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_for_published_agreement();

-- migrate:down
DROP TRIGGER agreement_template_families_public_resource_trigger ON agreement_template_families;
CREATE TRIGGER agreement_template_families_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON agreement_template_families
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

DROP TRIGGER bookings_public_resource_update_trigger ON bookings;
DROP TRIGGER bookings_public_resource_insert_delete_trigger ON bookings;
CREATE TRIGGER bookings_public_resource_insert_delete_trigger
AFTER INSERT OR DELETE ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
CREATE TRIGGER bookings_public_resource_update_trigger
AFTER UPDATE OF client_id, customer_id, service_id, status, end_at ON bookings
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

DROP TRIGGER IF EXISTS provider_reviews_public_resource_update_trigger ON provider_reviews;
DROP TRIGGER IF EXISTS provider_reviews_public_resource_insert_delete_trigger ON provider_reviews;
DROP TRIGGER IF EXISTS provider_reviews_public_resource_trigger ON provider_reviews;
CREATE TRIGGER provider_reviews_public_resource_trigger
AFTER INSERT OR UPDATE OR DELETE ON provider_reviews
FOR EACH ROW EXECUTE FUNCTION bump_public_provider_resource_revision_direct();

DROP TRIGGER services_public_resource_update_trigger ON services;
DROP TRIGGER services_public_resource_insert_delete_trigger ON services;
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

CREATE OR REPLACE FUNCTION bump_public_provider_resource_revision_for_service() RETURNS trigger
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

DROP FUNCTION bump_public_provider_resource_revision_for_published_agreement();
DROP FUNCTION bump_public_provider_resource_revision_for_completed_booking();
DROP FUNCTION bump_public_provider_resource_revision_for_public_review();
DROP FUNCTION bump_public_provider_resource_revision_for_visible_service();
