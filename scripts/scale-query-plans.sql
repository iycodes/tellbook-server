\set ON_ERROR_STOP on
\pset pager off
\timing on

BEGIN READ ONLY;
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '2s';

SELECT client_id AS sample_provider_id
FROM client_profiles
WHERE marketplace_enabled AND market_configured_at IS NOT NULL
ORDER BY client_id LIMIT 1
\gset

SELECT marketplace_customer_id AS sample_marketplace_customer_id
FROM marketplace_saved_providers
GROUP BY marketplace_customer_id
ORDER BY COUNT(*) DESC, marketplace_customer_id LIMIT 1
\gset

\echo 'Discovery page: production home query (all Nigeria, no visitor location)'
PREPARE marketplace_home_query(text, uuid, uuid, integer, uuid, integer) AS
WITH visitor AS (
  SELECT geog FROM resolved_locations WHERE public_token = NULLIF($1, '') AND expires_at > NOW()
)
SELECT profile.client_id, profile.handle_slug, profile.business_name, profile.headline,
       category.id, category.name, COALESCE(profile.avatar_url, ''), COALESCE(profile.hero_image_url, ''),
       profile.verified, COALESCE(review_summary.rating, 0)::double precision,
       COALESCE(review_summary.count, 0)::int,
       CASE WHEN service.fulfillment_mode = 'virtual' THEN 'Online'
            WHEN profile.marketplace_location_visibility = 'exact' THEN location.formatted_address
            ELSE COALESCE(NULLIF(location.locality, ''), lga.name, state.name, profile.public_location_label) END,
       CASE WHEN visitor.geog IS NULL OR location.geog IS NULL THEN NULL
            ELSE ROUND(ST_Distance(visitor.geog, location.geog))::int END,
       service.id, service.slug, service.title, service.duration_minutes,
       service.price_amount_minor, service.currency_code, service.fulfillment_mode,
       (SELECT COUNT(*)::int FROM bookings completed
        WHERE completed.client_id=profile.client_id AND completed.status='completed'),
       ARRAY_REMOVE(ARRAY[
         CASE WHEN profile.verified THEN 'Verified' END,
         CASE service.fulfillment_mode WHEN 'virtual' THEN 'Online'
              WHEN 'customer_location' THEN 'Comes to you' END
       ], NULL)
FROM client_profiles profile
INNER JOIN marketplace_categories category ON category.id=profile.marketplace_category_id AND category.is_active
LEFT JOIN LATERAL (
  SELECT AVG(review.rating) AS rating, COUNT(*) AS count
  FROM provider_reviews review
  WHERE review.client_id=profile.client_id AND review.status='approved'
) review_summary ON TRUE
LEFT JOIN visitor ON TRUE
CROSS JOIN LATERAL (
  SELECT candidate.*
  FROM services candidate
  LEFT JOIN business_locations candidate_location ON candidate_location.id=candidate.provider_location_id
  WHERE candidate.client_id=profile.client_id
    AND candidate.status='published' AND candidate.is_active AND NOT candidate.is_hidden
    AND (
      (candidate.availability_mode='custom' AND EXISTS (
        SELECT 1 FROM service_availability_windows availability_window
        WHERE availability_window.service_id=candidate.id
          AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
              candidate.prep_time_minutes+candidate.duration_minutes+candidate.buffer_time_minutes
      )) OR (candidate.availability_mode<>'custom' AND EXISTS (
        SELECT 1 FROM provider_availability_windows availability_window
        WHERE availability_window.client_id=candidate.client_id
          AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
              candidate.prep_time_minutes+candidate.duration_minutes+candidate.buffer_time_minutes
      ))
    )
    AND (candidate.fulfillment_mode='virtual' OR
         (candidate_location.is_active AND candidate_location.resolution_status='coordinates_resolved'))
    AND ($2::uuid IS NULL OR candidate.fulfillment_mode='virtual' OR candidate_location.state_region_id=$2)
    AND ($3::uuid IS NULL OR candidate.fulfillment_mode='virtual' OR candidate_location.lga_region_id=$3)
    AND ($4=0 OR candidate.fulfillment_mode='virtual' OR
         (visitor.geog IS NOT NULL AND candidate_location.geog IS NOT NULL AND ST_DWithin(
           visitor.geog, candidate_location.geog,
           CASE WHEN candidate.fulfillment_mode='customer_location'
                THEN COALESCE(candidate.max_travel_distance_meters, $4) ELSE $4 END
         )))
  ORDER BY CASE WHEN visitor.geog IS NULL OR candidate_location.geog IS NULL THEN NULL
                ELSE ST_Distance(visitor.geog, candidate_location.geog) END NULLS LAST,
           candidate.price_amount_minor, candidate.sort_order, candidate.id
  LIMIT 1
) service
LEFT JOIN business_locations location ON location.id=service.provider_location_id
LEFT JOIN administrative_regions state ON state.id=location.state_region_id
LEFT JOIN administrative_regions lga ON lga.id=location.lga_region_id
WHERE profile.marketplace_enabled AND profile.market_configured_at IS NOT NULL
  AND ($5::uuid IS NULL OR profile.marketplace_category_id=$5)
ORDER BY CASE WHEN visitor.geog IS NULL OR location.geog IS NULL THEN NULL
              ELSE ST_Distance(visitor.geog, location.geog) END NULLS LAST,
         COALESCE(review_summary.rating,0) DESC, COALESCE(review_summary.count,0) DESC,
         profile.client_id
LIMIT $6;
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
EXECUTE marketplace_home_query('', NULL, NULL, 0, NULL, 20);
DEALLOCATE marketplace_home_query;

\if :{?sample_marketplace_customer_id}
\echo 'Customer saved-provider page: production first-page query'
PREPARE marketplace_saved_query(uuid, timestamptz, uuid, integer) AS
SELECT profile.client_id, profile.handle_slug, profile.business_name, profile.headline,
       category.id, category.name, COALESCE(profile.avatar_url, ''), COALESCE(profile.hero_image_url, ''),
       profile.verified, COALESCE(review_summary.rating, 0)::double precision,
       COALESCE(review_summary.count, 0)::int,
       CASE WHEN service.fulfillment_mode='virtual' THEN 'Online'
            WHEN profile.marketplace_location_visibility='exact' THEN location.formatted_address
            ELSE COALESCE(NULLIF(location.locality, ''), lga.name, state.name, profile.public_location_label) END,
       service.id, service.slug, service.title, service.duration_minutes,
       service.price_amount_minor, service.currency_code, service.fulfillment_mode,
       (SELECT COUNT(*)::int FROM bookings completed
        WHERE completed.client_id=profile.client_id AND completed.status='completed'),
       ARRAY_REMOVE(ARRAY[
         CASE WHEN profile.verified THEN 'Verified' END,
         CASE service.fulfillment_mode WHEN 'virtual' THEN 'Online'
              WHEN 'customer_location' THEN 'Comes to you' END
       ], NULL), saved.created_at
FROM marketplace_saved_providers saved
JOIN client_profiles profile ON profile.client_id = saved.provider_id
JOIN marketplace_categories category ON category.id=profile.marketplace_category_id AND category.is_active
LEFT JOIN LATERAL (
  SELECT AVG(review.rating) AS rating, COUNT(*) AS count
  FROM provider_reviews review
  WHERE review.client_id=profile.client_id AND review.status='approved'
) review_summary ON TRUE
CROSS JOIN LATERAL (
  SELECT candidate.*
  FROM services candidate
  LEFT JOIN business_locations candidate_location ON candidate_location.id=candidate.provider_location_id
  WHERE candidate.client_id=profile.client_id
    AND candidate.status='published' AND candidate.is_active AND NOT candidate.is_hidden
    AND (
      (candidate.availability_mode='custom' AND EXISTS (
        SELECT 1 FROM service_availability_windows availability_window
        WHERE availability_window.service_id=candidate.id
          AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
              candidate.prep_time_minutes+candidate.duration_minutes+candidate.buffer_time_minutes
      )) OR (candidate.availability_mode<>'custom' AND EXISTS (
        SELECT 1 FROM provider_availability_windows availability_window
        WHERE availability_window.client_id=candidate.client_id
          AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
              candidate.prep_time_minutes+candidate.duration_minutes+candidate.buffer_time_minutes
      ))
    )
    AND (candidate.fulfillment_mode='virtual' OR
         (candidate_location.is_active AND candidate_location.resolution_status='coordinates_resolved'))
  ORDER BY candidate.price_amount_minor, candidate.sort_order, candidate.id
  LIMIT 1
) service
LEFT JOIN business_locations location ON location.id=service.provider_location_id
LEFT JOIN administrative_regions state ON state.id=location.state_region_id
LEFT JOIN administrative_regions lga ON lga.id=location.lga_region_id
WHERE saved.marketplace_customer_id=$1
  AND profile.marketplace_enabled AND profile.market_configured_at IS NOT NULL
  AND ($2::timestamptz IS NULL OR (saved.created_at, saved.provider_id)<($2,$3::uuid))
ORDER BY saved.created_at DESC, saved.provider_id DESC
LIMIT $4;
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
EXECUTE marketplace_saved_query(:'sample_marketplace_customer_id'::uuid, NULL, NULL, 25);
DEALLOCATE marketplace_saved_query;

\echo 'Customer notifications: production unread count and keyset first page'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT COUNT(*)::int FROM marketplace_notifications
WHERE marketplace_customer_id=:'sample_marketplace_customer_id'::uuid AND read_at IS NULL;
PREPARE marketplace_notifications_query(uuid, text, boolean, timestamptz, uuid, integer) AS
SELECT notification.id, notification.kind, notification.event_type,
       notification.title, notification.body,
       COALESCE(notification.provider_id::text, ''), COALESCE(notification.booking_id::text, ''),
       COALESCE(notification.conversation_id::text, ''), notification.read_at, notification.created_at
FROM marketplace_notifications notification
WHERE notification.marketplace_customer_id=$1
  AND ($2='' OR notification.kind=$2)
  AND (NOT $3 OR notification.read_at IS NULL)
  AND ($4::timestamptz IS NULL OR (notification.created_at,notification.id)<($4,$5::uuid))
ORDER BY notification.created_at DESC, notification.id DESC
LIMIT $6;
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
EXECUTE marketplace_notifications_query(:'sample_marketplace_customer_id'::uuid, '', FALSE, NULL, NULL, 31);
DEALLOCATE marketplace_notifications_query;

\echo 'Customer upcoming booking page'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT COUNT(*) FILTER (WHERE status NOT IN ('cancelled','canceled','declined','expired') AND end_at>=NOW())::int,
       COUNT(*) FILTER (WHERE status NOT IN ('cancelled','canceled','declined','expired') AND end_at<NOW())::int,
       COUNT(*) FILTER (WHERE status IN ('cancelled','canceled','declined','expired'))::int
FROM bookings WHERE marketplace_customer_id=:'sample_marketplace_customer_id'::uuid;
PREPARE marketplace_bookings_query(uuid, text, integer, integer) AS
SELECT b.id, b.status, COALESCE(b.service_id::text, ''), b.title,
       COALESCE(b.image_url, service.image_url, ''), b.client_id,
       profile.business_name, handle.handle_slug, COALESCE(profile.avatar_url, ''),
       b.start_at, b.end_at, b.timezone, b.duration_minutes, b.location_label,
       b.fulfillment_mode, b.total_amount_minor,
       GREATEST(COALESCE(payment_totals.gross_paid_minor,0)-COALESCE(payment_totals.adjusted_minor,0),0),
       b.currency_code, b.payment_status, b.agreement_status,
       CASE
         WHEN COALESCE(payment_totals.attention_refund_request,FALSE) THEN 'attention_required'
         WHEN COALESCE(payment_totals.failed_refund_request,FALSE) THEN 'failed'
         WHEN COALESCE(payment_totals.pending_adjustment,FALSE)
           OR COALESCE(payment_totals.pending_refund_request,FALSE) THEN 'pending'
         WHEN COALESCE(payment_totals.adjusted_minor,0)>0
           AND GREATEST(COALESCE(payment_totals.gross_paid_minor,0)-COALESCE(payment_totals.adjusted_minor,0),0)=0 THEN 'refunded'
         WHEN COALESCE(payment_totals.adjusted_minor,0)>0 THEN 'partially_refunded'
         ELSE ''
       END,
       COALESCE(latest_review.status, '')
FROM bookings b
INNER JOIN client_profiles profile ON profile.client_id=b.client_id
INNER JOIN client_profile_handles handle ON handle.client_id=b.client_id
LEFT JOIN services service ON service.id=b.service_id
LEFT JOIN LATERAL (
  SELECT
    COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
      WHERE payment.booking_id=b.id AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0) AS gross_paid_minor,
    COALESCE((SELECT SUM(adjustment.allocation_impact_minor) FROM payment_adjustments adjustment
      INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
      WHERE adjusted_payment.booking_id=b.id AND adjustment.status='successful'),0) AS adjusted_minor,
    EXISTS (SELECT 1 FROM payment_adjustments adjustment
      INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
      WHERE adjusted_payment.booking_id=b.id AND adjustment.status='pending') AS pending_adjustment,
    EXISTS (SELECT 1 FROM booking_refund_requests request
      WHERE request.booking_id=b.id AND request.status IN ('queued','processing')) AS pending_refund_request,
    EXISTS (SELECT 1 FROM booking_refund_requests request
      WHERE request.booking_id=b.id AND request.status='manual_review') AS attention_refund_request,
    EXISTS (SELECT 1 FROM booking_refund_requests request
      WHERE request.booking_id=b.id AND request.status='failed') AS failed_refund_request
) payment_totals ON TRUE
LEFT JOIN LATERAL (
  SELECT review.status FROM provider_reviews review
  WHERE review.booking_id=b.id
  ORDER BY review.created_at DESC, review.id DESC LIMIT 1
) latest_review ON TRUE
WHERE b.marketplace_customer_id=$1
  AND (
    ($2='upcoming' AND b.status NOT IN ('cancelled','canceled','declined','expired') AND b.end_at>=NOW()) OR
    ($2='past' AND b.status NOT IN ('cancelled','canceled','declined','expired') AND b.end_at<NOW()) OR
    ($2='cancelled' AND b.status IN ('cancelled','canceled','declined','expired'))
  )
ORDER BY CASE WHEN $2='upcoming' THEN b.start_at END ASC,
         CASE WHEN $2<>'upcoming' THEN b.start_at END DESC,
         b.id
LIMIT $3 OFFSET $4;
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
EXECUTE marketplace_bookings_query(:'sample_marketplace_customer_id'::uuid, 'upcoming', 21, 0);
DEALLOCATE marketplace_bookings_query;
\endif

\if :{?sample_provider_id}
\echo 'Provider dashboard: production profile query'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT c.id, c.full_name, cp.business_name, COALESCE(cp.avatar_url,c.cover_image_url,''),
       cp.category, cp.headline, cp.public_location_label, cp.review_rating::float8,
       cp.review_count, cp.verified, cp.currency_code, cp.timezone
FROM clients c INNER JOIN client_profiles cp ON cp.client_id=c.id
WHERE c.id=:'sample_provider_id'::uuid;

\echo 'Provider dashboard: production today stats query'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
WITH bounds AS (
  SELECT currency_code,
    (date_trunc('day',NOW() AT TIME ZONE timezone) AT TIME ZONE timezone) AS day_start,
    ((date_trunc('day',NOW() AT TIME ZONE timezone)+INTERVAL '1 day') AT TIME ZONE timezone) AS day_end
  FROM client_profiles WHERE client_id=:'sample_provider_id'::uuid
)
SELECT COUNT(*)::int,COALESCE(SUM(total_amount_minor),0)::bigint,
       COUNT(*) FILTER (WHERE currency_code<>bounds.currency_code)::int
FROM bookings CROSS JOIN bounds
WHERE client_id=:'sample_provider_id'::uuid
  AND start_at>=bounds.day_start AND start_at<bounds.day_end
GROUP BY bounds.currency_code;

\echo 'Provider dashboard: production attention query'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id,type,severity,title,description,action_label,action_route,
       COALESCE(image_url,''),COALESCE(icon_name,''),COALESCE(icon_tone,''),created_at
FROM notifications WHERE client_id=:'sample_provider_id'::uuid
ORDER BY (CASE WHEN severity='urgent' THEN 0 ELSE 1 END),created_at DESC
LIMIT 1;

\echo 'Provider dashboard: production today bookings query'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT b.id,b.title,b.start_at,c.full_name,b.status,b.payment_status,COALESCE(s.icon_name,'')
FROM bookings b
LEFT JOIN customers c ON c.id=b.customer_id
LEFT JOIN services s ON s.id=b.service_id
JOIN LATERAL (
  SELECT (date_trunc('day',NOW() AT TIME ZONE timezone) AT TIME ZONE timezone) AS day_start,
    ((date_trunc('day',NOW() AT TIME ZONE timezone)+INTERVAL '1 day') AT TIME ZONE timezone) AS day_end
  FROM client_profiles WHERE client_id=:'sample_provider_id'::uuid
) bounds ON TRUE
WHERE b.client_id=:'sample_provider_id'::uuid
  AND b.start_at>=bounds.day_start AND b.start_at<bounds.day_end
ORDER BY b.start_at ASC LIMIT 6;

\echo 'Provider 365-day projected revenue query'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT COALESCE(SUM(gross_revenue_minor),0)::bigint,
       COALESCE(SUM(net_revenue_minor),0)::bigint,
       COALESCE(SUM(payment_count),0)::int
FROM provider_daily_metrics metric
JOIN client_profiles profile ON profile.client_id=metric.client_id
WHERE metric.client_id=:'sample_provider_id'::uuid
  AND metric.currency_code=profile.currency_code
  AND metric.metric_date>=CURRENT_DATE-364
  AND metric.metric_date<CURRENT_DATE+1;

\echo 'Provider durable inbox event drain'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT sequence, conversation_id, event_type, payload, created_at
FROM inbox_events
WHERE client_id=:'sample_provider_id'::uuid AND sequence>0
ORDER BY sequence
LIMIT 101;
\endif

ROLLBACK;
