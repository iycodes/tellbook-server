\set ON_ERROR_STOP on
\pset pager off
\timing on

BEGIN READ ONLY;
SET LOCAL statement_timeout = '10s';
SET LOCAL lock_timeout = '2s';

\echo 'Inbox indexes'
SELECT indexname, indexdef
FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename IN (
    'inbox_conversations',
    'inbox_messages',
    'inbox_participant_states',
    'inbox_events',
    'bookings'
  )
ORDER BY tablename, indexname;

SELECT (
  SELECT client_id
  FROM inbox_conversations
  GROUP BY client_id
  ORDER BY COUNT(*) DESC, client_id
  LIMIT 1
) AS sample_provider_id
\gset

SELECT (
  SELECT marketplace_customer_id
  FROM inbox_conversations
  GROUP BY marketplace_customer_id
  ORDER BY COUNT(*) DESC, marketplace_customer_id
  LIMIT 1
) AS sample_marketplace_customer_id
\gset

SELECT (
  SELECT conversation_id
  FROM inbox_messages
  GROUP BY conversation_id
  ORDER BY COUNT(*) DESC, conversation_id
  LIMIT 1
) AS sample_conversation_id
\gset

\if :{?sample_provider_id}
\echo 'Provider conversation page (ordered actor index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id, last_message_at, created_at
FROM inbox_conversations
WHERE client_id = :'sample_provider_id'::uuid
ORDER BY last_message_at DESC NULLS LAST, created_at DESC, id DESC
LIMIT 51;

\echo 'Provider event drain (actor + sequence index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT sequence, conversation_id, event_type, payload, created_at
FROM inbox_events
WHERE client_id = :'sample_provider_id'::uuid
  AND sequence > 0
ORDER BY sequence ASC
LIMIT 101;
\else
\echo 'Skipped provider plans: inbox_conversations has no sample rows.'
\endif

\if :{?sample_marketplace_customer_id}
\echo 'Marketplace conversation page (ordered actor index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id, last_message_at, created_at
FROM inbox_conversations
WHERE marketplace_customer_id = :'sample_marketplace_customer_id'::uuid
ORDER BY last_message_at DESC NULLS LAST, created_at DESC, id DESC
LIMIT 51;

\echo 'Marketplace event drain (actor + sequence index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT sequence, conversation_id, event_type, payload, created_at
FROM inbox_events
WHERE marketplace_customer_id = :'sample_marketplace_customer_id'::uuid
  AND sequence > 0
ORDER BY sequence ASC
LIMIT 101;
\else
\echo 'Skipped marketplace plans: inbox_conversations has no sample rows.'
\endif

\if :{?sample_conversation_id}
\echo 'Conversation message page (conversation + descending sequence index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT id, sequence, sender_type, sender_id, client_message_id, content, sent_at
FROM inbox_messages
WHERE conversation_id = :'sample_conversation_id'::uuid
ORDER BY sequence DESC
LIMIT 51;

\echo 'Participant state lookup (primary key)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT last_read_sequence, last_read_at, archived_at
FROM inbox_participant_states
WHERE conversation_id = :'sample_conversation_id'::uuid
  AND participant_type = 'provider'
  AND participant_id = :'sample_provider_id'::uuid;
\else
\echo 'Skipped message/state plans: inbox_messages has no sample rows.'
\endif

\echo 'Due unpaid autopilot reservation batch (partial deadline index)'
EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS, FORMAT TEXT)
SELECT booking.id
FROM bookings booking
WHERE booking.reservation_expires_at <= NOW()
  AND booking.reservation_expired_at IS NULL
  AND booking.status NOT IN ('cancelled','canceled','declined','expired','completed','no_show')
  AND booking.payment_status NOT IN ('deposit_paid_balance_due','paid_in_full')
  AND EXISTS (
    SELECT 1 FROM inbox_ai_booking_sessions session WHERE session.booking_id=booking.id
  )
ORDER BY booking.reservation_expires_at, booking.id
LIMIT 100;

ROLLBACK;
