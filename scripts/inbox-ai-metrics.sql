\set ON_ERROR_STOP on
\pset pager off

\if :{?window}
\else
\set window '24 hours'
\endif

BEGIN READ ONLY;
SET LOCAL statement_timeout = '15s';
SET LOCAL lock_timeout = '2s';

SELECT NOW() - :'window'::interval AS since_at, NOW() AS until_at
\gset

\echo 'Tellbook inbox AI funnel'
\echo 'Window:' :'since_at' 'to' :'until_at'

WITH
message_events AS (
  SELECT event.conversation_id,
         event.created_at,
         message.presentation->>'kind' AS kind
  FROM inbox_events event
  INNER JOIN inbox_messages message
    ON message.id = (event.payload->>'message_id')::uuid
  WHERE event.event_type = 'message.created'
    AND event.created_at >= :'since_at'::timestamptz
    AND event.created_at < :'until_at'::timestamptz
    AND message.presentation IS NOT NULL
),
first_booking_link AS (
  SELECT conversation_id, MIN(created_at) AS delivered_at
  FROM message_events
  WHERE kind = 'booking_link'
  GROUP BY conversation_id
),
first_proposal AS (
  SELECT conversation_id, MIN(created_at) AS proposed_at
  FROM message_events
  WHERE kind = 'booking_proposal'
  GROUP BY conversation_id
),
booking_link_conversions AS (
  SELECT DISTINCT link.conversation_id
  FROM first_booking_link link
  INNER JOIN inbox_conversation_bookings booking
    ON booking.conversation_id = link.conversation_id
   AND booking.linked_at >= link.delivered_at
),
proposal_confirmations AS (
  SELECT DISTINCT proposal.conversation_id,
         confirmation.booking_id
  FROM first_proposal proposal
  INNER JOIN inbox_ai_booking_confirmations confirmation
    ON confirmation.conversation_id = proposal.conversation_id
   AND confirmation.confirmed_at >= proposal.proposed_at
   AND confirmation.confirmed_at < :'until_at'::timestamptz
),
funnel AS (
  SELECT
    COUNT(*) FILTER (WHERE kind = 'booking_link')::bigint AS booking_link_deliveries,
    COUNT(DISTINCT conversation_id) FILTER (WHERE kind = 'booking_link')::bigint AS booking_link_conversations,
    COUNT(*) FILTER (WHERE kind = 'booking_proposal')::bigint AS proposal_deliveries,
    COUNT(DISTINCT conversation_id) FILTER (WHERE kind = 'booking_proposal')::bigint AS proposal_conversations,
    COUNT(*) FILTER (WHERE kind = 'reservation_created')::bigint AS reservation_cards,
    COUNT(*) FILTER (WHERE kind = 'reservation_expired')::bigint AS expired_reservation_cards
  FROM message_events
),
totals AS (
  SELECT funnel.*,
         (SELECT COUNT(*) FROM booking_link_conversions)::bigint AS booking_link_converted_conversations,
         (SELECT COUNT(*) FROM proposal_confirmations)::bigint AS proposal_confirmed_conversations,
         (SELECT COUNT(*) FROM proposal_confirmations WHERE booking_id IS NOT NULL)::bigint AS reservation_successes
  FROM funnel
)
SELECT metric, value, denominator,
       CASE WHEN denominator > 0
         THEN ROUND(value::numeric * 100 / denominator, 2)
         ELSE NULL
       END AS percent
FROM totals
CROSS JOIN LATERAL (VALUES
  ('booking_link_deliveries', booking_link_deliveries, NULL::bigint),
  ('booking_link_conversations', booking_link_conversations, NULL::bigint),
  ('booking_link_converted_conversations', booking_link_converted_conversations, booking_link_conversations),
  ('proposal_deliveries', proposal_deliveries, NULL::bigint),
  ('proposal_conversations', proposal_conversations, NULL::bigint),
  ('proposal_confirmed_conversations', proposal_confirmed_conversations, proposal_conversations),
  ('reservation_successes', reservation_successes, proposal_confirmed_conversations),
  ('reservation_cards', reservation_cards, NULL::bigint),
  ('expired_reservation_cards', expired_reservation_cards, reservation_cards)
) AS report(metric, value, denominator);

\echo 'Automation control outcomes'
SELECT
  COUNT(*) FILTER (
    WHERE event_type = 'ai.control_updated' AND payload->>'state' = 'handoff'
  ) AS customer_or_system_handoffs,
  COUNT(*) FILTER (
    WHERE event_type = 'ai.control_updated' AND payload->>'state' = 'provider_takeover'
  ) AS provider_takeovers,
  COUNT(DISTINCT conversation_id) FILTER (
    WHERE event_type = 'ai.control_updated' AND payload->>'state' = 'handoff'
  ) AS handoff_conversations,
  COUNT(DISTINCT conversation_id) FILTER (
    WHERE event_type = 'ai.control_updated' AND payload->>'state' = 'provider_takeover'
  ) AS provider_takeover_conversations
FROM inbox_events
WHERE created_at >= :'since_at'::timestamptz
  AND created_at < :'until_at'::timestamptz;

\echo 'Inference reliability and latency'
SELECT
  COUNT(*) AS runs,
  COUNT(*) FILTER (WHERE status = 'completed') AS completed,
  COUNT(*) FILTER (WHERE status = 'failed') AS failed,
  ROUND(AVG(latency_ms) FILTER (WHERE latency_ms IS NOT NULL), 2) AS latency_average_ms,
  ROUND((PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY latency_ms)
    FILTER (WHERE latency_ms IS NOT NULL))::numeric, 2) AS latency_p50_ms,
  ROUND((PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)
    FILTER (WHERE latency_ms IS NOT NULL))::numeric, 2) AS latency_p95_ms,
  MAX(latency_ms) AS latency_max_ms
FROM inbox_ai_runs
WHERE created_at >= :'since_at'::timestamptz
  AND created_at < :'until_at'::timestamptz
  AND mode IN ('semi_pilot', 'autopilot');

\echo 'Worker attempts and retries'
SELECT
  COUNT(*) AS jobs,
  COUNT(*) FILTER (WHERE attempt_count > 1) AS retried_jobs,
  COALESCE(SUM(GREATEST(attempt_count - 1, 0)), 0) AS retry_attempts,
  COUNT(*) FILTER (WHERE status = 'failed') AS failed_jobs,
  COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled_jobs
FROM inbox_ai_turn_jobs
WHERE created_at >= :'since_at'::timestamptz
  AND created_at < :'until_at'::timestamptz;

\echo 'Paid reservation expiry outcomes'
SELECT
  COUNT(*) FILTER (WHERE reservation_expires_at IS NOT NULL) AS reservations_with_deadlines,
  COUNT(*) FILTER (WHERE reservation_expired_at IS NOT NULL) AS reservations_expired,
  COUNT(*) FILTER (WHERE status = 'expired') AS currently_expired,
  COUNT(*) FILTER (WHERE payment_status IN ('paid', 'deposit_paid')) AS payment_satisfied
FROM bookings booking
INNER JOIN inbox_ai_booking_confirmations confirmation
  ON confirmation.booking_id = booking.id
WHERE booking.created_at >= :'since_at'::timestamptz
  AND booking.created_at < :'until_at'::timestamptz;

COMMIT;
