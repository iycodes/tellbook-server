\set ON_ERROR_STOP on

EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
SELECT category.id, category.slug, category.name,
    COALESCE(discovery_count.provider_count, 0)
FROM marketplace_categories category
LEFT JOIN marketplace_discovery_counts discovery_count
    ON discovery_count.dimension = 'category'
   AND discovery_count.dimension_id = category.id
WHERE category.is_active
ORDER BY category.sort_order, category.name;

EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
SELECT provider.client_id, service.service_id, service.title,
    provider.review_rating, service.price_amount_minor
FROM marketplace_provider_documents provider
CROSS JOIN LATERAL (
    SELECT candidate.*
    FROM marketplace_service_documents candidate
    WHERE candidate.provider_id = provider.client_id
    ORDER BY candidate.price_amount_minor, candidate.sort_order, candidate.service_id
    LIMIT 1
) service
ORDER BY provider.review_rating DESC, provider.review_count DESC, provider.client_id
LIMIT 21;

EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
WITH candidate_services AS (
    SELECT provider.client_id AS provider_id, provider.review_rating, provider.review_count,
        service.service_id, service.price_amount_minor, service.sort_order,
        availability.first_available_at
    FROM marketplace_provider_documents provider
    JOIN marketplace_service_documents service ON service.provider_id = provider.client_id
    LEFT JOIN LATERAL (
        SELECT day.first_available_at
        FROM marketplace_service_availability_days day
        WHERE day.service_id = service.service_id
          AND day.has_available_slot
          AND day.first_available_at >= NOW()
        ORDER BY day.local_date, day.first_available_at
        LIMIT 1
    ) availability ON true
    WHERE provider.search_vector @@ websearch_to_tsquery('simple', 'service')
       OR service.search_vector @@ websearch_to_tsquery('simple', 'service')
), ranked_services AS (
    SELECT candidate_services.*,
        ROW_NUMBER() OVER (
            PARTITION BY provider_id
            ORDER BY price_amount_minor, sort_order, service_id
        ) AS service_rank
    FROM candidate_services
)
SELECT provider_id, service_id, price_amount_minor, first_available_at
FROM ranked_services
WHERE service_rank = 1
ORDER BY price_amount_minor, provider_id
LIMIT 21;

EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
SELECT provider.client_id, provider.availability_refresh_date
FROM marketplace_provider_documents provider
WHERE provider.availability_refresh_after <= NOW()
  AND provider.availability_refresh_date IS NOT NULL
ORDER BY provider.availability_refresh_after, provider.client_id
LIMIT 5000;
