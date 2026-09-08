-- migrate:up
ALTER TABLE auth_code_delivery_jobs
    ADD CONSTRAINT auth_code_delivery_jobs_template_contract_check CHECK (
        (channel='email' AND template_key='auth_code_email')
        OR (channel='whatsapp' AND template_key='v_c_x')
    );

CREATE INDEX auth_code_delivery_jobs_whatsapp_message_idx
    ON auth_code_delivery_jobs (provider_message_id)
    WHERE channel='whatsapp' AND provider_message_id<>'';

-- migrate:down
DROP INDEX IF EXISTS auth_code_delivery_jobs_whatsapp_message_idx;
ALTER TABLE auth_code_delivery_jobs
    DROP CONSTRAINT IF EXISTS auth_code_delivery_jobs_template_contract_check;
