-- migrate:up
CREATE INDEX IF NOT EXISTS agreement_instances_client_status_created_id_idx
    ON agreement_instances (client_id, status, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS promotions_client_type_updated_id_idx
    ON promotions (client_id, promotion_type, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS promotion_redemptions_promotion_created_id_idx
    ON promotion_redemptions (promotion_id, created_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS bookings_marketplace_customer_start_id_idx
    ON bookings (marketplace_customer_id, start_at, id)
    WHERE marketplace_customer_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS provider_reviews_public_created_id_idx
    ON provider_reviews (client_id, created_at DESC, id DESC)
    WHERE status = 'approved';

CREATE INDEX IF NOT EXISTS provider_reviews_public_rating_created_id_idx
    ON provider_reviews (client_id, rating DESC, created_at DESC, id DESC)
    WHERE status = 'approved';

CREATE INDEX IF NOT EXISTS payments_booking_created_id_idx
    ON payments (booking_id, created_at DESC, id DESC);

-- migrate:down
DROP INDEX IF EXISTS payments_booking_created_id_idx;
DROP INDEX IF EXISTS provider_reviews_public_rating_created_id_idx;
DROP INDEX IF EXISTS provider_reviews_public_created_id_idx;
DROP INDEX IF EXISTS bookings_marketplace_customer_start_id_idx;
DROP INDEX IF EXISTS promotion_redemptions_promotion_created_id_idx;
DROP INDEX IF EXISTS promotions_client_type_updated_id_idx;
DROP INDEX IF EXISTS agreement_instances_client_status_created_id_idx;
