package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agreementservice "booking/go-server/internal/agreements/service"
	"booking/go-server/internal/agreements/signature"
	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) PreparePublicBookingAgreement(ctx context.Context, bookingToken string) (PublicBookingAgreementResponse, error) {
	if r.agreementTokens == nil {
		return PublicBookingAgreementResponse{}, fmt.Errorf("agreement token encryption is not configured")
	}
	const query = `
		SELECT
			ai.id, ai.client_id, ai.title_snapshot, ai.status, ai.confirmation_method,
			ai.sent_to_email, ai.public_token_ciphertext, ai.public_token_nonce,
			ai.public_token_key_version, ai.delivery_revision, b.payment_status
		FROM bookings b
		INNER JOIN agreement_instances ai ON ai.booking_id = b.id
		WHERE b.public_token = $1 AND ai.timing = 'after_payment'
	`
	var response PublicBookingAgreementResponse
	var agreementID uuid.UUID
	var clientID uuid.UUID
	var ciphertext secure.Ciphertext
	var deliveryRevision int
	var paymentStatus string
	if err := r.db.QueryRow(ctx, query, strings.TrimSpace(bookingToken)).Scan(
		&agreementID, &clientID, &response.Title, &response.Status,
		&response.ConfirmationMethod, &response.SentToEmail, &ciphertext.Data,
		&ciphertext.Nonce, &ciphertext.KeyVersion, &deliveryRevision, &paymentStatus,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicBookingAgreementResponse{}, ErrNotFound
		}
		return PublicBookingAgreementResponse{}, fmt.Errorf("load booking agreement: %w", err)
	}
	if !initialBookingObligationSatisfied(paymentStatus) {
		return PublicBookingAgreementResponse{}, fmt.Errorf("agreement is available after payment is confirmed")
	}
	token, err := r.agreementTokens.Recover(clientID, agreementID, ciphertext)
	if err != nil {
		return PublicBookingAgreementResponse{}, fmt.Errorf("recover agreement link: %w", err)
	}
	response.AgreementID = agreementID.String()
	response.PublicToken = token

	if response.Status == "awaiting_customer" {
		dedupeKey := fmt.Sprintf("initial-email:%s:%d", agreementID, deliveryRevision)
		if _, err := r.db.Exec(ctx, `
			INSERT INTO agreement_jobs (id, agreement_id, kind, dedupe_key, status, run_at, created_at, updated_at)
			VALUES ($1,$2,'send_agreement_email',$3,'queued',NOW(),NOW(),NOW())
			ON CONFLICT (dedupe_key) DO NOTHING
		`, uuid.New(), agreementID, dedupeKey); err != nil {
			return PublicBookingAgreementResponse{}, fmt.Errorf("queue agreement delivery: %w", err)
		}
	}
	return response, nil
}

func (r *Repository) GetPublicAgreementByToken(ctx context.Context, token string) (PublicAgreementResponse, error) {
	token = strings.TrimSpace(token)
	if !agreementservice.ValidPublicToken(token) {
		return PublicAgreementResponse{}, ErrNotFound
	}
	response, agreementID, err := loadPublicAgreement(ctx, r.db, agreementservice.HashPublicToken(token), false)
	if err != nil {
		return PublicAgreementResponse{}, err
	}
	if response.Status == "awaiting_customer" {
		if _, err := r.db.Exec(ctx, `
			INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
			VALUES ($1,$2,'viewed','customer','viewed:first',NOW())
			ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
		`, uuid.New(), agreementID); err != nil {
			return PublicAgreementResponse{}, fmt.Errorf("record agreement view: %w", err)
		}
	}
	return response, nil
}

func (r *Repository) GetPublicAgreementPDFArtifact(ctx context.Context, token string) (string, string, error) {
	token = strings.TrimSpace(token)
	if !agreementservice.ValidPublicToken(token) {
		return "", "", ErrNotFound
	}
	var status, key string
	if err := r.db.QueryRow(ctx, `
		SELECT pdf_status, pdf_r2_key
		FROM agreement_instances
		WHERE public_token_hash = $1 AND status = 'completed'
	`, agreementservice.HashPublicToken(token)).Scan(&status, &key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", fmt.Errorf("load public agreement PDF: %w", err)
	}
	return status, key, nil
}

func (r *Repository) AcceptPublicAgreementByToken(
	ctx context.Context,
	token string,
	input PublicAgreementAcceptInput,
) (PublicAgreementResponse, error) {
	token = strings.TrimSpace(token)
	if !agreementservice.ValidPublicToken(token) {
		return PublicAgreementResponse{}, ErrNotFound
	}
	if !input.Accepted {
		return PublicAgreementResponse{}, fmt.Errorf("agreement must be accepted")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PublicAgreementResponse{}, fmt.Errorf("begin agreement completion: %w", err)
	}
	defer tx.Rollback(ctx)

	response, agreementID, err := loadPublicAgreement(ctx, tx, agreementservice.HashPublicToken(token), true)
	if err != nil {
		return PublicAgreementResponse{}, err
	}
	if response.Status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return PublicAgreementResponse{}, fmt.Errorf("commit completed agreement lookup: %w", err)
		}
		return response, nil
	}
	if response.Status != "awaiting_customer" {
		return PublicAgreementResponse{}, fmt.Errorf("agreement cannot be completed in its current state")
	}

	evidence := bookingAcceptanceEvidence{method: response.ConfirmationMethod, accepted: true}
	if response.ConfirmationMethod == "signature" {
		evidence.signer = strings.TrimSpace(input.FullName)
		if evidence.signer == "" {
			return PublicAgreementResponse{}, fmt.Errorf("full_name is required")
		}
		normalized, err := signature.NormalizeDataURL(input.SignatureDataURL)
		if err != nil {
			return PublicAgreementResponse{}, err
		}
		evidence.signature = &normalized
	} else if response.ConfirmationMethod != "confirmation" {
		return PublicAgreementResponse{}, fmt.Errorf("agreement confirmation method is invalid")
	}
	if err := insertAgreementAcceptance(ctx, tx, agreementID, response.ResolvedTermsHash, evidence); err != nil {
		return PublicAgreementResponse{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_instances
		SET status = 'completed', completed_at = NOW(), pdf_status = 'queued', updated_at = NOW()
		WHERE id = $1 AND status = 'awaiting_customer'
	`, agreementID); err != nil {
		return PublicAgreementResponse{}, fmt.Errorf("complete agreement: %w", err)
	}
	bookingStatus := "accepted"
	if response.ConfirmationMethod == "signature" {
		bookingStatus = "signed"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET agreement_status = $2, updated_at = NOW()
		WHERE id = (SELECT booking_id FROM agreement_instances WHERE id = $1)
	`, agreementID, bookingStatus); err != nil {
		return PublicAgreementResponse{}, fmt.Errorf("update booking agreement status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,'completed','customer','completed',NOW())
		ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
	`, uuid.New(), agreementID); err != nil {
		return PublicAgreementResponse{}, fmt.Errorf("record agreement completion: %w", err)
	}
	for _, job := range []struct {
		kind string
		key  string
	}{
		{kind: "render_completed_pdf", key: "render:" + agreementID.String() + ":" + response.ResolvedTermsHash},
		{kind: "send_completed_email", key: "completed-email:" + agreementID.String() + ":" + response.ResolvedTermsHash},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agreement_jobs (id, agreement_id, kind, dedupe_key, status, run_at, created_at, updated_at)
			VALUES ($1,$2,$3,$4,'queued',NOW(),NOW(),NOW())
			ON CONFLICT (dedupe_key) DO NOTHING
		`, uuid.New(), agreementID, job.kind, job.key); err != nil {
			return PublicAgreementResponse{}, fmt.Errorf("queue agreement completion work: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PublicAgreementResponse{}, fmt.Errorf("commit agreement completion: %w", err)
	}
	return r.GetPublicAgreementByToken(ctx, token)
}

type agreementQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadPublicAgreement(ctx context.Context, q agreementQueryer, tokenHash []byte, forUpdate bool) (PublicAgreementResponse, uuid.UUID, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE OF ai"
	}
	query := `
		SELECT
			ai.id, ai.title_snapshot, ai.status, ai.confirmation_method, ai.timing,
			ai.rendered_html_snapshot, ai.resolved_terms_hash,
			COALESCE(c.full_name, ''), COALESCE(cp.business_name, ''),
			ai.completed_at, ai.expires_at, COALESCE(b.payment_status, ''),
			COALESCE(aa.signer_name, ''), COALESCE(aa.signature_sha256, ''), aa.accepted_at,
			ai.pdf_status
		FROM agreement_instances ai
		LEFT JOIN customers c ON c.id = ai.customer_id
		INNER JOIN client_profiles cp ON cp.client_id = ai.client_id
		LEFT JOIN bookings b ON b.id = ai.booking_id
		LEFT JOIN agreement_acceptances aa ON aa.agreement_id = ai.id
		WHERE ai.public_token_hash = $1
	` + lockClause
	var response PublicAgreementResponse
	var agreementID uuid.UUID
	var expiresAt *time.Time
	var paymentStatus string
	var timing string
	if err := q.QueryRow(ctx, query, tokenHash).Scan(
		&agreementID, &response.Title, &response.Status, &response.ConfirmationMethod, &timing,
		&response.RenderedHTML, &response.ResolvedTermsHash, &response.CustomerName,
		&response.BusinessName, &response.CompletedAt, &expiresAt, &paymentStatus,
		&response.SignerName, &response.SignatureSHA256, &response.AcceptedAt, &response.PDFStatus,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicAgreementResponse{}, uuid.Nil, ErrNotFound
		}
		return PublicAgreementResponse{}, uuid.Nil, fmt.Errorf("load public agreement: %w", err)
	}
	if expiresAt != nil && !expiresAt.After(time.Now().UTC()) && response.Status != "completed" {
		return PublicAgreementResponse{}, uuid.Nil, fmt.Errorf("agreement link has expired")
	}
	if response.Status == "awaiting_customer" && timing == "after_payment" && !initialBookingObligationSatisfied(paymentStatus) {
		return PublicAgreementResponse{}, uuid.Nil, fmt.Errorf("agreement is available after payment is confirmed")
	}
	response.ID = agreementID.String()
	response.SignaturePresent = response.SignatureSHA256 != ""
	return response, agreementID, nil
}
