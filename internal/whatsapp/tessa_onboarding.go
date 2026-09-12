package whatsapp

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type TessaEmailLinkRequest struct {
	SourceReceiptID, ClientID                    uuid.UUID
	Email, PhoneNumberID, Sender, NoticeRevision string
}
type TessaEmailLinkResult struct {
	Verified         bool
	ClientID         uuid.UUID
	SecurityRevision int64
}
type TessaEmailLinkService interface {
	IssueTessaEmailLinkTx(context.Context, pgx.Tx, TessaEmailLinkRequest) (uuid.UUID, error)
	VerifyTessaEmailLinkTx(context.Context, pgx.Tx, uuid.UUID, string, string, string, string) (TessaEmailLinkResult, error)
}

func (r *TessaLinkRepository) WithEmailLinking(service TessaEmailLinkService, webURL string) *TessaLinkRepository {
	r.emailLinks = service
	r.webURL = strings.TrimRight(webURL, "/")
	return r
}

type tessaOnboardingState struct {
	ID                    uuid.UUID
	Revision              int64
	Stage, Notice         string
	ChallengeID, ClientID *uuid.UUID
	Fresh                 bool
}

// Only transient, signature-verified text reaches here. Never retain email/code text.
// Account authority is resolved by the server and proven by a dedicated email code.
func (r *TessaLinkRepository) onboardTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID uuid.UUID, sender string) error {
	if !r.enabled || (r.emailLinks == nil && r.conversations == nil) || receipt.ProviderTimestamp == nil {
		return nil
	}
	// Linked identities bypass onboarding; ordinary chat stays gated until B3/B4.
	var clientID uuid.UUID
	var connectionRevision, securityRevision int64
	err := tx.QueryRow(ctx, `SELECT c.id,g.revision,g.security_revision FROM tessa_whatsapp_connections g JOIN clients c ON c.id=g.client_id
      WHERE g.phone_number_id=$1 AND g.destination=$2 AND g.status='active' AND g.expires_at>NOW()
	      AND g.security_revision=c.security_revision AND g.notice_revision=$3`, r.phoneID, sender, TessaWhatsAppNotice).Scan(&clientID, &connectionRevision, &securityRevision)
	if err == nil {
		if r.conversations != nil && !tessaLinkingText(receipt.control.text) {
			return r.conversations.StoreTessaWhatsAppMessageTx(ctx, tx, TessaInboundMessage{ReceiptID: receiptID, ClientID: clientID, PhoneNumberID: r.phoneID, Sender: sender, MessageID: receipt.MessageID, Content: receipt.control.text, ConnectionRevision: connectionRevision, SecurityRevision: securityRevision, SourceTimestamp: *receipt.ProviderTimestamp})
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if r.emailLinks == nil {
		return nil
	}
	// The onboarding TTL is not a conversation TTL. Linked delayed messages must
	// reach durable ingress, which records expiry/rejection instead of losing them.
	var current bool
	if err := tx.QueryRow(ctx, `SELECT $1::timestamptz>NOW()-INTERVAL '10 minutes' AND $1::timestamptz<NOW()+INTERVAL '1 minute'`, receipt.ProviderTimestamp).Scan(&current); err != nil {
		return err
	}
	if !current {
		return nil
	}
	// Bound anonymous replies too, not just eligible-email sends. This DB-only lock
	// covers the budget check and intent commit across API replicas.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tessa-onboarding-budget',0))`); err != nil {
		return err
	}
	var budget bool
	if err := tx.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE onboarding_id IS NOT NULL AND created_at>NOW()-INTERVAL '1 minute')<600
      AND NOT EXISTS(SELECT 1 FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination=$2
      AND ((reply_window_started_at>NOW()-INTERVAL '1 hour' AND reply_count>=30) OR last_inbound_at>$3))`, r.phoneID, sender, receipt.ProviderTimestamp).Scan(&budget); err != nil {
		return err
	}
	if !budget {
		return nil
	}
	var s tessaOnboardingState
	err = tx.QueryRow(ctx, `SELECT id,revision,stage,notice_revision,challenge_id,client_id,expires_at>NOW() FROM tessa_whatsapp_onboarding WHERE phone_number_id=$1 AND destination=$2`, r.phoneID, sender).Scan(&s.ID, &s.Revision, &s.Stage, &s.Notice, &s.ChallengeID, &s.ClientID, &s.Fresh)
	if errors.Is(err, pgx.ErrNoRows) {
		s.ID = uuid.New()
		s.Stage = "menu"
		s.Fresh = false
	} else if err != nil {
		return err
	}
	text := strings.TrimSpace(receipt.control.token)
	command := strings.ToUpper(text)
	kind := "onboarding_menu"
	if !s.Fresh || s.Stage == "closed" {
		s.Stage = "menu"
		s.Notice = ""
	}
	switch {
	case command == "CREATE":
		s.Stage = "menu"
		s.Notice = ""
		kind = "onboarding_signup"
	case command == "CONNECT":
		s.Stage = "consent"
		s.Notice = ""
		kind = "onboarding_consent"
	case s.Stage == "consent" && command == "AGREE":
		s.Stage = "email"
		s.Notice = TessaWhatsAppNotice
		kind = "onboarding_email"
	case s.Stage == "email" && s.Notice == TessaWhatsAppNotice:
		s.Stage = "code"
		kind = "onboarding_code"
		var clientID uuid.UUID
		email := strings.ToLower(text)
		if address, parseErr := mail.ParseAddress(email); parseErr == nil && address.Address == email && len(email) <= 254 {
			err = tx.QueryRow(ctx, `SELECT id FROM clients WHERE email=$1 AND email_verified_at IS NOT NULL`, email).Scan(&clientID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		id, issueErr := r.emailLinks.IssueTessaEmailLinkTx(ctx, tx, TessaEmailLinkRequest{SourceReceiptID: receiptID, ClientID: clientID, Email: email, PhoneNumberID: r.phoneID, Sender: sender, NoticeRevision: s.Notice})
		if issueErr != nil {
			return issueErr
		}
		s.ChallengeID = nil
		s.ClientID = nil
		if id != uuid.Nil {
			s.ChallengeID = &id
			s.ClientID = &clientID
		}
	case s.Stage == "code" && s.Notice == TessaWhatsAppNotice:
		kind = "onboarding_invalid"
		if command == "RESEND" {
			kind = "onboarding_code"
			// Resolve only the original eligible account and unchanged security revision.
			if s.ChallengeID != nil && s.ClientID != nil {
				var email string
				err = tx.QueryRow(ctx, `SELECT c.email FROM clients c JOIN tessa_whatsapp_email_challenges q ON q.client_id=c.id WHERE q.id=$1 AND c.id=$2 AND c.security_revision=q.security_revision AND c.email_verified_at IS NOT NULL`, s.ChallengeID, s.ClientID).Scan(&email)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				if err == nil {
					id, issueErr := r.emailLinks.IssueTessaEmailLinkTx(ctx, tx, TessaEmailLinkRequest{SourceReceiptID: receiptID, ClientID: *s.ClientID, Email: email, PhoneNumberID: r.phoneID, Sender: sender, NoticeRevision: s.Notice})
					if issueErr != nil {
						return issueErr
					}
					if id != uuid.Nil {
						s.ChallengeID = &id
					}
				}
			}
		} else if s.ChallengeID != nil && s.ClientID != nil {
			proof, verifyErr := r.emailLinks.VerifyTessaEmailLinkTx(ctx, tx, *s.ChallengeID, r.phoneID, sender, text, s.Notice)
			if verifyErr != nil {
				return verifyErr
			}
			if proof.Verified {
				// The verifier holds the account and sender locks until commit.
				completed, completeErr := r.completeLinkTx(ctx, tx, receipt, receiptID, proof.ClientID, sender, proof.SecurityRevision)
				if completeErr != nil {
					return completeErr
				}
				if completed {
					return nil
				}
				kind = "onboarding_failed"
				s.Stage = "menu"
				s.Notice = ""
			}
		}
	case s.Stage == "consent":
		kind = "onboarding_consent"
	}
	if s.Stage == "menu" || s.Stage == "consent" {
		if s.ChallengeID != nil {
			if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW() WHERE id=$1 AND consumed_at IS NULL`, s.ChallengeID); err != nil {
				return err
			}
		}
		s.ChallengeID = nil
		s.ClientID = nil
	}
	return r.persistOnboardingTx(ctx, tx, receipt, receiptID, sender, s, kind)
}

func (r *TessaLinkRepository) persistOnboardingTx(ctx context.Context, tx pgx.Tx, receipt WebhookReceipt, receiptID uuid.UUID, sender string, s tessaOnboardingState, kind string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO tessa_whatsapp_onboarding(id,phone_number_id,destination,stage,revision,notice_revision,challenge_id,client_id,expires_at,last_inbound_at)
      VALUES($1,$2,$3,$4,1,$5,$6,$7,NOW()+INTERVAL '10 minutes',$8)
      ON CONFLICT(phone_number_id,destination) DO UPDATE SET stage=EXCLUDED.stage,revision=tessa_whatsapp_onboarding.revision+1,
      notice_revision=EXCLUDED.notice_revision,challenge_id=EXCLUDED.challenge_id,client_id=EXCLUDED.client_id,
      expires_at=EXCLUDED.expires_at,last_inbound_at=EXCLUDED.last_inbound_at,last_reply_at=NOW(),
      reply_count=CASE WHEN tessa_whatsapp_onboarding.reply_window_started_at<=NOW()-INTERVAL '1 hour' THEN 1 ELSE tessa_whatsapp_onboarding.reply_count+1 END,
      reply_window_started_at=CASE WHEN tessa_whatsapp_onboarding.reply_window_started_at<=NOW()-INTERVAL '1 hour' THEN NOW() ELSE tessa_whatsapp_onboarding.reply_window_started_at END
      WHERE tessa_whatsapp_onboarding.revision=$9`, s.ID, r.phoneID, sender, s.Stage, s.Notice, s.ChallengeID, s.ClientID, receipt.ProviderTimestamp, s.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("Tessa onboarding changed during processing")
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox SET status='cancelled',completed_at=NOW(),last_error_code='onboarding_advanced' WHERE onboarding_id=$1 AND status IN ('pending','retry','processing')`, s.ID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_outbox(id,source_receipt_id,phone_number_id,destination,connection_revision,security_revision,kind,window_expires_at,onboarding_id,onboarding_revision)
      SELECT $1,$2,phone_number_id,destination,0,0,$3,LEAST(expires_at,$4),id,revision FROM tessa_whatsapp_onboarding WHERE id=$5`, uuid.New(), receiptID, kind, receipt.ProviderTimestamp.Add(24*time.Hour), s.ID)
	if err == nil {
		_, err = tx.Exec(ctx, `SELECT pg_notify('tellbook_worker_core','tessa_whatsapp')`)
	}
	return err
}

func (r *TessaLinkRepository) closeOnboardingTx(ctx context.Context, tx pgx.Tx, clientID *uuid.UUID, sender string) error {
	_, err := tx.Exec(ctx, `UPDATE tessa_whatsapp_onboarding SET stage='closed',revision=revision+1 WHERE stage<>'closed' AND (($1::uuid IS NOT NULL AND client_id=$1) OR ($2<>'' AND phone_number_id=$3 AND destination=$2))`, clientID, sender, r.phoneID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_outbox d SET status='cancelled',completed_at=NOW(),last_error_code='onboarding_closed'
      FROM tessa_whatsapp_onboarding s WHERE d.onboarding_id=s.id AND s.stage='closed' AND (($1::uuid IS NOT NULL AND s.client_id=$1) OR ($2<>'' AND s.phone_number_id=$3 AND s.destination=$2)) AND d.status IN ('pending','retry','processing')`, clientID, sender, r.phoneID)
	return err
}
