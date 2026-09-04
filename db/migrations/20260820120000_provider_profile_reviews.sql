-- migrate:up
ALTER TABLE public.provider_reviews
    ADD COLUMN booking_id uuid,
    ADD COLUMN service_id uuid,
    ADD COLUMN status text NOT NULL DEFAULT 'pending',
    ADD COLUMN updated_at timestamp with time zone NOT NULL DEFAULT now();

ALTER TABLE public.provider_reviews
    ADD CONSTRAINT provider_reviews_booking_id_fkey
        FOREIGN KEY (booking_id) REFERENCES public.bookings(id) ON DELETE SET NULL,
    ADD CONSTRAINT provider_reviews_service_id_fkey
        FOREIGN KEY (service_id) REFERENCES public.services(id) ON DELETE SET NULL,
    ADD CONSTRAINT provider_reviews_status_check
        CHECK (status IN ('pending', 'approved', 'rejected', 'hidden'));

CREATE UNIQUE INDEX provider_reviews_booking_id_unique_idx
    ON public.provider_reviews (booking_id)
    WHERE booking_id IS NOT NULL;

CREATE INDEX provider_reviews_public_listing_idx
    ON public.provider_reviews (client_id, created_at DESC, id)
    WHERE status = 'approved';

CREATE INDEX provider_reviews_public_service_idx
    ON public.provider_reviews (client_id, service_id, created_at DESC)
    WHERE status = 'approved';

-- migrate:down
DROP INDEX IF EXISTS public.provider_reviews_public_service_idx;
DROP INDEX IF EXISTS public.provider_reviews_public_listing_idx;
DROP INDEX IF EXISTS public.provider_reviews_booking_id_unique_idx;

ALTER TABLE public.provider_reviews
    DROP CONSTRAINT IF EXISTS provider_reviews_status_check,
    DROP CONSTRAINT IF EXISTS provider_reviews_service_id_fkey,
    DROP CONSTRAINT IF EXISTS provider_reviews_booking_id_fkey,
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS status,
    DROP COLUMN IF EXISTS service_id,
    DROP COLUMN IF EXISTS booking_id;
