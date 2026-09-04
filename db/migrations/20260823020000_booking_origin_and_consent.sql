-- migrate:up
ALTER TABLE bookings
    ADD COLUMN whatsapp_consent boolean NOT NULL DEFAULT false,
    ADD COLUMN sms_consent boolean NOT NULL DEFAULT false;

-- migrate:down
ALTER TABLE bookings
    DROP COLUMN sms_consent,
    DROP COLUMN whatsapp_consent;
