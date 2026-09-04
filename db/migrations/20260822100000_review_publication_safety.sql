-- migrate:up
UPDATE public.provider_reviews
SET status = 'pending', updated_at = now()
WHERE status = 'approved'
  AND booking_id IS NULL
  AND service_id IS NULL;

ALTER TABLE public.provider_reviews
    ALTER COLUMN status SET DEFAULT 'pending';

-- migrate:down
ALTER TABLE public.provider_reviews
    ALTER COLUMN status SET DEFAULT 'approved';
