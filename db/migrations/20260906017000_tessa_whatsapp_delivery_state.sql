-- migrate:up
ALTER TABLE tessa_whatsapp_outbox
    ADD COLUMN accepted_at timestamptz,
    ADD COLUMN sent_at timestamptz,
    ADD COLUMN delivered_at timestamptz,
    ADD COLUMN read_at timestamptz,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT NOW();

-- Retain only timestamps for which existing rows already contain evidence.
UPDATE tessa_whatsapp_outbox SET
    accepted_at=CASE WHEN status='accepted' THEN completed_at END,
    sent_at=CASE WHEN status='sent' THEN status_at END,
    delivered_at=CASE WHEN status='delivered' THEN status_at END,
    read_at=CASE WHEN status='read' THEN status_at END;

CREATE FUNCTION tessa_whatsapp_public_status(value text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT CASE WHEN value IN ('pending','processing','dispatching','retry') THEN 'pending' ELSE value END;
$$;

ALTER TABLE tessa_events DROP CONSTRAINT tessa_events_type_check,
    ADD CONSTRAINT tessa_events_type_check CHECK (event_type IN (
        'thread.created','message.created','message.delivery_changed','run.started','run.stage_changed',
        'run.completed','run.failed','run.cancelled'
    )),
    DROP CONSTRAINT tessa_events_reference_check,
    ADD CONSTRAINT tessa_events_reference_check CHECK (
        (event_type='thread.created' AND message_id IS NULL AND run_id IS NULL)
        OR (event_type IN ('message.created','message.delivery_changed') AND message_id IS NOT NULL)
        OR (event_type LIKE 'run.%' AND run_id IS NOT NULL)
    );

CREATE FUNCTION tessa_whatsapp_delivery_timestamps() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status IS DISTINCT FROM OLD.status THEN
        NEW.updated_at=clock_timestamp();
        IF NEW.status IN ('accepted','sent','delivered','read') THEN
            NEW.accepted_at=COALESCE(OLD.accepted_at,clock_timestamp());
        END IF;
        IF NEW.status='sent' THEN NEW.sent_at=COALESCE(OLD.sent_at,NEW.status_at); END IF;
        IF NEW.status='delivered' THEN NEW.delivered_at=COALESCE(OLD.delivered_at,NEW.status_at); END IF;
        IF NEW.status='read' THEN NEW.read_at=COALESCE(OLD.read_at,NEW.status_at); END IF;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER tessa_whatsapp_delivery_timestamps BEFORE UPDATE OF status ON tessa_whatsapp_outbox
    FOR EACH ROW EXECUTE FUNCTION tessa_whatsapp_delivery_timestamps();

-- Every durable public transition, including revocation/expiry, publishes through
-- the existing Tessa event stream. Internal lease/retry transitions stay quiet.
-- Writers acquire the account before the outbox row (including callback/finalize).
CREATE FUNCTION tessa_whatsapp_publish_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_sequence bigint;
BEGIN
    IF NEW.kind='answer' AND tessa_whatsapp_public_status(NEW.status) IS DISTINCT FROM tessa_whatsapp_public_status(OLD.status) THEN
        INSERT INTO tessa_events(client_id,thread_id,message_id,event_type)
        VALUES(NEW.client_id,NEW.thread_id,NEW.assistant_message_id,'message.delivery_changed')
        RETURNING sequence INTO event_sequence;
        PERFORM pg_notify('tellbook_tessa_events',event_sequence::text || '|' || NEW.client_id::text);
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER tessa_whatsapp_publish_delivery AFTER UPDATE OF status ON tessa_whatsapp_outbox
    FOR EACH ROW EXECUTE FUNCTION tessa_whatsapp_publish_delivery();

-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Delivery timestamps/events require an explicit data-preserving downgrade'; END $$;
