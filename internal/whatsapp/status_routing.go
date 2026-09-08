package whatsapp

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RouteStatusReceipts is shared by all status consumers. Routing never consumes a
// domain receipt. Outbox IDs exist before network I/O, so early callbacks are safe.
func RouteStatusReceipts(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `
	WITH candidates AS (
	 SELECT * FROM meta_whatsapp_webhook_receipts
	 WHERE event_kind='status' AND processing_owner='unassigned'
	 AND processing_status IN ('pending','retry','processing')
	 AND (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END)<=NOW()
	 ORDER BY (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END),created_at,id
	 FOR UPDATE SKIP LOCKED LIMIT 100
	), matches AS (
	 SELECT r.id,COUNT(m.id) AS n,MIN(m.owner) AS owner,
	 BOOL_OR((r.correlation_id<>'' AND r.correlation_id<>m.id::text)
	 OR (m.wamid<>'' AND m.wamid<>r.wamid)) AS conflict
	 FROM candidates r LEFT JOIN LATERAL (
	  SELECT j.id,j.provider_message_id AS wamid,'auth' AS owner FROM auth_code_delivery_jobs j
	  WHERE j.channel='whatsapp' AND (j.id=NULLIF(r.correlation_id,'')::uuid OR j.provider_message_id=r.wamid)
	  UNION ALL
	  SELECT d.id,d.provider_message_id,'notification' FROM notification_deliveries d
	  WHERE d.channel='whatsapp' AND (d.id=NULLIF(r.correlation_id,'')::uuid OR d.provider_message_id=r.wamid)
	  UNION ALL
	  SELECT d.id,d.provider_message_id,'tessa' FROM tessa_whatsapp_outbox d
	  WHERE d.phone_number_id=r.phone_number_id AND (d.id=NULLIF(r.correlation_id,'')::uuid OR d.provider_message_id=r.wamid)
	 ) m ON TRUE GROUP BY r.id
	)
	UPDATE meta_whatsapp_webhook_receipts r SET
	 processing_owner=CASE WHEN m.n>1 OR COALESCE(m.conflict,FALSE) THEN 'quarantined' WHEN m.n=1 THEN m.owner ELSE 'unassigned' END,
	 processing_status=CASE WHEN m.n>1 OR COALESCE(m.conflict,FALSE) THEN 'dead_letter'
	 WHEN m.n=0 AND r.created_at<NOW()-INTERVAL '15 minutes' THEN 'completed' ELSE 'pending' END,
	 lease_owner='',lease_token=NULL,lease_expires_at=NULL,
	 last_error_code=CASE WHEN m.n>1 OR COALESCE(m.conflict,FALSE) THEN 'status_owner_conflict'
	 WHEN m.n=0 THEN 'status_owner_unresolved' ELSE '' END,
	 available_at=CASE WHEN m.n=0 THEN NOW()+INTERVAL '5 seconds' ELSE NOW() END,
	 processed_at=CASE WHEN m.n>1 OR COALESCE(m.conflict,FALSE) OR (m.n=0 AND r.created_at<NOW()-INTERVAL '15 minutes') THEN NOW() ELSE NULL END
	FROM matches m WHERE r.id=m.id`)
	return err
}
