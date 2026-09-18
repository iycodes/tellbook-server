-- migrate:up
CREATE TABLE account_security_events (
 id uuid PRIMARY KEY,
 realm text NOT NULL CHECK (realm IN ('provider','marketplace_customer')),
 provider_client_id uuid REFERENCES clients(id) ON DELETE CASCADE,
 marketplace_customer_id uuid REFERENCES marketplace_customers(id) ON DELETE CASCADE,
 recipient_email text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('password_changed','password_reset','password_set','email_linked','phone_linked','payout_account_added','payout_account_default_changed','payout_account_removed')),
 details jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT NOW(),
 email_deadline timestamptz NOT NULL DEFAULT NOW()+INTERVAL '24 hours',
 email_queued_at timestamptz,
 email_accepted_at timestamptz,
 skip_reason text NOT NULL DEFAULT '',
 CHECK ((realm='provider' AND provider_client_id IS NOT NULL AND marketplace_customer_id IS NULL) OR (realm='marketplace_customer' AND marketplace_customer_id IS NOT NULL AND provider_client_id IS NULL))
);
CREATE INDEX account_security_pending_idx ON account_security_events(created_at,id) WHERE email_queued_at IS NULL AND skip_reason='';
ALTER TABLE auth_code_delivery_jobs ADD COLUMN account_security_event_id uuid UNIQUE REFERENCES account_security_events(id) ON DELETE CASCADE;
ALTER TABLE auth_code_delivery_jobs DROP CONSTRAINT auth_code_delivery_jobs_challenge_check, DROP CONSTRAINT auth_code_delivery_jobs_template_contract_check;
ALTER TABLE auth_code_delivery_jobs ADD CONSTRAINT auth_code_delivery_jobs_challenge_check CHECK (
 (account_security_event_id IS NULL AND (
 (realm='provider' AND provider_challenge_id IS NOT NULL AND marketplace_challenge_id IS NULL AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
 OR (realm='marketplace_customer' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NOT NULL AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
 OR (realm='provider' AND provider_challenge_id IS NULL AND marketplace_challenge_id IS NULL AND num_nonnulls(tessa_security_event_id,tessa_link_challenge_id)=1)))
 OR (account_security_event_id IS NOT NULL AND num_nonnulls(provider_challenge_id,marketplace_challenge_id,tessa_security_event_id,tessa_link_challenge_id)=0)
), ADD CONSTRAINT auth_code_delivery_jobs_template_contract_check CHECK (
 (account_security_event_id IS NOT NULL AND channel='email' AND template_key='account_security_email')
 OR (account_security_event_id IS NULL AND (
 (channel='email' AND template_key='auth_code_email' AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
 OR (channel='whatsapp' AND template_key='v_c_x' AND tessa_security_event_id IS NULL AND tessa_link_challenge_id IS NULL)
 OR (channel='email' AND template_key='tessa_security_email' AND tessa_security_event_id IS NOT NULL)
 OR (channel='email' AND template_key='tessa_link_email' AND tessa_link_challenge_id IS NOT NULL)))
);
DROP INDEX auth_email_delivery_priority_idx;
CREATE INDEX auth_email_delivery_priority_idx ON auth_code_delivery_jobs(channel,(CASE WHEN template_key IN ('tessa_security_email','account_security_email') THEN 1 ELSE 0 END),next_attempt_at,created_at,id) WHERE status IN ('pending','retry');
CREATE TRIGGER account_security_wake AFTER INSERT ON account_security_events FOR EACH STATEMENT EXECUTE FUNCTION notify_auth_code_delivery_job();

CREATE FUNCTION capture_payout_account_email() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_kind text; recipient text;
BEGIN
 IF current_setting('tellbook.security_emails',true) IS DISTINCT FROM 'true' THEN RETURN NEW; END IF;
 IF TG_OP='INSERT' THEN event_kind='payout_account_added';
 ELSIF OLD.status IS DISTINCT FROM NEW.status AND NEW.status='disabled' THEN event_kind='payout_account_removed';
 ELSIF NOT OLD.is_default AND NEW.is_default THEN event_kind='payout_account_default_changed';
 ELSE RETURN NEW; END IF;
 SELECT email INTO recipient FROM clients WHERE id=NEW.client_id AND email_verified_at IS NOT NULL;
 IF COALESCE(recipient,'')='' THEN RETURN NEW; END IF;
 INSERT INTO account_security_events(id,realm,provider_client_id,recipient_email,kind,details)
 VALUES(gen_random_uuid(),'provider',NEW.client_id,lower(btrim(recipient)),event_kind,jsonb_build_object('institution_name',NEW.institution_name,'account_last_four',right(NEW.masked_identifier,4)));
 RETURN NEW;
END $$;
CREATE TRIGGER payout_account_email AFTER INSERT OR UPDATE ON payout_destinations FOR EACH ROW EXECUTE FUNCTION capture_payout_account_email();

-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Additional email event history must not be discarded by an automatic downgrade'; END $$;
