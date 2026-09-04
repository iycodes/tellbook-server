-- migrate:up
CREATE TABLE booking_domain_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id uuid NOT NULL UNIQUE,
    client_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    booking_id uuid NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    dedupe_key text NOT NULL UNIQUE,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT booking_domain_events_event_type_check CHECK (BTRIM(event_type) <> ''),
    CONSTRAINT booking_domain_events_dedupe_key_check CHECK (BTRIM(dedupe_key) <> ''),
    CONSTRAINT booking_domain_events_payload_object_check CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX booking_domain_events_client_sequence_idx
    ON booking_domain_events (client_id, sequence);

-- migrate:down
DROP TABLE booking_domain_events;
