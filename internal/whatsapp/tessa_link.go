package whatsapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const TessaWhatsAppNotice = "tessa-whatsapp-v1"

var ErrTessaLinkUnavailable = errors.New("Tessa WhatsApp linking is unavailable")
var ErrTessaLinkRateLimited = errors.New("wait before requesting another Tessa link")
var ErrTessaLinkNotice = errors.New("review the Tessa WhatsApp notice")

type TessaWhatsAppState struct {
	LinkingAvailable      bool       `json:"linking_available"`
	ConversationAvailable bool       `json:"conversation_available"`
	Status                string     `json:"status"`
	Destination           string     `json:"destination"`
	NoticeRevision        string     `json:"notice_revision"`
	ExpiresAt             *time.Time `json:"expires_at,omitempty"`
	PendingExpiresAt      *time.Time `json:"pending_expires_at,omitempty"`
}
type TessaLinkChallenge struct {
	URL         string    `json:"url"`
	Destination string    `json:"destination"`
	ExpiresAt   time.Time `json:"expires_at"`
}
type TessaLinkRepository struct {
	db                         *pgxpool.Pool
	phoneID, businessPhone     string
	enabled                    bool
	allowed                    map[uuid.UUID]bool
	emailLinks                 TessaEmailLinkService
	webURL                     string
	conversations              TessaConversationIngress
	answerOrigin, answerNotice string
}

func NewTessaLinkRepository(db *pgxpool.Pool, phoneID, businessPhone string, enabled bool, providerIDs []string) *TessaLinkRepository {
	allowed := make(map[uuid.UUID]bool, len(providerIDs))
	for _, raw := range providerIDs {
		if id, err := uuid.Parse(raw); err == nil {
			allowed[id] = true
		}
	}
	return &TessaLinkRepository{db: db, phoneID: phoneID, businessPhone: businessPhone, enabled: enabled, allowed: allowed}
}
func (r *TessaLinkRepository) State(ctx context.Context, clientID uuid.UUID) (TessaWhatsAppState, error) {
	state := TessaWhatsAppState{LinkingAvailable: r.enabled && r.allowed[clientID], Status: "disconnected", NoticeRevision: TessaWhatsAppNotice}
	var destination string
	err := r.db.QueryRow(ctx, `SELECT c.destination,CASE WHEN c.status='revoked' THEN 'disconnected'
	 WHEN c.security_revision<>p.security_revision OR c.expires_at<=NOW() OR c.notice_revision<>$3 THEN 'expired'
	 ELSE 'connected' END,c.expires_at FROM tessa_whatsapp_connections c JOIN clients p ON p.id=c.client_id
	 WHERE c.client_id=$1 AND c.phone_number_id=$2`, clientID, r.phoneID, TessaWhatsAppNotice).Scan(&destination, &state.Status, &state.ExpiresAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, err
	}
	if destination != "" {
		state.Destination = maskE164(destination)
	}
	err = r.db.QueryRow(ctx, `SELECT q.expires_at FROM tessa_whatsapp_link_challenges q JOIN clients c ON c.id=q.client_id
	 WHERE q.client_id=$1 AND q.phone_number_id=$2 AND q.consumed_at IS NULL AND q.expires_at>NOW()
	 AND q.security_revision=c.security_revision`, clientID, r.phoneID).Scan(&state.PendingExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	state.ConversationAvailable = r.conversationAvailable() && r.allowed[clientID]
	return state, err
}

func (r *TessaLinkRepository) conversationAvailable() bool {
	return r.enabled && r.conversations != nil && r.answerOrigin != "" && r.answerNotice != ""
}
func (r *TessaLinkRepository) Start(ctx context.Context, clientID uuid.UUID, number, notice string) (TessaLinkChallenge, error) {
	if !r.enabled || !r.allowed[clientID] {
		return TessaLinkChallenge{}, ErrTessaLinkUnavailable
	}
	if notice != TessaWhatsAppNotice {
		return TessaLinkChallenge{}, ErrTessaLinkNotice
	}
	if !strings.HasPrefix(strings.TrimSpace(number), "+") {
		return TessaLinkChallenge{}, ErrInvalidWhatsAppDestination
	}
	destination, err := normalizeInternationalE164(number)
	if err != nil {
		return TessaLinkChallenge{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return TessaLinkChallenge{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256([]byte(token))
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return TessaLinkChallenge{}, err
	}
	defer tx.Rollback(ctx)
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT security_revision FROM clients WHERE id=$1 FOR UPDATE`, clientID).Scan(&revision); err != nil {
		return TessaLinkChallenge{}, err
	}
	var recent int
	if err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_link_challenges WHERE client_id=$1 AND created_at>NOW()-INTERVAL '1 minute'`, clientID).Scan(&recent); err != nil {
		return TessaLinkChallenge{}, err
	}
	if recent > 0 {
		return TessaLinkChallenge{}, ErrTessaLinkRateLimited
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET consumed_at=NOW() WHERE client_id=$1 AND consumed_at IS NULL`, clientID); err != nil {
		return TessaLinkChallenge{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW() WHERE client_id=$1 AND consumed_at IS NULL`, clientID); err != nil {
		return TessaLinkChallenge{}, err
	}
	if err = r.closeOnboardingTx(ctx, tx, &clientID, ""); err != nil {
		return TessaLinkChallenge{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='cancelled',completed_at=NOW(),last_error_code='challenge_replaced'
	 WHERE client_id=$1 AND kind IN ('mismatch','unavailable') AND status IN ('pending','retry','processing')`, clientID); err != nil {
		return TessaLinkChallenge{}, err
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO tessa_whatsapp_link_challenges(id,client_id,token_hash,phone_number_id,destination,security_revision,notice_revision,expires_at)
	 VALUES($1,$2,$3,$4,$5,$6,$7,NOW()+INTERVAL '10 minutes') RETURNING expires_at`, uuid.New(), clientID, hash[:], r.phoneID, destination, revision, notice).Scan(&expires)
	if err != nil {
		return TessaLinkChallenge{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return TessaLinkChallenge{}, err
	}
	return TessaLinkChallenge{URL: "https://wa.me/" + strings.TrimPrefix(r.businessPhone, "+") + "?text=" + url.QueryEscape("TESSA LINK "+token), Destination: maskE164(destination), ExpiresAt: expires}, nil
}
func (r *TessaLinkRepository) Disconnect(ctx context.Context, clientID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, clientID); err != nil {
		return err
	}
	if err = r.revokeTessaLinkTx(ctx, tx, clientID, true); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (r *TessaLinkRepository) revokeTessaLinkTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, notify bool) error {
	tag, err := tx.Exec(ctx, `UPDATE tessa_whatsapp_connections SET status='revoked',revision=revision+1,revoked_at=NOW() WHERE client_id=$1 AND status='active'`, clientID)
	if err != nil {
		return err
	}
	if notify && tag.RowsAffected() != 0 {
		if err = r.recordSecurityEventTx(ctx, tx, clientID, "disconnected"); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_ingress SET status='cancelled',reason='connection_revoked',content=NULL,completed_at=NOW() WHERE client_id=$1 AND status='pending'`, clientID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET consumed_at=NOW() WHERE client_id=$1 AND consumed_at IS NULL`, clientID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW() WHERE client_id=$1 AND consumed_at IS NULL`, clientID); err != nil {
		return err
	}
	if err = r.closeOnboardingTx(ctx, tx, &clientID, ""); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='cancelled',completed_at=NOW(),last_error_code='connection_revoked'
	 WHERE client_id=$1 AND status IN ('pending','retry','processing')`, clientID)
	return err
}

// Runs inside receipt persistence. No control text/token enters a Tessa transcript.
func (r *TessaLinkRepository) applyControlTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID uuid.UUID) error {
	if receipt.PhoneNumberID != r.phoneID {
		return nil
	}
	control := receipt.control
	if control.kind != "tessa_link" && control.kind != "tessa_disconnect" && control.kind != "stop" && control.kind != "tessa_onboarding" {
		return nil
	}
	if control.kind == "tessa_onboarding" && (!r.enabled || (r.emailLinks == nil && r.conversations == nil)) {
		return nil
	}
	sender, err := normalizeInternationalE164(control.sender)
	if err != nil {
		return nil
	}
	// All signed Tessa controls for a sender serialize before account locks.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "tessa-onboarding:"+r.phoneID+":"+sender); err != nil {
		return err
	}
	if control.kind == "tessa_onboarding" {
		return r.onboardTx(ctx, tx, receipt, receiptID, sender)
	}
	if control.kind != "tessa_link" {
		if err = r.disconnectSenderTx(ctx, tx, receipt, receiptID, sender); err != nil {
			return err
		}
		return r.closeOnboardingTx(ctx, tx, nil, sender)
	}
	if !r.enabled {
		return nil
	}
	hash := sha256.Sum256([]byte(control.token))
	var clientID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT client_id FROM tessa_whatsapp_link_challenges WHERE token_hash=$1`, hash[:]).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT security_revision FROM clients WHERE id=$1 FOR UPDATE`, clientID).Scan(&revision); err != nil {
		return err
	}
	if !r.allowed[clientID] {
		return nil
	}
	var id uuid.UUID
	var expected, notice string
	var expectedRevision int64
	var attempts int
	var valid bool
	err = tx.QueryRow(ctx, `SELECT id,destination,security_revision,notice_revision,attempts,
	 expires_at>NOW() AND consumed_at IS NULL FROM tessa_whatsapp_link_challenges WHERE token_hash=$1 AND phone_number_id=$2 FOR UPDATE`, hash[:], r.phoneID).Scan(&id, &expected, &expectedRevision, &notice, &attempts, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !valid || attempts >= 5 {
		return nil
	}
	if expectedRevision != revision || notice != TessaWhatsAppNotice {
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET consumed_at=NOW() WHERE id=$1`, id)
		return err
	}
	if expected != sender {
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET attempts=attempts+1,consumed_at=CASE WHEN attempts>=4 THEN NOW() ELSE NULL END WHERE id=$1`, id)
		if err != nil {
			return err
		}
		return r.enqueueControlTx(ctx, tx, receipt, receiptID, clientID, sender, "mismatch")
	}
	// Serializes competing claims of this number across provider accounts.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, r.phoneID+":"+sender); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET consumed_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	completed, err := r.completeLinkTx(ctx, tx, receipt, receiptID, clientID, sender, revision)
	if err != nil {
		return err
	}
	if !completed {
		return r.enqueueControlTx(ctx, tx, receipt, receiptID, clientID, sender, "unavailable")
	}
	return nil
}

// Both proof types complete here while holding the account and signed-sender locks.
// Never transfer a sender's active grant from a different provider.
func (r *TessaLinkRepository) completeLinkTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID, clientID uuid.UUID, sender string, revision int64) (bool, error) {
	var other bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_connections WHERE phone_number_id=$1 AND destination=$2 AND status='active' AND client_id<>$3)`, r.phoneID, sender, clientID).Scan(&other); err != nil {
		return false, err
	}
	if other {
		return false, nil
	}
	var replaced bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_connections WHERE client_id=$1 AND status='active' AND destination<>$2)`, clientID, sender).Scan(&replaced); err != nil {
		return false, err
	}
	if err := r.revokeTessaLinkTx(ctx, tx, clientID, false); err != nil {
		return false, err
	}
	if err := r.closeOnboardingTx(ctx, tx, nil, sender); err != nil {
		return false, err
	}
	_, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_connections(client_id,phone_number_id,destination,status,security_revision,notice_revision,expires_at)
	 VALUES($1,$2,$3,'active',$4,$5,NOW()+INTERVAL '30 days') ON CONFLICT(client_id) DO UPDATE SET phone_number_id=EXCLUDED.phone_number_id,
	 destination=EXCLUDED.destination,status='active',revision=tessa_whatsapp_connections.revision+1,security_revision=EXCLUDED.security_revision,
	 notice_revision=EXCLUDED.notice_revision,linked_at=NOW(),last_active_at=NOW(),expires_at=EXCLUDED.expires_at,revoked_at=NULL`, clientID, r.phoneID, sender, revision, TessaWhatsAppNotice)
	if err != nil {
		return false, err
	}
	kind := "linked"
	if replaced {
		kind = "replaced"
	}
	if err = r.recordSecurityEventTx(ctx, tx, clientID, kind); err != nil {
		return false, err
	}
	return true, r.enqueueControlTx(ctx, tx, receipt, receiptID, clientID, sender, "linked")
}

// STOP also applies before the first grant exists and to generic mismatch replies.
// Lock affected accounts in a stable order, then recheck the signed sender's scope.
func (r *TessaLinkRepository) disconnectSenderTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID uuid.UUID, sender string) error {
	rows, err := tx.Query(ctx, `WITH targets AS (
	 SELECT client_id FROM tessa_whatsapp_connections WHERE phone_number_id=$1 AND destination=$2 AND status='active'
	 UNION SELECT client_id FROM tessa_whatsapp_link_challenges WHERE phone_number_id=$1 AND destination=$2 AND consumed_at IS NULL
	 UNION SELECT client_id FROM tessa_whatsapp_email_challenges WHERE phone_number_id=$1 AND destination=$2 AND consumed_at IS NULL
	 UNION SELECT client_id FROM tessa_whatsapp_outbox WHERE phone_number_id=$1 AND destination=$2 AND status IN ('pending','retry','processing')
	) SELECT c.id FROM clients c JOIN targets t ON t.client_id=c.id ORDER BY c.id FOR UPDATE OF c`, r.phoneID, sender)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	for _, clientID := range ids {
		var active bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tessa_whatsapp_connections WHERE client_id=$1 AND phone_number_id=$2 AND destination=$3 AND status='active')`, clientID, r.phoneID, sender).Scan(&active); err != nil {
			return err
		}
		if active {
			if err = r.revokeTessaLinkTx(ctx, tx, clientID, true); err != nil {
				return err
			}
			if receipt.control.kind == "tessa_disconnect" {
				if err = r.enqueueControlTx(ctx, tx, receipt, receiptID, clientID, sender, "disconnected"); err != nil {
					return err
				}
			}
			continue
		}
		// A sender cancelling a pending replacement must not revoke the account's
		// existing grant to a different number, or someone else's correct-number challenge.
		if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW()
		 WHERE client_id=$1 AND phone_number_id=$2 AND destination=$3 AND consumed_at IS NULL`, clientID, r.phoneID, sender); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_link_challenges SET consumed_at=NOW()
		 WHERE client_id=$1 AND phone_number_id=$2 AND destination=$3 AND consumed_at IS NULL`, clientID, r.phoneID, sender); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='cancelled',completed_at=NOW(),last_error_code='sender_disconnected'
		 WHERE client_id=$1 AND phone_number_id=$2 AND destination=$3 AND status IN ('pending','retry','processing')`, clientID, r.phoneID, sender); err != nil {
			return err
		}
	}
	return nil
}

func (r *TessaLinkRepository) enqueueControlTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID, clientID uuid.UUID, sender, kind string) error {
	if receipt.ProviderTimestamp == nil {
		return nil
	}
	hash := sha256.Sum256([]byte(receipt.control.token))
	_, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,client_id,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at,challenge_id)
	 SELECT $1,$2,c.id,$3,$4,COALESCE(w.revision,0),c.security_revision,$5,$6,
	 (SELECT id FROM tessa_whatsapp_link_challenges WHERE client_id=c.id AND token_hash=$8)
	 FROM clients c LEFT JOIN tessa_whatsapp_connections w ON w.client_id=c.id WHERE c.id=$7
	 ON CONFLICT(source_receipt_id) DO NOTHING`, uuid.New(), receiptID, r.phoneID, sender, kind, receipt.ProviderTimestamp.Add(24*time.Hour), clientID, hash[:])
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','tessa_whatsapp')`)
	}
	return err
}
