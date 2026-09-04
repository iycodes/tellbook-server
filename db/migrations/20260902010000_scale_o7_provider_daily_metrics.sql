-- migrate:up
CREATE TABLE provider_daily_metrics (
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    metric_date date NOT NULL,
    currency_code text NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
    total_bookings integer NOT NULL DEFAULT 0 CHECK (total_bookings >= 0),
    completed_bookings integer NOT NULL DEFAULT 0 CHECK (completed_bookings >= 0),
    scheduled_bookings integer NOT NULL DEFAULT 0 CHECK (scheduled_bookings >= 0),
    cancelled_bookings integer NOT NULL DEFAULT 0 CHECK (cancelled_bookings >= 0),
    secured_bookings integer NOT NULL DEFAULT 0 CHECK (secured_bookings >= 0),
    unique_customers integer NOT NULL DEFAULT 0 CHECK (unique_customers >= 0),
    booked_value_minor bigint NOT NULL DEFAULT 0 CHECK (booked_value_minor >= 0),
    gross_revenue_minor bigint NOT NULL DEFAULT 0 CHECK (gross_revenue_minor >= 0),
    net_revenue_minor bigint NOT NULL DEFAULT 0 CHECK (net_revenue_minor >= 0),
    payment_count integer NOT NULL DEFAULT 0 CHECK (payment_count >= 0),
    projected_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (client_id, metric_date, currency_code)
);

CREATE INDEX provider_daily_metrics_client_currency_date_idx
ON provider_daily_metrics (client_id, currency_code, metric_date);

CREATE TABLE provider_daily_metric_jobs (
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    metric_date date NOT NULL,
    currency_code text NOT NULL CHECK (currency_code ~ '^[A-Z]{3}$'),
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    enqueued_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (client_id, metric_date, currency_code)
);

CREATE INDEX provider_daily_metric_jobs_enqueued_idx
ON provider_daily_metric_jobs (enqueued_at, client_id, metric_date, currency_code);

ALTER TABLE provider_daily_metric_jobs SET (
    autovacuum_vacuum_scale_factor = 0.02,
    autovacuum_analyze_scale_factor = 0.01,
    autovacuum_vacuum_threshold = 100,
    autovacuum_analyze_threshold = 100
);

CREATE FUNCTION enqueue_provider_daily_metric(
    p_client_id uuid,
    p_event_at timestamptz,
    p_currency_code text
) RETURNS void
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

CREATE FUNCTION enqueue_provider_daily_metric_for_booking() RETURNS trigger
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

CREATE FUNCTION enqueue_provider_daily_metric_for_payment() RETURNS trigger
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

CREATE FUNCTION enqueue_provider_daily_metric_for_allocation() RETURNS trigger
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

CREATE FUNCTION enqueue_provider_daily_metrics_for_profile_timezone() RETURNS trigger
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

CREATE TRIGGER bookings_provider_daily_metric_trigger
AFTER INSERT OR DELETE OR UPDATE OF client_id, customer_id, status, payment_status,
    start_at, total_amount_minor, currency_code
ON bookings
FOR EACH ROW EXECUTE FUNCTION enqueue_provider_daily_metric_for_booking();

CREATE TRIGGER payments_provider_daily_metric_trigger
AFTER INSERT OR DELETE OR UPDATE OF client_id, paid_at, currency_code
ON payments
FOR EACH ROW EXECUTE FUNCTION enqueue_provider_daily_metric_for_payment();

CREATE TRIGGER payment_allocations_provider_daily_metric_trigger
AFTER INSERT OR DELETE OR UPDATE OF payment_id, gross_amount_minor,
    business_net_amount_minor, status, currency_code
ON payment_allocations
FOR EACH ROW EXECUTE FUNCTION enqueue_provider_daily_metric_for_allocation();

CREATE TRIGGER client_profiles_provider_daily_metric_trigger
AFTER UPDATE OF timezone, currency_code ON client_profiles
FOR EACH ROW EXECUTE FUNCTION enqueue_provider_daily_metrics_for_profile_timezone();

INSERT INTO provider_daily_metric_jobs (client_id, metric_date, currency_code)
SELECT DISTINCT booking.client_id,
    (booking.start_at AT TIME ZONE profile.timezone)::date,
    booking.currency_code
FROM bookings booking
JOIN client_profiles profile ON profile.client_id = booking.client_id
WHERE booking.start_at >= NOW() - INTERVAL '366 days'
ON CONFLICT (client_id, metric_date, currency_code) DO NOTHING;

INSERT INTO provider_daily_metric_jobs (client_id, metric_date, currency_code)
SELECT DISTINCT payment.client_id,
    (payment.paid_at AT TIME ZONE profile.timezone)::date,
    payment.currency_code
FROM payments payment
JOIN client_profiles profile ON profile.client_id = payment.client_id
WHERE payment.paid_at >= NOW() - INTERVAL '366 days'
ON CONFLICT (client_id, metric_date, currency_code) DO UPDATE
SET revision = provider_daily_metric_jobs.revision + 1,
    enqueued_at = NOW();

-- migrate:down
DROP TRIGGER client_profiles_provider_daily_metric_trigger ON client_profiles;
DROP TRIGGER payment_allocations_provider_daily_metric_trigger ON payment_allocations;
DROP TRIGGER payments_provider_daily_metric_trigger ON payments;
DROP TRIGGER bookings_provider_daily_metric_trigger ON bookings;
DROP FUNCTION enqueue_provider_daily_metrics_for_profile_timezone();
DROP FUNCTION enqueue_provider_daily_metric_for_allocation();
DROP FUNCTION enqueue_provider_daily_metric_for_payment();
DROP FUNCTION enqueue_provider_daily_metric_for_booking();
DROP FUNCTION enqueue_provider_daily_metric(uuid, timestamptz, text);
DROP TABLE provider_daily_metric_jobs;
DROP TABLE provider_daily_metrics;
