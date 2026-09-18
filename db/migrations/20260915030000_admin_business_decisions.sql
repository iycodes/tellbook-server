-- migrate:up
ALTER TABLE client_profiles ADD COLUMN platform_restricted boolean NOT NULL DEFAULT false;
CREATE TABLE admin_business_decisions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 business_id uuid NOT NULL REFERENCES clients(id),
 actor_id uuid NOT NULL REFERENCES admin_staff(id),
 request_key uuid NOT NULL,
 action text NOT NULL CHECK(action IN ('verify','reject','restrict','restore')),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 1000),
 evidence text NOT NULL DEFAULT '' CHECK(length(evidence)<=2000),
 expected_updated_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor_id,request_key)
);
CREATE INDEX admin_business_decisions_business_idx ON admin_business_decisions(business_id,created_at DESC,id DESC);
-- Keep the existing discovery projection and public revision mechanisms.
CREATE TRIGGER client_profiles_restriction_discovery
AFTER UPDATE OF platform_restricted ON client_profiles
FOR EACH ROW WHEN (OLD.platform_restricted IS DISTINCT FROM NEW.platform_restricted)
EXECUTE FUNCTION enqueue_marketplace_discovery_direct('true','true','true');
CREATE TRIGGER client_profiles_restriction_public_revision
AFTER UPDATE OF platform_restricted ON client_profiles
FOR EACH ROW WHEN (OLD.platform_restricted IS DISTINCT FROM NEW.platform_restricted)
EXECUTE FUNCTION bump_public_provider_resource_revision_direct();
-- migrate:down
DROP TRIGGER client_profiles_restriction_public_revision ON client_profiles;
DROP TRIGGER client_profiles_restriction_discovery ON client_profiles;
DROP TABLE admin_business_decisions;
ALTER TABLE client_profiles DROP COLUMN platform_restricted;
