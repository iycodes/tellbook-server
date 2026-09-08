package authchallenge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const tessaLinkEmailTemplate = "tessa_link_email"
const PurposeTessaWhatsAppLink = "tessa_whatsapp_link"

var tessaSenderPattern = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)
var tessaReceiverPattern = regexp.MustCompile(`^[0-9]{1,32}$`)

type TessaEmailLinkRequest = whatsapp.TessaEmailLinkRequest
type TessaEmailLinkResult = whatsapp.TessaEmailLinkResult
type linkEmailPayload struct {
	PhoneSuffix string `json:"phone_suffix"`
}

func (s *Service) tessaCodeHash(id uuid.UUID, code string) []byte {
	h := hmac.New(sha256.New, s.destinationKey)
	h.Write([]byte(PurposeTessaWhatsAppLink + ":" + id.String() + ":" + code))
	return h.Sum(nil)
}

// Internal signed-ingress operation. The caller resolves/allowlists the provider,
// persists the source receipt and commits its onboarding response in this same tx.
// uuid.Nil means no mail was issued; never expose eligibility/rate distinctions.
func (s *Service) IssueTessaEmailLinkTx(ctx context.Context, tx pgx.Tx, in TessaEmailLinkRequest) (uuid.UUID, error) {
	if !s.emailEnabled || s.keyring == nil || len(s.destinationKey) < 32 {
		return uuid.Nil, ErrUnavailable
	}
	in.Sender = "+" + strings.TrimPrefix(strings.TrimSpace(in.Sender), "+")
	if in.SourceReceiptID == uuid.Nil || !tessaReceiverPattern.MatchString(in.PhoneNumberID) || !tessaSenderPattern.MatchString(in.Sender) || in.NoticeRevision != whatsapp.TessaWhatsAppNotice {
		return uuid.Nil, ErrInvalidChallenge
	}
	_, email, _, err := NormalizeIdentifier(in.Email, ChannelEmail)
	if err != nil || in.ClientID == uuid.Nil {
		return uuid.Nil, nil
	}
	var revision int64
	err = tx.QueryRow(ctx, `SELECT security_revision FROM clients WHERE id=$1 AND email=$2 AND email_verified_at IS NOT NULL FOR UPDATE`, in.ClientID, email).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, err
	}
	// Match the account-before-sender lock order used by dashboard completion.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tessa-email-issue-budget',0))`); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.PhoneNumberID+":"+in.Sender); err != nil {
		return uuid.Nil, err
	}
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM tessa_whatsapp_email_challenges WHERE source_receipt_id=$1 AND client_id=$2 AND phone_number_id=$3 AND destination=$4`, in.SourceReceiptID, in.ClientID, in.PhoneNumberID, in.Sender).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT
	 (SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE created_at>NOW()-INTERVAL '1 minute')<60
	 AND (SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE client_id=$1 AND created_at>NOW()-INTERVAL '1 hour')<3
	 AND (SELECT COUNT(*) FROM tessa_whatsapp_email_challenges WHERE phone_number_id=$2 AND destination=$3 AND created_at>NOW()-INTERVAL '1 hour')<3
	 AND NOT EXISTS(SELECT 1 FROM tessa_whatsapp_email_challenges WHERE (client_id=$1 OR (phone_number_id=$2 AND destination=$3)) AND created_at>NOW()-INTERVAL '1 minute')
	 AND (SELECT COALESCE(SUM(failed_attempts),0) FROM tessa_whatsapp_email_challenges WHERE phone_number_id=$2 AND destination=$3 AND created_at>NOW()-INTERVAL '1 hour')<5`, in.ClientID, in.PhoneNumberID, in.Sender).Scan(&allowed)
	if err != nil || !allowed {
		return uuid.Nil, err
	}
	var validSource bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM meta_whatsapp_webhook_receipts WHERE id=$1 AND phone_number_id=$2 AND event_kind='inbound_message')`, in.SourceReceiptID, in.PhoneNumberID).Scan(&validSource); err != nil {
		return uuid.Nil, err
	}
	if !validSource {
		return uuid.Nil, ErrInvalidChallenge
	}
	code, err := generateCode()
	if err != nil {
		return uuid.Nil, err
	}
	id, jobID := uuid.New(), uuid.New()
	encoded, err := json.Marshal(deliveryPayload{Destination: email, Code: code, Link: &linkEmailPayload{PhoneSuffix: in.Sender[len(in.Sender)-4:]}})
	if err != nil {
		return uuid.Nil, err
	}
	ciphertext, err := s.keyring.Encrypt(encoded, deliveryAAD(jobID))
	if err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW() WHERE consumed_at IS NULL AND (client_id=$1 OR (phone_number_id=$2 AND destination=$3))`, in.ClientID, in.PhoneNumberID, in.Sender); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO tessa_whatsapp_email_challenges(id,source_receipt_id,client_id,phone_number_id,destination,security_revision,notice_revision,code_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, in.SourceReceiptID, in.ClientID, in.PhoneNumberID, in.Sender, revision, in.NoticeRevision, s.tessaCodeHash(id, code)); err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_code_delivery_jobs(id,realm,channel,template_key,tessa_link_challenge_id,payload_ciphertext,payload_nonce,payload_key_version,destination_fingerprint,delivery_deadline)
	 VALUES($1,'provider','email','tessa_link_email',$2,$3,$4,$5,$6,NOW()+INTERVAL '90 seconds')`, jobID, id, ciphertext.Data, ciphertext.Nonce, ciphertext.KeyVersion, s.destinationFingerprint(ChannelEmail, email))
	return id, err
}

// Invalid codes return Verified=false with nil error so the caller commits the
// failed-attempt counter and receipt deduplication. Only DB failures roll back.
// Success must be committed atomically with grant creation, never on its own.
func (s *Service) VerifyTessaEmailLinkTx(ctx context.Context, tx pgx.Tx, id uuid.UUID, phoneID, sender, code, notice string) (TessaEmailLinkResult, error) {
	var result TessaEmailLinkResult
	if len(s.destinationKey) < 32 || notice != whatsapp.TessaWhatsAppNotice {
		return result, nil
	}
	sender = "+" + strings.TrimPrefix(strings.TrimSpace(sender), "+")
	var clientID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT client_id FROM tessa_whatsapp_email_challenges WHERE id=$1 AND phone_number_id=$2 AND destination=$3`, id, phoneID, sender).Scan(&clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	var currentRevision int64
	if err = tx.QueryRow(ctx, `SELECT security_revision FROM clients WHERE id=$1 AND email_verified_at IS NOT NULL FOR UPDATE`, clientID).Scan(&currentRevision); errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	} else if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, phoneID+":"+sender); err != nil {
		return result, err
	}
	var hash []byte
	var expectedRevision int64
	var valid bool
	err = tx.QueryRow(ctx, `SELECT code_hash,security_revision,consumed_at IS NULL AND expires_at>NOW() AND delivery_accepted_at IS NOT NULL AND notice_revision=$2 AND failed_attempts<5 FROM tessa_whatsapp_email_challenges WHERE id=$1 FOR UPDATE`, id, notice).Scan(&hash, &expectedRevision, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !valid || currentRevision != expectedRevision {
		return result, nil
	}
	var attempts int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(failed_attempts),0) FROM tessa_whatsapp_email_challenges WHERE phone_number_id=$1 AND destination=$2 AND created_at>NOW()-INTERVAL '1 hour'`, phoneID, sender).Scan(&attempts); err != nil {
		return result, err
	}
	if attempts >= 5 {
		return result, nil
	}
	if !validCode(code) || !hmac.Equal(hash, s.tessaCodeHash(id, code)) {
		_, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET failed_attempts=failed_attempts+1,consumed_at=CASE WHEN $2>=4 THEN NOW() ELSE consumed_at END WHERE id=$1`, id, attempts)
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE tessa_whatsapp_email_challenges SET consumed_at=NOW() WHERE id=$1`, id); err != nil {
		return result, err
	}
	return TessaEmailLinkResult{Verified: true, ClientID: clientID, SecurityRevision: currentRevision}, nil
}

func (s *Service) tessaLinkEmailDispatchable(ctx context.Context, job DeliveryJob) (bool, error) {
	var valid bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_code_delivery_jobs j JOIN tessa_whatsapp_email_challenges q ON q.id=j.tessa_link_challenge_id JOIN clients c ON c.id=q.client_id
	 WHERE j.id=$1 AND j.status='processing' AND j.lease_owner=$2 AND j.lease_expires_at>NOW() AND j.delivery_deadline>NOW()
	 AND q.consumed_at IS NULL AND q.expires_at>NOW() AND q.security_revision=c.security_revision AND c.email_verified_at IS NOT NULL AND q.notice_revision=$3)`, job.ID, job.LeaseOwner, whatsapp.TessaWhatsAppNotice).Scan(&valid)
	return valid, err
}

func validLinkEmailPayload(payload *linkEmailPayload) bool {
	if payload == nil || len(payload.PhoneSuffix) != 4 {
		return false
	}
	for _, digit := range payload.PhoneSuffix {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}
