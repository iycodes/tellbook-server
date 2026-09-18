package authchallenge

import (
	"context"
	"encoding/json"
	"time"

	"booking/go-server/internal/transactionemail"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) prepareAccountSecurityEmails(ctx context.Context, limit int) error {
	if !s.additionalEmailsEnabled || !s.emailEnabled || s.keyring == nil {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id,realm,recipient_email,kind,details,created_at,email_deadline FROM account_security_events WHERE email_queued_at IS NULL AND skip_reason='' ORDER BY created_at,id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return err
	}
	type event struct {
		id                 uuid.UUID
		realm, email, kind string
		details            []byte
		at, deadline       time.Time
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
		var e event
		err := r.Scan(&e.id, &e.realm, &e.email, &e.kind, &e.details, &e.at, &e.deadline)
		return e, err
	})
	if err != nil {
		return err
	}
	for _, e := range events {
		reason := ""
		_, email, _, normalizeErr := NormalizeIdentifier(e.email, ChannelEmail)
		if !e.deadline.After(s.now()) {
			reason = "expired"
		} else if normalizeErr != nil {
			reason = "invalid_recipient"
		}
		var details map[string]string
		if err := json.Unmarshal(e.details, &details); err != nil {
			reason = "invalid_content"
		}
		jobID := uuid.New()
		input := transactionemail.SecurityInput{Event: transactionemail.Event{DeliveryID: jobID, Recipient: email, OccurredAt: e.at}, Kind: transactionemail.SecurityKind(e.kind), PhoneLastFour: details["phone_last_four"], InstitutionName: details["institution_name"], AccountLastFour: details["account_last_four"]}
		if _, err := transactionemail.RenderSecurity(input); err != nil && reason == "" {
			reason = "invalid_content"
		}
		if reason != "" {
			if _, err := tx.Exec(ctx, `UPDATE account_security_events SET skip_reason=$2 WHERE id=$1`, e.id, reason); err != nil {
				return err
			}
			continue
		}
		encoded, err := json.Marshal(deliveryPayload{Destination: email, AccountSecurity: &input})
		if err != nil {
			return err
		}
		cipher, err := s.keyring.Encrypt(encoded, deliveryAAD(jobID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `WITH job AS (
   INSERT INTO auth_code_delivery_jobs(id,realm,channel,template_key,account_security_event_id,payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,delivery_deadline)
   VALUES($1,$2,'email','account_security_email',$3,$4,$5,$6,$7,$8) RETURNING id
  ) UPDATE account_security_events SET email_queued_at=NOW() WHERE id=$3 AND EXISTS(SELECT 1 FROM job)`, jobID, e.realm, e.id, cipher.Data, cipher.Nonce, cipher.KeyVersion, s.destinationFingerprint(ChannelEmail, email), e.deadline)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
