\pset pager off
\timing on

SELECT queue, depth, oldest_runnable_seconds, retry_attempts, dead_letters
FROM (
    SELECT 'event_planner' AS queue,
        COUNT(*) FILTER (WHERE status IN ('pending','retry','processing')) AS depth,
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(
            CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END
        ) FILTER (
            WHERE status IN ('pending','retry','processing')
              AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
        )),0)::numeric(12,3) AS oldest_runnable_seconds,
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE status IN ('pending','retry','processing')
        ),0) AS retry_attempts,
        COUNT(*) FILTER (WHERE status='dead_letter') AS dead_letters
    FROM notification_event_jobs
    UNION ALL
    SELECT 'scope_planner',
        COUNT(*) FILTER (WHERE status IN ('pending','retry','processing')),
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(
            CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END
        ) FILTER (
            WHERE status IN ('pending','retry','processing')
              AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
        )),0)::numeric(12,3),
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE status IN ('pending','retry','processing')
        ),0),
        COUNT(*) FILTER (WHERE status='dead_letter')
    FROM notification_scope_replan_jobs
    UNION ALL
    SELECT 'in_app',
        COUNT(*) FILTER (WHERE status IN ('pending','retry','processing')),
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(scheduled_for) FILTER (
            WHERE status IN ('pending','retry','processing') AND scheduled_for<=NOW()
              AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
        )),0)::numeric(12,3),
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE status IN ('pending','retry','processing')
        ),0),
        COUNT(*) FILTER (WHERE status='dead_letter')
    FROM notification_in_app_jobs
    UNION ALL
    SELECT 'email',
        COUNT(*) FILTER (WHERE channel='email' AND status IN ('pending','retry','processing')),
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(scheduled_for) FILTER (
            WHERE channel='email' AND status IN ('pending','retry','processing') AND scheduled_for<=NOW()
              AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
        )),0)::numeric(12,3),
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE channel='email' AND status IN ('pending','retry','processing')
        ),0),
        COUNT(*) FILTER (WHERE channel='email' AND status IN ('failed','manual_review'))
    FROM notification_deliveries
    UNION ALL
    SELECT 'whatsapp',
        COUNT(*) FILTER (WHERE channel='whatsapp' AND status IN ('pending','retry','processing')),
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(scheduled_for) FILTER (
            WHERE channel='whatsapp' AND status IN ('pending','retry','processing') AND scheduled_for<=NOW()
              AND (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()
        )),0)::numeric(12,3),
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE channel='whatsapp' AND status IN ('pending','retry','processing')
        ),0),
        COUNT(*) FILTER (WHERE channel='whatsapp' AND status IN ('failed','manual_review'))
    FROM notification_deliveries
    UNION ALL
    SELECT 'whatsapp_status',
        COUNT(*) FILTER (WHERE event_kind='status' AND processing_status IN ('pending','retry','processing')),
        COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(created_at) FILTER (
            WHERE event_kind='status' AND processing_status IN ('pending','retry','processing')
              AND (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END)<=NOW()
        )),0)::numeric(12,3),
        COALESCE(SUM(GREATEST(attempt_count-1,0)) FILTER (
            WHERE event_kind='status' AND processing_status IN ('pending','retry','processing')
        ),0),
        COUNT(*) FILTER (WHERE event_kind='status' AND processing_status='dead_letter')
    FROM meta_whatsapp_webhook_receipts
) health
ORDER BY queue;

SELECT channel, status, COUNT(*) AS deliveries,
    MIN(created_at) AS oldest_created_at,
    MAX(updated_at) AS latest_updated_at
FROM notification_deliveries
WHERE status IN ('unknown','manual_review','failed')
GROUP BY channel,status
ORDER BY channel,status;

SELECT message_status, processing_status, COUNT(*) AS receipts,
    COALESCE(EXTRACT(EPOCH FROM NOW()-MIN(created_at)),0)::numeric(12,3) AS oldest_seconds
FROM meta_whatsapp_webhook_receipts
WHERE event_kind='status' AND processing_status IN ('pending','retry','processing','dead_letter')
GROUP BY message_status,processing_status
ORDER BY processing_status,message_status;
