package appdata

// Admin transient records use the existing maintenance scheduler, batch limits,
// locking and process ownership. Staff, recovery codes, notes and audit history
// are intentionally excluded from expiry cleanup.
var adminDataMaintenanceTasks = []dataMaintenanceTask{
	{name: "admin_sessions", query: `WITH expired AS (
 SELECT id FROM admin_sessions WHERE expires_at<$1 ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM admin_sessions item USING expired WHERE item.id=expired.id`},
	{name: "admin_auth_limits", query: `WITH expired AS (
 SELECT key_hash FROM admin_auth_limits WHERE window_end<$1 ORDER BY window_end,key_hash LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM admin_auth_limits item USING expired WHERE item.key_hash=expired.key_hash`},
	{name: "admin_interrupted_reset_deliveries", query: `WITH stale AS (
 SELECT id FROM admin_password_resets WHERE delivery_state='sending' AND delivery_updated_at<$1::timestamptz-INTERVAL '10 minutes'
 ORDER BY delivery_updated_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
 ), updated AS (
 UPDATE admin_password_resets item SET delivery_state='unknown',delivery_updated_at=$1 FROM stale WHERE item.id=stale.id RETURNING item.id,item.staff_id
 ) INSERT INTO admin_audit_events(action,entity_type,entity_id,reason,details)
 SELECT 'staff.password_reset_email_unknown','staff',staff_id,'Email delivery was interrupted; outcome is unknown',jsonb_build_object('delivery_id',id,'delivery_state','unknown','source','maintenance') FROM updated`},
	{name: "admin_password_resets", query: `WITH expired AS (
 SELECT token_hash FROM admin_password_resets WHERE expires_at<$1::timestamptz-INTERVAL '30 days'
 ORDER BY expires_at,token_hash LIMIT $2 FOR UPDATE SKIP LOCKED
 ) DELETE FROM admin_password_resets item USING expired WHERE item.token_hash=expired.token_hash`},
	{name: "admin_invitations", query: `WITH expired AS (
 SELECT i.id FROM admin_invitations i JOIN admin_staff p ON p.id=i.staff_id
 WHERE i.expires_at<$1::timestamptz-INTERVAL '90 days'
 AND (p.status<>'invited' OR EXISTS(SELECT 1 FROM admin_invitations newer WHERE newer.staff_id=i.staff_id AND (newer.created_at,newer.id)>(i.created_at,i.id)))
 ORDER BY i.expires_at,i.id LIMIT $2 FOR UPDATE OF i SKIP LOCKED
 ) DELETE FROM admin_invitations item USING expired WHERE item.id=expired.id`},
}

// Configure before Start. Disabled deployments keep their previous task list.
func (worker *DataMaintenanceWorker) WithAdminRetention(enabled bool) *DataMaintenanceWorker {
	worker.tasks = dataMaintenanceTasks
	if enabled {
		worker.tasks = append(append([]dataMaintenanceTask{}, dataMaintenanceTasks...), adminDataMaintenanceTasks...)
	}
	return worker
}
