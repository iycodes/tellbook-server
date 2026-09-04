-- migrate:up
ALTER TABLE public.client_profiles
    ADD COLUMN concurrent_booking_capacity smallint DEFAULT 1 NOT NULL,
    ADD CONSTRAINT client_profiles_concurrent_booking_capacity_check
        CHECK (concurrent_booking_capacity BETWEEN 1 AND 50);

-- migrate:down
ALTER TABLE public.client_profiles
    DROP CONSTRAINT client_profiles_concurrent_booking_capacity_check,
    DROP COLUMN concurrent_booking_capacity;
