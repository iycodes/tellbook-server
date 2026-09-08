-- migrate:up
CREATE INDEX tessa_whatsapp_outbox_terminal_retention_idx ON tessa_whatsapp_outbox(updated_at,id)
    WHERE status IN ('accepted','sent','delivered','read','failed','cancelled','expired','manual_review');
CREATE INDEX tessa_whatsapp_ingress_retention_idx ON tessa_whatsapp_ingress(completed_at,id) WHERE status<>'pending';
CREATE INDEX tessa_email_link_expiry_idx ON tessa_whatsapp_email_challenges(expires_at,id);
CREATE INDEX tessa_whatsapp_outbox_challenge_idx ON tessa_whatsapp_outbox(challenge_id) WHERE challenge_id IS NOT NULL;
CREATE INDEX tessa_whatsapp_outbox_onboarding_idx ON tessa_whatsapp_outbox(onboarding_id) WHERE onboarding_id IS NOT NULL;
CREATE INDEX tessa_whatsapp_onboarding_challenge_idx ON tessa_whatsapp_onboarding(challenge_id) WHERE challenge_id IS NOT NULL;

-- migrate:down
DROP INDEX tessa_whatsapp_onboarding_challenge_idx;
DROP INDEX tessa_whatsapp_outbox_onboarding_idx;
DROP INDEX tessa_whatsapp_outbox_challenge_idx;
DROP INDEX tessa_email_link_expiry_idx;
DROP INDEX tessa_whatsapp_ingress_retention_idx;
DROP INDEX tessa_whatsapp_outbox_terminal_retention_idx;
