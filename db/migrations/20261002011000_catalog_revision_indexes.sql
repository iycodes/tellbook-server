-- migrate:up
DROP TRIGGER service_membership_revision ON services;
CREATE TRIGGER service_membership_revision AFTER INSERT OR UPDATE OF section_id, sort_order OR DELETE ON services FOR EACH ROW EXECUTE FUNCTION catalog_membership_revision();
CREATE INDEX services_catalog_image_reference ON services(image_url) WHERE image_url <> '';
CREATE INDEX sections_catalog_image_reference ON service_sections(cover_image_url) WHERE cover_image_url <> '';
CREATE INDEX integration_authorization_requests_expiry ON integration_authorization_requests(expires_at);
CREATE INDEX integration_tokens_expiry ON integration_tokens(expires_at);

-- migrate:down
DROP INDEX integration_tokens_expiry, integration_authorization_requests_expiry, sections_catalog_image_reference, services_catalog_image_reference;
DROP TRIGGER service_membership_revision ON services;
CREATE TRIGGER service_membership_revision AFTER INSERT OR UPDATE OF section_id OR DELETE ON services FOR EACH ROW EXECUTE FUNCTION catalog_membership_revision();
