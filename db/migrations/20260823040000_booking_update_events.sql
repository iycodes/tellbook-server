-- migrate:up
CREATE FUNCTION append_booking_update_event() RETURNS trigger AS $$
BEGIN
    IF ROW(OLD.status, OLD.payment_status, OLD.agreement_status, OLD.start_at, OLD.end_at)
       IS DISTINCT FROM
       ROW(NEW.status, NEW.payment_status, NEW.agreement_status, NEW.start_at, NEW.end_at) THEN
        INSERT INTO booking_domain_events (
            id, client_id, booking_id, event_type, dedupe_key, payload, created_at
        ) VALUES (
            gen_random_uuid(), NEW.client_id, NEW.id, 'booking_updated',
            'booking-updated:' || gen_random_uuid()::text,
            jsonb_build_object(
                'booking_id', NEW.id::text,
                'status', NEW.status,
                'payment_status', NEW.payment_status,
                'agreement_status', NEW.agreement_status,
                'starts_at', NEW.start_at
            ),
            NOW()
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER bookings_append_update_event
AFTER UPDATE ON bookings
FOR EACH ROW EXECUTE FUNCTION append_booking_update_event();

-- migrate:down
DROP TRIGGER bookings_append_update_event ON bookings;
DROP FUNCTION append_booking_update_event();
