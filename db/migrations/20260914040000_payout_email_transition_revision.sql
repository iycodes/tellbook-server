-- migrate:up
-- Payout version also advances on same-status reconciliation. Retain the version
-- of the last real transition so those reconciliations do not suppress its email.
ALTER TABLE payouts ADD COLUMN notification_revision bigint NOT NULL DEFAULT 1;
UPDATE payouts SET notification_revision=version;
CREATE FUNCTION advance_payout_notification_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status IS DISTINCT FROM OLD.status THEN NEW.notification_revision=NEW.version;
 ELSE NEW.notification_revision=OLD.notification_revision;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER payout_notification_revision BEFORE UPDATE ON payouts FOR EACH ROW EXECUTE FUNCTION advance_payout_notification_revision();
-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Payout notification revisions must not be discarded by an automatic downgrade'; END $$;
