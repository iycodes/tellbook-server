-- migrate:up
ALTER TABLE services ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);
ALTER TABLE service_sections ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0);

CREATE FUNCTION catalog_bump_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN NEW.revision := OLD.revision + 1; NEW.updated_at := clock_timestamp(); RETURN NEW; END $$;
CREATE TRIGGER services_revision BEFORE UPDATE ON services FOR EACH ROW EXECUTE FUNCTION catalog_bump_revision();
CREATE TRIGGER service_sections_revision BEFORE UPDATE ON service_sections FOR EACH ROW EXECUTE FUNCTION catalog_bump_revision();

CREATE FUNCTION catalog_child_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN UPDATE services SET updated_at=clock_timestamp() WHERE id=OLD.service_id; END IF;
  IF TG_OP <> 'DELETE' AND (TG_OP='INSERT' OR NEW.service_id IS DISTINCT FROM OLD.service_id) THEN
    UPDATE services SET updated_at=clock_timestamp() WHERE id=NEW.service_id;
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER service_windows_revision AFTER INSERT OR UPDATE OR DELETE ON service_availability_windows FOR EACH ROW EXECUTE FUNCTION catalog_child_revision();
CREATE TRIGGER service_rules_revision AFTER INSERT OR UPDATE OR DELETE ON service_short_notice_rules FOR EACH ROW EXECUTE FUNCTION catalog_child_revision();

CREATE FUNCTION catalog_membership_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' THEN UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=OLD.section_id; END IF;
  IF TG_OP <> 'DELETE' AND (TG_OP='INSERT' OR NEW.section_id IS DISTINCT FROM OLD.section_id) THEN
    UPDATE service_sections SET updated_at=clock_timestamp() WHERE id=NEW.section_id;
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER service_membership_revision AFTER INSERT OR UPDATE OF section_id OR DELETE ON services FOR EACH ROW EXECUTE FUNCTION catalog_membership_revision();

CREATE TABLE integration_authorization_requests (
  id uuid PRIMARY KEY, platform text NOT NULL CHECK(platform IN ('chatgpt','claude')),
  client_id text NOT NULL, redirect_uri text NOT NULL, resource text NOT NULL,
  scopes text[] NOT NULL, state text NOT NULL, code_challenge text NOT NULL,
  provider_id uuid REFERENCES clients(id) ON DELETE CASCADE,
  csrf_hash bytea, code_hash bytea UNIQUE, approved_scopes text[], security_revision bigint,
  expires_at timestamptz NOT NULL, code_expires_at timestamptz,
  decided_at timestamptz, consumed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE integration_grants (
  id uuid PRIMARY KEY, provider_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
  platform text NOT NULL CHECK(platform IN ('chatgpt','claude')), client_id text NOT NULL,
  resource text NOT NULL, scopes text[] NOT NULL, security_revision bigint NOT NULL,
  expires_at timestamptz NOT NULL, revoked_at timestamptz, last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX integration_grants_provider ON integration_grants(provider_id,created_at DESC);
CREATE TABLE integration_tokens (
  token_hash bytea PRIMARY KEY, grant_id uuid NOT NULL REFERENCES integration_grants(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK(kind IN ('access','refresh')), expires_at timestamptz NOT NULL,
  consumed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX integration_tokens_grant ON integration_tokens(grant_id);
CREATE TABLE catalog_mutation_receipts (
  id uuid PRIMARY KEY, provider_id uuid NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
  grant_id uuid REFERENCES integration_grants(id) ON DELETE CASCADE,
  actor_key text NOT NULL, idempotency_key text NOT NULL,
  operation text NOT NULL, request_hash bytea NOT NULL, result jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(provider_id,actor_key,idempotency_key)
);

-- migrate:down
DROP TABLE catalog_mutation_receipts, integration_tokens, integration_grants, integration_authorization_requests;
DROP TRIGGER service_membership_revision ON services;
DROP FUNCTION catalog_membership_revision();
DROP TRIGGER service_rules_revision ON service_short_notice_rules;
DROP TRIGGER service_windows_revision ON service_availability_windows;
DROP FUNCTION catalog_child_revision();
DROP TRIGGER service_sections_revision ON service_sections;
DROP TRIGGER services_revision ON services;
DROP FUNCTION catalog_bump_revision();
ALTER TABLE service_sections DROP COLUMN revision;
ALTER TABLE services DROP COLUMN revision;
