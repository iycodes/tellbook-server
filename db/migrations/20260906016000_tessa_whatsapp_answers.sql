-- migrate:up
ALTER TABLE tessa_whatsapp_outbox
    ADD COLUMN assistant_message_id uuid UNIQUE,
    ADD COLUMN thread_id uuid,
    ADD COLUMN core_notice_revision text,
    ADD CONSTRAINT tessa_whatsapp_outbox_answer_fk FOREIGN KEY(assistant_message_id,thread_id,client_id)
        REFERENCES tessa_messages(id,thread_id,client_id) ON DELETE CASCADE,
    ADD CONSTRAINT tessa_whatsapp_outbox_answer_check CHECK (
        (kind='answer' AND assistant_message_id IS NOT NULL AND thread_id IS NOT NULL
            AND core_notice_revision IS NOT NULL AND btrim(core_notice_revision)<>'')
        OR (kind<>'answer' AND assistant_message_id IS NULL AND thread_id IS NULL AND core_notice_revision IS NULL)
    ),
    DROP CONSTRAINT tessa_whatsapp_outbox_kind_check,
    ADD CONSTRAINT tessa_whatsapp_outbox_kind_check CHECK (
        (kind IN ('answer','linked','disconnected','mismatch','unavailable','queue_busy','queue_expired','queue_unavailable')
            AND client_id IS NOT NULL AND onboarding_id IS NULL AND onboarding_revision IS NULL)
        OR (kind IN ('onboarding_menu','onboarding_consent','onboarding_email','onboarding_code','onboarding_invalid','onboarding_signup','onboarding_failed')
            AND client_id IS NULL AND onboarding_id IS NOT NULL AND onboarding_revision IS NOT NULL
            AND challenge_id IS NULL AND connection_revision=0 AND security_revision=0)
    );

-- migrate:down
DO $$ BEGIN RAISE EXCEPTION 'Committed assistant deliveries must not be discarded by automatic downgrade'; END $$;
