package authchallenge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const tessaSecurityEmailTemplate = "tessa_security_email"

type securityEmailPayload struct {
	Kind        string    `json:"kind"`
	PhoneSuffix string    `json:"phone_suffix"`
	OccurredAt  time.Time `json:"occurred_at"`
}

func validSecurityEmailPayload(p *securityEmailPayload) bool {
	if p == nil || p.OccurredAt.IsZero() || len(p.PhoneSuffix) != 4 {
		return false
	}
	if p.Kind != "linked" && p.Kind != "replaced" && p.Kind != "disconnected" {
		return false
	}
	for _, c := range p.PhoneSuffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func securityEmailText(p securityEmailPayload) string {
	action := "was connected to"
	if p.Kind == "replaced" {
		action = "replaced the previous number connected to"
	}
	if p.Kind == "disconnected" {
		action = "was disconnected from"
	}
	return fmt.Sprintf("WhatsApp ending %s %s your Tellbook Tessa assistant at %s.\n\nThis is an assistant connection change, not a change to your login number or booking reminder preferences.\n\nIf you did not make this change, sign in to Tellbook directly, review your Tessa WhatsApp connection and secure your account. This email contains no sign-in or verification code.", p.PhoneSuffix, action, p.OccurredAt.UTC().Format(time.RFC3339))
}

// Reuses the authentication email queue/transport, not the booking-email flag.
// queued_at survives delivery-job retention, so old events can never be resent.
func (s *Service) prepareTessaSecurityEmails(ctx context.Context, limit int) error {
	if !s.emailEnabled || s.keyring == nil {
		return nil
	}
	if limit < 1 || limit > 100 {
		limit = 100
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,kind,destination,recipient_email,created_at,email_deadline
	 FROM tessa_whatsapp_security_events WHERE email_queued_at IS NULL AND skip_reason=''
	 ORDER BY created_at,id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return err
	}
	type event struct {
		ID                       uuid.UUID
		Kind, Destination, Email string
		CreatedAt, Deadline      time.Time
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (event, error) {
		var e event
		err := row.Scan(&e.ID, &e.Kind, &e.Destination, &e.Email, &e.CreatedAt, &e.Deadline)
		return e, err
	})
	if err != nil {
		return err
	}
	now := s.now()
	for _, e := range events {
		reason := ""
		_, email, _, normalizeErr := NormalizeIdentifier(e.Email, ChannelEmail)
		if !e.Deadline.After(now) {
			reason = "expired"
		} else if normalizeErr != nil || len(e.Destination) < 4 {
			reason = "invalid_recipient"
		}
		if reason != "" {
			if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_security_events SET skip_reason=$2 WHERE id=$1`, e.ID, reason); err != nil {
				return err
			}
			continue
		}
		payload := deliveryPayload{Destination: email, Security: &securityEmailPayload{Kind: e.Kind, PhoneSuffix: e.Destination[len(e.Destination)-4:], OccurredAt: e.CreatedAt}}
		if !validSecurityEmailPayload(payload.Security) {
			return fmt.Errorf("invalid Tessa security event %s", e.ID)
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		jobID := uuid.New()
		ciphertext, err := s.keyring.Encrypt(encoded, deliveryAAD(jobID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `WITH job AS (
		 INSERT INTO auth_code_delivery_jobs(id,realm,channel,template_key,tessa_security_event_id,payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,delivery_deadline)
		 VALUES($1,'provider','email','tessa_security_email',$2,$3,$4,$5,$6,$7) RETURNING id
		) UPDATE tessa_whatsapp_security_events SET email_queued_at=$8 WHERE id=$2 AND EXISTS(SELECT 1 FROM job)`, jobID, e.ID, ciphertext.Data, ciphertext.Nonce, ciphertext.KeyVersion, s.destinationFingerprint(ChannelEmail, email), e.Deadline, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
