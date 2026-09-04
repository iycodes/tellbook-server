-- migrate:up
CREATE EXTENSION IF NOT EXISTS pg_stat_statements WITH SCHEMA public;

CREATE TABLE marketplace_saved_providers (
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    provider_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY (marketplace_customer_id, provider_id)
);

CREATE INDEX marketplace_saved_providers_customer_recent_idx
    ON marketplace_saved_providers (marketplace_customer_id, created_at DESC, provider_id DESC);

CREATE TABLE marketplace_notifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    marketplace_customer_id uuid NOT NULL REFERENCES marketplace_customers(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('booking', 'message', 'offer', 'system')),
    event_type text NOT NULL CHECK (BTRIM(event_type) <> ''),
    event_key text NOT NULL CHECK (BTRIM(event_key) <> ''),
    title text NOT NULL CHECK (BTRIM(title) <> '' AND CHAR_LENGTH(title) <= 180),
    body text NOT NULL DEFAULT '' CHECK (CHAR_LENGTH(body) <= 1000),
    provider_id uuid REFERENCES clients(id) ON DELETE SET NULL,
    booking_id uuid REFERENCES bookings(id) ON DELETE CASCADE,
    conversation_id uuid REFERENCES inbox_conversations(id) ON DELETE CASCADE,
    read_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    UNIQUE (marketplace_customer_id, event_key)
);

CREATE INDEX marketplace_notifications_customer_recent_idx
    ON marketplace_notifications (marketplace_customer_id, created_at DESC, id DESC);
CREATE INDEX marketplace_notifications_customer_unread_idx
    ON marketplace_notifications (marketplace_customer_id, created_at DESC, id DESC)
    WHERE read_at IS NULL;

CREATE FUNCTION create_marketplace_booking_notification() RETURNS trigger
LANGUAGE plpgsql AS $$
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

CREATE TRIGGER booking_domain_events_marketplace_notification
AFTER INSERT ON booking_domain_events
FOR EACH ROW EXECUTE FUNCTION create_marketplace_booking_notification();

CREATE FUNCTION create_marketplace_message_notification() RETURNS trigger
LANGUAGE plpgsql AS $$
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

CREATE TRIGGER inbox_messages_marketplace_notification
AFTER INSERT ON inbox_messages
FOR EACH ROW EXECUTE FUNCTION create_marketplace_message_notification();

-- migrate:down
DROP TRIGGER inbox_messages_marketplace_notification ON inbox_messages;
DROP FUNCTION create_marketplace_message_notification();
DROP TRIGGER booking_domain_events_marketplace_notification ON booking_domain_events;
DROP FUNCTION create_marketplace_booking_notification();
DROP TABLE marketplace_notifications;
DROP TABLE marketplace_saved_providers;
