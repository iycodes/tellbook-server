-- migrate:up
-- Keep the existing table and business routes compatible during rollout.
-- One canonical staff-note store now supports four explicit record types.
ALTER TABLE admin_business_notes ALTER COLUMN business_id DROP NOT NULL;
ALTER TABLE admin_business_notes
 ADD COLUMN booking_id uuid REFERENCES bookings(id),
 ADD COLUMN contact_id uuid REFERENCES customers(id),
 ADD COLUMN account_id uuid REFERENCES marketplace_customers(id),
 ADD CONSTRAINT admin_notes_one_target CHECK(num_nonnulls(business_id,booking_id,contact_id,account_id)=1);
CREATE INDEX admin_notes_booking_idx ON admin_business_notes(booking_id,created_at DESC,id DESC) WHERE booking_id IS NOT NULL;
CREATE INDEX admin_notes_contact_idx ON admin_business_notes(contact_id,created_at DESC,id DESC) WHERE contact_id IS NOT NULL;
CREATE INDEX admin_notes_account_idx ON admin_business_notes(account_id,created_at DESC,id DESC) WHERE account_id IS NOT NULL;
-- migrate:down
-- Refuse rollback while new note targets exist; do not delete investigation history.
ALTER TABLE admin_business_notes ALTER COLUMN business_id SET NOT NULL;
DROP INDEX admin_notes_account_idx;
DROP INDEX admin_notes_contact_idx;
DROP INDEX admin_notes_booking_idx;
ALTER TABLE admin_business_notes DROP CONSTRAINT admin_notes_one_target,
 DROP COLUMN account_id, DROP COLUMN contact_id, DROP COLUMN booking_id;
