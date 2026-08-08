package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ListManagedAgreements(ctx context.Context, clientID uuid.UUID, status, search string) ([]ManagedAgreementListItem, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != "draft" && status != "awaiting_customer" && status != "completed" && status != "expired" && status != "cancelled" {
		return nil, fmt.Errorf("invalid agreement status")
	}
	const query = `
		SELECT
			ai.id, COALESCE(ai.booking_id::text, ''), COALESCE(ai.customer_id::text, ''),
			COALESCE(c.full_name, ''), ai.title_snapshot, ai.status, ai.timing,
			ai.confirmation_method, ai.sent_to_email, ai.pdf_status,
			(aa.agreement_id IS NOT NULL), COALESCE(aa.signature_sha256, ''),
			ai.completed_at, ai.created_at, ai.updated_at
		FROM agreement_instances ai
		LEFT JOIN customers c ON c.id = ai.customer_id
		LEFT JOIN agreement_acceptances aa ON aa.agreement_id = ai.id
		WHERE ai.client_id = $1
		  AND ($2 = '' OR ai.status = $2)
		  AND (
			$3 = '' OR ai.title_snapshot ILIKE '%' || $3 || '%'
			OR COALESCE(c.full_name, '') ILIKE '%' || $3 || '%'
			OR ai.sent_to_email ILIKE '%' || $3 || '%'
		  )
		ORDER BY ai.created_at DESC, ai.id DESC
		LIMIT 100
	`
	rows, err := r.db.Query(ctx, query, clientID, status, strings.TrimSpace(search))
	if err != nil {
		return nil, fmt.Errorf("list agreements: %w", err)
	}
	defer rows.Close()
	items := make([]ManagedAgreementListItem, 0)
	for rows.Next() {
		var item ManagedAgreementListItem
		if err := rows.Scan(
			&item.ID, &item.BookingID, &item.CustomerID, &item.CustomerName,
			&item.Title, &item.Status, &item.Timing, &item.ConfirmationMethod,
			&item.SentToEmail, &item.PDFStatus, &item.Accepted,
			&item.SignatureSHA256, &item.CompletedAt, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agreement: %w", err)
		}
		item.SignaturePresent = item.SignatureSHA256 != ""
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agreements: %w", err)
	}
	return items, nil
}

func (r *Repository) GetManagedAgreementDeliveryToken(ctx context.Context, clientID, agreementID uuid.UUID) (string, error) {
	if r.agreementTokens == nil {
		return "", fmt.Errorf("agreement token encryption is not configured")
	}
	var ciphertext secure.Ciphertext
	if err := r.db.QueryRow(ctx, `
		SELECT public_token_ciphertext, public_token_nonce, public_token_key_version
		FROM agreement_instances
		WHERE client_id = $1 AND id = $2 AND status IN ('awaiting_customer', 'completed')
	`, clientID, agreementID).Scan(&ciphertext.Data, &ciphertext.Nonce, &ciphertext.KeyVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("load agreement delivery token: %w", err)
	}
	token, err := r.agreementTokens.Recover(clientID, agreementID, ciphertext)
	if err != nil {
		return "", fmt.Errorf("recover agreement delivery token: %w", err)
	}
	return token, nil
}

func (r *Repository) ResendManagedAgreement(ctx context.Context, clientID, agreementID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agreement resend: %w", err)
	}
	defer tx.Rollback(ctx)
	var revision int
	var email string
	if err := tx.QueryRow(ctx, `
		SELECT delivery_revision, sent_to_email
		FROM agreement_instances
		WHERE client_id = $1 AND id = $2 AND status = 'awaiting_customer'
		FOR UPDATE
	`, clientID, agreementID).Scan(&revision, &email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock agreement for resend: %w", err)
	}
	if strings.TrimSpace(email) == "" {
		return fmt.Errorf("agreement customer email is unavailable")
	}
	revision++
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_instances SET delivery_revision = $3, updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`, clientID, agreementID, revision); err != nil {
		return fmt.Errorf("update agreement delivery revision: %w", err)
	}
	key := fmt.Sprintf("resend-email:%s:%d", agreementID, revision)
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_jobs (id, agreement_id, kind, dedupe_key, status, run_at, created_at, updated_at)
		VALUES ($1,$2,'send_agreement_email',$3,'queued',NOW(),NOW(),NOW())
	`, uuid.New(), agreementID, key); err != nil {
		return fmt.Errorf("queue agreement resend: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) ChangeManagedAgreementStatus(ctx context.Context, clientID, agreementID uuid.UUID, target string) error {
	if target != "cancelled" && target != "expired" {
		return fmt.Errorf("invalid agreement status transition")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agreement status change: %w", err)
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `
		UPDATE agreement_instances
		SET status = $3, updated_at = NOW()
		WHERE client_id = $1 AND id = $2 AND status IN ('draft', 'awaiting_customer')
	`, clientID, agreementID, target)
	if err != nil {
		return fmt.Errorf("change agreement status: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_jobs
		SET status = 'failed', lease_owner = '', lease_expires_at = NULL,
		    error_code = 'agreement_inactive', error_message = '', updated_at = NOW()
		WHERE agreement_id = $1 AND kind = 'send_agreement_email'
		  AND status IN ('queued', 'processing')
	`, agreementID); err != nil {
		return fmt.Errorf("stop agreement delivery jobs: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,$3,'business',$3,NOW())
	`, uuid.New(), agreementID, target); err != nil {
		return fmt.Errorf("record agreement status change: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) RetryManagedAgreementProcessing(ctx context.Context, clientID, agreementID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin agreement processing retry: %w", err)
	}
	defer tx.Rollback(ctx)
	var pdfStatus string
	if err := tx.QueryRow(ctx, `
		SELECT pdf_status FROM agreement_instances
		WHERE client_id=$1 AND id=$2 AND status='completed'
		FOR UPDATE
	`, clientID, agreementID).Scan(&pdfStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock agreement processing retry: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE agreement_jobs
		SET status = 'queued', attempt_count = 0, run_at = NOW(), lease_owner = '',
		    lease_expires_at = NULL, error_code = '', error_message = '', completed_at = NULL,
		    updated_at = NOW()
		WHERE agreement_id = $1
		  AND kind IN ('render_completed_pdf', 'send_completed_email')
		  AND status = 'failed'
	`, agreementID)
	if err != nil {
		return fmt.Errorf("retry agreement processing jobs: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrNotFound
	}
	if pdfStatus == "failed" {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_instances
			SET pdf_status='queued', pdf_error_code='', updated_at=NOW()
			WHERE id=$1
		`, agreementID); err != nil {
			return fmt.Errorf("queue agreement PDF retry: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) GetManagedAgreementArtifact(ctx context.Context, clientID, agreementID uuid.UUID) (string, string, error) {
	var status, key string
	if err := r.db.QueryRow(ctx, `
		SELECT pdf_status, pdf_r2_key FROM agreement_instances WHERE client_id = $1 AND id = $2
	`, clientID, agreementID).Scan(&status, &key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrNotFound
		}
		return "", "", err
	}
	return status, key, nil
}

func (r *Repository) GetManagedAgreementSignature(ctx context.Context, clientID, agreementID uuid.UUID) ([]byte, error) {
	var content []byte
	if err := r.db.QueryRow(ctx, `
		SELECT aa.signature_png
		FROM agreement_acceptances aa
		INNER JOIN agreement_instances ai ON ai.id = aa.agreement_id
		WHERE ai.client_id = $1 AND ai.id = $2 AND aa.method = 'signature'
	`, clientID, agreementID).Scan(&content); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load agreement signature: %w", err)
	}
	return content, nil
}

func (r *Repository) GetManagedAgreement(ctx context.Context, clientID, agreementID uuid.UUID) (ManagedAgreementDetailsResponse, error) {
	const query = `
		SELECT
			ai.id, COALESCE(ai.booking_id::text, ''), COALESCE(ai.customer_id::text, ''),
			COALESCE(c.full_name, ''), ai.title_snapshot, ai.status, ai.timing,
			ai.confirmation_method, ai.sent_to_email, ai.rendered_html_snapshot,
			ai.resolved_terms_hash, ai.pdf_status, ai.pdf_sha256, ai.pdf_error_code,
			COALESCE(aa.signer_name, ''), COALESCE(aa.signature_sha256, ''),
			aa.accepted_at, ai.completed_at, ai.expires_at,
			EXISTS (
				SELECT 1 FROM agreement_jobs j
				WHERE j.agreement_id=ai.id
				  AND j.kind IN ('render_completed_pdf','send_completed_email')
				  AND j.status='failed'
			),
			ai.created_at, ai.updated_at
		FROM agreement_instances ai
		LEFT JOIN customers c ON c.id = ai.customer_id
		LEFT JOIN agreement_acceptances aa ON aa.agreement_id = ai.id
		WHERE ai.client_id = $1 AND ai.id = $2
	`
	var response ManagedAgreementDetailsResponse
	if err := r.db.QueryRow(ctx, query, clientID, agreementID).Scan(
		&response.ID, &response.BookingID, &response.CustomerID, &response.CustomerName,
		&response.Title, &response.Status, &response.Timing, &response.ConfirmationMethod,
		&response.SentToEmail, &response.RenderedHTML, &response.ResolvedTermsHash,
		&response.PDFStatus, &response.PDFSHA256, &response.PDFErrorCode,
		&response.SignerName, &response.SignatureSHA256, &response.AcceptedAt,
		&response.CompletedAt, &response.ExpiresAt, &response.ProcessingFailed,
		&response.CreatedAt, &response.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedAgreementDetailsResponse{}, ErrNotFound
		}
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("get agreement: %w", err)
	}
	response.Accepted = response.AcceptedAt != nil
	response.SignaturePresent = response.SignatureSHA256 != ""

	eventRows, err := r.db.Query(ctx, `
		SELECT event_type, actor_type, occurred_at
		FROM agreement_events
		WHERE agreement_id = $1
		ORDER BY occurred_at ASC, id ASC
	`, agreementID)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("list agreement events: %w", err)
	}
	defer eventRows.Close()
	response.Events = make([]ManagedAgreementEvent, 0)
	for eventRows.Next() {
		var event ManagedAgreementEvent
		if err := eventRows.Scan(&event.Type, &event.ActorType, &event.OccurredAt); err != nil {
			return ManagedAgreementDetailsResponse{}, fmt.Errorf("scan agreement event: %w", err)
		}
		response.Events = append(response.Events, event)
	}
	if err := eventRows.Err(); err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("iterate agreement events: %w", err)
	}
	return response, nil
}

type ManagedAgreementListItem struct {
	ID                 string     `json:"id"`
	BookingID          string     `json:"booking_id,omitempty"`
	CustomerID         string     `json:"customer_id,omitempty"`
	CustomerName       string     `json:"customer_name,omitempty"`
	Title              string     `json:"title"`
	Status             string     `json:"status"`
	Timing             string     `json:"timing"`
	ConfirmationMethod string     `json:"confirmation_method"`
	SentToEmail        string     `json:"sent_to_email,omitempty"`
	PDFStatus          string     `json:"pdf_status"`
	Accepted           bool       `json:"accepted"`
	SignaturePresent   bool       `json:"signature_present"`
	SignatureSHA256    string     `json:"signature_sha256,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type ManagedAgreementDetailsResponse struct {
	ManagedAgreementListItem
	RenderedHTML      string                  `json:"rendered_html"`
	ResolvedTermsHash string                  `json:"resolved_terms_hash"`
	PDFSHA256         string                  `json:"pdf_sha256,omitempty"`
	PDFErrorCode      string                  `json:"pdf_error_code,omitempty"`
	SignerName        string                  `json:"signer_name,omitempty"`
	AcceptedAt        *time.Time              `json:"accepted_at,omitempty"`
	ExpiresAt         *time.Time              `json:"expires_at,omitempty"`
	ProcessingFailed  bool                    `json:"processing_failed"`
	Events            []ManagedAgreementEvent `json:"events"`
}

type ManagedAgreementEvent struct {
	Type       string    `json:"type"`
	ActorType  string    `json:"actor_type"`
	OccurredAt time.Time `json:"occurred_at"`
}
