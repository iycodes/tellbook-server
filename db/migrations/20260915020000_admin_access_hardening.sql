-- migrate:up
ALTER TABLE admin_password_resets
 ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE,
 ADD COLUMN staff_revision bigint,
 ADD COLUMN created_at timestamptz,
 ADD COLUMN delivery_state text NOT NULL DEFAULT 'unknown' CHECK(delivery_state IN ('not_sent','sending','sent','failed','unknown')),
 ADD COLUMN delivery_updated_at timestamptz;
-- Old grants did not bind to an identity revision. Fail closed without inventing
-- historical delivery outcomes or creation timestamps.
UPDATE admin_password_resets SET consumed_at=coalesce(consumed_at,now());
ALTER TABLE admin_password_resets ALTER COLUMN created_at SET DEFAULT now();
ALTER TABLE admin_password_resets ALTER COLUMN delivery_state SET DEFAULT 'not_sent';
CREATE INDEX admin_password_resets_expiry_idx ON admin_password_resets(expires_at);
CREATE INDEX admin_password_resets_staff_idx ON admin_password_resets(staff_id);
CREATE INDEX admin_invitations_expiry_idx ON admin_invitations(expires_at);
-- migrate:down
DROP INDEX admin_invitations_expiry_idx;
DROP INDEX admin_password_resets_staff_idx;
DROP INDEX admin_password_resets_expiry_idx;
ALTER TABLE admin_password_resets DROP COLUMN delivery_updated_at,DROP COLUMN delivery_state,DROP COLUMN created_at,DROP COLUMN staff_revision,DROP COLUMN id;
