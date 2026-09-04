\set ON_ERROR_STOP on
\pset pager off
\timing on

BEGIN READ ONLY;
SET LOCAL statement_timeout = '10s';
SET LOCAL lock_timeout = '2s';

\echo 'Tessa indexes'
SELECT tablename,indexname,indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename IN (
    'tessa_threads','tessa_messages','tessa_runs','tessa_run_steps','tessa_events'
  )
ORDER BY tablename,indexname;

SELECT COALESCE(
  (SELECT client_id FROM tessa_threads ORDER BY last_activity_at DESC LIMIT 1),
  '00000000-0000-0000-0000-000000000000'::uuid
) AS sample_client_id
\gset

SELECT COALESCE(
  (SELECT id FROM tessa_threads WHERE client_id=:'sample_client_id'::uuid ORDER BY last_activity_at DESC LIMIT 1),
  '00000000-0000-0000-0000-000000000000'::uuid
) AS sample_thread_id
\gset

SELECT COALESCE(
  (SELECT id FROM tessa_runs WHERE thread_id=:'sample_thread_id'::uuid ORDER BY created_at DESC LIMIT 1),
  '00000000-0000-0000-0000-000000000000'::uuid
) AS sample_run_id
\gset

\echo 'Bootstrap active thread (partial unique client index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id,status,last_activity_at,created_at,archived_at
FROM tessa_threads
WHERE client_id=:'sample_client_id'::uuid AND status='active';

\echo 'Message keyset page (thread + descending sequence index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id,thread_id,sequence,sender_type,source_channel,client_message_id,content,
       presentation,entity_references,run_id,created_at
FROM tessa_messages
WHERE thread_id=:'sample_thread_id'::uuid
  AND client_id=:'sample_client_id'::uuid
  AND sequence < 9223372036854775807
ORDER BY sequence DESC
LIMIT 51;

\echo 'Active run lookup (partial unique thread index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id,thread_id,trigger_message_id,status,stage,error_code,fallback_used,
       created_at,started_at,completed_at,cancelled_at
FROM tessa_runs
WHERE client_id=:'sample_client_id'::uuid
  AND thread_id=:'sample_thread_id'::uuid
  AND status IN ('queued','processing');

\echo 'Worker claim candidate (partial queue index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id,thread_id,client_id,trigger_message_id,attempt_count,max_attempts,created_at
FROM tessa_runs
WHERE available_at<=NOW() AND (
  (status='queued' AND attempt_count<max_attempts)
  OR (status='processing' AND lease_expires_at<NOW() AND attempt_count<max_attempts)
)
ORDER BY available_at,created_at,id
LIMIT 1;

\echo 'Committed planning step replay (run + sequence unique index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT safe_result,provider,model,error_code
FROM tessa_run_steps
WHERE run_id=:'sample_run_id'::uuid
  AND sequence=1 AND stage='planning' AND status='succeeded';

\echo 'Bounded customer candidates before booking aggregation'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
WITH candidate_customers AS MATERIALIZED (
  SELECT id,full_name,tier_label,status_label
  FROM customers
  WHERE client_id=:'sample_client_id'::uuid
  ORDER BY updated_at DESC,id
  LIMIT 64
)
SELECT customer.id,customer.full_name,
  COALESCE(summary.has_upcoming,FALSE),summary.next_booking_at
FROM candidate_customers customer
LEFT JOIN LATERAL (
  SELECT BOOL_OR(booking.start_at>=NOW()) AS has_upcoming,
    MIN(booking.start_at) FILTER (WHERE booking.start_at>=NOW()) AS next_booking_at
  FROM bookings booking
  WHERE booking.client_id=:'sample_client_id'::uuid
    AND booking.customer_id=customer.id
) summary ON TRUE
ORDER BY COALESCE(summary.has_upcoming,FALSE) DESC,summary.next_booking_at NULLS LAST,
  customer.full_name,customer.id
LIMIT 9;

\echo 'Client event bounds and cursor drain (client + sequence index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT COALESCE(MIN(sequence),0),COALESCE(MAX(sequence),0)
FROM tessa_events
WHERE client_id=:'sample_client_id'::uuid;

EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT sequence,thread_id,message_id,run_id,event_type,created_at
FROM tessa_events
WHERE client_id=:'sample_client_id'::uuid AND sequence>0
ORDER BY sequence
LIMIT 101;

\echo 'Bounded retention candidates (created/archive partial indexes)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT sequence
FROM tessa_events
WHERE created_at < NOW()-INTERVAL '30 days'
ORDER BY created_at,sequence
LIMIT 5000;

EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id
FROM tessa_threads
WHERE status='archived' AND archived_at < NOW()-INTERVAL '90 days'
ORDER BY archived_at,id
LIMIT 5000;

ROLLBACK;
