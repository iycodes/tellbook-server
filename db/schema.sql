SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: pg_stat_statements; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;


--
-- Name: EXTENSION pg_stat_statements; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_stat_statements IS 'track planning and execution statistics of all SQL statements executed';


--
-- Name: pg_trgm; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;


--
-- Name: EXTENSION pg_trgm; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pg_trgm IS 'text similarity measurement and index searching based on trigrams';


--
-- Name: postgis; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS postgis WITH SCHEMA public;


--
-- Name: EXTENSION postgis; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION postgis IS 'PostGIS geometry and geography spatial types and functions';


--
-- Name: append_booking_update_event(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.append_booking_update_event() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF ROW(OLD.status,OLD.payment_status,OLD.agreement_status,OLD.start_at,OLD.end_at)
       IS DISTINCT FROM
       ROW(NEW.status,NEW.payment_status,NEW.agreement_status,NEW.start_at,NEW.end_at) THEN
        INSERT INTO booking_domain_events (
            id,client_id,booking_id,event_type,dedupe_key,payload,created_at
        ) VALUES (
            gen_random_uuid(),NEW.client_id,NEW.id,'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id',NEW.id::text,
                'status',NEW.status,'previous_status',OLD.status,
                'payment_status',NEW.payment_status,'previous_payment_status',OLD.payment_status,
                'agreement_status',NEW.agreement_status,'previous_agreement_status',OLD.agreement_status,
                'starts_at',NEW.start_at,'previous_starts_at',OLD.start_at,
                'ends_at',NEW.end_at,'previous_ends_at',OLD.end_at
            ),NOW()
        );
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: bump_public_provider_resource_revision(uuid); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision(target_client_id uuid) RETURNS void
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


--
-- Name: bump_public_provider_resource_revision_direct(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_direct() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_category(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_category() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_completed_booking(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_completed_booking() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_public_review(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_public_review() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_published_agreement(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_published_agreement() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_service(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_service() RETURNS trigger
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


--
-- Name: bump_public_provider_resource_revision_for_visible_service(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.bump_public_provider_resource_revision_for_visible_service() RETURNS trigger
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


--
-- Name: create_marketplace_booking_notification(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_marketplace_booking_notification() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    customer_id uuid;
    provider_name text;
    notification_title text;
    notification_body text;
BEGIN
    SELECT booking.marketplace_customer_id, profile.business_name
    INTO customer_id, provider_name
    FROM bookings booking
    JOIN client_profiles profile ON profile.client_id = booking.client_id
    WHERE booking.id = NEW.booking_id;

    IF customer_id IS NULL THEN
        RETURN NEW;
    END IF;

    notification_title := CASE NEW.event_type
        WHEN 'booking_created' THEN 'Booking created with ' || provider_name
        WHEN 'booking_confirmed' THEN provider_name || ' confirmed your booking'
        WHEN 'booking_cancelled' THEN 'Your booking with ' || provider_name || ' was cancelled'
        WHEN 'booking_rescheduled' THEN 'Your booking with ' || provider_name || ' has a new time'
        WHEN 'booking_completed' THEN 'Your booking with ' || provider_name || ' is complete'
        WHEN 'booking_expired' THEN 'Your reservation with ' || provider_name || ' expired'
        WHEN 'booking_updated' THEN 'Your booking with ' || provider_name || ' was updated'
        ELSE 'Booking update from ' || provider_name
    END;
    notification_body := CASE NEW.event_type
        WHEN 'booking_rescheduled' THEN 'Open the booking to review the updated date and time.'
        WHEN 'booking_updated' THEN 'Open the booking to review its latest payment or agreement status.'
        ELSE 'Open the booking to see the latest details.'
    END;

    INSERT INTO marketplace_notifications (
        marketplace_customer_id, kind, event_type, event_key, title, body,
        provider_id, booking_id, created_at
    ) VALUES (
        customer_id, 'booking', NEW.event_type, 'booking_event:' || NEW.id::text,
        notification_title, notification_body, NEW.client_id, NEW.booking_id, NEW.created_at
    ) ON CONFLICT (marketplace_customer_id, event_key) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: create_marketplace_message_notification(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.create_marketplace_message_notification() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    customer_id uuid;
    provider_id uuid;
    provider_name text;
BEGIN
    IF NEW.sender_type NOT IN ('provider', 'ai') THEN
        RETURN NEW;
    END IF;

    SELECT conversation.marketplace_customer_id, conversation.client_id, profile.business_name
    INTO customer_id, provider_id, provider_name
    FROM inbox_conversations conversation
    JOIN client_profiles profile ON profile.client_id = conversation.client_id
    WHERE conversation.id = NEW.conversation_id;

    IF customer_id IS NULL THEN
        RETURN NEW;
    END IF;

    INSERT INTO marketplace_notifications (
        marketplace_customer_id, kind, event_type, event_key, title, body,
        provider_id, booking_id, conversation_id, created_at
    ) VALUES (
        customer_id, 'message', 'message.created', 'inbox_message:' || NEW.id::text,
        provider_name || ' replied', LEFT(NEW.content, 1000), provider_id,
        NEW.booking_id, NEW.conversation_id, NEW.sent_at
    ) ON CONFLICT (marketplace_customer_id, event_key) DO NOTHING;
    RETURN NEW;
END;
$$;


--
-- Name: enforce_inbox_booking_ownership(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enforce_inbox_booking_ownership() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM inbox_conversations conversation
        INNER JOIN bookings booking ON booking.id = NEW.booking_id
        WHERE conversation.id = NEW.conversation_id
          AND conversation.client_id = booking.client_id
          AND conversation.customer_id = booking.customer_id
          AND conversation.marketplace_customer_id = booking.marketplace_customer_id
    ) THEN
        RAISE EXCEPTION 'inbox conversation and booking ownership do not match'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: enqueue_marketplace_discovery_booking(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_marketplace_discovery_booking() RETURNS trigger
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


--
-- Name: enqueue_marketplace_discovery_category(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_marketplace_discovery_category() RETURNS trigger
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


--
-- Name: enqueue_marketplace_discovery_direct(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_marketplace_discovery_direct() RETURNS trigger
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


--
-- Name: enqueue_marketplace_discovery_provider(uuid, boolean, boolean, boolean, date, date); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_marketplace_discovery_provider(target_client_id uuid, counts_changed boolean DEFAULT false, services_changed boolean DEFAULT false, availability_changed boolean DEFAULT false, changed_from date DEFAULT NULL::date, changed_to date DEFAULT NULL::date) RETURNS void
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


--
-- Name: enqueue_marketplace_discovery_service_window(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_marketplace_discovery_service_window() RETURNS trigger
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


--
-- Name: enqueue_notification_event_job(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_notification_event_job() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO notification_event_jobs (booking_event_id,event_sequence)
    VALUES (NEW.id,NEW.sequence)
    ON CONFLICT (booking_event_id) DO NOTHING;
    PERFORM pg_notify('tellbook_worker_core','notification_event');
    RETURN NEW;
END;
$$;


--
-- Name: enqueue_provider_daily_metric(uuid, timestamp with time zone, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_provider_daily_metric(p_client_id uuid, p_event_at timestamp with time zone, p_currency_code text) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    provider_timezone text;
    provider_date date;
BEGIN
    IF p_client_id IS NULL OR p_event_at IS NULL OR p_currency_code IS NULL THEN
        RETURN;
    END IF;
    SELECT timezone INTO provider_timezone
    FROM client_profiles
    WHERE client_id = p_client_id;
    IF provider_timezone IS NULL THEN
        RETURN;
    END IF;
    provider_date := (p_event_at AT TIME ZONE provider_timezone)::date;
    INSERT INTO provider_daily_metric_jobs (
        client_id, metric_date, currency_code, revision, enqueued_at
    ) VALUES (
        p_client_id, provider_date, UPPER(p_currency_code), 1, NOW()
    )
    ON CONFLICT (client_id, metric_date, currency_code) DO UPDATE
    SET revision = provider_daily_metric_jobs.revision + 1,
        enqueued_at = NOW();
END;
$$;


--
-- Name: enqueue_provider_daily_metric_for_allocation(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_provider_daily_metric_for_allocation() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    payment_client_id uuid;
    payment_paid_at timestamptz;
    payment_currency_code text;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        SELECT client_id, paid_at, currency_code
        INTO payment_client_id, payment_paid_at, payment_currency_code
        FROM payments WHERE id = OLD.payment_id;
        IF payment_paid_at IS NOT NULL THEN
            PERFORM enqueue_provider_daily_metric(payment_client_id, payment_paid_at, payment_currency_code);
        END IF;
    END IF;
    IF TG_OP <> 'DELETE' THEN
        SELECT client_id, paid_at, currency_code
        INTO payment_client_id, payment_paid_at, payment_currency_code
        FROM payments WHERE id = NEW.payment_id;
        IF payment_paid_at IS NOT NULL THEN
            PERFORM enqueue_provider_daily_metric(payment_client_id, payment_paid_at, payment_currency_code);
        END IF;
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: enqueue_provider_daily_metric_for_booking(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_provider_daily_metric_for_booking() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        PERFORM enqueue_provider_daily_metric(OLD.client_id, OLD.start_at, OLD.currency_code);
    END IF;
    IF TG_OP <> 'DELETE' THEN
        PERFORM enqueue_provider_daily_metric(NEW.client_id, NEW.start_at, NEW.currency_code);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: enqueue_provider_daily_metric_for_payment(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_provider_daily_metric_for_payment() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND OLD.paid_at IS NOT NULL THEN
        PERFORM enqueue_provider_daily_metric(OLD.client_id, OLD.paid_at, OLD.currency_code);
    END IF;
    IF TG_OP <> 'DELETE' AND NEW.paid_at IS NOT NULL THEN
        PERFORM enqueue_provider_daily_metric(NEW.client_id, NEW.paid_at, NEW.currency_code);
    END IF;
    RETURN NULL;
END;
$$;


--
-- Name: enqueue_provider_daily_metrics_for_profile_timezone(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.enqueue_provider_daily_metrics_for_profile_timezone() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO provider_daily_metric_jobs (client_id, metric_date, currency_code)
    SELECT DISTINCT NEW.client_id, day_value::date, currency_value
    FROM generate_series(CURRENT_DATE - 365, CURRENT_DATE, INTERVAL '1 day') day_value
    CROSS JOIN LATERAL (
        VALUES (OLD.currency_code), (NEW.currency_code)
    ) currencies(currency_value)
    WHERE OLD.timezone IS DISTINCT FROM NEW.timezone
       OR OLD.currency_code IS DISTINCT FROM NEW.currency_code
    ON CONFLICT (client_id, metric_date, currency_code) DO UPDATE
    SET revision = provider_daily_metric_jobs.revision + 1,
        enqueued_at = NOW();
    RETURN NULL;
END;
$$;


--
-- Name: notify_ai_worker_queue(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_ai_worker_queue() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.status = 'queued' THEN
        PERFORM pg_notify('tellbook_worker_ai', TG_TABLE_NAME);
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: notify_booking_domain_event(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_booking_domain_event() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify(
        'tellbook_booking_events',
        NEW.sequence::text || '|' || NEW.client_id::text
    );
    RETURN NEW;
END;
$$;


--
-- Name: notify_core_worker_queue(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_core_worker_queue() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.status IN ('pending','failed','queued') THEN
        PERFORM pg_notify('tellbook_worker_core', TG_TABLE_NAME);
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: notify_payment_status_change(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_payment_status_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    PERFORM pg_notify('tellbook_payment_status', NEW.public_token);
    RETURN NEW;
END;
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: administrative_regions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.administrative_regions (
    id uuid NOT NULL,
    parent_id uuid,
    country_code text NOT NULL,
    level text NOT NULL,
    code text NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    boundary public.geometry(MultiPolygon,4326),
    source text DEFAULT ''::text NOT NULL,
    source_version text DEFAULT ''::text NOT NULL,
    source_feature_id text DEFAULT ''::text NOT NULL,
    source_license text DEFAULT ''::text NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT administrative_regions_code_check CHECK ((code ~ '^[A-Z0-9]+(?:-[A-Z0-9]+)*$'::text)),
    CONSTRAINT administrative_regions_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT administrative_regions_level_check CHECK ((level = ANY (ARRAY['country'::text, 'state'::text, 'lga'::text]))),
    CONSTRAINT administrative_regions_name_check CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT administrative_regions_parent_check CHECK ((((level = 'country'::text) AND (parent_id IS NULL) AND (boundary IS NULL)) OR ((level = ANY (ARRAY['state'::text, 'lga'::text])) AND (parent_id IS NOT NULL) AND (boundary IS NOT NULL)))),
    CONSTRAINT administrative_regions_slug_check CHECK ((slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'::text))
);


--
-- Name: agreement_acceptances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_acceptances (
    agreement_id uuid NOT NULL,
    method text NOT NULL,
    signer_name text DEFAULT ''::text NOT NULL,
    signature_png bytea DEFAULT '\x'::bytea NOT NULL,
    signature_sha256 text DEFAULT ''::text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    resolved_terms_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_acceptances_evidence_check CHECK ((((method = 'confirmation'::text) AND (signer_name = ''::text) AND (octet_length(signature_png) = 0) AND (signature_sha256 = ''::text)) OR ((method = 'signature'::text) AND (btrim(signer_name) <> ''::text) AND (octet_length(signature_png) > 0) AND (signature_sha256 ~ '^[a-f0-9]{64}$'::text)))),
    CONSTRAINT agreement_acceptances_method_check CHECK ((method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_acceptances_resolved_terms_hash_check CHECK ((resolved_terms_hash ~ '^[a-f0-9]{64}$'::text))
);


--
-- Name: agreement_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_events (
    id uuid NOT NULL,
    agreement_id uuid NOT NULL,
    event_type text NOT NULL,
    actor_type text NOT NULL,
    dedupe_key text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    occurred_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_events_actor_type_check CHECK ((actor_type = ANY (ARRAY['system'::text, 'business'::text, 'customer'::text]))),
    CONSTRAINT agreement_events_dedupe_key_check CHECK ((btrim(dedupe_key) <> ''::text)),
    CONSTRAINT agreement_events_event_type_check CHECK ((event_type = ANY (ARRAY['created'::text, 'sent'::text, 'delivery_failed'::text, 'viewed'::text, 'completed'::text, 'pdf_ready'::text, 'pdf_failed'::text, 'resent'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT agreement_events_metadata_check CHECK ((jsonb_typeof(metadata) = 'object'::text))
);


--
-- Name: agreement_instances; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_instances (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    booking_id uuid,
    template_family_id uuid,
    template_version_id uuid,
    title_snapshot text NOT NULL,
    booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    resolved_document_snapshot jsonb NOT NULL,
    schema_version_snapshot integer NOT NULL,
    renderer_version_snapshot integer NOT NULL,
    rendered_html_snapshot text NOT NULL,
    resolved_terms_hash text NOT NULL,
    confirmation_method text NOT NULL,
    timing text NOT NULL,
    status text NOT NULL,
    public_token_hash bytea NOT NULL,
    public_token_ciphertext bytea NOT NULL,
    public_token_nonce bytea NOT NULL,
    public_token_key_version text NOT NULL,
    sent_to_email text DEFAULT ''::text NOT NULL,
    personal_message_snapshot text DEFAULT ''::text NOT NULL,
    delivery_revision integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone,
    completed_at timestamp with time zone,
    pdf_status text DEFAULT 'not_requested'::text NOT NULL,
    pdf_r2_key text DEFAULT ''::text NOT NULL,
    pdf_sha256 text DEFAULT ''::text NOT NULL,
    pdf_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_instances_booking_summary_snapshot_check CHECK ((jsonb_typeof(booking_summary_snapshot) = 'object'::text)),
    CONSTRAINT agreement_instances_completion_check CHECK ((((status = 'completed'::text) AND (completed_at IS NOT NULL)) OR ((status <> 'completed'::text) AND (completed_at IS NULL)))),
    CONSTRAINT agreement_instances_confirmation_method_check CHECK ((confirmation_method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_instances_delivery_revision_check CHECK ((delivery_revision >= 0)),
    CONSTRAINT agreement_instances_pdf_status_check CHECK ((pdf_status = ANY (ARRAY['not_requested'::text, 'queued'::text, 'processing'::text, 'ready'::text, 'failed'::text]))),
    CONSTRAINT agreement_instances_public_token_hash_check CHECK ((octet_length(public_token_hash) = 32)),
    CONSTRAINT agreement_instances_public_token_key_version_check CHECK ((btrim(public_token_key_version) <> ''::text)),
    CONSTRAINT agreement_instances_rendered_html_snapshot_check CHECK ((btrim(rendered_html_snapshot) <> ''::text)),
    CONSTRAINT agreement_instances_renderer_version_snapshot_check CHECK ((renderer_version_snapshot > 0)),
    CONSTRAINT agreement_instances_resolved_document_snapshot_check CHECK ((jsonb_typeof(resolved_document_snapshot) = 'object'::text)),
    CONSTRAINT agreement_instances_resolved_terms_hash_check CHECK ((resolved_terms_hash ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT agreement_instances_schema_version_snapshot_check CHECK ((schema_version_snapshot > 0)),
    CONSTRAINT agreement_instances_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'awaiting_customer'::text, 'completed'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT agreement_instances_template_snapshot_check CHECK ((((template_family_id IS NULL) AND (template_version_id IS NULL) AND (timing = 'manual'::text)) OR ((template_family_id IS NOT NULL) AND (template_version_id IS NOT NULL)))),
    CONSTRAINT agreement_instances_timing_check CHECK ((timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text, 'manual'::text]))),
    CONSTRAINT agreement_instances_title_snapshot_check CHECK ((btrim(title_snapshot) <> ''::text))
);


--
-- Name: agreement_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_jobs (
    id uuid NOT NULL,
    agreement_id uuid NOT NULL,
    kind text NOT NULL,
    dedupe_key text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 5 NOT NULL,
    run_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_jobs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT agreement_jobs_dedupe_key_check CHECK ((btrim(dedupe_key) <> ''::text)),
    CONSTRAINT agreement_jobs_kind_check CHECK ((kind = ANY (ARRAY['render_completed_pdf'::text, 'send_agreement_email'::text, 'send_completed_email'::text]))),
    CONSTRAINT agreement_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT agreement_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: agreement_template_families; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_families (
    id uuid NOT NULL,
    client_id uuid,
    owner_type text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    category text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    confirmation_method text NOT NULL,
    status text NOT NULL,
    current_published_version_id uuid,
    source_family_id uuid,
    created_by_client_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT agreement_template_families_category_check CHECK ((btrim(category) <> ''::text)),
    CONSTRAINT agreement_template_families_confirmation_method_check CHECK ((confirmation_method = ANY (ARRAY['confirmation'::text, 'signature'::text]))),
    CONSTRAINT agreement_template_families_owner_check CHECK ((((owner_type = 'system'::text) AND (client_id IS NULL)) OR ((owner_type = 'client'::text) AND (client_id IS NOT NULL)))),
    CONSTRAINT agreement_template_families_owner_type_check CHECK ((owner_type = ANY (ARRAY['system'::text, 'client'::text]))),
    CONSTRAINT agreement_template_families_status_check CHECK ((status = ANY (ARRAY['draft'::text, 'published'::text, 'archived'::text]))),
    CONSTRAINT agreement_template_families_title_check CHECK ((btrim(title) <> ''::text))
);


--
-- Name: agreement_template_generation_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_generation_jobs (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    family_id uuid NOT NULL,
    version_id uuid NOT NULL,
    input_kind text NOT NULL,
    input_json jsonb NOT NULL,
    status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    run_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    error_code text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_template_generation_jobs_attempt_count_check CHECK ((attempt_count >= 0)),
    CONSTRAINT agreement_template_generation_jobs_input_json_check CHECK ((jsonb_typeof(input_json) = 'object'::text)),
    CONSTRAINT agreement_template_generation_jobs_input_kind_check CHECK ((input_kind = ANY (ARRAY['fields'::text, 'upload'::text]))),
    CONSTRAINT agreement_template_generation_jobs_max_attempts_check CHECK ((max_attempts > 0)),
    CONSTRAINT agreement_template_generation_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: agreement_template_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agreement_template_versions (
    id uuid NOT NULL,
    family_id uuid NOT NULL,
    version_number integer NOT NULL,
    state text NOT NULL,
    document_schema jsonb,
    used_variable_keys text[] DEFAULT '{}'::text[] NOT NULL,
    schema_version integer NOT NULL,
    renderer_version integer NOT NULL,
    source_kind text NOT NULL,
    source_pdf_r2_key text DEFAULT ''::text NOT NULL,
    source_pdf_file_name text DEFAULT ''::text NOT NULL,
    template_schema_hash text DEFAULT ''::text NOT NULL,
    review_warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    published_at timestamp with time zone,
    created_by_client_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT agreement_template_versions_document_check CHECK ((((document_schema IS NULL) AND (state = 'draft'::text) AND (template_schema_hash = ''::text) AND (cardinality(used_variable_keys) = 0)) OR ((document_schema IS NOT NULL) AND (template_schema_hash <> ''::text)))),
    CONSTRAINT agreement_template_versions_published_at_check CHECK ((((state = 'published'::text) AND (published_at IS NOT NULL)) OR (state <> 'published'::text))),
    CONSTRAINT agreement_template_versions_renderer_version_check CHECK ((renderer_version > 0)),
    CONSTRAINT agreement_template_versions_review_warnings_check CHECK ((jsonb_typeof(review_warnings) = 'array'::text)),
    CONSTRAINT agreement_template_versions_revision_check CHECK ((revision > 0)),
    CONSTRAINT agreement_template_versions_schema_version_check CHECK ((schema_version > 0)),
    CONSTRAINT agreement_template_versions_source_kind_check CHECK ((source_kind = ANY (ARRAY['ai'::text, 'upload'::text, 'library_copy'::text, 'system_seed'::text]))),
    CONSTRAINT agreement_template_versions_state_check CHECK ((state = ANY (ARRAY['draft'::text, 'published'::text, 'retired'::text]))),
    CONSTRAINT agreement_template_versions_version_number_check CHECK ((version_number > 0))
);


--
-- Name: auth_password_reset_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_password_reset_tokens (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_pending_registrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_pending_registrations (
    id uuid NOT NULL,
    full_name text NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    cover_image_data_url text DEFAULT ''::text NOT NULL,
    cover_image_content_type text DEFAULT ''::text NOT NULL,
    token_hash bytea NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: auth_refresh_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_refresh_sessions (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    ip_address text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: automation_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.automation_settings (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    automation_key text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    action_label text DEFAULT ''::text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: booking_change_commands; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_change_commands (
    id uuid NOT NULL,
    booking_id uuid NOT NULL,
    actor_type text NOT NULL,
    actor_id uuid NOT NULL,
    command text NOT NULL,
    idempotency_key uuid NOT NULL,
    quote_id uuid,
    reason text DEFAULT ''::text NOT NULL,
    response_snapshot jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    request_fingerprint text NOT NULL,
    CONSTRAINT booking_change_commands_actor_check CHECK ((actor_type = ANY (ARRAY['customer'::text, 'provider'::text]))),
    CONSTRAINT booking_change_commands_command_check CHECK ((command = ANY (ARRAY['cancel'::text, 'reschedule'::text, 'confirm'::text, 'decline'::text, 'complete'::text, 'mark_no_show'::text]))),
    CONSTRAINT booking_change_commands_fingerprint_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT booking_change_commands_response_check CHECK ((jsonb_typeof(response_snapshot) = 'object'::text))
);


--
-- Name: booking_change_quotes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_change_quotes (
    id uuid NOT NULL,
    public_token text NOT NULL,
    booking_id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    kind text NOT NULL,
    idempotency_key uuid NOT NULL,
    request_fingerprint text NOT NULL,
    booking_updated_at timestamp with time zone NOT NULL,
    current_start_at timestamp with time zone NOT NULL,
    current_end_at timestamp with time zone NOT NULL,
    proposed_start_at timestamp with time zone,
    proposed_end_at timestamp with time zone,
    proposed_occupied_start_at timestamp with time zone,
    proposed_occupied_end_at timestamp with time zone,
    refund_amount_minor bigint DEFAULT 0 NOT NULL,
    retained_amount_minor bigint DEFAULT 0 NOT NULL,
    fee_amount_minor bigint DEFAULT 0 NOT NULL,
    currency_code text NOT NULL,
    policy_message text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_change_quotes_amount_check CHECK (((refund_amount_minor >= 0) AND (retained_amount_minor >= 0) AND (fee_amount_minor >= 0))),
    CONSTRAINT booking_change_quotes_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT booking_change_quotes_expiry_check CHECK ((expires_at > created_at)),
    CONSTRAINT booking_change_quotes_fingerprint_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT booking_change_quotes_kind_check CHECK ((kind = ANY (ARRAY['cancellation'::text, 'reschedule'::text]))),
    CONSTRAINT booking_change_quotes_reschedule_shape_check CHECK ((((kind = 'cancellation'::text) AND (proposed_start_at IS NULL) AND (proposed_end_at IS NULL) AND (proposed_occupied_start_at IS NULL) AND (proposed_occupied_end_at IS NULL)) OR ((kind = 'reschedule'::text) AND (proposed_start_at IS NOT NULL) AND (proposed_end_at IS NOT NULL) AND (proposed_occupied_start_at IS NOT NULL) AND (proposed_occupied_end_at IS NOT NULL) AND (proposed_occupied_start_at <= proposed_start_at) AND (proposed_start_at < proposed_end_at) AND (proposed_end_at <= proposed_occupied_end_at))))
);


--
-- Name: booking_domain_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_domain_events (
    sequence bigint NOT NULL,
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    booking_id uuid NOT NULL,
    event_type text NOT NULL,
    dedupe_key text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_domain_events_dedupe_key_check CHECK ((btrim(dedupe_key) <> ''::text)),
    CONSTRAINT booking_domain_events_event_type_check CHECK ((btrim(event_type) <> ''::text)),
    CONSTRAINT booking_domain_events_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text))
);


--
-- Name: booking_domain_events_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.booking_domain_events ALTER COLUMN sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.booking_domain_events_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: booking_quote_promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_quote_promotions (
    booking_quote_id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    customer_email_normalized text NOT NULL,
    code_used text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_quote_promotions_amount_check CHECK ((discount_amount_minor > 0)),
    CONSTRAINT booking_quote_promotions_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text))
);


--
-- Name: booking_quotes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_quotes (
    id uuid NOT NULL,
    public_token text NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid NOT NULL,
    booking_id uuid,
    service_title text NOT NULL,
    business_name text NOT NULL,
    service_image_url text DEFAULT ''::text NOT NULL,
    duration_minutes integer NOT NULL,
    appointment_start_at timestamp with time zone NOT NULL,
    appointment_end_at timestamp with time zone NOT NULL,
    occupied_start_at timestamp with time zone NOT NULL,
    occupied_end_at timestamp with time zone NOT NULL,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    timezone text NOT NULL,
    fulfillment_mode text NOT NULL,
    location_label text NOT NULL,
    provider_location_label text DEFAULT ''::text NOT NULL,
    provider_place_id text,
    provider_latitude numeric(9,6),
    provider_longitude numeric(9,6),
    customer_location_label text DEFAULT ''::text NOT NULL,
    customer_place_id text,
    customer_latitude numeric(9,6),
    customer_longitude numeric(9,6),
    travel_distance_meters integer,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    locale text NOT NULL,
    cancellation_policy text DEFAULT ''::text NOT NULL,
    lateness_policy text DEFAULT ''::text NOT NULL,
    base_service_amount_minor bigint NOT NULL,
    promotion_id uuid,
    discount_name text DEFAULT ''::text NOT NULL,
    discount_source text DEFAULT ''::text NOT NULL,
    discount_code text DEFAULT ''::text NOT NULL,
    discount_type text DEFAULT ''::text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_rule_id uuid,
    short_notice_threshold_minutes integer,
    short_notice_surcharge_type text DEFAULT ''::text NOT NULL,
    short_notice_surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    short_notice_fee_minor bigint DEFAULT 0 NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    discounted_service_amount_minor bigint NOT NULL,
    total_amount_minor bigint NOT NULL,
    deposit_amount_minor bigint NOT NULL,
    remaining_amount_minor bigint NOT NULL,
    customer_email_normalized text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    customer_name_snapshot text DEFAULT ''::text NOT NULL,
    customer_phone_snapshot text DEFAULT ''::text NOT NULL,
    booking_notes_snapshot text DEFAULT ''::text NOT NULL,
    agreement_template_family_id_snapshot uuid,
    agreement_template_version_id_snapshot uuid,
    agreement_title_snapshot text DEFAULT ''::text NOT NULL,
    agreement_booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    agreement_resolved_document_snapshot jsonb,
    agreement_schema_version_snapshot integer,
    agreement_renderer_version_snapshot integer,
    agreement_rendered_html_snapshot text DEFAULT ''::text NOT NULL,
    agreement_resolved_terms_hash_snapshot text DEFAULT ''::text NOT NULL,
    agreement_confirmation_method_snapshot text DEFAULT ''::text NOT NULL,
    agreement_timing_snapshot text DEFAULT ''::text NOT NULL,
    standalone_signature_required_snapshot boolean DEFAULT false NOT NULL,
    idempotency_key uuid,
    request_fingerprint text,
    response_snapshot jsonb,
    CONSTRAINT booking_quotes_agreement_snapshot_shape_check CHECK ((((agreement_template_family_id_snapshot IS NULL) AND (agreement_template_version_id_snapshot IS NULL) AND (agreement_title_snapshot = ''::text) AND (agreement_resolved_document_snapshot IS NULL) AND (agreement_schema_version_snapshot IS NULL) AND (agreement_renderer_version_snapshot IS NULL) AND (agreement_rendered_html_snapshot = ''::text) AND (agreement_resolved_terms_hash_snapshot = ''::text) AND (agreement_confirmation_method_snapshot = ''::text) AND (agreement_timing_snapshot = ''::text)) OR ((agreement_template_family_id_snapshot IS NOT NULL) AND (agreement_template_version_id_snapshot IS NOT NULL) AND (btrim(agreement_title_snapshot) <> ''::text) AND (agreement_resolved_document_snapshot IS NOT NULL) AND (agreement_schema_version_snapshot > 0) AND (agreement_renderer_version_snapshot > 0) AND (btrim(agreement_rendered_html_snapshot) <> ''::text) AND (agreement_resolved_terms_hash_snapshot ~ '^[a-f0-9]{64}$'::text) AND (agreement_confirmation_method_snapshot = ANY (ARRAY['confirmation'::text, 'signature'::text])) AND (agreement_timing_snapshot = ANY (ARRAY['before_payment'::text, 'after_payment'::text])) AND (standalone_signature_required_snapshot = false)))),
    CONSTRAINT booking_quotes_consumption_check CHECK ((((consumed_at IS NULL) AND (booking_id IS NULL)) OR ((consumed_at IS NOT NULL) AND (booking_id IS NOT NULL)))),
    CONSTRAINT booking_quotes_expiry_check CHECK ((expires_at > created_at)),
    CONSTRAINT booking_quotes_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT booking_quotes_idempotency_shape_check CHECK ((((idempotency_key IS NULL) AND (request_fingerprint IS NULL) AND (response_snapshot IS NULL)) OR ((idempotency_key IS NOT NULL) AND (request_fingerprint ~ '^[a-f0-9]{64}$'::text) AND (jsonb_typeof(response_snapshot) = 'object'::text)))),
    CONSTRAINT booking_quotes_market_check CHECK (((country_code ~ '^[A-Z]{2}$'::text) AND (currency_code ~ '^[A-Z]{3}$'::text))),
    CONSTRAINT booking_quotes_nonnegative_check CHECK (((prep_time_minutes >= 0) AND (buffer_time_minutes >= 0) AND (duration_minutes > 0) AND (base_service_amount_minor >= 0) AND (discount_amount_minor >= 0) AND (short_notice_fee_minor >= 0) AND (travel_fee_minor >= 0) AND (discounted_service_amount_minor >= 0) AND (total_amount_minor >= 0) AND (deposit_amount_minor >= 0) AND (remaining_amount_minor >= 0))),
    CONSTRAINT booking_quotes_time_check CHECK (((occupied_start_at <= appointment_start_at) AND (appointment_start_at < appointment_end_at) AND (appointment_end_at <= occupied_end_at))),
    CONSTRAINT booking_quotes_total_check CHECK (((((discounted_service_amount_minor + short_notice_fee_minor) + travel_fee_minor) = total_amount_minor) AND ((deposit_amount_minor + remaining_amount_minor) = total_amount_minor)))
);


--
-- Name: booking_refund_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_refund_attempts (
    id uuid NOT NULL,
    request_id uuid NOT NULL,
    payment_id uuid NOT NULL,
    provider text NOT NULL,
    transaction_reference text NOT NULL,
    provider_reference text,
    amount_minor bigint NOT NULL,
    payment_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    currency_exponent smallint NOT NULL,
    status text DEFAULT 'prepared'::text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_refund_attempts_amount_check CHECK (((amount_minor > 0) AND (payment_amount_minor > 0) AND (amount_minor <= payment_amount_minor))),
    CONSTRAINT booking_refund_attempts_currency_check CHECK (((currency_code ~ '^[A-Z]{3}$'::text) AND ((currency_exponent >= 0) AND (currency_exponent <= 6)))),
    CONSTRAINT booking_refund_attempts_provider_check CHECK ((provider = ANY (ARRAY['paystack'::text, 'payaza'::text]))),
    CONSTRAINT booking_refund_attempts_status_check CHECK ((status = ANY (ARRAY['prepared'::text, 'initiating'::text, 'pending'::text, 'successful'::text, 'failed'::text, 'unknown'::text])))
);


--
-- Name: booking_refund_requests; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_refund_requests (
    id uuid NOT NULL,
    booking_id uuid NOT NULL,
    command_id uuid NOT NULL,
    amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT booking_refund_requests_amount_check CHECK ((amount_minor > 0)),
    CONSTRAINT booking_refund_requests_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT booking_refund_requests_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'successful'::text, 'failed'::text, 'cancelled'::text, 'manual_review'::text])))
);


--
-- Name: booking_standalone_signatures; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.booking_standalone_signatures (
    booking_id uuid NOT NULL,
    signer_name text NOT NULL,
    signature_png bytea NOT NULL,
    signature_sha256 text NOT NULL,
    accepted_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT booking_standalone_signatures_signature_png_check CHECK ((octet_length(signature_png) > 0)),
    CONSTRAINT booking_standalone_signatures_signature_sha256_check CHECK ((signature_sha256 ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT booking_standalone_signatures_signer_name_check CHECK ((btrim(signer_name) <> ''::text))
);


--
-- Name: bookings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bookings (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    service_id uuid,
    title text DEFAULT ''::text NOT NULL,
    stylist_name text DEFAULT ''::text NOT NULL,
    source text DEFAULT ''::text NOT NULL,
    status text DEFAULT ''::text NOT NULL,
    payment_status text DEFAULT ''::text NOT NULL,
    agreement_status text DEFAULT ''::text NOT NULL,
    start_at timestamp with time zone NOT NULL,
    end_at timestamp with time zone NOT NULL,
    timezone text DEFAULT 'Africa/Lagos'::text NOT NULL,
    base_service_amount_minor bigint DEFAULT 0 NOT NULL,
    total_amount_minor bigint DEFAULT 0 NOT NULL,
    currency_code text NOT NULL,
    duration_minutes integer DEFAULT 0 NOT NULL,
    notes text DEFAULT ''::text NOT NULL,
    location_label text DEFAULT ''::text NOT NULL,
    image_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    promotion_id uuid,
    discount_name text DEFAULT ''::text NOT NULL,
    discount_source text DEFAULT ''::text NOT NULL,
    discount_code text DEFAULT ''::text NOT NULL,
    discount_type text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    original_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_amount_minor bigint DEFAULT 0 NOT NULL,
    country_code text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    public_token text DEFAULT (replace((gen_random_uuid())::text, '-'::text, ''::text) || replace((gen_random_uuid())::text, '-'::text, ''::text)) NOT NULL,
    booking_quote_id uuid,
    discounted_service_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_rule_id uuid,
    short_notice_threshold_minutes integer,
    short_notice_surcharge_type text DEFAULT ''::text NOT NULL,
    short_notice_surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    short_notice_surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    short_notice_fee_minor bigint DEFAULT 0 NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    fulfillment_mode text DEFAULT 'provider_location'::text NOT NULL,
    provider_location_label text DEFAULT ''::text NOT NULL,
    provider_place_id text,
    provider_latitude numeric(9,6),
    provider_longitude numeric(9,6),
    customer_location_label text DEFAULT ''::text NOT NULL,
    customer_place_id text,
    customer_latitude numeric(9,6),
    customer_longitude numeric(9,6),
    travel_distance_meters integer,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    occupied_start_at timestamp with time zone NOT NULL,
    occupied_end_at timestamp with time zone NOT NULL,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    cancellation_policy_snapshot text DEFAULT ''::text NOT NULL,
    lateness_policy_snapshot text DEFAULT ''::text NOT NULL,
    agreement_template_family_id_snapshot uuid,
    agreement_template_version_id_snapshot uuid,
    agreement_title_snapshot text DEFAULT ''::text NOT NULL,
    agreement_booking_summary_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    agreement_resolved_document_snapshot jsonb,
    agreement_schema_version_snapshot integer,
    agreement_renderer_version_snapshot integer,
    agreement_rendered_html_snapshot text DEFAULT ''::text NOT NULL,
    agreement_resolved_terms_hash_snapshot text DEFAULT ''::text NOT NULL,
    agreement_confirmation_method_snapshot text DEFAULT ''::text NOT NULL,
    agreement_timing_snapshot text DEFAULT ''::text NOT NULL,
    standalone_signature_required_snapshot boolean DEFAULT false NOT NULL,
    whatsapp_consent boolean DEFAULT false NOT NULL,
    sms_consent boolean DEFAULT false NOT NULL,
    marketplace_customer_id uuid,
    cancellation_notice_minutes_snapshot integer DEFAULT 0 NOT NULL,
    cancellation_refund_bps_snapshot integer DEFAULT 0 NOT NULL,
    reschedule_notice_minutes_snapshot integer DEFAULT 0 NOT NULL,
    reschedule_fee_minor_snapshot bigint DEFAULT 0 NOT NULL,
    automated_reschedule_snapshot boolean DEFAULT false NOT NULL,
    reservation_expires_at timestamp with time zone,
    reservation_expired_at timestamp with time zone,
    reservation_expiry_reason text DEFAULT ''::text NOT NULL,
    customer_email_snapshot text,
    email_reminder_consent boolean DEFAULT false NOT NULL,
    email_reminder_consent_at timestamp with time zone,
    email_reminder_consent_source text DEFAULT ''::text NOT NULL,
    customer_whatsapp_e164_snapshot text,
    whatsapp_consent_at timestamp with time zone,
    whatsapp_consent_source text DEFAULT ''::text NOT NULL,
    notification_consent_policy_revision integer DEFAULT 0 NOT NULL,
    CONSTRAINT bookings_change_policy_snapshot_check CHECK (((cancellation_notice_minutes_snapshot >= 0) AND ((cancellation_refund_bps_snapshot >= 0) AND (cancellation_refund_bps_snapshot <= 10000)) AND (reschedule_notice_minutes_snapshot >= 0) AND (reschedule_fee_minor_snapshot >= 0))),
    CONSTRAINT bookings_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT bookings_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT bookings_customer_email_snapshot_check CHECK (((customer_email_snapshot IS NULL) OR ((customer_email_snapshot = lower(btrim(customer_email_snapshot))) AND (customer_email_snapshot !~ '[[:space:]]'::text) AND (POSITION(('@'::text) IN (customer_email_snapshot)) > 1) AND (char_length(customer_email_snapshot) <= 320)))),
    CONSTRAINT bookings_customer_location_check CHECK (((fulfillment_mode = 'customer_location'::text) OR ((customer_location_label = ''::text) AND (customer_place_id IS NULL) AND (customer_latitude IS NULL) AND (customer_longitude IS NULL)))),
    CONSTRAINT bookings_customer_whatsapp_snapshot_check CHECK (((customer_whatsapp_e164_snapshot IS NULL) OR (customer_whatsapp_e164_snapshot ~ '^\+[1-9][0-9]{7,14}$'::text))),
    CONSTRAINT bookings_discount_value_shape_check CHECK ((((discount_type = ''::text) AND (discount_percentage_bps = 0) AND (discount_value_minor = 0)) OR ((discount_type = 'percentage'::text) AND ((discount_percentage_bps >= 1) AND (discount_percentage_bps <= 10000)) AND (discount_value_minor = 0)) OR ((discount_type = ANY (ARRAY['fixed_amount'::text, 'set_price'::text])) AND (discount_percentage_bps = 0) AND (discount_value_minor > 0)))),
    CONSTRAINT bookings_email_reminder_consent_shape_check CHECK (((email_reminder_consent AND (customer_email_snapshot IS NOT NULL) AND (email_reminder_consent_at IS NOT NULL) AND (email_reminder_consent_source = ANY (ARRAY['public_checkout'::text, 'marketplace_checkout'::text, 'inbox_autopilot'::text]))) OR ((NOT email_reminder_consent) AND (email_reminder_consent_at IS NULL) AND (email_reminder_consent_source = ''::text)))),
    CONSTRAINT bookings_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT bookings_notification_consent_policy_check CHECK ((notification_consent_policy_revision = ANY (ARRAY[0, 1]))),
    CONSTRAINT bookings_notification_snapshot_revision_shape_check CHECK (((notification_consent_policy_revision = 0) OR ((notification_consent_policy_revision = 1) AND (customer_email_snapshot IS NOT NULL)))),
    CONSTRAINT bookings_occupied_time_check CHECK (((occupied_start_at <= start_at) AND (start_at < end_at) AND (end_at <= occupied_end_at))),
    CONSTRAINT bookings_pricing_nonnegative_check CHECK (((base_service_amount_minor >= 0) AND (discounted_service_amount_minor >= 0) AND (short_notice_fee_minor >= 0) AND (travel_fee_minor >= 0) AND (total_amount_minor >= 0) AND (deposit_amount_minor >= 0))),
    CONSTRAINT bookings_pricing_total_check CHECK (((((discounted_service_amount_minor + short_notice_fee_minor) + travel_fee_minor) = total_amount_minor) AND (deposit_amount_minor <= total_amount_minor))),
    CONSTRAINT bookings_public_token_shape_check CHECK ((public_token ~ '^[A-Za-z0-9_-]{32,128}$'::text)),
    CONSTRAINT bookings_reservation_expired_state_check CHECK (((reservation_expired_at IS NULL) OR ((status = 'expired'::text) AND (btrim(reservation_expiry_reason) <> ''::text)))),
    CONSTRAINT bookings_reservation_expiry_deadline_check CHECK (((reservation_expires_at IS NULL) OR (reservation_expires_at > created_at))),
    CONSTRAINT bookings_virtual_physical_check CHECK (((fulfillment_mode <> 'virtual'::text) OR ((provider_place_id IS NULL) AND (provider_latitude IS NULL) AND (provider_longitude IS NULL) AND (customer_place_id IS NULL) AND (customer_latitude IS NULL) AND (customer_longitude IS NULL)))),
    CONSTRAINT bookings_whatsapp_consent_shape_check CHECK (((whatsapp_consent AND (customer_whatsapp_e164_snapshot IS NOT NULL) AND (whatsapp_consent_at IS NOT NULL) AND (whatsapp_consent_source = ANY (ARRAY['public_checkout'::text, 'marketplace_checkout'::text, 'inbox_autopilot'::text]))) OR ((NOT whatsapp_consent) AND (whatsapp_consent_at IS NULL) AND (whatsapp_consent_source = ''::text))))
);


--
-- Name: business_balance_entries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_balance_entries (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    payment_adjustment_id uuid,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    kind text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT business_balance_entries_amount_nonzero_check CHECK ((amount_minor <> 0)),
    CONSTRAINT business_balance_entries_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT business_balance_entries_kind_check CHECK ((kind = ANY (ARRAY['credit'::text, 'debt'::text, 'recovery'::text, 'manual_adjustment'::text]))),
    CONSTRAINT business_balance_entries_status_check CHECK ((status = ANY (ARRAY['open'::text, 'partially_resolved'::text, 'resolved'::text, 'void'::text])))
);


--
-- Name: business_locations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.business_locations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    label text NOT NULL,
    formatted_address text NOT NULL,
    provider_place_id text,
    latitude numeric(9,6),
    longitude numeric(9,6),
    address_source text NOT NULL,
    resolution_status text NOT NULL,
    timezone text NOT NULL,
    is_primary boolean DEFAULT false NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    country_code text,
    state_region_id uuid,
    lga_region_id uuid,
    locality text DEFAULT ''::text NOT NULL,
    geog public.geography(Point,4326) GENERATED ALWAYS AS (
CASE
    WHEN ((latitude IS NULL) OR (longitude IS NULL)) THEN NULL::public.geography
    ELSE (public.st_setsrid(public.st_makepoint((longitude)::double precision, (latitude)::double precision), 4326))::public.geography
END) STORED,
    CONSTRAINT business_locations_address_source_check CHECK ((address_source = ANY (ARRAY['manual'::text, 'google_place'::text, 'current_location'::text]))),
    CONSTRAINT business_locations_coordinate_pair_check CHECK ((((latitude IS NULL) AND (longitude IS NULL)) OR ((latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT business_locations_coordinate_range_check CHECK ((((latitude IS NULL) OR ((latitude >= ('-90'::integer)::numeric) AND (latitude <= (90)::numeric))) AND ((longitude IS NULL) OR ((longitude >= ('-180'::integer)::numeric) AND (longitude <= (180)::numeric))))),
    CONSTRAINT business_locations_country_code_check CHECK (((country_code IS NULL) OR (country_code ~ '^[A-Z]{2}$'::text))),
    CONSTRAINT business_locations_required_text_check CHECK (((NULLIF(btrim(label), ''::text) IS NOT NULL) AND (NULLIF(btrim(formatted_address), ''::text) IS NOT NULL) AND (NULLIF(btrim(timezone), ''::text) IS NOT NULL))),
    CONSTRAINT business_locations_resolution_coordinate_check CHECK ((((resolution_status = 'text_only'::text) AND (latitude IS NULL) AND (longitude IS NULL)) OR ((resolution_status = 'coordinates_resolved'::text) AND (latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT business_locations_resolution_status_check CHECK ((resolution_status = ANY (ARRAY['text_only'::text, 'coordinates_resolved'::text])))
);


--
-- Name: client_profile_handles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_profile_handles (
    handle_slug text NOT NULL,
    client_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: client_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.client_profiles (
    client_id uuid NOT NULL,
    business_name text DEFAULT ''::text NOT NULL,
    handle_slug text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    headline text DEFAULT ''::text NOT NULL,
    short_bio text DEFAULT ''::text NOT NULL,
    public_location_label text DEFAULT ''::text NOT NULL,
    city text DEFAULT ''::text NOT NULL,
    region text DEFAULT ''::text NOT NULL,
    timezone text,
    hero_image_url text,
    avatar_url text,
    verified boolean DEFAULT false NOT NULL,
    years_experience integer DEFAULT 0 NOT NULL,
    review_rating numeric(3,2) DEFAULT 0 NOT NULL,
    review_count integer DEFAULT 0 NOT NULL,
    currency_code text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    public_profile_about text DEFAULT ''::text NOT NULL,
    booking_page_intro text DEFAULT ''::text NOT NULL,
    country_code text,
    locale text,
    market_configured_at timestamp with time zone,
    marketplace_enabled boolean DEFAULT false NOT NULL,
    marketplace_category_id uuid,
    marketplace_location_visibility text DEFAULT 'approximate'::text NOT NULL,
    concurrent_booking_capacity smallint DEFAULT 1 NOT NULL,
    CONSTRAINT client_profiles_concurrent_booking_capacity_check CHECK (((concurrent_booking_capacity >= 1) AND (concurrent_booking_capacity <= 50))),
    CONSTRAINT client_profiles_market_tuple_check CHECK ((((country_code IS NULL) AND (currency_code IS NULL) AND (timezone IS NULL) AND (locale IS NULL) AND (market_configured_at IS NULL)) OR ((country_code ~ '^[A-Z]{2}$'::text) AND (currency_code ~ '^[A-Z]{3}$'::text) AND (NULLIF(btrim(timezone), ''::text) IS NOT NULL) AND (locale ~ '^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})+$'::text) AND (market_configured_at IS NOT NULL)))),
    CONSTRAINT client_profiles_marketplace_location_visibility_check CHECK ((marketplace_location_visibility = ANY (ARRAY['approximate'::text, 'exact'::text])))
);


--
-- Name: clients; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.clients (
    id uuid NOT NULL,
    full_name text NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    email_verified_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    cover_image_url text
);


--
-- Name: customers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.customers (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    full_name text NOT NULL,
    email text DEFAULT ''::text NOT NULL,
    phone text DEFAULT ''::text NOT NULL,
    avatar_url text,
    tier_label text DEFAULT ''::text NOT NULL,
    status_label text DEFAULT ''::text NOT NULL,
    badge_label text DEFAULT ''::text NOT NULL,
    badge_tone text DEFAULT ''::text NOT NULL,
    tags text[] DEFAULT ARRAY[]::text[] NOT NULL,
    private_notes text DEFAULT ''::text NOT NULL,
    last_seen_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: financial_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.financial_jobs (
    id uuid NOT NULL,
    kind text NOT NULL,
    aggregate_type text NOT NULL,
    aggregate_id uuid NOT NULL,
    deduplication_key text NOT NULL,
    payload jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT financial_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT financial_jobs_lease_check CHECK (((status <> 'processing'::text) OR ((NULLIF(btrim(lease_owner), ''::text) IS NOT NULL) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT financial_jobs_required_text_check CHECK (((NULLIF(btrim(kind), ''::text) IS NOT NULL) AND (NULLIF(btrim(aggregate_type), ''::text) IS NOT NULL) AND (NULLIF(btrim(deduplication_key), ''::text) IS NOT NULL))),
    CONSTRAINT financial_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'cancelled'::text, 'dead_letter'::text])))
)
WITH (autovacuum_vacuum_scale_factor='0.05', autovacuum_analyze_scale_factor='0.02', autovacuum_vacuum_threshold='500', autovacuum_analyze_threshold='500');


--
-- Name: inbox_ai_actions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_actions (
    id uuid NOT NULL,
    session_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    idempotency_key uuid NOT NULL,
    action_name text NOT NULL,
    status text NOT NULL,
    safe_input jsonb DEFAULT '{}'::jsonb NOT NULL,
    safe_result jsonb DEFAULT '{}'::jsonb NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    turn_job_id uuid,
    CONSTRAINT inbox_ai_actions_completion_check CHECK ((((status = 'running'::text) AND (completed_at IS NULL) AND (error_code = ''::text)) OR ((status = 'succeeded'::text) AND (completed_at IS NOT NULL) AND (error_code = ''::text)) OR ((status = 'failed'::text) AND (completed_at IS NOT NULL) AND (btrim(error_code) <> ''::text)))),
    CONSTRAINT inbox_ai_actions_json_check CHECK (((jsonb_typeof(safe_input) = 'object'::text) AND (jsonb_typeof(safe_result) = 'object'::text))),
    CONSTRAINT inbox_ai_actions_name_check CHECK ((action_name = ANY (ARRAY['list_relevant_services'::text, 'get_service_details'::text, 'get_booking_link'::text, 'search_availability'::text, 'get_booking_requirements'::text, 'refresh_booking_proposal'::text, 'start_booking_session'::text, 'accept_booking_agreement'::text, 'confirm_booking_proposal'::text, 'customer_handoff'::text]))),
    CONSTRAINT inbox_ai_actions_status_check CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text])))
);


--
-- Name: inbox_ai_booking_confirmations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_booking_confirmations (
    id uuid NOT NULL,
    booking_session_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    proposal_revision bigint NOT NULL,
    proposal_hash text NOT NULL,
    idempotency_key uuid NOT NULL,
    contact_details_confirmed boolean NOT NULL,
    customer_details_revision timestamp with time zone NOT NULL,
    whatsapp_consent boolean NOT NULL,
    sms_consent boolean NOT NULL,
    agreement_instance_id uuid,
    confirmed_at timestamp with time zone DEFAULT now() NOT NULL,
    booking_id uuid,
    reservation_created_at timestamp with time zone,
    email_reminder_consent boolean DEFAULT false NOT NULL,
    CONSTRAINT inbox_ai_booking_confirmations_contact_check CHECK (contact_details_confirmed),
    CONSTRAINT inbox_ai_booking_confirmations_hash_check CHECK ((proposal_hash ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT inbox_ai_booking_confirmations_reservation_check CHECK ((((booking_id IS NULL) AND (reservation_created_at IS NULL)) OR ((booking_id IS NOT NULL) AND (reservation_created_at IS NOT NULL)))),
    CONSTRAINT inbox_ai_booking_confirmations_revision_check CHECK ((proposal_revision > 0))
);


--
-- Name: inbox_ai_booking_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_booking_sessions (
    id uuid NOT NULL,
    ai_session_id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    state text DEFAULT 'collecting_preferences'::text NOT NULL,
    selected_service_id uuid,
    requested_from date,
    requested_days integer,
    timezone text DEFAULT ''::text NOT NULL,
    selected_start_at timestamp with time zone,
    selected_end_at timestamp with time zone,
    quote_id uuid,
    quote_token text DEFAULT ''::text NOT NULL,
    quote_expires_at timestamp with time zone,
    currency_code text DEFAULT ''::text NOT NULL,
    total_amount_minor bigint,
    proposal_id uuid,
    proposal_revision bigint DEFAULT 0 NOT NULL,
    proposal_hash text DEFAULT ''::text NOT NULL,
    proposal_customer_details_revision timestamp with time zone,
    agreement_instance_id uuid,
    confirmation_id uuid,
    current_action_id uuid NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    booking_id uuid,
    CONSTRAINT inbox_ai_booking_sessions_booking_state_check CHECK (((booking_id IS NULL) OR (state = ANY (ARRAY['reservation_created'::text, 'awaiting_payment'::text, 'awaiting_after_payment_agreement'::text, 'awaiting_provider_confirmation'::text, 'completed'::text, 'expired'::text])))),
    CONSTRAINT inbox_ai_booking_sessions_proposal_check CHECK ((((proposal_id IS NULL) AND (proposal_revision = 0) AND (proposal_hash = ''::text) AND (proposal_customer_details_revision IS NULL)) OR ((proposal_id IS NOT NULL) AND (proposal_revision > 0) AND (proposal_hash ~ '^[a-f0-9]{64}$'::text) AND (quote_id IS NOT NULL) AND (proposal_customer_details_revision IS NOT NULL)))),
    CONSTRAINT inbox_ai_booking_sessions_quote_check CHECK ((((quote_id IS NULL) AND (quote_token = ''::text) AND (quote_expires_at IS NULL) AND (currency_code = ''::text) AND (total_amount_minor IS NULL)) OR ((quote_id IS NOT NULL) AND (btrim(quote_token) <> ''::text) AND (quote_expires_at IS NOT NULL) AND (currency_code ~ '^[A-Z]{3}$'::text) AND (total_amount_minor >= 0)))),
    CONSTRAINT inbox_ai_booking_sessions_request_check CHECK ((((requested_from IS NULL) AND (requested_days IS NULL)) OR ((requested_from IS NOT NULL) AND ((requested_days >= 1) AND (requested_days <= 14))))),
    CONSTRAINT inbox_ai_booking_sessions_revision_check CHECK ((revision > 0)),
    CONSTRAINT inbox_ai_booking_sessions_slot_check CHECK ((((selected_start_at IS NULL) AND (selected_end_at IS NULL)) OR ((selected_start_at IS NOT NULL) AND (selected_end_at > selected_start_at)))),
    CONSTRAINT inbox_ai_booking_sessions_state_check CHECK ((state = ANY (ARRAY['collecting_preferences'::text, 'collecting_customer_details'::text, 'offering_slots'::text, 'preparing_proposal'::text, 'awaiting_before_booking_agreement'::text, 'awaiting_confirmation'::text, 'creating_reservation'::text, 'reservation_created'::text, 'awaiting_payment'::text, 'awaiting_after_payment_agreement'::text, 'awaiting_provider_confirmation'::text, 'completed'::text, 'expired'::text, 'handoff'::text, 'provider_takeover'::text, 'failed'::text])))
);


--
-- Name: inbox_ai_conversation_controls; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_conversation_controls (
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    mode_override text,
    state text DEFAULT 'active'::text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    updated_by_actor text NOT NULL,
    updated_by_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_ai_conversation_controls_actor_check CHECK ((updated_by_actor = ANY (ARRAY['provider'::text, 'marketplace_customer'::text, 'system'::text]))),
    CONSTRAINT inbox_ai_conversation_controls_mode_check CHECK (((mode_override IS NULL) OR (mode_override = ANY (ARRAY['manual'::text, 'semi_pilot'::text, 'autopilot'::text])))),
    CONSTRAINT inbox_ai_conversation_controls_reason_check CHECK (((char_length(reason) <= 500) AND (((state = 'active'::text) AND (reason = ''::text)) OR ((state <> 'active'::text) AND (btrim(reason) <> ''::text))))),
    CONSTRAINT inbox_ai_conversation_controls_revision_check CHECK ((revision > 0)),
    CONSTRAINT inbox_ai_conversation_controls_state_check CHECK ((state = ANY (ARRAY['active'::text, 'paused'::text, 'provider_takeover'::text, 'handoff'::text])))
);


--
-- Name: inbox_ai_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_policies (
    client_id uuid NOT NULL,
    default_mode text DEFAULT 'manual'::text NOT NULL,
    enabled_service_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    paused boolean DEFAULT false NOT NULL,
    max_turns integer DEFAULT 12 NOT NULL,
    inactivity_timeout_minutes integer DEFAULT 60 NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    updated_by uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_ai_policies_limits_check CHECK ((((max_turns >= 1) AND (max_turns <= 30)) AND ((inactivity_timeout_minutes >= 5) AND (inactivity_timeout_minutes <= 1440)) AND (revision > 0))),
    CONSTRAINT inbox_ai_policies_mode_check CHECK ((default_mode = ANY (ARRAY['manual'::text, 'semi_pilot'::text, 'autopilot'::text]))),
    CONSTRAINT inbox_ai_policies_services_check CHECK ((cardinality(enabled_service_ids) <= 100))
);


--
-- Name: inbox_ai_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_runs (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    requested_by uuid NOT NULL,
    mode text DEFAULT 'manual'::text NOT NULL,
    trigger_type text DEFAULT 'provider_on_demand'::text NOT NULL,
    status text NOT NULL,
    model_provider text NOT NULL,
    model_name text NOT NULL,
    context_version integer NOT NULL,
    context_hash text NOT NULL,
    latest_message_sequence bigint DEFAULT 0 NOT NULL,
    input_character_count integer DEFAULT 0 NOT NULL,
    output_draft text DEFAULT ''::text NOT NULL,
    warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    latency_ms integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    request_id uuid NOT NULL,
    prompt_input_hash text NOT NULL,
    prompt_version integer NOT NULL,
    model_config_hash text NOT NULL,
    needs_provider_input boolean,
    provider_outcome text DEFAULT 'pending'::text NOT NULL,
    provider_message_id uuid,
    provider_final_content_hash text,
    provider_outcome_at timestamp with time zone,
    turn_job_id uuid,
    structured_decision jsonb DEFAULT '{}'::jsonb NOT NULL,
    tool_call_count integer DEFAULT 0 NOT NULL,
    resulting_message_id uuid,
    input_snapshot jsonb DEFAULT '{}'::jsonb NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    CONSTRAINT inbox_ai_runs_completion_check CHECK ((((status = ANY (ARRAY['queued'::text, 'processing'::text, 'running'::text])) AND (completed_at IS NULL) AND (output_draft = ''::text) AND (error_code = ''::text) AND (latency_ms IS NULL)) OR ((status = 'completed'::text) AND (completed_at IS NOT NULL) AND (btrim(output_draft) <> ''::text) AND (error_code = ''::text) AND (latency_ms IS NOT NULL)) OR ((status = 'failed'::text) AND (completed_at IS NOT NULL) AND (output_draft = ''::text) AND (btrim(error_code) <> ''::text) AND (latency_ms IS NOT NULL)))),
    CONSTRAINT inbox_ai_runs_context_check CHECK (((context_version > 0) AND (context_hash ~ '^[a-f0-9]{64}$'::text) AND (latest_message_sequence >= 0) AND (input_character_count >= 0))),
    CONSTRAINT inbox_ai_runs_latency_check CHECK (((latency_ms IS NULL) OR (latency_ms >= 0))),
    CONSTRAINT inbox_ai_runs_mode_check CHECK ((mode = ANY (ARRAY['manual'::text, 'semi_pilot'::text, 'autopilot'::text]))),
    CONSTRAINT inbox_ai_runs_model_check CHECK (((btrim(model_provider) <> ''::text) AND (char_length(model_provider) <= 80) AND (btrim(model_name) <> ''::text) AND (char_length(model_name) <= 240))),
    CONSTRAINT inbox_ai_runs_output_check CHECK ((char_length(output_draft) <= 4000)),
    CONSTRAINT inbox_ai_runs_prompt_hash_check CHECK (((prompt_input_hash ~ '^[a-f0-9]{64}$'::text) AND (model_config_hash ~ '^[a-f0-9]{64}$'::text) AND (prompt_version > 0))),
    CONSTRAINT inbox_ai_runs_provider_outcome_check CHECK ((provider_outcome = ANY (ARRAY['pending'::text, 'sent_unchanged'::text, 'sent_edited'::text, 'discarded'::text, 'stale'::text, 'unavailable'::text]))),
    CONSTRAINT inbox_ai_runs_provider_result_check CHECK ((((provider_outcome = ANY (ARRAY['sent_unchanged'::text, 'sent_edited'::text])) AND (provider_final_content_hash ~ '^[a-f0-9]{64}$'::text) AND (provider_outcome_at IS NOT NULL)) OR ((provider_outcome = ANY (ARRAY['discarded'::text, 'stale'::text])) AND (provider_message_id IS NULL) AND (provider_final_content_hash IS NULL) AND (provider_outcome_at IS NOT NULL)) OR ((provider_outcome = ANY (ARRAY['pending'::text, 'unavailable'::text])) AND (provider_message_id IS NULL) AND (provider_final_content_hash IS NULL) AND (provider_outcome_at IS NULL)))),
    CONSTRAINT inbox_ai_runs_queue_check CHECK ((((attempt_count >= 0) AND (attempt_count <= max_attempts)) AND ((max_attempts >= 1) AND (max_attempts <= 5)) AND (jsonb_typeof(input_snapshot) = 'object'::text) AND (((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL))) AND ((mode = 'manual'::text) OR ((status <> ALL (ARRAY['queued'::text, 'processing'::text])) AND (input_snapshot = '{}'::jsonb))))),
    CONSTRAINT inbox_ai_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'running'::text, 'completed'::text, 'failed'::text]))),
    CONSTRAINT inbox_ai_runs_trigger_check CHECK ((((mode = 'manual'::text) AND (trigger_type = 'provider_on_demand'::text)) OR ((mode = ANY (ARRAY['semi_pilot'::text, 'autopilot'::text])) AND (trigger_type = 'customer_message'::text)))),
    CONSTRAINT inbox_ai_runs_turn_check CHECK (((jsonb_typeof(structured_decision) = 'object'::text) AND ((tool_call_count >= 0) AND (tool_call_count <= 4)) AND (((mode = 'manual'::text) AND (turn_job_id IS NULL) AND (resulting_message_id IS NULL)) OR ((mode = ANY (ARRAY['semi_pilot'::text, 'autopilot'::text])) AND (turn_job_id IS NOT NULL))))),
    CONSTRAINT inbox_ai_runs_warnings_check CHECK ((jsonb_typeof(warnings) = 'array'::text))
);


--
-- Name: inbox_ai_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_sessions (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    mode text NOT NULL,
    state text NOT NULL,
    selected_service_id uuid,
    last_processed_message_sequence bigint DEFAULT 0 NOT NULL,
    last_ai_message_id uuid,
    booking_link_url text DEFAULT ''::text NOT NULL,
    booking_link_revision bigint DEFAULT 0 NOT NULL,
    policy_revision bigint NOT NULL,
    control_revision bigint NOT NULL,
    turn_count integer DEFAULT 0 NOT NULL,
    max_turns_snapshot integer NOT NULL,
    handoff_reason text DEFAULT ''::text NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_ai_sessions_counters_check CHECK (((last_processed_message_sequence >= 0) AND (booking_link_revision >= 0) AND (policy_revision > 0) AND (control_revision >= 0) AND (turn_count >= 0) AND ((max_turns_snapshot >= 1) AND (max_turns_snapshot <= 30)) AND (revision > 0))),
    CONSTRAINT inbox_ai_sessions_mode_check CHECK ((mode = ANY (ARRAY['semi_pilot'::text, 'autopilot'::text]))),
    CONSTRAINT inbox_ai_sessions_state_check CHECK ((state = ANY (ARRAY['qualifying'::text, 'service_identified'::text, 'link_ready'::text, 'link_sent'::text, 'collecting_preferences'::text, 'collecting_customer_details'::text, 'offering_slots'::text, 'preparing_proposal'::text, 'awaiting_before_booking_agreement'::text, 'awaiting_confirmation'::text, 'creating_reservation'::text, 'reservation_created'::text, 'awaiting_payment'::text, 'awaiting_after_payment_agreement'::text, 'awaiting_provider_confirmation'::text, 'completed'::text, 'handoff'::text, 'provider_takeover'::text, 'expired'::text, 'failed'::text]))),
    CONSTRAINT inbox_ai_sessions_text_check CHECK (((char_length(booking_link_url) <= 1000) AND (char_length(handoff_reason) <= 500)))
);


--
-- Name: inbox_ai_turn_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_ai_turn_jobs (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    session_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    trigger_message_sequence bigint NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    turn_number integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT inbox_ai_turn_jobs_attempt_check CHECK ((((attempt_count >= 0) AND (attempt_count <= max_attempts)) AND ((max_attempts >= 1) AND (max_attempts <= 5)) AND (turn_number >= 0) AND (trigger_message_sequence > 0))),
    CONSTRAINT inbox_ai_turn_jobs_completion_check CHECK ((((status = ANY (ARRAY['completed'::text, 'failed'::text, 'cancelled'::text])) AND (completed_at IS NOT NULL)) OR ((status = ANY (ARRAY['queued'::text, 'processing'::text])) AND (completed_at IS NULL)))),
    CONSTRAINT inbox_ai_turn_jobs_error_check CHECK (((char_length(error_code) <= 80) AND (((status = ANY (ARRAY['failed'::text, 'cancelled'::text])) AND (btrim(error_code) <> ''::text)) OR ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text])) AND (error_code = ''::text))))),
    CONSTRAINT inbox_ai_turn_jobs_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL)))),
    CONSTRAINT inbox_ai_turn_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'cancelled'::text])))
);


--
-- Name: inbox_conversation_bookings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_conversation_bookings (
    conversation_id uuid NOT NULL,
    booking_id uuid NOT NULL,
    linked_at timestamp with time zone DEFAULT now() NOT NULL,
    linked_by_actor text NOT NULL,
    CONSTRAINT inbox_conversation_bookings_actor_check CHECK ((linked_by_actor = ANY (ARRAY['customer'::text, 'provider'::text, 'system'::text])))
);


--
-- Name: inbox_conversation_moderation_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_conversation_moderation_events (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    action text NOT NULL,
    reason text NOT NULL,
    operator text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_conversation_moderation_action_check CHECK ((action = ANY (ARRAY['disabled'::text, 'enabled'::text]))),
    CONSTRAINT inbox_conversation_moderation_operator_check CHECK (((btrim(operator) <> ''::text) AND (char_length(operator) <= 160))),
    CONSTRAINT inbox_conversation_moderation_reason_check CHECK (((btrim(reason) <> ''::text) AND (char_length(reason) <= 500)))
);


--
-- Name: inbox_conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_conversations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    marketplace_customer_id uuid NOT NULL,
    channel text DEFAULT 'tellbook'::text NOT NULL,
    preview text DEFAULT ''::text NOT NULL,
    last_message_sequence bigint,
    last_message_at timestamp with time zone,
    disabled_at timestamp with time zone,
    disabled_reason text DEFAULT ''::text NOT NULL,
    disabled_by text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_conversations_channel_check CHECK ((channel = 'tellbook'::text)),
    CONSTRAINT inbox_conversations_disabled_reason_check CHECK ((char_length(disabled_reason) <= 500)),
    CONSTRAINT inbox_conversations_disabled_state_check CHECK ((((disabled_at IS NULL) AND (disabled_reason = ''::text) AND (disabled_by = ''::text)) OR ((disabled_at IS NOT NULL) AND (btrim(disabled_reason) <> ''::text) AND (btrim(disabled_by) <> ''::text)))),
    CONSTRAINT inbox_conversations_preview_check CHECK ((char_length(preview) <= 240))
);


--
-- Name: inbox_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_events (
    sequence bigint NOT NULL,
    conversation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    event_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT inbox_events_payload_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT inbox_events_type_check CHECK ((event_type = ANY (ARRAY['conversation.created'::text, 'conversation.updated'::text, 'message.created'::text, 'read.updated'::text, 'participant.archive_updated'::text, 'conversation.disabled'::text, 'ai.control_updated'::text, 'ai.session_updated'::text])))
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01', autovacuum_vacuum_threshold='1000', autovacuum_analyze_threshold='1000');


--
-- Name: inbox_events_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.inbox_events ALTER COLUMN sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.inbox_events_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: inbox_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_messages (
    id uuid NOT NULL,
    conversation_id uuid NOT NULL,
    sequence bigint NOT NULL,
    sender_type text NOT NULL,
    sender_id uuid,
    client_message_id uuid,
    request_fingerprint text,
    booking_id uuid,
    content text NOT NULL,
    message_type text DEFAULT 'text'::text NOT NULL,
    sent_at timestamp with time zone DEFAULT now() NOT NULL,
    presentation jsonb,
    CONSTRAINT inbox_messages_content_check CHECK (((btrim(content) <> ''::text) AND (char_length(content) <= 4000))),
    CONSTRAINT inbox_messages_idempotency_check CHECK ((((client_message_id IS NULL) AND (request_fingerprint IS NULL)) OR ((client_message_id IS NOT NULL) AND (request_fingerprint ~ '^[a-f0-9]{64}$'::text)))),
    CONSTRAINT inbox_messages_presentation_check CHECK (((presentation IS NULL) OR ((jsonb_typeof(presentation) = 'object'::text) AND (presentation ? 'kind'::text) AND (presentation ? 'version'::text) AND (presentation ? 'data'::text) AND (jsonb_typeof((presentation -> 'kind'::text)) = 'string'::text) AND ((presentation ->> 'kind'::text) = ANY (ARRAY['booking_link'::text, 'service_choices'::text, 'availability_choices'::text, 'booking_proposal'::text, 'reservation_created'::text, 'reservation_expired'::text, 'booking_next_step'::text])) AND ((presentation -> 'version'::text) = '1'::jsonb) AND (jsonb_typeof((presentation -> 'data'::text)) = 'object'::text)))),
    CONSTRAINT inbox_messages_sender_id_check CHECK ((((sender_type = 'system'::text) AND (sender_id IS NULL)) OR ((sender_type = ANY (ARRAY['provider'::text, 'marketplace_customer'::text, 'ai'::text])) AND (sender_id IS NOT NULL)))),
    CONSTRAINT inbox_messages_sender_type_check CHECK ((sender_type = ANY (ARRAY['provider'::text, 'marketplace_customer'::text, 'ai'::text, 'system'::text]))),
    CONSTRAINT inbox_messages_type_check CHECK ((message_type = 'text'::text))
);


--
-- Name: inbox_messages_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.inbox_messages ALTER COLUMN sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.inbox_messages_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: inbox_participant_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.inbox_participant_states (
    conversation_id uuid NOT NULL,
    participant_type text NOT NULL,
    participant_id uuid NOT NULL,
    last_read_sequence bigint DEFAULT 0 NOT NULL,
    last_read_at timestamp with time zone,
    archived_at timestamp with time zone,
    CONSTRAINT inbox_participant_states_read_sequence_check CHECK ((last_read_sequence >= 0)),
    CONSTRAINT inbox_participant_states_type_check CHECK ((participant_type = ANY (ARRAY['provider'::text, 'marketplace_customer'::text])))
);


--
-- Name: marketplace_auth_challenges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_auth_challenges (
    id uuid NOT NULL,
    identifier_type text NOT NULL,
    identifier text NOT NULL,
    delivery_channel text NOT NULL,
    code_hash bytea NOT NULL,
    failed_attempts integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    purpose text DEFAULT 'sign_in'::text NOT NULL,
    target_customer_id uuid,
    CONSTRAINT marketplace_auth_challenges_delivery_channel_check CHECK ((delivery_channel = ANY (ARRAY['email'::text, 'sms'::text, 'whatsapp'::text]))),
    CONSTRAINT marketplace_auth_challenges_failed_attempts_check CHECK (((failed_attempts >= 0) AND (failed_attempts <= 6))),
    CONSTRAINT marketplace_auth_challenges_identifier_type_check CHECK ((identifier_type = ANY (ARRAY['email'::text, 'phone'::text, 'whatsapp'::text]))),
    CONSTRAINT marketplace_auth_challenges_purpose_check CHECK ((purpose = ANY (ARRAY['sign_in'::text, 'link_identity'::text]))),
    CONSTRAINT marketplace_auth_challenges_target_check CHECK ((((purpose = 'sign_in'::text) AND (target_customer_id IS NULL)) OR ((purpose = 'link_identity'::text) AND (target_customer_id IS NOT NULL))))
)
WITH (autovacuum_vacuum_scale_factor='0.05', autovacuum_analyze_scale_factor='0.02', autovacuum_vacuum_threshold='500', autovacuum_analyze_threshold='500');


--
-- Name: marketplace_auth_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_auth_sessions (
    id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    ip_address text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    last_used_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    session_revision bigint DEFAULT 1 NOT NULL,
    CONSTRAINT marketplace_auth_sessions_session_revision_check CHECK ((session_revision > 0))
)
WITH (autovacuum_vacuum_scale_factor='0.05', autovacuum_analyze_scale_factor='0.02', autovacuum_vacuum_threshold='500', autovacuum_analyze_threshold='500');


--
-- Name: marketplace_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_categories (
    id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    image_url text DEFAULT ''::text NOT NULL,
    image_alt text DEFAULT ''::text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_categories_name_check CHECK ((btrim(name) <> ''::text)),
    CONSTRAINT marketplace_categories_slug_check CHECK ((slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'::text))
);


--
-- Name: marketplace_customer_addresses; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_customer_addresses (
    id uuid NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    label text NOT NULL,
    address_line_1 text NOT NULL,
    address_line_2 text DEFAULT ''::text NOT NULL,
    locality text DEFAULT ''::text NOT NULL,
    state_region_id uuid,
    lga_region_id uuid,
    country_code text DEFAULT 'NG'::text NOT NULL,
    postal_code text DEFAULT ''::text NOT NULL,
    latitude numeric(9,6),
    longitude numeric(9,6),
    is_default boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_customer_addresses_coordinates_check CHECK ((((latitude IS NULL) AND (longitude IS NULL)) OR (((latitude >= ('-90'::integer)::numeric) AND (latitude <= (90)::numeric)) AND ((longitude >= ('-180'::integer)::numeric) AND (longitude <= (180)::numeric))))),
    CONSTRAINT marketplace_customer_addresses_country_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT marketplace_customer_addresses_label_check CHECK ((btrim(label) <> ''::text)),
    CONSTRAINT marketplace_customer_addresses_line_1_check CHECK ((btrim(address_line_1) <> ''::text))
);


--
-- Name: marketplace_customer_identities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_customer_identities (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    identifier_type text NOT NULL,
    normalized_identifier text NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_customer_identities_type_check CHECK ((identifier_type = ANY (ARRAY['email'::text, 'phone'::text, 'whatsapp'::text]))),
    CONSTRAINT marketplace_customer_identities_value_check CHECK ((btrim(normalized_identifier) <> ''::text))
);


--
-- Name: marketplace_customers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_customers (
    id uuid NOT NULL,
    full_name text DEFAULT ''::text NOT NULL,
    email text,
    phone_e164 text,
    whatsapp_e164 text,
    email_verified_at timestamp with time zone,
    phone_verified_at timestamp with time zone,
    whatsapp_verified_at timestamp with time zone,
    password_hash text,
    birthday date,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    security_revision bigint DEFAULT 1 NOT NULL,
    CONSTRAINT marketplace_customers_full_name_check CHECK ((char_length(full_name) <= 160)),
    CONSTRAINT marketplace_customers_identifier_check CHECK (((email IS NOT NULL) OR (phone_e164 IS NOT NULL) OR (whatsapp_e164 IS NOT NULL))),
    CONSTRAINT marketplace_customers_security_revision_check CHECK ((security_revision > 0))
);


--
-- Name: marketplace_discovery_count_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_discovery_count_jobs (
    singleton boolean DEFAULT true NOT NULL,
    requested_revision bigint DEFAULT 1 NOT NULL,
    applied_revision bigint DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_discovery_count_jobs_revision_check CHECK (((requested_revision > 0) AND (applied_revision >= 0) AND (applied_revision <= requested_revision))),
    CONSTRAINT marketplace_discovery_count_jobs_singleton_check CHECK (singleton)
);


--
-- Name: marketplace_discovery_counts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_discovery_counts (
    dimension text NOT NULL,
    dimension_id uuid NOT NULL,
    provider_count integer NOT NULL,
    refreshed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_discovery_counts_dimension_check CHECK ((dimension = ANY (ARRAY['category'::text, 'state'::text, 'lga'::text]))),
    CONSTRAINT marketplace_discovery_counts_value_check CHECK ((provider_count >= 0))
);


--
-- Name: marketplace_discovery_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_discovery_jobs (
    client_id uuid NOT NULL,
    requested_revision bigint DEFAULT 1 NOT NULL,
    applied_revision bigint DEFAULT 0 NOT NULL,
    refresh_counts boolean DEFAULT false NOT NULL,
    refresh_services boolean DEFAULT true NOT NULL,
    refresh_availability boolean DEFAULT true NOT NULL,
    availability_from date,
    availability_to date,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    CONSTRAINT marketplace_discovery_jobs_availability_range_check CHECK ((((availability_from IS NULL) AND (availability_to IS NULL)) OR (refresh_availability AND (availability_from IS NOT NULL) AND (availability_to >= availability_from)))),
    CONSTRAINT marketplace_discovery_jobs_revision_check CHECK (((requested_revision > 0) AND (applied_revision >= 0) AND (applied_revision <= requested_revision)))
);


--
-- Name: marketplace_notification_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_notification_preferences (
    marketplace_customer_id uuid NOT NULL,
    booking_email boolean DEFAULT true NOT NULL,
    booking_sms boolean DEFAULT false NOT NULL,
    booking_whatsapp boolean DEFAULT false NOT NULL,
    marketing_email boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: marketplace_notifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_notifications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    marketplace_customer_id uuid NOT NULL,
    kind text NOT NULL,
    event_type text NOT NULL,
    event_key text NOT NULL,
    title text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    provider_id uuid,
    booking_id uuid,
    conversation_id uuid,
    read_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_notifications_body_check CHECK ((char_length(body) <= 1000)),
    CONSTRAINT marketplace_notifications_event_key_check CHECK ((btrim(event_key) <> ''::text)),
    CONSTRAINT marketplace_notifications_event_type_check CHECK ((btrim(event_type) <> ''::text)),
    CONSTRAINT marketplace_notifications_kind_check CHECK ((kind = ANY (ARRAY['booking'::text, 'message'::text, 'offer'::text, 'system'::text]))),
    CONSTRAINT marketplace_notifications_title_check CHECK (((btrim(title) <> ''::text) AND (char_length(title) <= 180)))
);


--
-- Name: marketplace_provider_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_provider_documents (
    client_id uuid NOT NULL,
    handle_slug text NOT NULL,
    business_name text NOT NULL,
    headline text DEFAULT ''::text NOT NULL,
    category_id uuid NOT NULL,
    category_name text NOT NULL,
    avatar_url text DEFAULT ''::text NOT NULL,
    hero_image_url text DEFAULT ''::text NOT NULL,
    verified boolean DEFAULT false NOT NULL,
    review_rating double precision DEFAULT 0 NOT NULL,
    review_count integer DEFAULT 0 NOT NULL,
    completed_bookings integer DEFAULT 0 NOT NULL,
    location_visibility text NOT NULL,
    timezone text NOT NULL,
    public_location_label text DEFAULT ''::text NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, ((((COALESCE(business_name, ''::text) || ' '::text) || COALESCE(headline, ''::text)) || ' '::text) || COALESCE(category_name, ''::text)))) STORED,
    document_revision bigint NOT NULL,
    source_updated_at timestamp with time zone NOT NULL,
    projected_at timestamp with time zone DEFAULT now() NOT NULL,
    availability_refresh_after timestamp with time zone,
    availability_refresh_date date,
    CONSTRAINT marketplace_provider_documents_counts_check CHECK (((review_count >= 0) AND (completed_bookings >= 0))),
    CONSTRAINT marketplace_provider_documents_rating_check CHECK (((review_rating >= (0)::double precision) AND (review_rating <= (5)::double precision))),
    CONSTRAINT marketplace_provider_documents_visibility_check CHECK ((location_visibility = ANY (ARRAY['approximate'::text, 'exact'::text])))
);


--
-- Name: marketplace_saved_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_saved_providers (
    marketplace_customer_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: marketplace_service_availability_days; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_service_availability_days (
    service_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    local_date date NOT NULL,
    has_available_slot boolean NOT NULL,
    first_available_at timestamp with time zone,
    document_revision bigint NOT NULL,
    projected_at timestamp with time zone DEFAULT now() NOT NULL,
    refresh_after timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_service_availability_days_slot_shape_check CHECK ((has_available_slot = (first_available_at IS NOT NULL)))
);


--
-- Name: marketplace_service_documents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.marketplace_service_documents (
    service_id uuid NOT NULL,
    provider_id uuid NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    image_url text DEFAULT ''::text NOT NULL,
    duration_minutes integer NOT NULL,
    price_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    fulfillment_mode text NOT NULL,
    provider_location_id uuid,
    location_label text NOT NULL,
    state_region_id uuid,
    lga_region_id uuid,
    internal_geog public.geography(Point,4326),
    public_geog public.geography(Point,4326),
    public_latitude numeric(9,6),
    public_longitude numeric(9,6),
    map_precision text NOT NULL,
    max_travel_distance_meters integer,
    sort_order integer DEFAULT 0 NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, ((COALESCE(title, ''::text) || ' '::text) || COALESCE(description, ''::text)))) STORED,
    document_revision bigint NOT NULL,
    source_updated_at timestamp with time zone NOT NULL,
    projected_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT marketplace_service_documents_fulfillment_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT marketplace_service_documents_map_precision_check CHECK ((map_precision = ANY (ARRAY['exact'::text, 'approximate'::text, 'none'::text]))),
    CONSTRAINT marketplace_service_documents_map_shape_check CHECK ((((map_precision = 'none'::text) AND (public_geog IS NULL) AND (public_latitude IS NULL) AND (public_longitude IS NULL)) OR ((map_precision = ANY (ARRAY['exact'::text, 'approximate'::text])) AND (public_geog IS NOT NULL) AND (public_latitude IS NOT NULL) AND (public_longitude IS NOT NULL)))),
    CONSTRAINT marketplace_service_documents_price_duration_check CHECK (((price_amount_minor >= 0) AND (duration_minutes > 0)))
);


--
-- Name: meta_whatsapp_webhook_receipts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.meta_whatsapp_webhook_receipts (
    id uuid NOT NULL,
    dedupe_key text NOT NULL,
    waba_id text NOT NULL,
    phone_number_id text NOT NULL,
    event_kind text NOT NULL,
    wamid text NOT NULL,
    message_status text DEFAULT ''::text NOT NULL,
    provider_timestamp timestamp with time zone,
    provider_error_code text DEFAULT ''::text NOT NULL,
    processing_status text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    last_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    CONSTRAINT meta_whatsapp_webhook_receipts_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 20))),
    CONSTRAINT meta_whatsapp_webhook_receipts_dedupe_check CHECK ((dedupe_key ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT meta_whatsapp_webhook_receipts_error_length_check CHECK (((char_length(provider_error_code) <= 80) AND (char_length(last_error_code) <= 80))),
    CONSTRAINT meta_whatsapp_webhook_receipts_kind_check CHECK ((event_kind = ANY (ARRAY['status'::text, 'inbound_message'::text]))),
    CONSTRAINT meta_whatsapp_webhook_receipts_lease_check CHECK ((((processing_status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_token IS NOT NULL) AND (lease_expires_at IS NOT NULL)) OR ((processing_status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_token IS NULL) AND (lease_expires_at IS NULL)))),
    CONSTRAINT meta_whatsapp_webhook_receipts_phone_check CHECK ((phone_number_id ~ '^[0-9]{1,32}$'::text)),
    CONSTRAINT meta_whatsapp_webhook_receipts_processed_check CHECK ((((processing_status = ANY (ARRAY['completed'::text, 'dead_letter'::text])) AND (processed_at IS NOT NULL)) OR ((processing_status <> ALL (ARRAY['completed'::text, 'dead_letter'::text])) AND (processed_at IS NULL)))),
    CONSTRAINT meta_whatsapp_webhook_receipts_processing_check CHECK ((processing_status = ANY (ARRAY['pending'::text, 'processing'::text, 'retry'::text, 'completed'::text, 'dead_letter'::text]))),
    CONSTRAINT meta_whatsapp_webhook_receipts_status_length_check CHECK ((char_length(message_status) <= 40)),
    CONSTRAINT meta_whatsapp_webhook_receipts_waba_check CHECK ((waba_id ~ '^[0-9]{1,32}$'::text)),
    CONSTRAINT meta_whatsapp_webhook_receipts_wamid_check CHECK (((char_length(wamid) >= 1) AND (char_length(wamid) <= 512)))
);


--
-- Name: notification_contact_suppressions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_contact_suppressions (
    channel text NOT NULL,
    destination_hmac bytea NOT NULL,
    reason text NOT NULL,
    source text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT notification_contact_suppressions_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'whatsapp'::text]))),
    CONSTRAINT notification_contact_suppressions_hmac_check CHECK ((octet_length(destination_hmac) = 32)),
    CONSTRAINT notification_contact_suppressions_reason_check CHECK ((reason = ANY (ARRAY['user_opt_out'::text, 'invalid_address'::text, 'hard_bounce'::text, 'manual'::text]))),
    CONSTRAINT notification_contact_suppressions_source_check CHECK ((source = ANY (ARRAY['inbound_control'::text, 'customer_preference'::text, 'provider_response'::text, 'admin'::text])))
);


--
-- Name: notification_deliveries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_deliveries (
    id uuid NOT NULL,
    idempotency_key text NOT NULL,
    client_id uuid NOT NULL,
    marketplace_customer_id uuid,
    booking_id uuid NOT NULL,
    booking_event_id uuid,
    booking_event_sequence bigint NOT NULL,
    audience_type text NOT NULL,
    channel text NOT NULL,
    notification_type text NOT NULL,
    template_key text DEFAULT ''::text NOT NULL,
    reminder_occurrence_at timestamp with time zone,
    reminder_offset_minutes integer,
    scheduled_for timestamp with time zone NOT NULL,
    destination_hmac bytea NOT NULL,
    preference_revision bigint DEFAULT 0 NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    dispatch_authorized_at timestamp with time zone,
    authorized_booking_event_sequence bigint,
    authorized_preference_revision bigint,
    reconcile_after timestamp with time zone,
    provider_message_id text DEFAULT ''::text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    provider_error_code text DEFAULT ''::text NOT NULL,
    provider_status_at timestamp with time zone,
    accepted_at timestamp with time zone,
    sent_at timestamp with time zone,
    delivered_at timestamp with time zone,
    read_at timestamp with time zone,
    last_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT notification_deliveries_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 20))),
    CONSTRAINT notification_deliveries_audience_check CHECK ((audience_type = ANY (ARRAY['provider'::text, 'customer'::text]))),
    CONSTRAINT notification_deliveries_channel_check CHECK ((channel = ANY (ARRAY['email'::text, 'whatsapp'::text]))),
    CONSTRAINT notification_deliveries_dispatch_check CHECK ((((dispatch_authorized_at IS NULL) AND (authorized_booking_event_sequence IS NULL) AND (authorized_preference_revision IS NULL)) OR ((dispatch_authorized_at IS NOT NULL) AND (authorized_booking_event_sequence IS NOT NULL) AND (authorized_preference_revision IS NOT NULL)))),
    CONSTRAINT notification_deliveries_error_check CHECK (((char_length(last_error_code) <= 80) AND (char_length(provider_error_code) <= 120))),
    CONSTRAINT notification_deliveries_hmac_check CHECK ((octet_length(destination_hmac) = 32)),
    CONSTRAINT notification_deliveries_idempotency_check CHECK (((btrim(idempotency_key) <> ''::text) AND (char_length(idempotency_key) <= 240))),
    CONSTRAINT notification_deliveries_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL)))),
    CONSTRAINT notification_deliveries_reminder_check CHECK ((((notification_type = 'appointment_reminder'::text) AND (reminder_occurrence_at IS NOT NULL) AND (reminder_offset_minutes = 1440)) OR ((notification_type <> 'appointment_reminder'::text) AND (reminder_occurrence_at IS NULL) AND (reminder_offset_minutes IS NULL)))),
    CONSTRAINT notification_deliveries_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'dispatching'::text, 'accepted'::text, 'sent'::text, 'delivered'::text, 'read'::text, 'retry'::text, 'unknown'::text, 'manual_review'::text, 'failed'::text, 'deleted'::text, 'cancelled'::text]))),
    CONSTRAINT notification_deliveries_template_check CHECK ((char_length(template_key) <= 80)),
    CONSTRAINT notification_deliveries_type_check CHECK (((btrim(notification_type) <> ''::text) AND (char_length(notification_type) <= 80)))
);


--
-- Name: notification_event_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_event_jobs (
    booking_event_id uuid NOT NULL,
    event_sequence bigint NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT notification_event_jobs_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 20))),
    CONSTRAINT notification_event_jobs_completion_check CHECK ((((status = ANY (ARRAY['completed'::text, 'dead_letter'::text])) AND (completed_at IS NOT NULL)) OR ((status <> ALL (ARRAY['completed'::text, 'dead_letter'::text])) AND (completed_at IS NULL)))),
    CONSTRAINT notification_event_jobs_error_check CHECK ((char_length(last_error_code) <= 80)),
    CONSTRAINT notification_event_jobs_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL)))),
    CONSTRAINT notification_event_jobs_sequence_check CHECK ((event_sequence > 0)),
    CONSTRAINT notification_event_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'retry'::text, 'completed'::text, 'dead_letter'::text])))
);


--
-- Name: notification_in_app_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_in_app_jobs (
    id uuid NOT NULL,
    idempotency_key text NOT NULL,
    client_id uuid NOT NULL,
    marketplace_customer_id uuid,
    booking_id uuid NOT NULL,
    audience_type text NOT NULL,
    reminder_occurrence_at timestamp with time zone NOT NULL,
    scheduled_for timestamp with time zone NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT notification_in_app_jobs_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 20))),
    CONSTRAINT notification_in_app_jobs_audience_check CHECK ((audience_type = ANY (ARRAY['provider'::text, 'customer'::text]))),
    CONSTRAINT notification_in_app_jobs_completion_check CHECK ((((status = ANY (ARRAY['completed'::text, 'dead_letter'::text, 'cancelled'::text])) AND (completed_at IS NOT NULL)) OR ((status <> ALL (ARRAY['completed'::text, 'dead_letter'::text, 'cancelled'::text])) AND (completed_at IS NULL)))),
    CONSTRAINT notification_in_app_jobs_customer_check CHECK (((audience_type = 'provider'::text) OR (marketplace_customer_id IS NOT NULL))),
    CONSTRAINT notification_in_app_jobs_error_check CHECK ((char_length(last_error_code) <= 80)),
    CONSTRAINT notification_in_app_jobs_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL)))),
    CONSTRAINT notification_in_app_jobs_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'retry'::text, 'completed'::text, 'dead_letter'::text, 'cancelled'::text])))
);


--
-- Name: notification_scope_replan_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notification_scope_replan_jobs (
    client_id uuid NOT NULL,
    preference_revision bigint NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    booking_cursor_start_at timestamp with time zone,
    booking_cursor_id uuid,
    last_error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT notification_scope_replan_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 20))),
    CONSTRAINT notification_scope_replan_completion_check CHECK ((((status = ANY (ARRAY['completed'::text, 'dead_letter'::text, 'superseded'::text])) AND (completed_at IS NOT NULL)) OR ((status <> ALL (ARRAY['completed'::text, 'dead_letter'::text, 'superseded'::text])) AND (completed_at IS NULL)))),
    CONSTRAINT notification_scope_replan_cursor_check CHECK ((((booking_cursor_start_at IS NULL) AND (booking_cursor_id IS NULL)) OR ((booking_cursor_start_at IS NOT NULL) AND (booking_cursor_id IS NOT NULL)))),
    CONSTRAINT notification_scope_replan_error_check CHECK ((char_length(last_error_code) <= 80)),
    CONSTRAINT notification_scope_replan_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_expires_at IS NULL)))),
    CONSTRAINT notification_scope_replan_revision_check CHECK ((preference_revision > 0)),
    CONSTRAINT notification_scope_replan_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'retry'::text, 'completed'::text, 'dead_letter'::text, 'superseded'::text])))
);


--
-- Name: notifications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.notifications (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    booking_id uuid,
    type text DEFAULT ''::text NOT NULL,
    severity text DEFAULT ''::text NOT NULL,
    title text DEFAULT ''::text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    action_label text DEFAULT ''::text NOT NULL,
    action_route text DEFAULT ''::text NOT NULL,
    image_url text,
    icon_name text DEFAULT ''::text NOT NULL,
    icon_tone text DEFAULT ''::text NOT NULL,
    metadata jsonb DEFAULT '{}'::jsonb NOT NULL,
    read_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: payment_adjustments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_adjustments (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    provider text NOT NULL,
    provider_reference text NOT NULL,
    kind text NOT NULL,
    status text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    occurred_at timestamp with time zone NOT NULL,
    allocation_impact_minor bigint DEFAULT 0 NOT NULL,
    funds_already_paid_out boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_adjustments_allocation_impact_check CHECK ((allocation_impact_minor >= 0)),
    CONSTRAINT payment_adjustments_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payment_adjustments_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_adjustments_kind_check CHECK ((kind = ANY (ARRAY['partial_refund'::text, 'refund'::text, 'reversal'::text, 'dispute'::text, 'chargeback'::text]))),
    CONSTRAINT payment_adjustments_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payment_adjustments_reference_check CHECK ((NULLIF(btrim(provider_reference), ''::text) IS NOT NULL)),
    CONSTRAINT payment_adjustments_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'successful'::text, 'failed'::text, 'reversed'::text])))
);


--
-- Name: payment_allocations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_allocations (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    client_id uuid NOT NULL,
    currency_code text NOT NULL,
    gross_amount_minor bigint NOT NULL,
    provider_collection_fee_minor bigint DEFAULT 0 NOT NULL,
    platform_fee_minor bigint DEFAULT 0 NOT NULL,
    tax_amount_minor bigint DEFAULT 0 NOT NULL,
    adjustment_amount_minor bigint DEFAULT 0 NOT NULL,
    business_net_amount_minor bigint NOT NULL,
    policy_version text NOT NULL,
    calculation_snapshot jsonb NOT NULL,
    settlement_status text DEFAULT 'pending'::text NOT NULL,
    settlement_reference text DEFAULT ''::text NOT NULL,
    available_for_payout_at timestamp with time zone,
    status text DEFAULT 'pending'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payment_allocations_amounts_check CHECK (((gross_amount_minor > 0) AND (provider_collection_fee_minor >= 0) AND (platform_fee_minor >= 0) AND (tax_amount_minor >= 0) AND (adjustment_amount_minor >= 0) AND (business_net_amount_minor >= 0) AND (gross_amount_minor = ((((provider_collection_fee_minor + platform_fee_minor) + tax_amount_minor) + adjustment_amount_minor) + business_net_amount_minor)))),
    CONSTRAINT payment_allocations_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_allocations_eligibility_check CHECK (((status <> 'eligible'::text) OR ((settlement_status = 'available'::text) AND (available_for_payout_at IS NOT NULL)))),
    CONSTRAINT payment_allocations_policy_version_check CHECK ((NULLIF(btrim(policy_version), ''::text) IS NOT NULL)),
    CONSTRAINT payment_allocations_settlement_status_check CHECK ((settlement_status = ANY (ARRAY['pending'::text, 'available'::text, 'held'::text, 'unavailable'::text, 'reversed'::text]))),
    CONSTRAINT payment_allocations_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'eligible'::text, 'reserved'::text, 'paid'::text, 'blocked'::text, 'reversed'::text])))
);


--
-- Name: payment_exceptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_exceptions (
    id uuid NOT NULL,
    payment_id uuid NOT NULL,
    booking_id uuid NOT NULL,
    provider text NOT NULL,
    exception_kind text NOT NULL,
    provider_reference text NOT NULL,
    evidence_source text NOT NULL,
    evidence_reference text NOT NULL,
    observed_amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    resolution_notes text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    resolved_at timestamp with time zone,
    CONSTRAINT payment_exceptions_amount_check CHECK ((observed_amount_minor >= 0)),
    CONSTRAINT payment_exceptions_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payment_exceptions_kind_check CHECK ((exception_kind = ANY (ARRAY['late_success'::text, 'amount_mismatch'::text]))),
    CONSTRAINT payment_exceptions_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payment_exceptions_required_text_check CHECK (((NULLIF(btrim(provider_reference), ''::text) IS NOT NULL) AND (NULLIF(btrim(evidence_source), ''::text) IS NOT NULL) AND (NULLIF(btrim(evidence_reference), ''::text) IS NOT NULL))),
    CONSTRAINT payment_exceptions_resolution_check CHECK ((((status = 'open'::text) AND (resolved_at IS NULL)) OR ((status = 'resolved'::text) AND (resolved_at IS NOT NULL)))),
    CONSTRAINT payment_exceptions_status_check CHECK ((status = ANY (ARRAY['open'::text, 'resolved'::text])))
);


--
-- Name: payment_provider_request_budgets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payment_provider_request_budgets (
    provider text NOT NULL,
    next_allowed_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    CONSTRAINT payment_provider_request_budgets_lease_check CHECK ((((lease_owner = ''::text) AND (lease_expires_at IS NULL)) OR ((btrim(lease_owner) <> ''::text) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT payment_provider_request_budgets_provider_check CHECK ((btrim(provider) <> ''::text))
);


--
-- Name: payments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payments (
    id uuid NOT NULL,
    public_token text NOT NULL,
    booking_id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid NOT NULL,
    purpose text NOT NULL,
    provider text NOT NULL,
    method text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    price_snapshot jsonb NOT NULL,
    reference text NOT NULL,
    provider_reference text DEFAULT ''::text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint text NOT NULL,
    status text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    reconciliation_reason text DEFAULT ''::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    checkout_url text DEFAULT ''::text NOT NULL,
    expires_at timestamp with time zone,
    paid_at timestamp with time zone,
    last_reconciled_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    checkout_details jsonb DEFAULT '{}'::jsonb NOT NULL,
    reconciliation_lease_owner text DEFAULT ''::text NOT NULL,
    reconciliation_lease_expires_at timestamp with time zone,
    provider_channel text,
    checkout_initialization_state text DEFAULT 'ready'::text NOT NULL,
    checkout_initialization_lease_owner text DEFAULT ''::text NOT NULL,
    checkout_initialization_lease_expires_at timestamp with time zone,
    next_provider_check_at timestamp with time zone,
    CONSTRAINT payments_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payments_checkout_details_object_check CHECK ((jsonb_typeof(checkout_details) = 'object'::text)),
    CONSTRAINT payments_checkout_initialization_lease_check CHECK ((((checkout_initialization_lease_owner = ''::text) AND (checkout_initialization_lease_expires_at IS NULL)) OR ((NULLIF(btrim(checkout_initialization_lease_owner), ''::text) IS NOT NULL) AND (checkout_initialization_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payments_checkout_initialization_state_check CHECK ((checkout_initialization_state = ANY (ARRAY['prepared'::text, 'ready'::text, 'unknown'::text]))),
    CONSTRAINT payments_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payments_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payments_idempotency_key_shape_check CHECK ((idempotency_key ~ '^[A-Za-z0-9_-]{16,128}$'::text)),
    CONSTRAINT payments_paid_at_check CHECK (((status <> 'paid'::text) OR (paid_at IS NOT NULL))),
    CONSTRAINT payments_provider_channel_shape_check CHECK (((provider_channel IS NULL) OR (provider_channel ~ '^[a-z][a-z0-9_]{1,63}$'::text))),
    CONSTRAINT payments_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payments_public_token_shape_check CHECK ((public_token ~ '^[A-Za-z0-9_-]{32,128}$'::text)),
    CONSTRAINT payments_purpose_check CHECK ((purpose = ANY (ARRAY['deposit'::text, 'full'::text, 'balance'::text]))),
    CONSTRAINT payments_reconciliation_lease_check CHECK ((((reconciliation_lease_owner = ''::text) AND (reconciliation_lease_expires_at IS NULL)) OR ((NULLIF(btrim(reconciliation_lease_owner), ''::text) IS NOT NULL) AND (reconciliation_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payments_reference_shape_check CHECK ((NULLIF(btrim(reference), ''::text) IS NOT NULL)),
    CONSTRAINT payments_request_fingerprint_shape_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payments_status_check CHECK ((status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'paid'::text, 'partially_refunded'::text, 'refunded'::text, 'disputed'::text, 'reversed'::text, 'failed'::text, 'expired'::text, 'cancelled'::text]))),
    CONSTRAINT payments_version_positive_check CHECK ((version > 0))
);


--
-- Name: payout_destinations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payout_destinations (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    provider text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    rail text NOT NULL,
    institution_code text NOT NULL,
    institution_name text NOT NULL,
    masked_identifier text NOT NULL,
    identifier_ciphertext bytea,
    identifier_nonce bytea,
    encryption_key_version text,
    resolved_account_name text NOT NULL,
    provider_recipient_id text DEFAULT ''::text NOT NULL,
    verification_fingerprint text NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    is_default boolean DEFAULT false NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT payout_destinations_active_identifier_check CHECK (((status <> 'active'::text) OR (identifier_ciphertext IS NOT NULL))),
    CONSTRAINT payout_destinations_ciphertext_group_check CHECK ((((identifier_ciphertext IS NULL) AND (identifier_nonce IS NULL) AND (encryption_key_version IS NULL)) OR ((identifier_ciphertext IS NOT NULL) AND (identifier_nonce IS NOT NULL) AND (NULLIF(btrim(encryption_key_version), ''::text) IS NOT NULL)))),
    CONSTRAINT payout_destinations_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payout_destinations_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payout_destinations_fingerprint_shape_check CHECK ((verification_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payout_destinations_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payout_destinations_required_text_check CHECK (((NULLIF(btrim(rail), ''::text) IS NOT NULL) AND (NULLIF(btrim(institution_code), ''::text) IS NOT NULL) AND (NULLIF(btrim(institution_name), ''::text) IS NOT NULL) AND (NULLIF(btrim(masked_identifier), ''::text) IS NOT NULL) AND (NULLIF(btrim(resolved_account_name), ''::text) IS NOT NULL))),
    CONSTRAINT payout_destinations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'invalidated'::text, 'disabled'::text])))
);


--
-- Name: payouts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.payouts (
    id uuid NOT NULL,
    payment_allocation_id uuid NOT NULL,
    client_id uuid NOT NULL,
    payout_destination_id uuid NOT NULL,
    provider text NOT NULL,
    rail text NOT NULL,
    country_code text NOT NULL,
    currency_code text NOT NULL,
    amount_minor bigint NOT NULL,
    fee_minor bigint DEFAULT 0 NOT NULL,
    reference text NOT NULL,
    provider_reference text DEFAULT ''::text NOT NULL,
    idempotency_key text NOT NULL,
    request_fingerprint text NOT NULL,
    destination_snapshot jsonb NOT NULL,
    status text NOT NULL,
    provider_status text DEFAULT ''::text NOT NULL,
    failure_code text DEFAULT ''::text NOT NULL,
    failure_message text DEFAULT ''::text NOT NULL,
    initiated_at timestamp with time zone,
    completed_at timestamp with time zone,
    reversed_at timestamp with time zone,
    last_reconciled_at timestamp with time zone,
    version bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    reconciliation_lease_owner text DEFAULT ''::text NOT NULL,
    reconciliation_lease_expires_at timestamp with time zone,
    CONSTRAINT payouts_amount_positive_check CHECK ((amount_minor > 0)),
    CONSTRAINT payouts_country_code_check CHECK ((country_code ~ '^[A-Z]{2}$'::text)),
    CONSTRAINT payouts_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT payouts_fee_nonnegative_check CHECK ((fee_minor >= 0)),
    CONSTRAINT payouts_idempotency_key_shape_check CHECK ((idempotency_key ~ '^[A-Za-z0-9_-]{16,128}$'::text)),
    CONSTRAINT payouts_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT payouts_reconciliation_lease_check CHECK ((((reconciliation_lease_owner = ''::text) AND (reconciliation_lease_expires_at IS NULL)) OR ((NULLIF(btrim(reconciliation_lease_owner), ''::text) IS NOT NULL) AND (reconciliation_lease_expires_at IS NOT NULL)))),
    CONSTRAINT payouts_request_fingerprint_shape_check CHECK ((request_fingerprint ~ '^[a-f0-9]{64}$'::text)),
    CONSTRAINT payouts_required_text_check CHECK (((NULLIF(btrim(rail), ''::text) IS NOT NULL) AND (NULLIF(btrim(reference), ''::text) IS NOT NULL))),
    CONSTRAINT payouts_status_check CHECK ((status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'successful'::text, 'failed'::text, 'reversed'::text, 'cancelled'::text, 'unknown'::text]))),
    CONSTRAINT payouts_version_positive_check CHECK ((version > 0))
);


--
-- Name: promotion_redemptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_redemptions (
    id uuid NOT NULL,
    promotion_id uuid NOT NULL,
    client_id uuid NOT NULL,
    booking_id uuid,
    customer_id uuid,
    customer_email text DEFAULT ''::text NOT NULL,
    code_used text DEFAULT ''::text NOT NULL,
    discount_amount_minor bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    currency_code text NOT NULL,
    CONSTRAINT promotion_redemptions_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text))
);


--
-- Name: promotion_sections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_sections (
    promotion_id uuid NOT NULL,
    section_id uuid NOT NULL
);


--
-- Name: promotion_services; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotion_services (
    promotion_id uuid NOT NULL,
    service_id uuid NOT NULL
);


--
-- Name: promotions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.promotions (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    name text NOT NULL,
    promotion_type text NOT NULL,
    code text DEFAULT ''::text NOT NULL,
    discount_type text NOT NULL,
    scope_type text DEFAULT 'all_services'::text NOT NULL,
    starts_at timestamp with time zone NOT NULL,
    ends_at timestamp with time zone,
    is_active boolean DEFAULT true NOT NULL,
    max_redemptions integer DEFAULT 0 NOT NULL,
    max_redemptions_per_customer integer DEFAULT 0 NOT NULL,
    minimum_spend_minor bigint DEFAULT 0 NOT NULL,
    first_time_customers_only boolean DEFAULT false NOT NULL,
    applies_to_deposit boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    stack_with_automatic_discounts boolean DEFAULT false NOT NULL,
    currency_code text NOT NULL,
    discount_percentage_bps bigint DEFAULT 0 NOT NULL,
    discount_value_minor bigint DEFAULT 0 NOT NULL,
    CONSTRAINT promotions_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT promotions_discount_value_shape_check CHECK ((((discount_type = 'percentage'::text) AND ((discount_percentage_bps >= 1) AND (discount_percentage_bps <= 10000)) AND (discount_value_minor = 0)) OR ((discount_type = ANY (ARRAY['fixed_amount'::text, 'set_price'::text])) AND (discount_percentage_bps = 0) AND (discount_value_minor > 0))))
);


--
-- Name: provider_availability_windows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_availability_windows (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    day_of_week smallint NOT NULL,
    start_time time without time zone NOT NULL,
    end_time time without time zone NOT NULL,
    slot_interval_minutes integer DEFAULT 30 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_availability_windows_day_of_week_check CHECK (((day_of_week >= 0) AND (day_of_week <= 6)))
);


--
-- Name: provider_daily_metric_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_daily_metric_jobs (
    client_id uuid NOT NULL,
    metric_date date NOT NULL,
    currency_code text NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    enqueued_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_daily_metric_jobs_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT provider_daily_metric_jobs_revision_check CHECK ((revision > 0))
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01', autovacuum_vacuum_threshold='100', autovacuum_analyze_threshold='100');


--
-- Name: provider_daily_metrics; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_daily_metrics (
    client_id uuid NOT NULL,
    metric_date date NOT NULL,
    currency_code text NOT NULL,
    total_bookings integer DEFAULT 0 NOT NULL,
    completed_bookings integer DEFAULT 0 NOT NULL,
    scheduled_bookings integer DEFAULT 0 NOT NULL,
    cancelled_bookings integer DEFAULT 0 NOT NULL,
    secured_bookings integer DEFAULT 0 NOT NULL,
    unique_customers integer DEFAULT 0 NOT NULL,
    booked_value_minor bigint DEFAULT 0 NOT NULL,
    gross_revenue_minor bigint DEFAULT 0 NOT NULL,
    net_revenue_minor bigint DEFAULT 0 NOT NULL,
    payment_count integer DEFAULT 0 NOT NULL,
    projected_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_daily_metrics_booked_value_minor_check CHECK ((booked_value_minor >= 0)),
    CONSTRAINT provider_daily_metrics_cancelled_bookings_check CHECK ((cancelled_bookings >= 0)),
    CONSTRAINT provider_daily_metrics_completed_bookings_check CHECK ((completed_bookings >= 0)),
    CONSTRAINT provider_daily_metrics_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT provider_daily_metrics_gross_revenue_minor_check CHECK ((gross_revenue_minor >= 0)),
    CONSTRAINT provider_daily_metrics_net_revenue_minor_check CHECK ((net_revenue_minor >= 0)),
    CONSTRAINT provider_daily_metrics_payment_count_check CHECK ((payment_count >= 0)),
    CONSTRAINT provider_daily_metrics_scheduled_bookings_check CHECK ((scheduled_bookings >= 0)),
    CONSTRAINT provider_daily_metrics_secured_bookings_check CHECK ((secured_bookings >= 0)),
    CONSTRAINT provider_daily_metrics_total_bookings_check CHECK ((total_bookings >= 0)),
    CONSTRAINT provider_daily_metrics_unique_customers_check CHECK ((unique_customers >= 0))
);


--
-- Name: provider_notification_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_notification_preferences (
    client_id uuid NOT NULL,
    booking_email boolean DEFAULT true NOT NULL,
    booking_whatsapp boolean DEFAULT false NOT NULL,
    appointment_reminder_enabled boolean DEFAULT true NOT NULL,
    appointment_reminder_minutes integer DEFAULT 1440 NOT NULL,
    whatsapp_e164 text,
    whatsapp_verified_at timestamp with time zone,
    whatsapp_verification_method text DEFAULT ''::text NOT NULL,
    whatsapp_verification_revision bigint DEFAULT 0 NOT NULL,
    preference_revision bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_notification_preferences_enabled_check CHECK (((NOT booking_whatsapp) OR (whatsapp_verified_at IS NOT NULL))),
    CONSTRAINT provider_notification_preferences_phone_check CHECK (((whatsapp_e164 IS NULL) OR (whatsapp_e164 ~ '^\+[1-9][0-9]{7,14}$'::text))),
    CONSTRAINT provider_notification_preferences_reminder_check CHECK ((appointment_reminder_minutes = 1440)),
    CONSTRAINT provider_notification_preferences_revision_check CHECK (((preference_revision > 0) AND (whatsapp_verification_revision >= 0))),
    CONSTRAINT provider_notification_preferences_verification_check CHECK ((((whatsapp_verified_at IS NULL) AND (whatsapp_verification_method = ''::text)) OR ((whatsapp_e164 IS NOT NULL) AND (whatsapp_verified_at IS NOT NULL) AND (whatsapp_verification_method = 'inbound_challenge'::text))))
);


--
-- Name: provider_portfolio_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_portfolio_items (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid,
    title text DEFAULT ''::text NOT NULL,
    image_url text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_reviews; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_reviews (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    customer_id uuid,
    author_name text DEFAULT ''::text NOT NULL,
    rating smallint NOT NULL,
    review_text text DEFAULT ''::text NOT NULL,
    image_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    booking_id uuid,
    service_id uuid,
    status text DEFAULT 'pending'::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_reviews_rating_check CHECK (((rating >= 1) AND (rating <= 5))),
    CONSTRAINT provider_reviews_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'hidden'::text])))
);


--
-- Name: provider_settlement_evidence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settlement_evidence (
    id uuid NOT NULL,
    provider text NOT NULL,
    settlement_reference text NOT NULL,
    payment_reference text NOT NULL,
    payment_id uuid,
    provider_status text NOT NULL,
    amount_minor bigint NOT NULL,
    currency_code text NOT NULL,
    status text NOT NULL,
    available_at timestamp with time zone NOT NULL,
    observed_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_settlement_evidence_amount_check CHECK ((amount_minor > 0)),
    CONSTRAINT provider_settlement_evidence_currency_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT provider_settlement_evidence_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT provider_settlement_evidence_reference_check CHECK (((NULLIF(btrim(settlement_reference), ''::text) IS NOT NULL) AND (NULLIF(btrim(payment_reference), ''::text) IS NOT NULL))),
    CONSTRAINT provider_settlement_evidence_status_check CHECK ((status = ANY (ARRAY['unmatched'::text, 'available'::text, 'mismatched'::text])))
);


--
-- Name: provider_settlement_sync_states; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settlement_sync_states (
    provider text NOT NULL,
    cursor_at timestamp with time zone NOT NULL,
    last_success_at timestamp with time zone,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_settlement_sync_lease_check CHECK ((((lease_owner = ''::text) AND (lease_expires_at IS NULL)) OR ((NULLIF(btrim(lease_owner), ''::text) IS NOT NULL) AND (lease_expires_at IS NOT NULL)))),
    CONSTRAINT provider_settlement_sync_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text])))
);


--
-- Name: provider_webhook_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_webhook_events (
    id uuid NOT NULL,
    provider text NOT NULL,
    provider_event_id text DEFAULT ''::text NOT NULL,
    body_sha256 bytea NOT NULL,
    event_type text NOT NULL,
    raw_body_ciphertext bytea NOT NULL,
    raw_body_nonce bytea NOT NULL,
    encryption_key_version text NOT NULL,
    normalized_event jsonb NOT NULL,
    processing_status text DEFAULT 'pending'::text NOT NULL,
    processing_attempts integer DEFAULT 0 NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    verified_at timestamp with time zone NOT NULL,
    processed_at timestamp with time zone,
    next_attempt_at timestamp with time zone DEFAULT now() NOT NULL,
    processing_result text DEFAULT ''::text NOT NULL,
    processing_error text DEFAULT ''::text NOT NULL,
    processing_lease_expires_at timestamp with time zone,
    CONSTRAINT provider_webhook_events_attempts_check CHECK ((processing_attempts >= 0)),
    CONSTRAINT provider_webhook_events_body_hash_length_check CHECK ((octet_length(body_sha256) = 32)),
    CONSTRAINT provider_webhook_events_processing_status_check CHECK ((processing_status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'failed'::text]))),
    CONSTRAINT provider_webhook_events_provider_check CHECK ((provider = ANY (ARRAY['payaza'::text, 'paystack'::text]))),
    CONSTRAINT provider_webhook_events_required_text_check CHECK (((NULLIF(btrim(event_type), ''::text) IS NOT NULL) AND (NULLIF(btrim(encryption_key_version), ''::text) IS NOT NULL)))
);


--
-- Name: provider_whatsapp_verification_challenges; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_whatsapp_verification_challenges (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    destination_hmac bytea NOT NULL,
    verification_revision bigint NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT provider_whatsapp_challenge_attempt_check CHECK (((attempt_count >= 0) AND (attempt_count <= 5))),
    CONSTRAINT provider_whatsapp_challenge_expiry_check CHECK ((expires_at > created_at)),
    CONSTRAINT provider_whatsapp_challenge_hash_check CHECK (((octet_length(token_hash) = 32) AND (octet_length(destination_hmac) = 32))),
    CONSTRAINT provider_whatsapp_challenge_revision_check CHECK ((verification_revision > 0))
);


--
-- Name: public_provider_resource_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.public_provider_resource_revisions (
    client_id uuid NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT public_provider_resource_revisions_revision_check CHECK ((revision > 0))
);


--
-- Name: resolved_locations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.resolved_locations (
    id uuid NOT NULL,
    public_token text NOT NULL,
    provider text NOT NULL,
    provider_place_id text,
    formatted_address text NOT NULL,
    latitude numeric(9,6),
    longitude numeric(9,6),
    address_source text NOT NULL,
    resolution_status text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    country_code text,
    state_region_id uuid,
    lga_region_id uuid,
    locality text DEFAULT ''::text NOT NULL,
    geog public.geography(Point,4326) GENERATED ALWAYS AS (
CASE
    WHEN ((latitude IS NULL) OR (longitude IS NULL)) THEN NULL::public.geography
    ELSE (public.st_setsrid(public.st_makepoint((longitude)::double precision, (latitude)::double precision), 4326))::public.geography
END) STORED,
    CONSTRAINT resolved_locations_address_source_check CHECK ((address_source = ANY (ARRAY['manual'::text, 'google_place'::text, 'current_location'::text]))),
    CONSTRAINT resolved_locations_coordinate_pair_check CHECK ((((latitude IS NULL) AND (longitude IS NULL)) OR ((latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT resolved_locations_coordinate_range_check CHECK ((((latitude IS NULL) OR ((latitude >= ('-90'::integer)::numeric) AND (latitude <= (90)::numeric))) AND ((longitude IS NULL) OR ((longitude >= ('-180'::integer)::numeric) AND (longitude <= (180)::numeric))))),
    CONSTRAINT resolved_locations_country_code_check CHECK (((country_code IS NULL) OR (country_code ~ '^[A-Z]{2}$'::text))),
    CONSTRAINT resolved_locations_provider_check CHECK ((provider = ANY (ARRAY['manual'::text, 'google'::text]))),
    CONSTRAINT resolved_locations_required_address_check CHECK ((NULLIF(btrim(formatted_address), ''::text) IS NOT NULL)),
    CONSTRAINT resolved_locations_resolution_coordinate_check CHECK ((((resolution_status = 'text_only'::text) AND (latitude IS NULL) AND (longitude IS NULL)) OR ((resolution_status = 'coordinates_resolved'::text) AND (latitude IS NOT NULL) AND (longitude IS NOT NULL)))),
    CONSTRAINT resolved_locations_resolution_status_check CHECK ((resolution_status = ANY (ARRAY['text_only'::text, 'coordinates_resolved'::text])))
)
WITH (autovacuum_vacuum_scale_factor='0.05', autovacuum_analyze_scale_factor='0.02', autovacuum_vacuum_threshold='500', autovacuum_analyze_threshold='500');


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version character varying NOT NULL
);


--
-- Name: service_availability_windows; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_availability_windows (
    id uuid NOT NULL,
    service_id uuid NOT NULL,
    day_of_week smallint NOT NULL,
    start_time time without time zone NOT NULL,
    end_time time without time zone NOT NULL,
    slot_interval_minutes integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_availability_windows_day_check CHECK (((day_of_week >= 0) AND (day_of_week <= 6))),
    CONSTRAINT service_availability_windows_interval_check CHECK ((slot_interval_minutes > 0)),
    CONSTRAINT service_availability_windows_time_check CHECK ((end_time > start_time))
);


--
-- Name: service_sections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_sections (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    name text NOT NULL,
    slug text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    cover_image_url text,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: service_short_notice_rules; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_short_notice_rules (
    id uuid NOT NULL,
    service_id uuid NOT NULL,
    threshold_minutes integer NOT NULL,
    surcharge_type text NOT NULL,
    surcharge_amount_minor bigint DEFAULT 0 NOT NULL,
    surcharge_percentage_bps integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_short_notice_rules_threshold_check CHECK ((threshold_minutes > 0)),
    CONSTRAINT service_short_notice_rules_type_check CHECK ((surcharge_type = ANY (ARRAY['fixed_amount'::text, 'percentage'::text]))),
    CONSTRAINT service_short_notice_rules_value_check CHECK ((((surcharge_type = 'fixed_amount'::text) AND (surcharge_amount_minor > 0) AND (surcharge_percentage_bps = 0)) OR ((surcharge_type = 'percentage'::text) AND (surcharge_amount_minor = 0) AND ((surcharge_percentage_bps >= 1) AND (surcharge_percentage_bps <= 10000)))))
);


--
-- Name: service_wizard_drafts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.service_wizard_drafts (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    service_id uuid,
    payload jsonb NOT NULL,
    current_step text NOT NULL,
    revision bigint DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT service_wizard_drafts_current_step_check CHECK ((current_step = ANY (ARRAY['choose-section'::text, 'info'::text, 'pricing'::text, 'duration'::text, 'availability'::text, 'location'::text, 'policy'::text, 'agreement-settings'::text, 'preview'::text]))),
    CONSTRAINT service_wizard_drafts_payload_object_check CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT service_wizard_drafts_revision_check CHECK ((revision > 0))
);


--
-- Name: services; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.services (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    title text NOT NULL,
    slug text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    category text DEFAULT ''::text NOT NULL,
    icon_name text DEFAULT ''::text NOT NULL,
    image_url text,
    duration_minutes integer DEFAULT 0 NOT NULL,
    price_amount_minor bigint DEFAULT 0 NOT NULL,
    is_active boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    section_id uuid,
    status text DEFAULT 'published'::text NOT NULL,
    compare_price_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_required boolean DEFAULT false NOT NULL,
    deposit_type text DEFAULT 'fixed'::text NOT NULL,
    deposit_amount_minor bigint DEFAULT 0 NOT NULL,
    deposit_percentage_bps integer DEFAULT 0 NOT NULL,
    prep_time_minutes integer DEFAULT 0 NOT NULL,
    buffer_time_minutes integer DEFAULT 0 NOT NULL,
    availability_mode text DEFAULT 'inherit_business_hours'::text NOT NULL,
    minimum_notice_minutes integer DEFAULT 0 NOT NULL,
    max_bookings_per_day integer DEFAULT 0 NOT NULL,
    fulfillment_mode text DEFAULT 'provider_location'::text NOT NULL,
    travel_fee_minor bigint DEFAULT 0 NOT NULL,
    max_travel_distance_meters integer,
    cancellation_policy text DEFAULT ''::text NOT NULL,
    lateness_policy text DEFAULT ''::text NOT NULL,
    prep_aftercare_instructions text DEFAULT ''::text NOT NULL,
    badge text DEFAULT ''::text NOT NULL,
    agreement_timing text DEFAULT 'before_payment'::text,
    is_hidden boolean DEFAULT false NOT NULL,
    currency_code text NOT NULL,
    provider_location_id uuid,
    virtual_delivery_label text DEFAULT ''::text NOT NULL,
    virtual_join_url text,
    virtual_instructions text,
    agreement_template_family_id uuid,
    standalone_signature_required boolean DEFAULT false NOT NULL,
    CONSTRAINT services_agreement_configuration_check CHECK ((((agreement_template_family_id IS NULL) AND (agreement_timing IS NULL)) OR ((agreement_template_family_id IS NOT NULL) AND (agreement_timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text])) AND (standalone_signature_required = false)))),
    CONSTRAINT services_availability_mode_check CHECK ((availability_mode = ANY (ARRAY['inherit_business_hours'::text, 'custom'::text]))),
    CONSTRAINT services_currency_code_check CHECK ((currency_code ~ '^[A-Z]{3}$'::text)),
    CONSTRAINT services_deposit_percentage_bps_check CHECK (((deposit_percentage_bps >= 0) AND (deposit_percentage_bps <= 10000))),
    CONSTRAINT services_fulfillment_mode_check CHECK ((fulfillment_mode = ANY (ARRAY['provider_location'::text, 'customer_location'::text, 'virtual'::text]))),
    CONSTRAINT services_max_travel_distance_check CHECK (((max_travel_distance_meters IS NULL) OR ((fulfillment_mode = 'customer_location'::text) AND (max_travel_distance_meters > 0)))),
    CONSTRAINT services_nonnegative_scheduling_check CHECK (((minimum_notice_minutes >= 0) AND (prep_time_minutes >= 0) AND (buffer_time_minutes >= 0) AND (max_bookings_per_day >= 0))),
    CONSTRAINT services_published_location_check CHECK (((status <> 'published'::text) OR (fulfillment_mode = 'virtual'::text) OR (provider_location_id IS NOT NULL))),
    CONSTRAINT services_travel_fee_check CHECK (((travel_fee_minor >= 0) AND ((fulfillment_mode = 'customer_location'::text) OR (travel_fee_minor = 0)))),
    CONSTRAINT services_virtual_fields_check CHECK (((fulfillment_mode = 'virtual'::text) OR ((virtual_delivery_label = ''::text) AND (virtual_join_url IS NULL) AND (virtual_instructions IS NULL))))
);


--
-- Name: tessa_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_events (
    sequence bigint NOT NULL,
    client_id uuid NOT NULL,
    thread_id uuid NOT NULL,
    message_id uuid,
    run_id uuid,
    event_type text NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tessa_events_payload_check CHECK (((jsonb_typeof(payload) = 'object'::text) AND (octet_length((payload)::text) <= 4096))),
    CONSTRAINT tessa_events_reference_check CHECK ((((event_type = 'thread.created'::text) AND (message_id IS NULL) AND (run_id IS NULL)) OR ((event_type = 'message.created'::text) AND (message_id IS NOT NULL)) OR ((event_type ~~ 'run.%'::text) AND (run_id IS NOT NULL)))),
    CONSTRAINT tessa_events_type_check CHECK ((event_type = ANY (ARRAY['thread.created'::text, 'message.created'::text, 'run.started'::text, 'run.stage_changed'::text, 'run.completed'::text, 'run.failed'::text, 'run.cancelled'::text])))
)
WITH (autovacuum_vacuum_scale_factor='0.02', autovacuum_analyze_scale_factor='0.01', autovacuum_vacuum_threshold='1000', autovacuum_analyze_threshold='1000');


--
-- Name: tessa_events_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.tessa_events ALTER COLUMN sequence ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME public.tessa_events_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: tessa_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_messages (
    id uuid NOT NULL,
    thread_id uuid NOT NULL,
    client_id uuid NOT NULL,
    sequence bigint NOT NULL,
    sender_type text NOT NULL,
    source_channel text DEFAULT 'web'::text NOT NULL,
    client_message_id uuid,
    request_fingerprint text,
    content text NOT NULL,
    presentation jsonb DEFAULT '{}'::jsonb NOT NULL,
    entity_references jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    run_id uuid,
    CONSTRAINT tessa_messages_channel_check CHECK ((source_channel = 'web'::text)),
    CONSTRAINT tessa_messages_content_check CHECK (((btrim(content) <> ''::text) AND (char_length(content) <= 4000))),
    CONSTRAINT tessa_messages_idempotency_check CHECK ((((sender_type = 'provider'::text) AND (client_message_id IS NOT NULL) AND (request_fingerprint ~ '^[a-f0-9]{64}$'::text)) OR ((sender_type <> 'provider'::text) AND (client_message_id IS NULL) AND (request_fingerprint IS NULL)))),
    CONSTRAINT tessa_messages_presentation_check CHECK (((jsonb_typeof(presentation) = 'object'::text) AND (jsonb_typeof(entity_references) = 'array'::text) AND (octet_length((presentation)::text) <= 4096) AND (octet_length((entity_references)::text) <= 8192))),
    CONSTRAINT tessa_messages_result_run_check CHECK ((((sender_type = 'tessa'::text) AND (run_id IS NOT NULL)) OR ((sender_type <> 'tessa'::text) AND (run_id IS NULL)))),
    CONSTRAINT tessa_messages_sender_check CHECK ((sender_type = ANY (ARRAY['provider'::text, 'tessa'::text, 'system'::text]))),
    CONSTRAINT tessa_messages_sequence_check CHECK ((sequence > 0))
);


--
-- Name: tessa_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_preferences (
    client_id uuid NOT NULL,
    introduction_completed_at timestamp with time zone,
    acknowledged_notice_revision text DEFAULT ''::text NOT NULL,
    notice_acknowledged_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tessa_preferences_notice_check CHECK ((((acknowledged_notice_revision = ''::text) AND (notice_acknowledged_at IS NULL)) OR ((btrim(acknowledged_notice_revision) <> ''::text) AND (notice_acknowledged_at IS NOT NULL)))),
    CONSTRAINT tessa_preferences_notice_revision_length_check CHECK ((char_length(acknowledged_notice_revision) <= 80))
);


--
-- Name: tessa_run_steps; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_run_steps (
    id uuid NOT NULL,
    run_id uuid NOT NULL,
    sequence integer NOT NULL,
    stage text NOT NULL,
    idempotency_key text,
    provider text DEFAULT ''::text NOT NULL,
    model text DEFAULT ''::text NOT NULL,
    tool_name text DEFAULT ''::text NOT NULL,
    safe_argument_hash text DEFAULT ''::text NOT NULL,
    status text NOT NULL,
    safe_result jsonb DEFAULT '{}'::jsonb NOT NULL,
    safe_result_count integer DEFAULT 0 NOT NULL,
    duration_ms integer DEFAULT 0 NOT NULL,
    input_tokens integer,
    output_tokens integer,
    error_code text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    CONSTRAINT tessa_run_steps_hash_check CHECK (((safe_argument_hash = ''::text) OR (safe_argument_hash ~ '^[a-f0-9]{64}$'::text))),
    CONSTRAINT tessa_run_steps_lengths_check CHECK (((char_length(COALESCE(idempotency_key, ''::text)) <= 160) AND (char_length(provider) <= 40) AND (char_length(model) <= 160) AND (char_length(tool_name) <= 80) AND (char_length(error_code) <= 80))),
    CONSTRAINT tessa_run_steps_result_check CHECK (((jsonb_typeof(safe_result) = 'object'::text) AND (octet_length((safe_result)::text) <= 4096) AND (safe_result_count >= 0) AND (duration_ms >= 0))),
    CONSTRAINT tessa_run_steps_sequence_check CHECK ((sequence > 0)),
    CONSTRAINT tessa_run_steps_stage_check CHECK ((stage = ANY (ARRAY['planning'::text, 'tool'::text, 'synthesis'::text]))),
    CONSTRAINT tessa_run_steps_status_check CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT tessa_run_steps_tool_key_check CHECK ((((stage = 'tool'::text) AND (btrim(COALESCE(idempotency_key, ''::text)) <> ''::text) AND (btrim(tool_name) <> ''::text)) OR ((stage <> 'tool'::text) AND (idempotency_key IS NULL) AND (tool_name = ''::text))))
);


--
-- Name: tessa_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_runs (
    id uuid NOT NULL,
    thread_id uuid NOT NULL,
    client_id uuid NOT NULL,
    trigger_message_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    stage text DEFAULT 'queued'::text NOT NULL,
    lease_owner text DEFAULT ''::text NOT NULL,
    lease_token uuid,
    lease_expires_at timestamp with time zone,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    input_hash text NOT NULL,
    context_hash text DEFAULT ''::text NOT NULL,
    schema_revision text NOT NULL,
    config_hash text NOT NULL,
    primary_provider text NOT NULL,
    primary_model text NOT NULL,
    final_provider text DEFAULT ''::text NOT NULL,
    final_model text DEFAULT ''::text NOT NULL,
    fallback_used boolean DEFAULT false NOT NULL,
    fallback_reason text DEFAULT ''::text NOT NULL,
    error_code text DEFAULT ''::text NOT NULL,
    queue_latency_ms integer,
    generation_latency_ms integer,
    total_latency_ms integer,
    input_tokens integer,
    output_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    cancelled_at timestamp with time zone,
    CONSTRAINT tessa_runs_attempt_check CHECK (((attempt_count >= 0) AND ((max_attempts >= 1) AND (max_attempts <= 5)) AND (attempt_count <= max_attempts))),
    CONSTRAINT tessa_runs_hash_check CHECK (((input_hash ~ '^[a-f0-9]{64}$'::text) AND ((context_hash = ''::text) OR (context_hash ~ '^[a-f0-9]{64}$'::text)) AND (config_hash ~ '^[a-f0-9]{64}$'::text))),
    CONSTRAINT tessa_runs_identity_length_check CHECK ((((char_length(schema_revision) >= 1) AND (char_length(schema_revision) <= 80)) AND ((char_length(primary_provider) >= 1) AND (char_length(primary_provider) <= 40)) AND ((char_length(primary_model) >= 1) AND (char_length(primary_model) <= 160)) AND (char_length(final_provider) <= 40) AND (char_length(final_model) <= 160) AND (char_length(fallback_reason) <= 80) AND (char_length(error_code) <= 80))),
    CONSTRAINT tessa_runs_lease_check CHECK ((((status = 'processing'::text) AND (btrim(lease_owner) <> ''::text) AND (lease_token IS NOT NULL) AND (lease_expires_at IS NOT NULL)) OR ((status <> 'processing'::text) AND (lease_owner = ''::text) AND (lease_token IS NULL) AND (lease_expires_at IS NULL)))),
    CONSTRAINT tessa_runs_stage_check CHECK ((stage = ANY (ARRAY['queued'::text, 'planning'::text, 'checking_help'::text, 'checking_business'::text, 'checking_bookings'::text, 'checking_schedule'::text, 'checking_availability'::text, 'answering'::text, 'completed'::text]))),
    CONSTRAINT tessa_runs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'cancelled'::text]))),
    CONSTRAINT tessa_runs_terminal_check CHECK ((((status = ANY (ARRAY['queued'::text, 'processing'::text])) AND (completed_at IS NULL) AND (cancelled_at IS NULL)) OR ((status = ANY (ARRAY['completed'::text, 'failed'::text])) AND (completed_at IS NOT NULL) AND (cancelled_at IS NULL)) OR ((status = 'cancelled'::text) AND (completed_at IS NULL) AND (cancelled_at IS NOT NULL))))
);


--
-- Name: tessa_threads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tessa_threads (
    id uuid NOT NULL,
    client_id uuid NOT NULL,
    creation_request_id uuid NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    summary text DEFAULT ''::text NOT NULL,
    summary_through_sequence bigint DEFAULT 0 NOT NULL,
    summary_revision integer DEFAULT 1 NOT NULL,
    last_message_sequence bigint DEFAULT 0 NOT NULL,
    last_activity_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    CONSTRAINT tessa_threads_archive_check CHECK ((((status = 'active'::text) AND (archived_at IS NULL)) OR ((status = 'archived'::text) AND (archived_at IS NOT NULL)))),
    CONSTRAINT tessa_threads_sequence_check CHECK (((summary_through_sequence >= 0) AND (last_message_sequence >= summary_through_sequence) AND (summary_revision >= 1))),
    CONSTRAINT tessa_threads_status_check CHECK ((status = ANY (ARRAY['active'::text, 'archived'::text]))),
    CONSTRAINT tessa_threads_summary_check CHECK ((char_length(summary) <= 12000))
);


--
-- Name: administrative_regions administrative_regions_country_code_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrative_regions
    ADD CONSTRAINT administrative_regions_country_code_code_key UNIQUE (country_code, code);


--
-- Name: administrative_regions administrative_regions_parent_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrative_regions
    ADD CONSTRAINT administrative_regions_parent_id_slug_key UNIQUE (parent_id, slug);


--
-- Name: administrative_regions administrative_regions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrative_regions
    ADD CONSTRAINT administrative_regions_pkey PRIMARY KEY (id);


--
-- Name: agreement_acceptances agreement_acceptances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_acceptances
    ADD CONSTRAINT agreement_acceptances_pkey PRIMARY KEY (agreement_id);


--
-- Name: agreement_events agreement_events_agreement_id_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_agreement_id_dedupe_key_key UNIQUE (agreement_id, dedupe_key);


--
-- Name: agreement_events agreement_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_pkey PRIMARY KEY (id);


--
-- Name: agreement_instances agreement_instances_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_pkey PRIMARY KEY (id);


--
-- Name: agreement_instances agreement_instances_public_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_public_token_hash_key UNIQUE (public_token_hash);


--
-- Name: agreement_jobs agreement_jobs_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_dedupe_key_key UNIQUE (dedupe_key);


--
-- Name: agreement_jobs agreement_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_families agreement_template_families_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_pkey PRIMARY KEY (id);


--
-- Name: agreement_template_versions agreement_template_versions_family_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_id_key UNIQUE (family_id, id);


--
-- Name: agreement_template_versions agreement_template_versions_family_id_version_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_version_number_key UNIQUE (family_id, version_number);


--
-- Name: agreement_template_versions agreement_template_versions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_pkey PRIMARY KEY (id);


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_pkey PRIMARY KEY (id);


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_token_hash_key UNIQUE (token_hash);


--
-- Name: auth_pending_registrations auth_pending_registrations_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_pending_registrations
    ADD CONSTRAINT auth_pending_registrations_email_key UNIQUE (email);


--
-- Name: auth_pending_registrations auth_pending_registrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_pending_registrations
    ADD CONSTRAINT auth_pending_registrations_pkey PRIMARY KEY (id);


--
-- Name: auth_refresh_sessions auth_refresh_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_pkey PRIMARY KEY (id);


--
-- Name: auth_refresh_sessions auth_refresh_sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: automation_settings automation_settings_client_id_automation_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_client_id_automation_key_key UNIQUE (client_id, automation_key);


--
-- Name: automation_settings automation_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_pkey PRIMARY KEY (id);


--
-- Name: booking_change_commands booking_change_commands_actor_type_actor_id_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_commands
    ADD CONSTRAINT booking_change_commands_actor_type_actor_id_idempotency_key_key UNIQUE (actor_type, actor_id, idempotency_key);


--
-- Name: booking_change_commands booking_change_commands_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_commands
    ADD CONSTRAINT booking_change_commands_pkey PRIMARY KEY (id);


--
-- Name: booking_change_quotes booking_change_quotes_marketplace_customer_id_kind_idempote_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_quotes
    ADD CONSTRAINT booking_change_quotes_marketplace_customer_id_kind_idempote_key UNIQUE (marketplace_customer_id, kind, idempotency_key);


--
-- Name: booking_change_quotes booking_change_quotes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_quotes
    ADD CONSTRAINT booking_change_quotes_pkey PRIMARY KEY (id);


--
-- Name: booking_change_quotes booking_change_quotes_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_quotes
    ADD CONSTRAINT booking_change_quotes_public_token_key UNIQUE (public_token);


--
-- Name: booking_domain_events booking_domain_events_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_domain_events
    ADD CONSTRAINT booking_domain_events_dedupe_key_key UNIQUE (dedupe_key);


--
-- Name: booking_domain_events booking_domain_events_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_domain_events
    ADD CONSTRAINT booking_domain_events_id_key UNIQUE (id);


--
-- Name: booking_domain_events booking_domain_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_domain_events
    ADD CONSTRAINT booking_domain_events_pkey PRIMARY KEY (sequence);


--
-- Name: booking_quote_promotions booking_quote_promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_pkey PRIMARY KEY (booking_quote_id, promotion_id);


--
-- Name: booking_quotes booking_quotes_booking_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_booking_id_key UNIQUE (booking_id);


--
-- Name: booking_quotes booking_quotes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_pkey PRIMARY KEY (id);


--
-- Name: booking_quotes booking_quotes_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_public_token_key UNIQUE (public_token);


--
-- Name: booking_refund_attempts booking_refund_attempts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_attempts
    ADD CONSTRAINT booking_refund_attempts_pkey PRIMARY KEY (id);


--
-- Name: booking_refund_attempts booking_refund_attempts_request_id_payment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_attempts
    ADD CONSTRAINT booking_refund_attempts_request_id_payment_id_key UNIQUE (request_id, payment_id);


--
-- Name: booking_refund_requests booking_refund_requests_command_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_requests
    ADD CONSTRAINT booking_refund_requests_command_id_key UNIQUE (command_id);


--
-- Name: booking_refund_requests booking_refund_requests_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_requests
    ADD CONSTRAINT booking_refund_requests_pkey PRIMARY KEY (id);


--
-- Name: booking_standalone_signatures booking_standalone_signatures_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_standalone_signatures
    ADD CONSTRAINT booking_standalone_signatures_pkey PRIMARY KEY (booking_id);


--
-- Name: bookings bookings_booking_quote_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_booking_quote_id_key UNIQUE (booking_quote_id);


--
-- Name: bookings bookings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_pkey PRIMARY KEY (id);


--
-- Name: bookings bookings_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_public_token_key UNIQUE (public_token);


--
-- Name: business_balance_entries business_balance_entries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_pkey PRIMARY KEY (id);


--
-- Name: business_locations business_locations_client_id_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_client_id_id_key UNIQUE (client_id, id);


--
-- Name: business_locations business_locations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_pkey PRIMARY KEY (id);


--
-- Name: client_profile_handles client_profile_handles_client_id_handle_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_client_id_handle_slug_key UNIQUE (client_id, handle_slug);


--
-- Name: client_profile_handles client_profile_handles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_pkey PRIMARY KEY (handle_slug);


--
-- Name: client_profiles client_profiles_handle_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_handle_slug_key UNIQUE (handle_slug);


--
-- Name: client_profiles client_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_pkey PRIMARY KEY (client_id);


--
-- Name: customers customers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_pkey PRIMARY KEY (id);


--
-- Name: financial_jobs financial_jobs_deduplication_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.financial_jobs
    ADD CONSTRAINT financial_jobs_deduplication_key_key UNIQUE (deduplication_key);


--
-- Name: financial_jobs financial_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.financial_jobs
    ADD CONSTRAINT financial_jobs_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_actions inbox_ai_actions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_actions inbox_ai_actions_session_id_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_session_id_idempotency_key_key UNIQUE (session_id, idempotency_key);


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmation_booking_session_id_idempotenc_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmation_booking_session_id_idempotenc_key UNIQUE (booking_session_id, idempotency_key);


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmation_booking_session_id_proposal_i_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmation_booking_session_id_proposal_i_key UNIQUE (booking_session_id, proposal_id, proposal_revision);


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_booking_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_booking_id_key UNIQUE (booking_id);


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_ai_session_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_ai_session_id_key UNIQUE (ai_session_id);


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_conversation_id_client_id_marketp_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_conversation_id_client_id_marketp_key UNIQUE (conversation_id, client_id, marketplace_customer_id);


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_conversation_controls inbox_ai_conversation_controls_conversation_id_client_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_conversation_controls
    ADD CONSTRAINT inbox_ai_conversation_controls_conversation_id_client_id_key UNIQUE (conversation_id, client_id);


--
-- Name: inbox_ai_conversation_controls inbox_ai_conversation_controls_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_conversation_controls
    ADD CONSTRAINT inbox_ai_conversation_controls_pkey PRIMARY KEY (conversation_id);


--
-- Name: inbox_ai_policies inbox_ai_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_policies
    ADD CONSTRAINT inbox_ai_policies_pkey PRIMARY KEY (client_id);


--
-- Name: inbox_ai_runs inbox_ai_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_sessions inbox_ai_sessions_conversation_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_conversation_id_key UNIQUE (conversation_id);


--
-- Name: inbox_ai_sessions inbox_ai_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_conversation_id_trigger_message_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_conversation_id_trigger_message_sequence_key UNIQUE (conversation_id, trigger_message_sequence);


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_pkey PRIMARY KEY (id);


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_trigger_message_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_trigger_message_id_key UNIQUE (trigger_message_id);


--
-- Name: inbox_conversation_bookings inbox_conversation_bookings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversation_bookings
    ADD CONSTRAINT inbox_conversation_bookings_pkey PRIMARY KEY (conversation_id, booking_id);


--
-- Name: inbox_conversation_moderation_events inbox_conversation_moderation_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversation_moderation_events
    ADD CONSTRAINT inbox_conversation_moderation_events_pkey PRIMARY KEY (id);


--
-- Name: inbox_conversations inbox_conversations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_pkey PRIMARY KEY (id);


--
-- Name: inbox_events inbox_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_events
    ADD CONSTRAINT inbox_events_pkey PRIMARY KEY (sequence);


--
-- Name: inbox_messages inbox_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_pkey PRIMARY KEY (id);


--
-- Name: inbox_messages inbox_messages_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_sequence_key UNIQUE (sequence);


--
-- Name: inbox_participant_states inbox_participant_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_participant_states
    ADD CONSTRAINT inbox_participant_states_pkey PRIMARY KEY (conversation_id, participant_type, participant_id);


--
-- Name: marketplace_auth_challenges marketplace_auth_challenges_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_auth_challenges
    ADD CONSTRAINT marketplace_auth_challenges_pkey PRIMARY KEY (id);


--
-- Name: marketplace_auth_sessions marketplace_auth_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_auth_sessions
    ADD CONSTRAINT marketplace_auth_sessions_pkey PRIMARY KEY (id);


--
-- Name: marketplace_auth_sessions marketplace_auth_sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_auth_sessions
    ADD CONSTRAINT marketplace_auth_sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: marketplace_categories marketplace_categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_categories
    ADD CONSTRAINT marketplace_categories_pkey PRIMARY KEY (id);


--
-- Name: marketplace_categories marketplace_categories_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_categories
    ADD CONSTRAINT marketplace_categories_slug_key UNIQUE (slug);


--
-- Name: marketplace_customer_addresses marketplace_customer_addresses_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_addresses
    ADD CONSTRAINT marketplace_customer_addresses_pkey PRIMARY KEY (id);


--
-- Name: marketplace_customer_identities marketplace_customer_identities_customer_type_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_identities
    ADD CONSTRAINT marketplace_customer_identities_customer_type_unique UNIQUE (marketplace_customer_id, identifier_type);


--
-- Name: marketplace_customer_identities marketplace_customer_identities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_identities
    ADD CONSTRAINT marketplace_customer_identities_pkey PRIMARY KEY (id);


--
-- Name: marketplace_customer_identities marketplace_customer_identities_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_identities
    ADD CONSTRAINT marketplace_customer_identities_unique UNIQUE (identifier_type, normalized_identifier);


--
-- Name: marketplace_customers marketplace_customers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customers
    ADD CONSTRAINT marketplace_customers_pkey PRIMARY KEY (id);


--
-- Name: marketplace_discovery_count_jobs marketplace_discovery_count_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_discovery_count_jobs
    ADD CONSTRAINT marketplace_discovery_count_jobs_pkey PRIMARY KEY (singleton);


--
-- Name: marketplace_discovery_counts marketplace_discovery_counts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_discovery_counts
    ADD CONSTRAINT marketplace_discovery_counts_pkey PRIMARY KEY (dimension, dimension_id);


--
-- Name: marketplace_discovery_jobs marketplace_discovery_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_discovery_jobs
    ADD CONSTRAINT marketplace_discovery_jobs_pkey PRIMARY KEY (client_id);


--
-- Name: marketplace_notification_preferences marketplace_notification_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notification_preferences
    ADD CONSTRAINT marketplace_notification_preferences_pkey PRIMARY KEY (marketplace_customer_id);


--
-- Name: marketplace_notifications marketplace_notifications_marketplace_customer_id_event_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_marketplace_customer_id_event_key_key UNIQUE (marketplace_customer_id, event_key);


--
-- Name: marketplace_notifications marketplace_notifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_pkey PRIMARY KEY (id);


--
-- Name: marketplace_provider_documents marketplace_provider_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_provider_documents
    ADD CONSTRAINT marketplace_provider_documents_pkey PRIMARY KEY (client_id);


--
-- Name: marketplace_saved_providers marketplace_saved_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_saved_providers
    ADD CONSTRAINT marketplace_saved_providers_pkey PRIMARY KEY (marketplace_customer_id, provider_id);


--
-- Name: marketplace_service_availability_days marketplace_service_availability_days_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_availability_days
    ADD CONSTRAINT marketplace_service_availability_days_pkey PRIMARY KEY (service_id, local_date);


--
-- Name: marketplace_service_documents marketplace_service_documents_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_pkey PRIMARY KEY (service_id);


--
-- Name: meta_whatsapp_webhook_receipts meta_whatsapp_webhook_receipts_dedupe_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meta_whatsapp_webhook_receipts
    ADD CONSTRAINT meta_whatsapp_webhook_receipts_dedupe_key_key UNIQUE (dedupe_key);


--
-- Name: meta_whatsapp_webhook_receipts meta_whatsapp_webhook_receipts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.meta_whatsapp_webhook_receipts
    ADD CONSTRAINT meta_whatsapp_webhook_receipts_pkey PRIMARY KEY (id);


--
-- Name: notification_contact_suppressions notification_contact_suppressions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_contact_suppressions
    ADD CONSTRAINT notification_contact_suppressions_pkey PRIMARY KEY (channel, destination_hmac);


--
-- Name: notification_deliveries notification_deliveries_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: notification_deliveries notification_deliveries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_pkey PRIMARY KEY (id);


--
-- Name: notification_event_jobs notification_event_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_event_jobs
    ADD CONSTRAINT notification_event_jobs_pkey PRIMARY KEY (booking_event_id);


--
-- Name: notification_in_app_jobs notification_in_app_jobs_idempotency_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_in_app_jobs
    ADD CONSTRAINT notification_in_app_jobs_idempotency_key_key UNIQUE (idempotency_key);


--
-- Name: notification_in_app_jobs notification_in_app_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_in_app_jobs
    ADD CONSTRAINT notification_in_app_jobs_pkey PRIMARY KEY (id);


--
-- Name: notification_scope_replan_jobs notification_scope_replan_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_scope_replan_jobs
    ADD CONSTRAINT notification_scope_replan_jobs_pkey PRIMARY KEY (client_id, preference_revision);


--
-- Name: notifications notifications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_pkey PRIMARY KEY (id);


--
-- Name: payment_adjustments payment_adjustments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_pkey PRIMARY KEY (id);


--
-- Name: payment_adjustments payment_adjustments_provider_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_provider_reference_key UNIQUE (provider, provider_reference);


--
-- Name: payment_allocations payment_allocations_payment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_payment_id_key UNIQUE (payment_id);


--
-- Name: payment_allocations payment_allocations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_pkey PRIMARY KEY (id);


--
-- Name: payment_exceptions payment_exceptions_evidence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_evidence_key UNIQUE (provider, evidence_reference, exception_kind);


--
-- Name: payment_exceptions payment_exceptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_pkey PRIMARY KEY (id);


--
-- Name: payment_provider_request_budgets payment_provider_request_budgets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_provider_request_budgets
    ADD CONSTRAINT payment_provider_request_budgets_pkey PRIMARY KEY (provider);


--
-- Name: payments payments_idempotency_scope_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_idempotency_scope_key UNIQUE (booking_id, purpose, idempotency_key);


--
-- Name: payments payments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_pkey PRIMARY KEY (id);


--
-- Name: payments payments_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_public_token_key UNIQUE (public_token);


--
-- Name: payments payments_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_reference_key UNIQUE (reference);


--
-- Name: payout_destinations payout_destinations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payout_destinations
    ADD CONSTRAINT payout_destinations_pkey PRIMARY KEY (id);


--
-- Name: payouts payouts_idempotency_scope_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_idempotency_scope_key UNIQUE (client_id, idempotency_key);


--
-- Name: payouts payouts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_pkey PRIMARY KEY (id);


--
-- Name: payouts payouts_reference_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_reference_key UNIQUE (reference);


--
-- Name: promotion_redemptions promotion_redemptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_pkey PRIMARY KEY (id);


--
-- Name: promotion_sections promotion_sections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_pkey PRIMARY KEY (promotion_id, section_id);


--
-- Name: promotion_services promotion_services_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_pkey PRIMARY KEY (promotion_id, service_id);


--
-- Name: promotions promotions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotions
    ADD CONSTRAINT promotions_pkey PRIMARY KEY (id);


--
-- Name: provider_availability_windows provider_availability_windows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_availability_windows
    ADD CONSTRAINT provider_availability_windows_pkey PRIMARY KEY (id);


--
-- Name: provider_daily_metric_jobs provider_daily_metric_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_daily_metric_jobs
    ADD CONSTRAINT provider_daily_metric_jobs_pkey PRIMARY KEY (client_id, metric_date, currency_code);


--
-- Name: provider_daily_metrics provider_daily_metrics_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_daily_metrics
    ADD CONSTRAINT provider_daily_metrics_pkey PRIMARY KEY (client_id, metric_date, currency_code);


--
-- Name: provider_notification_preferences provider_notification_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_notification_preferences
    ADD CONSTRAINT provider_notification_preferences_pkey PRIMARY KEY (client_id);


--
-- Name: provider_portfolio_items provider_portfolio_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_pkey PRIMARY KEY (id);


--
-- Name: provider_reviews provider_reviews_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_pkey PRIMARY KEY (id);


--
-- Name: provider_settlement_evidence provider_settlement_evidence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_key UNIQUE (provider, settlement_reference, payment_reference);


--
-- Name: provider_settlement_evidence provider_settlement_evidence_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_pkey PRIMARY KEY (id);


--
-- Name: provider_settlement_sync_states provider_settlement_sync_states_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_sync_states
    ADD CONSTRAINT provider_settlement_sync_states_pkey PRIMARY KEY (provider);


--
-- Name: provider_webhook_events provider_webhook_events_body_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_webhook_events
    ADD CONSTRAINT provider_webhook_events_body_key UNIQUE (provider, body_sha256);


--
-- Name: provider_webhook_events provider_webhook_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_webhook_events
    ADD CONSTRAINT provider_webhook_events_pkey PRIMARY KEY (id);


--
-- Name: provider_whatsapp_verification_challenges provider_whatsapp_verification_challenges_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_whatsapp_verification_challenges
    ADD CONSTRAINT provider_whatsapp_verification_challenges_pkey PRIMARY KEY (id);


--
-- Name: provider_whatsapp_verification_challenges provider_whatsapp_verification_challenges_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_whatsapp_verification_challenges
    ADD CONSTRAINT provider_whatsapp_verification_challenges_token_hash_key UNIQUE (token_hash);


--
-- Name: public_provider_resource_revisions public_provider_resource_revisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.public_provider_resource_revisions
    ADD CONSTRAINT public_provider_resource_revisions_pkey PRIMARY KEY (client_id);


--
-- Name: resolved_locations resolved_locations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_pkey PRIMARY KEY (id);


--
-- Name: resolved_locations resolved_locations_public_token_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_public_token_key UNIQUE (public_token);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: service_availability_windows service_availability_windows_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_availability_windows
    ADD CONSTRAINT service_availability_windows_pkey PRIMARY KEY (id);


--
-- Name: service_sections service_sections_client_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_client_id_slug_key UNIQUE (client_id, slug);


--
-- Name: service_sections service_sections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_pkey PRIMARY KEY (id);


--
-- Name: service_short_notice_rules service_short_notice_rules_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_pkey PRIMARY KEY (id);


--
-- Name: service_short_notice_rules service_short_notice_rules_service_threshold_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_service_threshold_key UNIQUE (service_id, threshold_minutes);


--
-- Name: service_wizard_drafts service_wizard_drafts_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_pkey PRIMARY KEY (id);


--
-- Name: services services_client_id_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_id_slug_key UNIQUE (client_id, slug);


--
-- Name: services services_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_pkey PRIMARY KEY (id);


--
-- Name: tessa_events tessa_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_events
    ADD CONSTRAINT tessa_events_pkey PRIMARY KEY (sequence);


--
-- Name: tessa_messages tessa_messages_id_thread_client_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_messages
    ADD CONSTRAINT tessa_messages_id_thread_client_key UNIQUE (id, thread_id, client_id);


--
-- Name: tessa_messages tessa_messages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_messages
    ADD CONSTRAINT tessa_messages_pkey PRIMARY KEY (id);


--
-- Name: tessa_messages tessa_messages_thread_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_messages
    ADD CONSTRAINT tessa_messages_thread_sequence_key UNIQUE (thread_id, sequence);


--
-- Name: tessa_preferences tessa_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_preferences
    ADD CONSTRAINT tessa_preferences_pkey PRIMARY KEY (client_id);


--
-- Name: tessa_run_steps tessa_run_steps_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_run_steps
    ADD CONSTRAINT tessa_run_steps_pkey PRIMARY KEY (id);


--
-- Name: tessa_run_steps tessa_run_steps_run_sequence_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_run_steps
    ADD CONSTRAINT tessa_run_steps_run_sequence_key UNIQUE (run_id, sequence);


--
-- Name: tessa_runs tessa_runs_id_thread_client_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_runs
    ADD CONSTRAINT tessa_runs_id_thread_client_key UNIQUE (id, thread_id, client_id);


--
-- Name: tessa_runs tessa_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_runs
    ADD CONSTRAINT tessa_runs_pkey PRIMARY KEY (id);


--
-- Name: tessa_runs tessa_runs_trigger_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_runs
    ADD CONSTRAINT tessa_runs_trigger_key UNIQUE (trigger_message_id);


--
-- Name: tessa_threads tessa_threads_creation_request_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_threads
    ADD CONSTRAINT tessa_threads_creation_request_key UNIQUE (client_id, creation_request_id);


--
-- Name: tessa_threads tessa_threads_id_client_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_threads
    ADD CONSTRAINT tessa_threads_id_client_key UNIQUE (id, client_id);


--
-- Name: tessa_threads tessa_threads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_threads
    ADD CONSTRAINT tessa_threads_pkey PRIMARY KEY (id);


--
-- Name: clients users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.clients
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: clients users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.clients
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: administrative_regions_boundary_gist_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX administrative_regions_boundary_gist_idx ON public.administrative_regions USING gist (boundary) WHERE ((boundary IS NOT NULL) AND is_active);


--
-- Name: administrative_regions_parent_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX administrative_regions_parent_list_idx ON public.administrative_regions USING btree (parent_id, level, is_active, name);


--
-- Name: agreement_events_agreement_occurred_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_events_agreement_occurred_idx ON public.agreement_events USING btree (agreement_id, occurred_at);


--
-- Name: agreement_instances_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_booking_idx ON public.agreement_instances USING btree (booking_id) WHERE (booking_id IS NOT NULL);


--
-- Name: agreement_instances_client_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_client_created_idx ON public.agreement_instances USING btree (client_id, created_at DESC);


--
-- Name: agreement_instances_client_status_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_client_status_created_id_idx ON public.agreement_instances USING btree (client_id, status, created_at DESC, id DESC);


--
-- Name: agreement_instances_customer_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_instances_customer_created_idx ON public.agreement_instances USING btree (customer_id, created_at DESC) WHERE (customer_id IS NOT NULL);


--
-- Name: agreement_instances_one_automated_per_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_instances_one_automated_per_booking_idx ON public.agreement_instances USING btree (booking_id) WHERE ((booking_id IS NOT NULL) AND (timing = ANY (ARRAY['before_payment'::text, 'after_payment'::text])));


--
-- Name: agreement_jobs_active_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_jobs_active_metrics_idx ON public.agreement_jobs USING btree (created_at) INCLUDE (attempt_count) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_jobs_claim_idx ON public.agreement_jobs USING btree (run_at, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_jobs_recent_completed_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_jobs_recent_completed_metrics_idx ON public.agreement_jobs USING btree (completed_at) WHERE (completed_at IS NOT NULL);


--
-- Name: agreement_template_families_client_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_families_client_list_idx ON public.agreement_template_families USING btree (client_id, updated_at DESC, id DESC) WHERE (owner_type = 'client'::text);


--
-- Name: agreement_template_families_system_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_families_system_list_idx ON public.agreement_template_families USING btree (category, updated_at DESC, id DESC) WHERE (owner_type = 'system'::text);


--
-- Name: agreement_template_generation_jobs_active_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_generation_jobs_active_metrics_idx ON public.agreement_template_generation_jobs USING btree (created_at) INCLUDE (attempt_count) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_generation_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_generation_jobs_claim_idx ON public.agreement_template_generation_jobs USING btree (run_at, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_generation_jobs_one_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_template_generation_jobs_one_active_idx ON public.agreement_template_generation_jobs USING btree (version_id) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: agreement_template_generation_jobs_recent_completed_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_generation_jobs_recent_completed_metrics_idx ON public.agreement_template_generation_jobs USING btree (completed_at) WHERE (completed_at IS NOT NULL);


--
-- Name: agreement_template_versions_family_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX agreement_template_versions_family_idx ON public.agreement_template_versions USING btree (family_id, version_number DESC);


--
-- Name: agreement_template_versions_one_draft_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX agreement_template_versions_one_draft_idx ON public.agreement_template_versions USING btree (family_id) WHERE (state = 'draft'::text);


--
-- Name: auth_password_reset_tokens_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_password_reset_tokens_expires_at_idx ON public.auth_password_reset_tokens USING btree (expires_at);


--
-- Name: auth_password_reset_tokens_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_password_reset_tokens_user_id_idx ON public.auth_password_reset_tokens USING btree (client_id);


--
-- Name: auth_pending_registrations_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_pending_registrations_expires_at_idx ON public.auth_pending_registrations USING btree (expires_at);


--
-- Name: auth_refresh_sessions_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_refresh_sessions_expires_at_idx ON public.auth_refresh_sessions USING btree (expires_at);


--
-- Name: auth_refresh_sessions_user_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX auth_refresh_sessions_user_id_idx ON public.auth_refresh_sessions USING btree (client_id);


--
-- Name: automation_settings_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX automation_settings_client_id_idx ON public.automation_settings USING btree (client_id);


--
-- Name: booking_change_commands_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_change_commands_booking_idx ON public.booking_change_commands USING btree (booking_id, created_at DESC);


--
-- Name: booking_change_quotes_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_change_quotes_booking_idx ON public.booking_change_quotes USING btree (booking_id, created_at DESC);


--
-- Name: booking_change_quotes_expired_unconsumed_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_change_quotes_expired_unconsumed_idx ON public.booking_change_quotes USING btree (expires_at, id) WHERE (consumed_at IS NULL);


--
-- Name: booking_domain_events_client_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_domain_events_client_sequence_idx ON public.booking_domain_events USING btree (client_id, sequence);


--
-- Name: booking_quote_promotions_capacity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quote_promotions_capacity_idx ON public.booking_quote_promotions USING btree (promotion_id, customer_email_normalized, booking_quote_id);


--
-- Name: booking_quotes_client_idempotency_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX booking_quotes_client_idempotency_key_idx ON public.booking_quotes USING btree (client_id, idempotency_key) WHERE (idempotency_key IS NOT NULL);


--
-- Name: booking_quotes_customer_promotion_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_customer_promotion_idx ON public.booking_quotes USING btree (promotion_id, customer_email_normalized, consumed_at, expires_at) WHERE (promotion_id IS NOT NULL);


--
-- Name: booking_quotes_expired_unconsumed_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_expired_unconsumed_idx ON public.booking_quotes USING btree (expires_at, id) WHERE ((consumed_at IS NULL) AND (booking_id IS NULL));


--
-- Name: booking_quotes_promotion_reservation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_promotion_reservation_idx ON public.booking_quotes USING btree (promotion_id, consumed_at, expires_at) WHERE (promotion_id IS NOT NULL);


--
-- Name: booking_quotes_service_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_quotes_service_expiry_idx ON public.booking_quotes USING btree (service_id, expires_at);


--
-- Name: booking_refund_attempts_provider_reference_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX booking_refund_attempts_provider_reference_idx ON public.booking_refund_attempts USING btree (provider, provider_reference) WHERE (provider_reference IS NOT NULL);


--
-- Name: booking_refund_attempts_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_refund_attempts_status_idx ON public.booking_refund_attempts USING btree (status, created_at) WHERE (status = ANY (ARRAY['prepared'::text, 'initiating'::text, 'pending'::text]));


--
-- Name: booking_refund_attempts_webhook_correlation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_refund_attempts_webhook_correlation_idx ON public.booking_refund_attempts USING btree (payment_id, provider, amount_minor, created_at DESC) WHERE (provider_reference IS NOT NULL);


--
-- Name: booking_refund_requests_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX booking_refund_requests_status_idx ON public.booking_refund_requests USING btree (status, created_at) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: bookings_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_client_id_idx ON public.bookings USING btree (client_id);


--
-- Name: bookings_client_id_start_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_client_id_start_at_idx ON public.bookings USING btree (client_id, start_at);


--
-- Name: bookings_customer_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_customer_id_idx ON public.bookings USING btree (customer_id);


--
-- Name: bookings_marketplace_availability_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_marketplace_availability_idx ON public.bookings USING btree (client_id, occupied_start_at, occupied_end_at) WHERE (status <> ALL (ARRAY['cancelled'::text, 'canceled'::text, 'declined'::text, 'expired'::text]));


--
-- Name: bookings_marketplace_customer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_marketplace_customer_idx ON public.bookings USING btree (marketplace_customer_id, start_at DESC) WHERE (marketplace_customer_id IS NOT NULL);


--
-- Name: bookings_marketplace_customer_start_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_marketplace_customer_start_id_idx ON public.bookings USING btree (marketplace_customer_id, start_at, id) WHERE (marketplace_customer_id IS NOT NULL);


--
-- Name: bookings_promotion_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_promotion_id_idx ON public.bookings USING btree (promotion_id);


--
-- Name: bookings_provider_customer_summary_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_provider_customer_summary_idx ON public.bookings USING btree (client_id, customer_id, start_at) INCLUDE (end_at, status);


--
-- Name: bookings_provider_window_cursor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_provider_window_cursor_idx ON public.bookings USING btree (client_id, start_at, id);


--
-- Name: bookings_unpaid_reservation_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX bookings_unpaid_reservation_expiry_idx ON public.bookings USING btree (reservation_expires_at, id) WHERE ((reservation_expires_at IS NOT NULL) AND (reservation_expired_at IS NULL) AND (status <> ALL (ARRAY['cancelled'::text, 'canceled'::text, 'declined'::text, 'expired'::text, 'completed'::text, 'no_show'::text])) AND (payment_status <> ALL (ARRAY['deposit_paid_balance_due'::text, 'paid_in_full'::text])));


--
-- Name: business_balance_entries_adjustment_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX business_balance_entries_adjustment_key ON public.business_balance_entries USING btree (payment_adjustment_id) WHERE (payment_adjustment_id IS NOT NULL);


--
-- Name: business_balance_entries_client_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_balance_entries_client_open_idx ON public.business_balance_entries USING btree (client_id, currency_code, created_at) WHERE (status = ANY (ARRAY['open'::text, 'partially_resolved'::text]));


--
-- Name: business_locations_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_locations_client_id_idx ON public.business_locations USING btree (client_id, is_active);


--
-- Name: business_locations_geog_gist_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_locations_geog_gist_idx ON public.business_locations USING gist (geog) WHERE ((geog IS NOT NULL) AND is_active);


--
-- Name: business_locations_one_active_primary_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX business_locations_one_active_primary_idx ON public.business_locations USING btree (client_id) WHERE (is_primary AND is_active);


--
-- Name: business_locations_region_discovery_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX business_locations_region_discovery_idx ON public.business_locations USING btree (state_region_id, lga_region_id, client_id) WHERE is_active;


--
-- Name: client_profiles_country_code_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_country_code_idx ON public.client_profiles USING btree (country_code) WHERE (country_code IS NOT NULL);


--
-- Name: client_profiles_inbox_business_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_inbox_business_name_trgm_idx ON public.client_profiles USING gin (lower(business_name) public.gin_trgm_ops);


--
-- Name: client_profiles_marketplace_business_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_marketplace_business_name_trgm_idx ON public.client_profiles USING gin (lower(business_name) public.gin_trgm_ops) WHERE marketplace_enabled;


--
-- Name: client_profiles_marketplace_discovery_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_marketplace_discovery_idx ON public.client_profiles USING btree (marketplace_category_id, client_id) WHERE marketplace_enabled;


--
-- Name: client_profiles_marketplace_headline_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX client_profiles_marketplace_headline_trgm_idx ON public.client_profiles USING gin (lower(headline) public.gin_trgm_ops) WHERE marketplace_enabled;


--
-- Name: clients_inbox_full_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX clients_inbox_full_name_trgm_idx ON public.clients USING gin (lower(full_name) public.gin_trgm_ops);


--
-- Name: customers_client_id_full_name_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_client_id_full_name_idx ON public.customers USING btree (client_id, full_name);


--
-- Name: customers_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_client_id_idx ON public.customers USING btree (client_id);


--
-- Name: customers_provider_page_cursor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_provider_page_cursor_idx ON public.customers USING btree (client_id, COALESCE(last_seen_at, created_at) DESC, id DESC);


--
-- Name: customers_tessa_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_tessa_name_trgm_idx ON public.customers USING gin (lower(full_name) public.gin_trgm_ops);


--
-- Name: customers_tessa_recent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX customers_tessa_recent_idx ON public.customers USING btree (client_id, updated_at DESC, id);


--
-- Name: financial_jobs_active_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX financial_jobs_active_metrics_idx ON public.financial_jobs USING btree (created_at) INCLUDE (attempts) WHERE (status = ANY (ARRAY['pending'::text, 'failed'::text, 'processing'::text]));


--
-- Name: financial_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX financial_jobs_claim_idx ON public.financial_jobs USING btree (available_at, created_at) WHERE (status = ANY (ARRAY['pending'::text, 'failed'::text]));


--
-- Name: financial_jobs_recent_completed_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX financial_jobs_recent_completed_metrics_idx ON public.financial_jobs USING btree (completed_at) WHERE (completed_at IS NOT NULL);


--
-- Name: idx_services_client_hidden_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_services_client_hidden_status ON public.services USING btree (client_id, is_hidden, status);


--
-- Name: inbox_ai_actions_conversation_started_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_actions_conversation_started_idx ON public.inbox_ai_actions USING btree (conversation_id, started_at DESC, id DESC);


--
-- Name: inbox_ai_actions_turn_job_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_actions_turn_job_idx ON public.inbox_ai_actions USING btree (turn_job_id, started_at, id) WHERE (turn_job_id IS NOT NULL);


--
-- Name: inbox_ai_booking_sessions_booking_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_ai_booking_sessions_booking_idx ON public.inbox_ai_booking_sessions USING btree (booking_id) WHERE (booking_id IS NOT NULL);


--
-- Name: inbox_ai_booking_sessions_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_booking_sessions_expiry_idx ON public.inbox_ai_booking_sessions USING btree (expires_at) WHERE (state <> ALL (ARRAY['expired'::text, 'handoff'::text, 'provider_takeover'::text, 'failed'::text]));


--
-- Name: inbox_ai_conversation_controls_client_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_conversation_controls_client_idx ON public.inbox_ai_conversation_controls USING btree (client_id, updated_at DESC);


--
-- Name: inbox_ai_runs_client_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_runs_client_created_idx ON public.inbox_ai_runs USING btree (client_id, created_at DESC, id DESC);


--
-- Name: inbox_ai_runs_conversation_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_runs_conversation_created_idx ON public.inbox_ai_runs USING btree (conversation_id, created_at DESC, id DESC);


--
-- Name: inbox_ai_runs_manual_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_runs_manual_ready_idx ON public.inbox_ai_runs USING btree (available_at, created_at, id) WHERE ((mode = 'manual'::text) AND (status = ANY (ARRAY['queued'::text, 'processing'::text])));


--
-- Name: inbox_ai_runs_provider_message_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_ai_runs_provider_message_idx ON public.inbox_ai_runs USING btree (provider_message_id) WHERE (provider_message_id IS NOT NULL);


--
-- Name: inbox_ai_runs_request_idempotency_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_ai_runs_request_idempotency_idx ON public.inbox_ai_runs USING btree (client_id, conversation_id, request_id);


--
-- Name: inbox_ai_runs_resulting_message_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_ai_runs_resulting_message_idx ON public.inbox_ai_runs USING btree (resulting_message_id) WHERE (resulting_message_id IS NOT NULL);


--
-- Name: inbox_ai_runs_running_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_runs_running_created_idx ON public.inbox_ai_runs USING btree (created_at) WHERE (status = 'running'::text);


--
-- Name: inbox_ai_runs_turn_job_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_ai_runs_turn_job_idx ON public.inbox_ai_runs USING btree (turn_job_id) WHERE (turn_job_id IS NOT NULL);


--
-- Name: inbox_ai_sessions_client_state_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_sessions_client_state_idx ON public.inbox_ai_sessions USING btree (client_id, state, updated_at DESC);


--
-- Name: inbox_ai_turn_jobs_active_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_active_metrics_idx ON public.inbox_ai_turn_jobs USING btree (created_at) INCLUDE (attempt_count) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: inbox_ai_turn_jobs_client_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_client_created_idx ON public.inbox_ai_turn_jobs USING btree (client_id, created_at DESC);


--
-- Name: inbox_ai_turn_jobs_conversation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_conversation_idx ON public.inbox_ai_turn_jobs USING btree (conversation_id, created_at DESC, id DESC);


--
-- Name: inbox_ai_turn_jobs_customer_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_customer_created_idx ON public.inbox_ai_turn_jobs USING btree (marketplace_customer_id, created_at DESC);


--
-- Name: inbox_ai_turn_jobs_ready_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_ready_idx ON public.inbox_ai_turn_jobs USING btree (available_at, created_at, id) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: inbox_ai_turn_jobs_recent_completed_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_recent_completed_metrics_idx ON public.inbox_ai_turn_jobs USING btree (completed_at) WHERE (completed_at IS NOT NULL);


--
-- Name: inbox_ai_turn_jobs_terminal_completed_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_ai_turn_jobs_terminal_completed_idx ON public.inbox_ai_turn_jobs USING btree (completed_at, id) WHERE (status = ANY (ARRAY['completed'::text, 'failed'::text, 'cancelled'::text]));


--
-- Name: inbox_conversation_bookings_booking_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_conversation_bookings_booking_key ON public.inbox_conversation_bookings USING btree (booking_id);


--
-- Name: inbox_conversation_moderation_conversation_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversation_moderation_conversation_created_idx ON public.inbox_conversation_moderation_events USING btree (conversation_id, created_at DESC, id DESC);


--
-- Name: inbox_conversations_marketplace_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_marketplace_list_idx ON public.inbox_conversations USING btree (marketplace_customer_id, last_message_at DESC NULLS LAST, created_at DESC, id DESC);


--
-- Name: inbox_conversations_provider_list_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_conversations_provider_list_idx ON public.inbox_conversations USING btree (client_id, last_message_at DESC NULLS LAST, created_at DESC, id DESC);


--
-- Name: inbox_conversations_provider_marketplace_customer_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_conversations_provider_marketplace_customer_key ON public.inbox_conversations USING btree (client_id, marketplace_customer_id);


--
-- Name: inbox_events_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_events_created_at_idx ON public.inbox_events USING btree (created_at);


--
-- Name: inbox_events_marketplace_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_events_marketplace_sequence_idx ON public.inbox_events USING btree (marketplace_customer_id, sequence);


--
-- Name: inbox_events_provider_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_events_provider_sequence_idx ON public.inbox_events USING btree (client_id, sequence);


--
-- Name: inbox_messages_client_idempotency_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX inbox_messages_client_idempotency_key ON public.inbox_messages USING btree (conversation_id, sender_type, sender_id, client_message_id) WHERE (client_message_id IS NOT NULL);


--
-- Name: inbox_messages_conversation_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_messages_conversation_sequence_idx ON public.inbox_messages USING btree (conversation_id, sequence DESC);


--
-- Name: inbox_participant_states_actor_archive_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX inbox_participant_states_actor_archive_idx ON public.inbox_participant_states USING btree (participant_type, participant_id, archived_at, conversation_id);


--
-- Name: marketplace_auth_challenges_cleanup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_challenges_cleanup_idx ON public.marketplace_auth_challenges USING btree (expires_at, id);


--
-- Name: marketplace_auth_challenges_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_challenges_expiry_idx ON public.marketplace_auth_challenges USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: marketplace_auth_challenges_identifier_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_challenges_identifier_idx ON public.marketplace_auth_challenges USING btree (identifier_type, identifier, created_at DESC);


--
-- Name: marketplace_auth_challenges_target_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_challenges_target_idx ON public.marketplace_auth_challenges USING btree (target_customer_id, created_at DESC) WHERE (target_customer_id IS NOT NULL);


--
-- Name: marketplace_auth_sessions_customer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_sessions_customer_idx ON public.marketplace_auth_sessions USING btree (marketplace_customer_id, expires_at DESC);


--
-- Name: marketplace_auth_sessions_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_auth_sessions_expiry_idx ON public.marketplace_auth_sessions USING btree (expires_at);


--
-- Name: marketplace_customer_addresses_customer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_customer_addresses_customer_idx ON public.marketplace_customer_addresses USING btree (marketplace_customer_id, is_default DESC, created_at);


--
-- Name: marketplace_customer_addresses_one_default; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_customer_addresses_one_default ON public.marketplace_customer_addresses USING btree (marketplace_customer_id) WHERE is_default;


--
-- Name: marketplace_customer_identities_customer_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_customer_identities_customer_idx ON public.marketplace_customer_identities USING btree (marketplace_customer_id, created_at);


--
-- Name: marketplace_customer_identities_phone_contact_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_customer_identities_phone_contact_unique ON public.marketplace_customer_identities USING btree (normalized_identifier) WHERE (identifier_type = ANY (ARRAY['phone'::text, 'whatsapp'::text]));


--
-- Name: marketplace_customers_email_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_customers_email_key ON public.marketplace_customers USING btree (lower(email)) WHERE (email IS NOT NULL);


--
-- Name: marketplace_customers_inbox_name_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_customers_inbox_name_trgm_idx ON public.marketplace_customers USING gin (lower(full_name) public.gin_trgm_ops);


--
-- Name: marketplace_customers_phone_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_customers_phone_key ON public.marketplace_customers USING btree (phone_e164) WHERE (phone_e164 IS NOT NULL);


--
-- Name: marketplace_customers_whatsapp_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_customers_whatsapp_key ON public.marketplace_customers USING btree (whatsapp_e164) WHERE (whatsapp_e164 IS NOT NULL);


--
-- Name: marketplace_discovery_jobs_available_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_discovery_jobs_available_idx ON public.marketplace_discovery_jobs USING btree (available_at, updated_at, client_id) WHERE (requested_revision > applied_revision);


--
-- Name: marketplace_notifications_customer_recent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_notifications_customer_recent_idx ON public.marketplace_notifications USING btree (marketplace_customer_id, created_at DESC, id DESC);


--
-- Name: marketplace_notifications_customer_unread_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_notifications_customer_unread_idx ON public.marketplace_notifications USING btree (marketplace_customer_id, created_at DESC, id DESC) WHERE (read_at IS NULL);


--
-- Name: marketplace_provider_documents_availability_refresh_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_provider_documents_availability_refresh_idx ON public.marketplace_provider_documents USING btree (availability_refresh_after, client_id) WHERE (availability_refresh_after IS NOT NULL);


--
-- Name: marketplace_provider_documents_category_client_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_provider_documents_category_client_idx ON public.marketplace_provider_documents USING btree (category_id, client_id);


--
-- Name: marketplace_provider_documents_handle_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX marketplace_provider_documents_handle_idx ON public.marketplace_provider_documents USING btree (handle_slug);


--
-- Name: marketplace_provider_documents_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_provider_documents_search_idx ON public.marketplace_provider_documents USING gin (search_vector);


--
-- Name: marketplace_saved_providers_customer_recent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_saved_providers_customer_recent_idx ON public.marketplace_saved_providers USING btree (marketplace_customer_id, created_at DESC, provider_id DESC);


--
-- Name: marketplace_service_availability_days_available_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_availability_days_available_idx ON public.marketplace_service_availability_days USING btree (local_date, provider_id, first_available_at, service_id) WHERE has_available_slot;


--
-- Name: marketplace_service_availability_days_provider_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_availability_days_provider_date_idx ON public.marketplace_service_availability_days USING btree (provider_id, local_date, first_available_at);


--
-- Name: marketplace_service_availability_days_provider_refresh_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_availability_days_provider_refresh_idx ON public.marketplace_service_availability_days USING btree (provider_id, refresh_after, local_date);


--
-- Name: marketplace_service_documents_fulfillment_provider_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_fulfillment_provider_idx ON public.marketplace_service_documents USING btree (fulfillment_mode, provider_id);


--
-- Name: marketplace_service_documents_internal_geog_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_internal_geog_idx ON public.marketplace_service_documents USING gist (internal_geog) WHERE (internal_geog IS NOT NULL);


--
-- Name: marketplace_service_documents_lga_provider_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_lga_provider_idx ON public.marketplace_service_documents USING btree (lga_region_id, provider_id) WHERE (lga_region_id IS NOT NULL);


--
-- Name: marketplace_service_documents_price_provider_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_price_provider_idx ON public.marketplace_service_documents USING btree (price_amount_minor, provider_id);


--
-- Name: marketplace_service_documents_provider_sort_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_provider_sort_idx ON public.marketplace_service_documents USING btree (provider_id, sort_order, service_id);


--
-- Name: marketplace_service_documents_public_geog_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_public_geog_idx ON public.marketplace_service_documents USING gist (public_geog) WHERE (public_geog IS NOT NULL);


--
-- Name: marketplace_service_documents_search_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_search_idx ON public.marketplace_service_documents USING gin (search_vector);


--
-- Name: marketplace_service_documents_state_provider_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX marketplace_service_documents_state_provider_idx ON public.marketplace_service_documents USING btree (state_region_id, provider_id) WHERE (state_region_id IS NOT NULL);


--
-- Name: meta_whatsapp_webhook_receipts_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX meta_whatsapp_webhook_receipts_pending_idx ON public.meta_whatsapp_webhook_receipts USING btree (available_at, created_at, id) WHERE (processing_status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text]));


--
-- Name: notification_deliveries_booking_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_deliveries_booking_active_idx ON public.notification_deliveries USING btree (booking_id, notification_type, status, scheduled_for, id) WHERE (status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text, 'dispatching'::text, 'unknown'::text, 'manual_review'::text]));


--
-- Name: notification_deliveries_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_deliveries_due_idx ON public.notification_deliveries USING btree (channel, (
CASE
    WHEN (status = 'processing'::text) THEN lease_expires_at
    ELSE next_attempt_at
END), scheduled_for, id) WHERE (status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text]));


--
-- Name: notification_deliveries_provider_message_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX notification_deliveries_provider_message_idx ON public.notification_deliveries USING btree (provider_message_id) WHERE (provider_message_id <> ''::text);


--
-- Name: notification_deliveries_terminal_retention_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_deliveries_terminal_retention_idx ON public.notification_deliveries USING btree (completed_at, id) WHERE (status = ANY (ARRAY['failed'::text, 'deleted'::text, 'cancelled'::text, 'read'::text, 'delivered'::text]));


--
-- Name: notification_event_jobs_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_event_jobs_claim_idx ON public.notification_event_jobs USING btree ((
CASE
    WHEN (status = 'processing'::text) THEN lease_expires_at
    ELSE next_attempt_at
END), event_sequence, booking_event_id) WHERE (status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text]));


--
-- Name: notification_in_app_jobs_due_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_in_app_jobs_due_idx ON public.notification_in_app_jobs USING btree ((
CASE
    WHEN (status = 'processing'::text) THEN lease_expires_at
    ELSE next_attempt_at
END), scheduled_for, id) WHERE (status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text]));


--
-- Name: notification_scope_replan_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notification_scope_replan_claim_idx ON public.notification_scope_replan_jobs USING btree ((
CASE
    WHEN (status = 'processing'::text) THEN lease_expires_at
    ELSE next_attempt_at
END), created_at, client_id, preference_revision) WHERE (status = ANY (ARRAY['pending'::text, 'retry'::text, 'processing'::text]));


--
-- Name: notifications_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_client_id_idx ON public.notifications USING btree (client_id, created_at DESC);


--
-- Name: notifications_client_id_read_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_client_id_read_at_idx ON public.notifications USING btree (client_id, read_at);


--
-- Name: notifications_planner_event_key_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX notifications_planner_event_key_idx ON public.notifications USING btree (client_id, ((metadata ->> 'notification_planner_key'::text))) WHERE (metadata ? 'notification_planner_key'::text);


--
-- Name: notifications_provider_page_cursor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_provider_page_cursor_idx ON public.notifications USING btree (client_id, created_at DESC, id DESC);


--
-- Name: notifications_provider_unread_cursor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_provider_unread_cursor_idx ON public.notifications USING btree (client_id, created_at DESC, id DESC) WHERE (read_at IS NULL);


--
-- Name: notifications_provider_urgent_cursor_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX notifications_provider_urgent_cursor_idx ON public.notifications USING btree (client_id, created_at DESC, id DESC) WHERE (severity = 'urgent'::text);


--
-- Name: payment_adjustments_payment_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_adjustments_payment_idx ON public.payment_adjustments USING btree (payment_id, occurred_at DESC);


--
-- Name: payment_allocations_eligible_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_allocations_eligible_idx ON public.payment_allocations USING btree (available_for_payout_at, created_at) WHERE (status = 'eligible'::text);


--
-- Name: payment_allocations_wallet_balance_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_allocations_wallet_balance_idx ON public.payment_allocations USING btree (client_id, currency_code, available_for_payout_at) WHERE (status = 'eligible'::text);


--
-- Name: payment_exceptions_open_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payment_exceptions_open_idx ON public.payment_exceptions USING btree (created_at) WHERE (status = 'open'::text);


--
-- Name: payments_booking_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_booking_created_id_idx ON public.payments USING btree (booking_id, created_at DESC, id DESC);


--
-- Name: payments_booking_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_booking_created_idx ON public.payments USING btree (booking_id, created_at DESC);


--
-- Name: payments_checkout_initialization_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_checkout_initialization_claim_idx ON public.payments USING btree (next_provider_check_at, checkout_initialization_lease_expires_at) WHERE ((checkout_initialization_state = ANY (ARRAY['prepared'::text, 'unknown'::text])) AND (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text])));


--
-- Name: payments_client_paid_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_client_paid_at_idx ON public.payments USING btree (client_id, paid_at DESC) WHERE (paid_at IS NOT NULL);


--
-- Name: payments_one_active_obligation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_one_active_obligation_idx ON public.payments USING btree (booking_id, purpose) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payments_pending_reconciliation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_pending_reconciliation_idx ON public.payments USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payments_provider_reference_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payments_provider_reference_key ON public.payments USING btree (provider, provider_reference) WHERE (provider_reference <> ''::text);


--
-- Name: payments_reconciliation_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payments_reconciliation_claim_idx ON public.payments USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text]));


--
-- Name: payout_destinations_active_verification_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payout_destinations_active_verification_key ON public.payout_destinations USING btree (client_id, provider, country_code, currency_code, rail, institution_code, verification_fingerprint) WHERE (status = 'active'::text);


--
-- Name: payout_destinations_client_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payout_destinations_client_idx ON public.payout_destinations USING btree (client_id, status, created_at DESC);


--
-- Name: payout_destinations_one_default_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payout_destinations_one_default_idx ON public.payout_destinations USING btree (client_id, country_code, currency_code, rail) WHERE (is_default AND (status = 'active'::text));


--
-- Name: payouts_one_active_allocation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payouts_one_active_allocation_idx ON public.payouts USING btree (payment_allocation_id) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: payouts_pending_reconciliation_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payouts_pending_reconciliation_idx ON public.payouts USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: payouts_provider_reference_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX payouts_provider_reference_key ON public.payouts USING btree (provider, provider_reference) WHERE (provider_reference <> ''::text);


--
-- Name: payouts_reconciliation_claim_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX payouts_reconciliation_claim_idx ON public.payouts USING btree (updated_at) WHERE (status = ANY (ARRAY['created'::text, 'pending'::text, 'requires_action'::text, 'unknown'::text]));


--
-- Name: promotion_redemptions_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_client_id_idx ON public.promotion_redemptions USING btree (client_id, created_at DESC);


--
-- Name: promotion_redemptions_customer_email_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_customer_email_idx ON public.promotion_redemptions USING btree (client_id, customer_email);


--
-- Name: promotion_redemptions_promotion_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_promotion_created_id_idx ON public.promotion_redemptions USING btree (promotion_id, created_at DESC, id DESC);


--
-- Name: promotion_redemptions_promotion_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_redemptions_promotion_id_idx ON public.promotion_redemptions USING btree (promotion_id, created_at DESC);


--
-- Name: promotion_sections_section_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_sections_section_id_idx ON public.promotion_sections USING btree (section_id);


--
-- Name: promotion_services_service_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotion_services_service_id_idx ON public.promotion_services USING btree (service_id);


--
-- Name: promotions_client_id_active_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotions_client_id_active_idx ON public.promotions USING btree (client_id, is_active, starts_at, ends_at);


--
-- Name: promotions_client_id_code_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX promotions_client_id_code_unique_idx ON public.promotions USING btree (client_id, lower(code)) WHERE (code <> ''::text);


--
-- Name: promotions_client_id_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotions_client_id_type_idx ON public.promotions USING btree (client_id, promotion_type, updated_at DESC);


--
-- Name: promotions_client_type_updated_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX promotions_client_type_updated_id_idx ON public.promotions USING btree (client_id, promotion_type, updated_at DESC, id DESC);


--
-- Name: provider_availability_windows_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_availability_windows_client_id_idx ON public.provider_availability_windows USING btree (client_id);


--
-- Name: provider_daily_metric_jobs_enqueued_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_daily_metric_jobs_enqueued_idx ON public.provider_daily_metric_jobs USING btree (enqueued_at, client_id, metric_date, currency_code);


--
-- Name: provider_daily_metrics_client_currency_date_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_daily_metrics_client_currency_date_idx ON public.provider_daily_metrics USING btree (client_id, currency_code, metric_date);


--
-- Name: provider_portfolio_items_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_portfolio_items_client_id_idx ON public.provider_portfolio_items USING btree (client_id, sort_order);


--
-- Name: provider_reviews_booking_id_unique_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX provider_reviews_booking_id_unique_idx ON public.provider_reviews USING btree (booking_id) WHERE (booking_id IS NOT NULL);


--
-- Name: provider_reviews_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_client_id_idx ON public.provider_reviews USING btree (client_id, created_at DESC);


--
-- Name: provider_reviews_public_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_public_created_id_idx ON public.provider_reviews USING btree (client_id, created_at DESC, id DESC) WHERE (status = 'approved'::text);


--
-- Name: provider_reviews_public_listing_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_public_listing_idx ON public.provider_reviews USING btree (client_id, created_at DESC, id) WHERE (status = 'approved'::text);


--
-- Name: provider_reviews_public_rating_created_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_public_rating_created_id_idx ON public.provider_reviews USING btree (client_id, rating DESC, created_at DESC, id DESC) WHERE (status = 'approved'::text);


--
-- Name: provider_reviews_public_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_reviews_public_service_idx ON public.provider_reviews USING btree (client_id, service_id, created_at DESC) WHERE (status = 'approved'::text);


--
-- Name: provider_settlement_evidence_unmatched_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_settlement_evidence_unmatched_idx ON public.provider_settlement_evidence USING btree (observed_at) WHERE (status = 'unmatched'::text);


--
-- Name: provider_webhook_events_pending_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_webhook_events_pending_idx ON public.provider_webhook_events USING btree (next_attempt_at, received_at) WHERE (processing_status = ANY (ARRAY['pending'::text, 'failed'::text]));


--
-- Name: provider_webhook_events_provider_event_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX provider_webhook_events_provider_event_key ON public.provider_webhook_events USING btree (provider, provider_event_id) WHERE (provider_event_id <> ''::text);


--
-- Name: provider_whatsapp_challenge_active_client_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX provider_whatsapp_challenge_active_client_idx ON public.provider_whatsapp_verification_challenges USING btree (client_id) WHERE (consumed_at IS NULL);


--
-- Name: provider_whatsapp_challenge_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX provider_whatsapp_challenge_expiry_idx ON public.provider_whatsapp_verification_challenges USING btree (expires_at) WHERE (consumed_at IS NULL);


--
-- Name: resolved_locations_expiry_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolved_locations_expiry_idx ON public.resolved_locations USING btree (expires_at);


--
-- Name: resolved_locations_geog_gist_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX resolved_locations_geog_gist_idx ON public.resolved_locations USING gist (geog) WHERE (geog IS NOT NULL);


--
-- Name: service_availability_windows_service_day_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_availability_windows_service_day_idx ON public.service_availability_windows USING btree (service_id, day_of_week, start_time);


--
-- Name: service_sections_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_sections_client_id_idx ON public.service_sections USING btree (client_id);


--
-- Name: service_sections_client_id_sort_order_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_sections_client_id_sort_order_idx ON public.service_sections USING btree (client_id, sort_order);


--
-- Name: service_short_notice_rules_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_short_notice_rules_service_idx ON public.service_short_notice_rules USING btree (service_id, threshold_minutes);


--
-- Name: service_wizard_drafts_client_updated_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX service_wizard_drafts_client_updated_idx ON public.service_wizard_drafts USING btree (client_id, updated_at DESC);


--
-- Name: service_wizard_drafts_one_per_service_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX service_wizard_drafts_one_per_service_idx ON public.service_wizard_drafts USING btree (client_id, service_id) WHERE (service_id IS NOT NULL);


--
-- Name: services_client_id_agreement_template_family_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_agreement_template_family_id_idx ON public.services USING btree (client_id, agreement_template_family_id);


--
-- Name: services_client_id_category_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_category_idx ON public.services USING btree (client_id, category);


--
-- Name: services_client_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_idx ON public.services USING btree (client_id);


--
-- Name: services_client_id_section_id_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_section_id_idx ON public.services USING btree (client_id, section_id);


--
-- Name: services_client_id_status_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_client_id_status_idx ON public.services USING btree (client_id, status);


--
-- Name: services_marketplace_description_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_marketplace_description_trgm_idx ON public.services USING gin (lower(description) public.gin_trgm_ops) WHERE ((status = 'published'::text) AND is_active AND (NOT is_hidden));


--
-- Name: services_marketplace_discovery_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_marketplace_discovery_idx ON public.services USING btree (client_id, price_amount_minor, sort_order, id) WHERE ((status = 'published'::text) AND is_active AND (NOT is_hidden));


--
-- Name: services_marketplace_title_trgm_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_marketplace_title_trgm_idx ON public.services USING gin (lower(title) public.gin_trgm_ops) WHERE ((status = 'published'::text) AND is_active AND (NOT is_hidden));


--
-- Name: services_marketplace_travel_extent_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX services_marketplace_travel_extent_idx ON public.services USING btree (max_travel_distance_meters DESC) WHERE ((status = 'published'::text) AND is_active AND (NOT is_hidden) AND (fulfillment_mode = 'customer_location'::text) AND (max_travel_distance_meters IS NOT NULL));


--
-- Name: tessa_events_client_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_events_client_sequence_idx ON public.tessa_events USING btree (client_id, sequence);


--
-- Name: tessa_events_created_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_events_created_sequence_idx ON public.tessa_events USING btree (created_at, sequence);


--
-- Name: tessa_messages_one_result_per_run; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tessa_messages_one_result_per_run ON public.tessa_messages USING btree (run_id) WHERE (sender_type = 'tessa'::text);


--
-- Name: tessa_messages_provider_idempotency_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tessa_messages_provider_idempotency_key ON public.tessa_messages USING btree (thread_id, client_message_id) WHERE (sender_type = 'provider'::text);


--
-- Name: tessa_messages_thread_sequence_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_messages_thread_sequence_idx ON public.tessa_messages USING btree (thread_id, sequence DESC);


--
-- Name: tessa_run_steps_action_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tessa_run_steps_action_key ON public.tessa_run_steps USING btree (run_id, idempotency_key) WHERE (idempotency_key IS NOT NULL);


--
-- Name: tessa_runs_active_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_runs_active_metrics_idx ON public.tessa_runs USING btree (created_at) INCLUDE (attempt_count) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: tessa_runs_client_created_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_runs_client_created_idx ON public.tessa_runs USING btree (client_id, created_at DESC, id DESC);


--
-- Name: tessa_runs_one_active_per_thread; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tessa_runs_one_active_per_thread ON public.tessa_runs USING btree (thread_id) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: tessa_runs_queue_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_runs_queue_idx ON public.tessa_runs USING btree (available_at, created_at, id) WHERE (status = ANY (ARRAY['queued'::text, 'processing'::text]));


--
-- Name: tessa_runs_recent_completed_metrics_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_runs_recent_completed_metrics_idx ON public.tessa_runs USING btree (completed_at) WHERE (completed_at IS NOT NULL);


--
-- Name: tessa_threads_archived_retention_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_threads_archived_retention_idx ON public.tessa_threads USING btree (archived_at, id) WHERE (status = 'archived'::text);


--
-- Name: tessa_threads_client_activity_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX tessa_threads_client_activity_idx ON public.tessa_threads USING btree (client_id, last_activity_at DESC, id DESC);


--
-- Name: tessa_threads_one_active_per_client; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX tessa_threads_one_active_per_client ON public.tessa_threads USING btree (client_id) WHERE (status = 'active'::text);


--
-- Name: agreement_jobs agreement_jobs_wake_core; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER agreement_jobs_wake_core AFTER INSERT OR UPDATE OF status, run_at ON public.agreement_jobs FOR EACH ROW EXECUTE FUNCTION public.notify_core_worker_queue();


--
-- Name: agreement_template_families agreement_template_families_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER agreement_template_families_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE OF client_id, title, confirmation_method, status ON public.agreement_template_families FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_published_agreement();


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_wake_core; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER agreement_template_generation_jobs_wake_core AFTER INSERT OR UPDATE OF status, run_at ON public.agreement_template_generation_jobs FOR EACH ROW EXECUTE FUNCTION public.notify_core_worker_queue();


--
-- Name: booking_domain_events booking_domain_events_enqueue_notification; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER booking_domain_events_enqueue_notification AFTER INSERT ON public.booking_domain_events FOR EACH ROW EXECUTE FUNCTION public.enqueue_notification_event_job();


--
-- Name: booking_domain_events booking_domain_events_marketplace_notification; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER booking_domain_events_marketplace_notification AFTER INSERT ON public.booking_domain_events FOR EACH ROW EXECUTE FUNCTION public.create_marketplace_booking_notification();


--
-- Name: booking_domain_events booking_domain_events_notify; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER booking_domain_events_notify AFTER INSERT ON public.booking_domain_events FOR EACH ROW EXECUTE FUNCTION public.notify_booking_domain_event();


--
-- Name: booking_refund_requests booking_refund_requests_wake_core; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER booking_refund_requests_wake_core AFTER INSERT OR UPDATE OF status ON public.booking_refund_requests FOR EACH ROW EXECUTE FUNCTION public.notify_core_worker_queue();


--
-- Name: bookings bookings_append_update_event; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_append_update_event AFTER UPDATE ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.append_booking_update_event();


--
-- Name: bookings bookings_marketplace_discovery_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_marketplace_discovery_insert_delete_trigger AFTER INSERT OR DELETE ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_booking();


--
-- Name: bookings bookings_marketplace_discovery_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_marketplace_discovery_update_trigger AFTER UPDATE OF client_id, service_id, status, start_at, end_at, occupied_start_at, occupied_end_at ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_booking();


--
-- Name: bookings bookings_provider_daily_metric_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_provider_daily_metric_trigger AFTER INSERT OR DELETE OR UPDATE OF client_id, customer_id, status, payment_status, start_at, total_amount_minor, currency_code ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.enqueue_provider_daily_metric_for_booking();


--
-- Name: bookings bookings_public_resource_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_public_resource_insert_delete_trigger AFTER INSERT OR DELETE ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_completed_booking();


--
-- Name: bookings bookings_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER bookings_public_resource_update_trigger AFTER UPDATE OF client_id, customer_id, service_id, status ON public.bookings FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_completed_booking();


--
-- Name: business_locations business_locations_marketplace_discovery_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER business_locations_marketplace_discovery_insert_delete_trigger AFTER INSERT OR DELETE ON public.business_locations FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: business_locations business_locations_marketplace_discovery_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER business_locations_marketplace_discovery_update_trigger AFTER UPDATE OF client_id, formatted_address, latitude, longitude, resolution_status, timezone, is_active, country_code, state_region_id, lga_region_id, locality ON public.business_locations FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: business_locations business_locations_public_resource_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER business_locations_public_resource_insert_delete_trigger AFTER INSERT OR DELETE ON public.business_locations FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: business_locations business_locations_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER business_locations_public_resource_update_trigger AFTER UPDATE OF client_id, formatted_address, resolution_status, is_active ON public.business_locations FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: client_profile_handles client_profile_handles_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profile_handles_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE ON public.client_profile_handles FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: client_profiles client_profiles_marketplace_discovery_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profiles_marketplace_discovery_insert_delete_trigger AFTER INSERT OR DELETE ON public.client_profiles FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: client_profiles client_profiles_marketplace_discovery_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profiles_marketplace_discovery_update_trigger AFTER UPDATE OF business_name, handle_slug, headline, public_location_label, timezone, hero_image_url, avatar_url, verified, country_code, currency_code, market_configured_at, marketplace_enabled, marketplace_category_id, marketplace_location_visibility, concurrent_booking_capacity ON public.client_profiles FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: client_profiles client_profiles_provider_daily_metric_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profiles_provider_daily_metric_trigger AFTER UPDATE OF timezone, currency_code ON public.client_profiles FOR EACH ROW EXECUTE FUNCTION public.enqueue_provider_daily_metrics_for_profile_timezone();


--
-- Name: client_profiles client_profiles_public_resource_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profiles_public_resource_insert_delete_trigger AFTER INSERT OR DELETE ON public.client_profiles FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: client_profiles client_profiles_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER client_profiles_public_resource_update_trigger AFTER UPDATE OF business_name, handle_slug, category, headline, short_bio, public_location_label, timezone, hero_image_url, avatar_url, verified, years_experience, currency_code, public_profile_about, booking_page_intro, country_code, locale, marketplace_enabled, marketplace_category_id ON public.client_profiles FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: financial_jobs financial_jobs_wake_core; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER financial_jobs_wake_core AFTER INSERT OR UPDATE OF status, available_at ON public.financial_jobs FOR EACH ROW EXECUTE FUNCTION public.notify_core_worker_queue();


--
-- Name: inbox_ai_runs inbox_ai_runs_wake_ai; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER inbox_ai_runs_wake_ai AFTER INSERT OR UPDATE OF status, available_at ON public.inbox_ai_runs FOR EACH ROW EXECUTE FUNCTION public.notify_ai_worker_queue();


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_wake_ai; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER inbox_ai_turn_jobs_wake_ai AFTER INSERT OR UPDATE OF status, available_at ON public.inbox_ai_turn_jobs FOR EACH ROW EXECUTE FUNCTION public.notify_ai_worker_queue();


--
-- Name: inbox_conversation_bookings inbox_conversation_bookings_enforce_ownership; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER inbox_conversation_bookings_enforce_ownership BEFORE INSERT OR UPDATE ON public.inbox_conversation_bookings FOR EACH ROW EXECUTE FUNCTION public.enforce_inbox_booking_ownership();


--
-- Name: inbox_messages inbox_messages_marketplace_notification; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER inbox_messages_marketplace_notification AFTER INSERT ON public.inbox_messages FOR EACH ROW EXECUTE FUNCTION public.create_marketplace_message_notification();


--
-- Name: marketplace_categories marketplace_categories_discovery_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER marketplace_categories_discovery_trigger AFTER UPDATE ON public.marketplace_categories FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_category();


--
-- Name: marketplace_categories marketplace_categories_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER marketplace_categories_public_resource_update_trigger AFTER UPDATE OF name ON public.marketplace_categories FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_category();


--
-- Name: payment_allocations payment_allocations_provider_daily_metric_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER payment_allocations_provider_daily_metric_trigger AFTER INSERT OR DELETE OR UPDATE OF payment_id, gross_amount_minor, business_net_amount_minor, status, currency_code ON public.payment_allocations FOR EACH ROW EXECUTE FUNCTION public.enqueue_provider_daily_metric_for_allocation();


--
-- Name: payments payments_notify_status_change; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER payments_notify_status_change AFTER UPDATE OF status, version ON public.payments FOR EACH ROW WHEN ((old.version IS DISTINCT FROM new.version)) EXECUTE FUNCTION public.notify_payment_status_change();


--
-- Name: payments payments_provider_daily_metric_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER payments_provider_daily_metric_trigger AFTER INSERT OR DELETE OR UPDATE OF client_id, paid_at, currency_code ON public.payments FOR EACH ROW EXECUTE FUNCTION public.enqueue_provider_daily_metric_for_payment();


--
-- Name: provider_availability_windows provider_availability_marketplace_discovery_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_availability_marketplace_discovery_trigger AFTER INSERT OR DELETE OR UPDATE ON public.provider_availability_windows FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: provider_availability_windows provider_availability_windows_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_availability_windows_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE ON public.provider_availability_windows FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: provider_portfolio_items provider_portfolio_items_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_portfolio_items_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE ON public.provider_portfolio_items FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_direct();


--
-- Name: provider_reviews provider_reviews_marketplace_discovery_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_reviews_marketplace_discovery_insert_delete_trigger AFTER INSERT OR DELETE ON public.provider_reviews FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('false', 'false', 'false');


--
-- Name: provider_reviews provider_reviews_marketplace_discovery_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_reviews_marketplace_discovery_update_trigger AFTER UPDATE OF client_id, rating, status ON public.provider_reviews FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('false', 'false', 'false');


--
-- Name: provider_reviews provider_reviews_public_resource_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_reviews_public_resource_insert_delete_trigger AFTER INSERT OR DELETE ON public.provider_reviews FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_public_review();


--
-- Name: provider_reviews provider_reviews_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER provider_reviews_public_resource_update_trigger AFTER UPDATE OF client_id, customer_id, author_name, rating, review_text, image_url, booking_id, service_id, status, created_at ON public.provider_reviews FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_public_review();


--
-- Name: service_availability_windows service_availability_marketplace_discovery_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER service_availability_marketplace_discovery_trigger AFTER INSERT OR DELETE OR UPDATE ON public.service_availability_windows FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_service_window();


--
-- Name: service_availability_windows service_availability_windows_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER service_availability_windows_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE ON public.service_availability_windows FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_service();


--
-- Name: service_short_notice_rules service_short_notice_rules_public_resource_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER service_short_notice_rules_public_resource_trigger AFTER INSERT OR DELETE OR UPDATE ON public.service_short_notice_rules FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_service();


--
-- Name: services services_marketplace_discovery_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER services_marketplace_discovery_insert_delete_trigger AFTER INSERT OR DELETE ON public.services FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: services services_marketplace_discovery_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER services_marketplace_discovery_update_trigger AFTER UPDATE OF client_id, title, slug, description, image_url, duration_minutes, price_amount_minor, is_active, sort_order, status, prep_time_minutes, buffer_time_minutes, availability_mode, minimum_notice_minutes, max_bookings_per_day, fulfillment_mode, max_travel_distance_meters, is_hidden, currency_code, provider_location_id ON public.services FOR EACH ROW EXECUTE FUNCTION public.enqueue_marketplace_discovery_direct('true', 'true', 'true');


--
-- Name: services services_public_resource_insert_delete_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER services_public_resource_insert_delete_trigger AFTER INSERT OR DELETE ON public.services FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_visible_service();


--
-- Name: services services_public_resource_update_trigger; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER services_public_resource_update_trigger AFTER UPDATE OF client_id, title, slug, description, category, icon_name, image_url, duration_minutes, price_amount_minor, sort_order, status, prep_time_minutes, buffer_time_minutes, availability_mode, minimum_notice_minutes, fulfillment_mode, cancellation_policy, lateness_policy, agreement_timing, is_hidden, currency_code, provider_location_id, virtual_delivery_label, agreement_template_family_id, standalone_signature_required ON public.services FOR EACH ROW EXECUTE FUNCTION public.bump_public_provider_resource_revision_for_visible_service();


--
-- Name: tessa_runs tessa_runs_wake_ai; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER tessa_runs_wake_ai AFTER INSERT OR UPDATE OF status, available_at ON public.tessa_runs FOR EACH ROW EXECUTE FUNCTION public.notify_ai_worker_queue();


--
-- Name: administrative_regions administrative_regions_parent_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.administrative_regions
    ADD CONSTRAINT administrative_regions_parent_id_fkey FOREIGN KEY (parent_id) REFERENCES public.administrative_regions(id) ON DELETE RESTRICT;


--
-- Name: agreement_acceptances agreement_acceptances_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_acceptances
    ADD CONSTRAINT agreement_acceptances_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_events agreement_events_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_events
    ADD CONSTRAINT agreement_events_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_instances agreement_instances_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: agreement_instances agreement_instances_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_instances agreement_instances_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: agreement_instances agreement_instances_template_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_template_family_id_fkey FOREIGN KEY (template_family_id) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: agreement_instances agreement_instances_template_version_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_instances
    ADD CONSTRAINT agreement_instances_template_version_id_fkey FOREIGN KEY (template_version_id) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: agreement_jobs agreement_jobs_agreement_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_jobs
    ADD CONSTRAINT agreement_jobs_agreement_id_fkey FOREIGN KEY (agreement_id) REFERENCES public.agreement_instances(id) ON DELETE CASCADE;


--
-- Name: agreement_template_families agreement_template_families_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_template_families agreement_template_families_created_by_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_created_by_client_id_fkey FOREIGN KEY (created_by_client_id) REFERENCES public.clients(id) ON DELETE SET NULL;


--
-- Name: agreement_template_families agreement_template_families_current_version_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_current_version_fkey FOREIGN KEY (id, current_published_version_id) REFERENCES public.agreement_template_versions(family_id, id) ON DELETE RESTRICT;


--
-- Name: agreement_template_families agreement_template_families_source_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_families
    ADD CONSTRAINT agreement_template_families_source_family_id_fkey FOREIGN KEY (source_family_id) REFERENCES public.agreement_template_families(id) ON DELETE SET NULL;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.agreement_template_families(id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_family_version_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_family_version_fkey FOREIGN KEY (family_id, version_id) REFERENCES public.agreement_template_versions(family_id, id) ON DELETE CASCADE;


--
-- Name: agreement_template_generation_jobs agreement_template_generation_jobs_version_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_generation_jobs
    ADD CONSTRAINT agreement_template_generation_jobs_version_id_fkey FOREIGN KEY (version_id) REFERENCES public.agreement_template_versions(id) ON DELETE CASCADE;


--
-- Name: agreement_template_versions agreement_template_versions_created_by_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_created_by_client_id_fkey FOREIGN KEY (created_by_client_id) REFERENCES public.clients(id) ON DELETE SET NULL;


--
-- Name: agreement_template_versions agreement_template_versions_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agreement_template_versions
    ADD CONSTRAINT agreement_template_versions_family_id_fkey FOREIGN KEY (family_id) REFERENCES public.agreement_template_families(id) ON DELETE CASCADE;


--
-- Name: auth_password_reset_tokens auth_password_reset_tokens_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_password_reset_tokens
    ADD CONSTRAINT auth_password_reset_tokens_user_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: auth_refresh_sessions auth_refresh_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_refresh_sessions
    ADD CONSTRAINT auth_refresh_sessions_user_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: automation_settings automation_settings_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.automation_settings
    ADD CONSTRAINT automation_settings_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: booking_change_commands booking_change_commands_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_commands
    ADD CONSTRAINT booking_change_commands_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: booking_change_commands booking_change_commands_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_commands
    ADD CONSTRAINT booking_change_commands_quote_id_fkey FOREIGN KEY (quote_id) REFERENCES public.booking_change_quotes(id) ON DELETE RESTRICT;


--
-- Name: booking_change_quotes booking_change_quotes_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_quotes
    ADD CONSTRAINT booking_change_quotes_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: booking_change_quotes booking_change_quotes_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_change_quotes
    ADD CONSTRAINT booking_change_quotes_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: booking_domain_events booking_domain_events_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_domain_events
    ADD CONSTRAINT booking_domain_events_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: booking_domain_events booking_domain_events_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_domain_events
    ADD CONSTRAINT booking_domain_events_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: booking_quote_promotions booking_quote_promotions_booking_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_booking_quote_id_fkey FOREIGN KEY (booking_quote_id) REFERENCES public.booking_quotes(id) ON DELETE CASCADE;


--
-- Name: booking_quote_promotions booking_quote_promotions_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quote_promotions
    ADD CONSTRAINT booking_quote_promotions_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_agreement_template_family_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_agreement_template_family_id_snapshot_fkey FOREIGN KEY (agreement_template_family_id_snapshot) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_agreement_template_version_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_agreement_template_version_id_snapshot_fkey FOREIGN KEY (agreement_template_version_id_snapshot) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE SET NULL;


--
-- Name: booking_quotes booking_quotes_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE RESTRICT;


--
-- Name: booking_quotes booking_quotes_short_notice_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_quotes
    ADD CONSTRAINT booking_quotes_short_notice_rule_id_fkey FOREIGN KEY (short_notice_rule_id) REFERENCES public.service_short_notice_rules(id) ON DELETE SET NULL;


--
-- Name: booking_refund_attempts booking_refund_attempts_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_attempts
    ADD CONSTRAINT booking_refund_attempts_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: booking_refund_attempts booking_refund_attempts_request_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_attempts
    ADD CONSTRAINT booking_refund_attempts_request_id_fkey FOREIGN KEY (request_id) REFERENCES public.booking_refund_requests(id) ON DELETE CASCADE;


--
-- Name: booking_refund_requests booking_refund_requests_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_requests
    ADD CONSTRAINT booking_refund_requests_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: booking_refund_requests booking_refund_requests_command_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_refund_requests
    ADD CONSTRAINT booking_refund_requests_command_id_fkey FOREIGN KEY (command_id) REFERENCES public.booking_change_commands(id) ON DELETE RESTRICT;


--
-- Name: booking_standalone_signatures booking_standalone_signatures_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.booking_standalone_signatures
    ADD CONSTRAINT booking_standalone_signatures_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_agreement_template_family_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_agreement_template_family_id_snapshot_fkey FOREIGN KEY (agreement_template_family_id_snapshot) REFERENCES public.agreement_template_families(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_agreement_template_version_id_snapshot_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_agreement_template_version_id_snapshot_fkey FOREIGN KEY (agreement_template_version_id_snapshot) REFERENCES public.agreement_template_versions(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_booking_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_booking_quote_id_fkey FOREIGN KEY (booking_quote_id) REFERENCES public.booking_quotes(id) ON DELETE RESTRICT;


--
-- Name: bookings bookings_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;


--
-- Name: bookings bookings_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE SET NULL;


--
-- Name: bookings bookings_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE SET NULL;


--
-- Name: bookings bookings_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: bookings bookings_short_notice_rule_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bookings
    ADD CONSTRAINT bookings_short_notice_rule_id_fkey FOREIGN KEY (short_notice_rule_id) REFERENCES public.service_short_notice_rules(id) ON DELETE SET NULL;


--
-- Name: business_balance_entries business_balance_entries_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: business_balance_entries business_balance_entries_payment_adjustment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_balance_entries
    ADD CONSTRAINT business_balance_entries_payment_adjustment_id_fkey FOREIGN KEY (payment_adjustment_id) REFERENCES public.payment_adjustments(id) ON DELETE RESTRICT;


--
-- Name: business_locations business_locations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: business_locations business_locations_lga_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_lga_region_id_fkey FOREIGN KEY (lga_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: business_locations business_locations_state_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.business_locations
    ADD CONSTRAINT business_locations_state_region_id_fkey FOREIGN KEY (state_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: client_profile_handles client_profile_handles_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profile_handles
    ADD CONSTRAINT client_profile_handles_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: client_profiles client_profiles_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: client_profiles client_profiles_current_handle_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_current_handle_fkey FOREIGN KEY (client_id, handle_slug) REFERENCES public.client_profile_handles(client_id, handle_slug);


--
-- Name: client_profiles client_profiles_marketplace_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.client_profiles
    ADD CONSTRAINT client_profiles_marketplace_category_id_fkey FOREIGN KEY (marketplace_category_id) REFERENCES public.marketplace_categories(id) ON DELETE SET NULL;


--
-- Name: customers customers_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.customers
    ADD CONSTRAINT customers_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_actions inbox_ai_actions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_actions inbox_ai_actions_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_actions inbox_ai_actions_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.inbox_ai_sessions(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_actions inbox_ai_actions_turn_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_actions
    ADD CONSTRAINT inbox_ai_actions_turn_job_id_fkey FOREIGN KEY (turn_job_id) REFERENCES public.inbox_ai_turn_jobs(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_agreement_instance_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_agreement_instance_id_fkey FOREIGN KEY (agreement_instance_id) REFERENCES public.agreement_instances(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_booking_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_booking_session_id_fkey FOREIGN KEY (booking_session_id) REFERENCES public.inbox_ai_booking_sessions(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_confirmations inbox_ai_booking_confirmations_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_confirmations
    ADD CONSTRAINT inbox_ai_booking_confirmations_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_agreement_instance_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_agreement_instance_id_fkey FOREIGN KEY (agreement_instance_id) REFERENCES public.agreement_instances(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_ai_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_ai_session_id_fkey FOREIGN KEY (ai_session_id) REFERENCES public.inbox_ai_sessions(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_confirmation_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_confirmation_fkey FOREIGN KEY (confirmation_id) REFERENCES public.inbox_ai_booking_confirmations(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_quote_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_quote_id_fkey FOREIGN KEY (quote_id) REFERENCES public.booking_quotes(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_booking_sessions inbox_ai_booking_sessions_selected_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_booking_sessions
    ADD CONSTRAINT inbox_ai_booking_sessions_selected_service_id_fkey FOREIGN KEY (selected_service_id) REFERENCES public.services(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_conversation_controls inbox_ai_conversation_controls_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_conversation_controls
    ADD CONSTRAINT inbox_ai_conversation_controls_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_conversation_controls inbox_ai_conversation_controls_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_conversation_controls
    ADD CONSTRAINT inbox_ai_conversation_controls_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_policies inbox_ai_policies_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_policies
    ADD CONSTRAINT inbox_ai_policies_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_policies inbox_ai_policies_updated_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_policies
    ADD CONSTRAINT inbox_ai_policies_updated_by_fkey FOREIGN KEY (updated_by) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: inbox_ai_runs inbox_ai_runs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_runs inbox_ai_runs_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_runs inbox_ai_runs_provider_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_provider_message_id_fkey FOREIGN KEY (provider_message_id) REFERENCES public.inbox_messages(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_runs inbox_ai_runs_requested_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_requested_by_fkey FOREIGN KEY (requested_by) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_runs inbox_ai_runs_resulting_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_resulting_message_id_fkey FOREIGN KEY (resulting_message_id) REFERENCES public.inbox_messages(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_runs inbox_ai_runs_turn_job_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_runs
    ADD CONSTRAINT inbox_ai_runs_turn_job_id_fkey FOREIGN KEY (turn_job_id) REFERENCES public.inbox_ai_turn_jobs(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_sessions inbox_ai_sessions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_sessions inbox_ai_sessions_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_sessions inbox_ai_sessions_last_ai_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_last_ai_message_id_fkey FOREIGN KEY (last_ai_message_id) REFERENCES public.inbox_messages(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_sessions inbox_ai_sessions_selected_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_sessions
    ADD CONSTRAINT inbox_ai_sessions_selected_service_id_fkey FOREIGN KEY (selected_service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_session_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_session_id_fkey FOREIGN KEY (session_id) REFERENCES public.inbox_ai_sessions(id) ON DELETE CASCADE;


--
-- Name: inbox_ai_turn_jobs inbox_ai_turn_jobs_trigger_message_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_ai_turn_jobs
    ADD CONSTRAINT inbox_ai_turn_jobs_trigger_message_id_fkey FOREIGN KEY (trigger_message_id) REFERENCES public.inbox_messages(id) ON DELETE CASCADE;


--
-- Name: inbox_conversation_bookings inbox_conversation_bookings_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversation_bookings
    ADD CONSTRAINT inbox_conversation_bookings_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: inbox_conversation_bookings inbox_conversation_bookings_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversation_bookings
    ADD CONSTRAINT inbox_conversation_bookings_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_conversation_moderation_events inbox_conversation_moderation_events_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversation_moderation_events
    ADD CONSTRAINT inbox_conversation_moderation_events_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_conversations inbox_conversations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_conversations inbox_conversations_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE CASCADE;


--
-- Name: inbox_conversations inbox_conversations_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_conversations
    ADD CONSTRAINT inbox_conversations_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: inbox_events inbox_events_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_events
    ADD CONSTRAINT inbox_events_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: inbox_events inbox_events_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_events
    ADD CONSTRAINT inbox_events_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_events inbox_events_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_events
    ADD CONSTRAINT inbox_events_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: inbox_messages inbox_messages_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: inbox_messages inbox_messages_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_messages
    ADD CONSTRAINT inbox_messages_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: inbox_participant_states inbox_participant_states_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.inbox_participant_states
    ADD CONSTRAINT inbox_participant_states_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE CASCADE;


--
-- Name: marketplace_auth_challenges marketplace_auth_challenges_target_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_auth_challenges
    ADD CONSTRAINT marketplace_auth_challenges_target_customer_id_fkey FOREIGN KEY (target_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_auth_sessions marketplace_auth_sessions_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_auth_sessions
    ADD CONSTRAINT marketplace_auth_sessions_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_customer_addresses marketplace_customer_addresses_lga_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_addresses
    ADD CONSTRAINT marketplace_customer_addresses_lga_region_id_fkey FOREIGN KEY (lga_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: marketplace_customer_addresses marketplace_customer_addresses_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_addresses
    ADD CONSTRAINT marketplace_customer_addresses_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_customer_addresses marketplace_customer_addresses_state_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_addresses
    ADD CONSTRAINT marketplace_customer_addresses_state_region_id_fkey FOREIGN KEY (state_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: marketplace_customer_identities marketplace_customer_identities_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_customer_identities
    ADD CONSTRAINT marketplace_customer_identities_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_notification_preferences marketplace_notification_preferenc_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notification_preferences
    ADD CONSTRAINT marketplace_notification_preferenc_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_notifications marketplace_notifications_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: marketplace_notifications marketplace_notifications_conversation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_conversation_id_fkey FOREIGN KEY (conversation_id) REFERENCES public.inbox_conversations(id) ON DELETE SET NULL;


--
-- Name: marketplace_notifications marketplace_notifications_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_notifications marketplace_notifications_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_notifications
    ADD CONSTRAINT marketplace_notifications_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.clients(id) ON DELETE SET NULL;


--
-- Name: marketplace_provider_documents marketplace_provider_documents_category_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_provider_documents
    ADD CONSTRAINT marketplace_provider_documents_category_id_fkey FOREIGN KEY (category_id) REFERENCES public.marketplace_categories(id) ON DELETE RESTRICT;


--
-- Name: marketplace_provider_documents marketplace_provider_documents_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_provider_documents
    ADD CONSTRAINT marketplace_provider_documents_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: marketplace_saved_providers marketplace_saved_providers_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_saved_providers
    ADD CONSTRAINT marketplace_saved_providers_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: marketplace_saved_providers marketplace_saved_providers_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_saved_providers
    ADD CONSTRAINT marketplace_saved_providers_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: marketplace_service_availability_days marketplace_service_availability_days_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_availability_days
    ADD CONSTRAINT marketplace_service_availability_days_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.marketplace_provider_documents(client_id) ON DELETE CASCADE;


--
-- Name: marketplace_service_availability_days marketplace_service_availability_days_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_availability_days
    ADD CONSTRAINT marketplace_service_availability_days_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.marketplace_service_documents(service_id) ON DELETE CASCADE;


--
-- Name: marketplace_service_documents marketplace_service_documents_lga_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_lga_region_id_fkey FOREIGN KEY (lga_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: marketplace_service_documents marketplace_service_documents_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.marketplace_provider_documents(client_id) ON DELETE CASCADE;


--
-- Name: marketplace_service_documents marketplace_service_documents_provider_location_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_provider_location_id_fkey FOREIGN KEY (provider_location_id) REFERENCES public.business_locations(id) ON DELETE CASCADE;


--
-- Name: marketplace_service_documents marketplace_service_documents_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: marketplace_service_documents marketplace_service_documents_state_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.marketplace_service_documents
    ADD CONSTRAINT marketplace_service_documents_state_region_id_fkey FOREIGN KEY (state_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: notification_deliveries notification_deliveries_booking_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_booking_event_id_fkey FOREIGN KEY (booking_event_id) REFERENCES public.booking_domain_events(id) ON DELETE SET NULL;


--
-- Name: notification_deliveries notification_deliveries_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: notification_deliveries notification_deliveries_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: notification_deliveries notification_deliveries_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE SET NULL;


--
-- Name: notification_event_jobs notification_event_jobs_booking_event_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_event_jobs
    ADD CONSTRAINT notification_event_jobs_booking_event_id_fkey FOREIGN KEY (booking_event_id) REFERENCES public.booking_domain_events(id) ON DELETE CASCADE;


--
-- Name: notification_in_app_jobs notification_in_app_jobs_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_in_app_jobs
    ADD CONSTRAINT notification_in_app_jobs_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE CASCADE;


--
-- Name: notification_in_app_jobs notification_in_app_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_in_app_jobs
    ADD CONSTRAINT notification_in_app_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: notification_in_app_jobs notification_in_app_jobs_marketplace_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_in_app_jobs
    ADD CONSTRAINT notification_in_app_jobs_marketplace_customer_id_fkey FOREIGN KEY (marketplace_customer_id) REFERENCES public.marketplace_customers(id) ON DELETE CASCADE;


--
-- Name: notification_scope_replan_jobs notification_scope_replan_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notification_scope_replan_jobs
    ADD CONSTRAINT notification_scope_replan_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: notifications notifications_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: notifications notifications_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.notifications
    ADD CONSTRAINT notifications_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: payment_adjustments payment_adjustments_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_adjustments
    ADD CONSTRAINT payment_adjustments_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payment_allocations payment_allocations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payment_allocations payment_allocations_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_allocations
    ADD CONSTRAINT payment_allocations_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payment_exceptions payment_exceptions_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: payment_exceptions payment_exceptions_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payment_exceptions
    ADD CONSTRAINT payment_exceptions_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: payments payments_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE RESTRICT;


--
-- Name: payments payments_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payments payments_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payments
    ADD CONSTRAINT payments_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE RESTRICT;


--
-- Name: payout_destinations payout_destinations_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payout_destinations
    ADD CONSTRAINT payout_destinations_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_payment_allocation_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_payment_allocation_id_fkey FOREIGN KEY (payment_allocation_id) REFERENCES public.payment_allocations(id) ON DELETE RESTRICT;


--
-- Name: payouts payouts_payout_destination_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.payouts
    ADD CONSTRAINT payouts_payout_destination_id_fkey FOREIGN KEY (payout_destination_id) REFERENCES public.payout_destinations(id) ON DELETE RESTRICT;


--
-- Name: promotion_redemptions promotion_redemptions_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: promotion_redemptions promotion_redemptions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: promotion_redemptions promotion_redemptions_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: promotion_redemptions promotion_redemptions_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_redemptions
    ADD CONSTRAINT promotion_redemptions_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_sections promotion_sections_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_sections promotion_sections_section_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_sections
    ADD CONSTRAINT promotion_sections_section_id_fkey FOREIGN KEY (section_id) REFERENCES public.service_sections(id) ON DELETE CASCADE;


--
-- Name: promotion_services promotion_services_promotion_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_promotion_id_fkey FOREIGN KEY (promotion_id) REFERENCES public.promotions(id) ON DELETE CASCADE;


--
-- Name: promotion_services promotion_services_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotion_services
    ADD CONSTRAINT promotion_services_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: promotions promotions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.promotions
    ADD CONSTRAINT promotions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_availability_windows provider_availability_windows_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_availability_windows
    ADD CONSTRAINT provider_availability_windows_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_daily_metric_jobs provider_daily_metric_jobs_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_daily_metric_jobs
    ADD CONSTRAINT provider_daily_metric_jobs_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_daily_metrics provider_daily_metrics_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_daily_metrics
    ADD CONSTRAINT provider_daily_metrics_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_notification_preferences provider_notification_preferences_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_notification_preferences
    ADD CONSTRAINT provider_notification_preferences_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_portfolio_items provider_portfolio_items_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_portfolio_items provider_portfolio_items_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_portfolio_items
    ADD CONSTRAINT provider_portfolio_items_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: provider_reviews provider_reviews_booking_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_booking_id_fkey FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL;


--
-- Name: provider_reviews provider_reviews_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: provider_reviews provider_reviews_customer_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_customer_id_fkey FOREIGN KEY (customer_id) REFERENCES public.customers(id) ON DELETE SET NULL;


--
-- Name: provider_reviews provider_reviews_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_reviews
    ADD CONSTRAINT provider_reviews_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL;


--
-- Name: provider_settlement_evidence provider_settlement_evidence_payment_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settlement_evidence
    ADD CONSTRAINT provider_settlement_evidence_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES public.payments(id) ON DELETE RESTRICT;


--
-- Name: provider_whatsapp_verification_challenges provider_whatsapp_verification_challenges_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_whatsapp_verification_challenges
    ADD CONSTRAINT provider_whatsapp_verification_challenges_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: public_provider_resource_revisions public_provider_resource_revisions_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.public_provider_resource_revisions
    ADD CONSTRAINT public_provider_resource_revisions_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: resolved_locations resolved_locations_lga_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_lga_region_id_fkey FOREIGN KEY (lga_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: resolved_locations resolved_locations_state_region_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.resolved_locations
    ADD CONSTRAINT resolved_locations_state_region_id_fkey FOREIGN KEY (state_region_id) REFERENCES public.administrative_regions(id) ON DELETE SET NULL;


--
-- Name: service_availability_windows service_availability_windows_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_availability_windows
    ADD CONSTRAINT service_availability_windows_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: service_sections service_sections_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_sections
    ADD CONSTRAINT service_sections_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: service_short_notice_rules service_short_notice_rules_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_short_notice_rules
    ADD CONSTRAINT service_short_notice_rules_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: service_wizard_drafts service_wizard_drafts_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: service_wizard_drafts service_wizard_drafts_service_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.service_wizard_drafts
    ADD CONSTRAINT service_wizard_drafts_service_id_fkey FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE CASCADE;


--
-- Name: services services_agreement_template_family_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_agreement_template_family_id_fkey FOREIGN KEY (agreement_template_family_id) REFERENCES public.agreement_template_families(id) ON DELETE SET NULL;


--
-- Name: services services_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: services services_client_provider_location_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_client_provider_location_fk FOREIGN KEY (client_id, provider_location_id) REFERENCES public.business_locations(client_id, id) ON DELETE RESTRICT;


--
-- Name: services services_section_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.services
    ADD CONSTRAINT services_section_id_fkey FOREIGN KEY (section_id) REFERENCES public.service_sections(id) ON DELETE SET NULL;


--
-- Name: tessa_events tessa_events_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_events
    ADD CONSTRAINT tessa_events_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: tessa_events tessa_events_message_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_events
    ADD CONSTRAINT tessa_events_message_fk FOREIGN KEY (message_id, thread_id, client_id) REFERENCES public.tessa_messages(id, thread_id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_events tessa_events_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_events
    ADD CONSTRAINT tessa_events_run_fk FOREIGN KEY (run_id, thread_id, client_id) REFERENCES public.tessa_runs(id, thread_id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_events tessa_events_thread_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_events
    ADD CONSTRAINT tessa_events_thread_fk FOREIGN KEY (thread_id, client_id) REFERENCES public.tessa_threads(id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_messages tessa_messages_result_run_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_messages
    ADD CONSTRAINT tessa_messages_result_run_fk FOREIGN KEY (run_id, thread_id, client_id) REFERENCES public.tessa_runs(id, thread_id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_messages tessa_messages_thread_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_messages
    ADD CONSTRAINT tessa_messages_thread_fk FOREIGN KEY (thread_id, client_id) REFERENCES public.tessa_threads(id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_preferences tessa_preferences_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_preferences
    ADD CONSTRAINT tessa_preferences_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- Name: tessa_run_steps tessa_run_steps_run_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_run_steps
    ADD CONSTRAINT tessa_run_steps_run_id_fkey FOREIGN KEY (run_id) REFERENCES public.tessa_runs(id) ON DELETE CASCADE;


--
-- Name: tessa_runs tessa_runs_thread_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_runs
    ADD CONSTRAINT tessa_runs_thread_fk FOREIGN KEY (thread_id, client_id) REFERENCES public.tessa_threads(id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_runs tessa_runs_trigger_fk; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_runs
    ADD CONSTRAINT tessa_runs_trigger_fk FOREIGN KEY (trigger_message_id, thread_id, client_id) REFERENCES public.tessa_messages(id, thread_id, client_id) ON DELETE CASCADE;


--
-- Name: tessa_threads tessa_threads_client_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tessa_threads
    ADD CONSTRAINT tessa_threads_client_id_fkey FOREIGN KEY (client_id) REFERENCES public.clients(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--


--
-- Dbmate schema migrations
--

INSERT INTO public.schema_migrations (version) VALUES
    ('20260808140000'),
    ('20260818120000'),
    ('20260820010000'),
    ('20260820020000'),
    ('20260820101500'),
    ('20260820120000'),
    ('20260822100000'),
    ('20260823010000'),
    ('20260823020000'),
    ('20260823030000'),
    ('20260823040000'),
    ('20260823050000'),
    ('20260823060000'),
    ('20260823070000'),
    ('20260823080000'),
    ('20260824010000'),
    ('20260824020000'),
    ('20260824030000'),
    ('20260824040000'),
    ('20260825010000'),
    ('20260825020000'),
    ('20260825030000'),
    ('20260825040000'),
    ('20260825050000'),
    ('20260826010000'),
    ('20260827010000'),
    ('20260827020000'),
    ('20260827030000'),
    ('20260827040000'),
    ('20260827050000'),
    ('20260827060000'),
    ('20260827070000'),
    ('20260827080000'),
    ('20260827090000'),
    ('20260828010000'),
    ('20260830010000'),
    ('20260830020000'),
    ('20260830030000'),
    ('20260831010000'),
    ('20260831020000'),
    ('20260831030000'),
    ('20260831040000'),
    ('20260831050000'),
    ('20260831060000'),
    ('20260901010000'),
    ('20260901020000'),
    ('20260901030000'),
    ('20260901031000'),
    ('20260901032000'),
    ('20260901040000'),
    ('20260901050000'),
    ('20260901051000'),
    ('20260902010000'),
    ('20260902011000'),
    ('20260904010000'),
    ('20260904020000'),
    ('20260904030000'),
    ('20260904031000'),
    ('20260904032000');
