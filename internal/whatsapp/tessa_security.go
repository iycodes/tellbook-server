package whatsapp

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Persist the verified recipient at the time of the security event. A later
// identity change must not redirect this notice. SMTP is never called here.
func (r *TessaLinkRepository) recordSecurityEventTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_security_events(id,client_id,connection_revision,kind,destination,recipient_email,skip_reason)
	 SELECT $1,c.id,g.revision,$3,g.destination,
	 CASE WHEN c.email_verified_at IS NOT NULL THEN COALESCE(c.email,'') ELSE '' END,
	 CASE WHEN c.email_verified_at IS NOT NULL AND COALESCE(c.email,'')<>'' THEN '' ELSE 'no_verified_email' END
	 FROM clients c JOIN tessa_whatsapp_connections g ON g.client_id=c.id WHERE c.id=$2
	 ON CONFLICT(client_id,connection_revision,kind) DO NOTHING`, uuid.New(), clientID, kind)
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','tessa_security_email')`)
	}
	return err
}
