-- migrate:up
ALTER TABLE booking_domain_events ADD COLUMN additional_email_enabled boolean NOT NULL DEFAULT false;
CREATE FUNCTION mark_booking_event_additional_email() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.additional_email_enabled=COALESCE(current_setting('tellbook.financial_emails',true)='true',false); RETURN NEW; END $$;
CREATE TRIGGER booking_event_additional_email_marker BEFORE INSERT ON booking_domain_events FOR EACH ROW EXECUTE FUNCTION mark_booking_event_additional_email();
-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Additional email event history must not be discarded by an automatic downgrade'; END $$;
