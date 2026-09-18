-- migrate:up
ALTER TABLE financial_jobs DROP CONSTRAINT financial_jobs_status_check;
ALTER TABLE financial_jobs ADD CONSTRAINT financial_jobs_status_check CHECK (status IN ('pending','processing','completed','failed','cancelled','dead_letter') OR (kind='financial_email' AND status IN ('dispatching','unknown')));
ALTER TABLE booking_refund_requests ADD COLUMN notification_revision bigint NOT NULL DEFAULT 1;
CREATE FUNCTION advance_refund_notification_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IS DISTINCT FROM OLD.status THEN NEW.notification_revision=OLD.notification_revision+1; ELSE NEW.notification_revision=OLD.notification_revision; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER refund_notification_revision BEFORE UPDATE ON booking_refund_requests FOR EACH ROW EXECUTE FUNCTION advance_refund_notification_revision();

CREATE FUNCTION capture_financial_email() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE audience text; recipient text; owner_id uuid; b bookings%ROWTYPE; revision bigint; delay interval; base jsonb;
BEGIN
 IF current_setting('tellbook.financial_emails',true) IS DISTINCT FROM 'true' THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN RETURN NEW; END IF;
 delay=CASE WHEN NEW.status IN ('pending','processing','queued') THEN INTERVAL '5 minutes' WHEN NEW.status IN ('unknown','manual_review') THEN INTERVAL '30 minutes' ELSE INTERVAL '0 seconds' END;
 IF TG_TABLE_NAME='payouts' THEN
  IF NEW.status='created' THEN RETURN NEW; END IF;
  SELECT lower(btrim(email)) INTO recipient FROM clients WHERE id=NEW.client_id AND email_verified_at IS NOT NULL;
  IF COALESCE(recipient,'')='' THEN RETURN NEW; END IF;
  revision=NEW.version;
  base=jsonb_build_object('family','payout','audience','provider','recipient',recipient,'client_id',NEW.client_id,'status',NEW.status,'revision',revision,'occurred_at',NOW(),'amount_minor',NEW.amount_minor,'currency_code',NEW.currency_code,'country_code',NEW.country_code,'reference',NEW.reference,'institution_name',NEW.destination_snapshot->>'institution_name','account_last_four',right(NEW.destination_snapshot->>'masked_identifier',4));
  INSERT INTO financial_jobs(id,kind,aggregate_type,aggregate_id,deduplication_key,payload,available_at)
  VALUES(gen_random_uuid(),'financial_email','payout',NEW.id,'financial_email:payout:'||NEW.id||':'||revision||':provider',base,NOW()+delay) ON CONFLICT(deduplication_key) DO NOTHING;
 ELSE
  IF NEW.status NOT IN ('queued','processing','failed','manual_review') THEN RETURN NEW; END IF;
  SELECT * INTO b FROM bookings WHERE id=NEW.booking_id;
  revision=NEW.notification_revision;
  FOREACH audience IN ARRAY ARRAY['provider','customer'] LOOP
   IF audience='provider' THEN
    SELECT lower(btrim(c.email)) INTO recipient FROM clients c LEFT JOIN provider_notification_preferences p ON p.client_id=c.id WHERE c.id=b.client_id AND c.email_verified_at IS NOT NULL AND COALESCE(p.booking_email,true);
   ELSE
    SELECT b.customer_email_snapshot INTO recipient WHERE b.notification_consent_policy_revision=1 AND COALESCE((SELECT booking_email FROM marketplace_notification_preferences WHERE marketplace_customer_id=b.marketplace_customer_id),true);
   END IF;
   IF COALESCE(recipient,'')='' THEN CONTINUE; END IF;
   base=jsonb_build_object('family','refund','audience',audience,'recipient',recipient,'client_id',b.client_id,'booking_id',b.id,'status',NEW.status,'revision',revision,'occurred_at',NOW(),'amount_minor',NEW.amount_minor,'currency_code',NEW.currency_code,'country_code',b.country_code,'reference',NEW.id::text);
   INSERT INTO financial_jobs(id,kind,aggregate_type,aggregate_id,deduplication_key,payload,available_at)
   VALUES(gen_random_uuid(),'financial_email','booking_refund',NEW.id,'financial_email:refund:'||NEW.id||':'||revision||':'||audience,base,NOW()+delay) ON CONFLICT(deduplication_key) DO NOTHING;
  END LOOP;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payout_email_event AFTER UPDATE OF status ON payouts FOR EACH ROW EXECUTE FUNCTION capture_financial_email();
CREATE TRIGGER refund_email_event AFTER INSERT OR UPDATE OF status ON booking_refund_requests FOR EACH ROW EXECUTE FUNCTION capture_financial_email();

-- Only bookings created in an explicitly enabled transaction get reminders.
ALTER TABLE bookings ADD COLUMN additional_email_reminder_enabled boolean NOT NULL DEFAULT false;
CREATE FUNCTION mark_booking_additional_email() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.additional_email_reminder_enabled=COALESCE(current_setting('tellbook.financial_emails',true)='true',false); RETURN NEW; END $$;
CREATE TRIGGER booking_additional_email_marker BEFORE INSERT ON bookings FOR EACH ROW EXECUTE FUNCTION mark_booking_additional_email();
-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Additional email delivery history must not be discarded by an automatic downgrade'; END $$;
