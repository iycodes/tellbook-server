package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	agreementrender "booking/go-server/internal/agreements/render"
	agreementservice "booking/go-server/internal/agreements/service"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/secure"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	agreementJobPollInterval = 2 * time.Second
	agreementJobLease        = 2 * time.Minute
)

type CompletedAgreementStore interface {
	PrivateBucketName() string
	Upload(context.Context, []byte, string, string, ...string) (string, error)
}

type LifecycleWorker struct {
	db            *pgxpool.Pool
	tokens        *agreementservice.PublicTokenManager
	mailer        mailer.Sender
	storage       CompletedAgreementStore
	publicBaseURL string
	logger        *slog.Logger
	workerID      string
}

func NewLifecycleWorker(
	db *pgxpool.Pool,
	tokens *agreementservice.PublicTokenManager,
	mailerSender mailer.Sender,
	storage CompletedAgreementStore,
	publicBaseURL string,
	logger *slog.Logger,
) (*LifecycleWorker, error) {
	if db == nil {
		return nil, errors.New("agreement lifecycle database is required")
	}
	if tokens == nil {
		return nil, errors.New("agreement token manager is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &LifecycleWorker{
		db: db, tokens: tokens, mailer: mailerSender, storage: storage,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		logger:        logger, workerID: "agreement-" + uuid.NewString(),
	}, nil
}

func (w *LifecycleWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(agreementJobPollInterval)
	defer ticker.Stop()
	for {
		processed, err := w.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.Error("agreement lifecycle job failed", "error", err)
		}
		if ctx.Err() != nil {
			return
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *LifecycleWorker) ProcessOne(ctx context.Context) (bool, error) {
	job, err := w.claim(ctx)
	if err != nil {
		return false, err
	}
	if job.ID == uuid.Nil {
		return false, nil
	}
	if err := w.process(ctx, job); err != nil {
		if failErr := w.fail(ctx, job, err); failErr != nil {
			return true, fmt.Errorf("process agreement job: %v; record failure: %w", err, failErr)
		}
		return true, err
	}
	if _, err := w.db.Exec(ctx, `
		UPDATE agreement_jobs
		SET status = 'completed', completed_at = NOW(), lease_owner = '',
			lease_expires_at = NULL, error_code = '', error_message = '', updated_at = NOW()
		WHERE id = $1 AND status = 'processing' AND lease_owner = $2
	`, job.ID, w.workerID); err != nil {
		return true, fmt.Errorf("complete agreement job: %w", err)
	}
	return true, nil
}

type lifecycleJob struct {
	ID           uuid.UUID
	AgreementID  uuid.UUID
	Kind         string
	DedupeKey    string
	AttemptCount int
	MaxAttempts  int
}

func (w *LifecycleWorker) claim(ctx context.Context) (lifecycleJob, error) {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return lifecycleJob{}, fmt.Errorf("begin agreement job claim: %w", err)
	}
	defer tx.Rollback(ctx)
	const query = `
		SELECT id, agreement_id, kind, dedupe_key, attempt_count, max_attempts
		FROM agreement_jobs
		WHERE run_at <= NOW()
		  AND (
			status = 'queued'
			OR (status = 'processing' AND lease_expires_at < NOW())
		  )
		ORDER BY run_at ASC, created_at ASC
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`
	var job lifecycleJob
	if err := tx.QueryRow(ctx, query).Scan(
		&job.ID, &job.AgreementID, &job.Kind, &job.DedupeKey, &job.AttemptCount, &job.MaxAttempts,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if err := tx.Commit(ctx); err != nil {
				return lifecycleJob{}, fmt.Errorf("commit empty agreement job claim: %w", err)
			}
			return lifecycleJob{}, nil
		}
		return lifecycleJob{}, fmt.Errorf("select agreement job: %w", err)
	}
	job.AttemptCount++
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_jobs
		SET status = 'processing', attempt_count = $2, lease_owner = $3,
			lease_expires_at = NOW() + ($4 * INTERVAL '1 second'), updated_at = NOW()
		WHERE id = $1
	`, job.ID, job.AttemptCount, w.workerID, int(agreementJobLease/time.Second)); err != nil {
		return lifecycleJob{}, fmt.Errorf("claim agreement job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return lifecycleJob{}, fmt.Errorf("commit agreement job claim: %w", err)
	}
	return job, nil
}

func (w *LifecycleWorker) process(ctx context.Context, job lifecycleJob) error {
	switch job.Kind {
	case string(domain.AgreementJobRenderCompletedPDF):
		return w.renderCompletedPDF(ctx, job.AgreementID)
	case string(domain.AgreementJobSendAgreementEmail):
		return w.sendAgreementEmail(ctx, job.AgreementID, job.DedupeKey, false)
	case string(domain.AgreementJobSendCompletedEmail):
		return w.sendAgreementEmail(ctx, job.AgreementID, job.DedupeKey, true)
	default:
		return fmt.Errorf("unsupported agreement job kind %q", job.Kind)
	}
}

type lifecycleAgreement struct {
	ID                 uuid.UUID
	ClientID           uuid.UUID
	Title              string
	Status             string
	ConfirmationMethod string
	SentToEmail        string
	CustomerName       string
	BusinessName       string
	BookingSummaryJSON []byte
	DocumentJSON       []byte
	SchemaVersion      int
	RendererVersion    int
	ResolvedTermsHash  string
	Token              secure.Ciphertext
	SignerName         string
	SignaturePNG       []byte
	AcceptedAt         *time.Time
}

func (w *LifecycleWorker) loadAgreement(ctx context.Context, agreementID uuid.UUID) (lifecycleAgreement, error) {
	const query = `
		SELECT
			ai.id, ai.client_id, ai.title_snapshot, ai.status, ai.confirmation_method,
			ai.sent_to_email, COALESCE(c.full_name, ''), COALESCE(cp.business_name, ''),
			ai.booking_summary_snapshot, ai.resolved_document_snapshot,
			ai.schema_version_snapshot, ai.renderer_version_snapshot, ai.resolved_terms_hash,
			ai.public_token_ciphertext, ai.public_token_nonce, ai.public_token_key_version,
			COALESCE(aa.signer_name, ''), COALESCE(aa.signature_png, '\x'::bytea), aa.accepted_at
		FROM agreement_instances ai
		INNER JOIN client_profiles cp ON cp.client_id = ai.client_id
		LEFT JOIN customers c ON c.id = ai.customer_id
		LEFT JOIN agreement_acceptances aa ON aa.agreement_id = ai.id
		WHERE ai.id = $1
	`
	var agreement lifecycleAgreement
	if err := w.db.QueryRow(ctx, query, agreementID).Scan(
		&agreement.ID, &agreement.ClientID, &agreement.Title, &agreement.Status,
		&agreement.ConfirmationMethod, &agreement.SentToEmail, &agreement.CustomerName,
		&agreement.BusinessName, &agreement.BookingSummaryJSON, &agreement.DocumentJSON,
		&agreement.SchemaVersion, &agreement.RendererVersion, &agreement.ResolvedTermsHash,
		&agreement.Token.Data, &agreement.Token.Nonce, &agreement.Token.KeyVersion,
		&agreement.SignerName, &agreement.SignaturePNG, &agreement.AcceptedAt,
	); err != nil {
		return lifecycleAgreement{}, fmt.Errorf("load agreement lifecycle data: %w", err)
	}
	return agreement, nil
}

func (w *LifecycleWorker) renderCompletedPDF(ctx context.Context, agreementID uuid.UUID) error {
	agreement, err := w.loadAgreement(ctx, agreementID)
	if err != nil {
		return err
	}
	if agreement.Status != string(domain.AgreementStatusCompleted) || agreement.AcceptedAt == nil {
		return errors.New("agreement is not completed")
	}
	if w.storage == nil || strings.TrimSpace(w.storage.PrivateBucketName()) == "" {
		return errors.New("private agreement storage is not configured")
	}
	method, err := domain.ParseConfirmationMethod(agreement.ConfirmationMethod)
	if err != nil {
		return err
	}
	var summary agreementrender.BookingSummary
	if err := json.Unmarshal(agreement.BookingSummaryJSON, &summary); err != nil {
		return fmt.Errorf("decode agreement booking summary: %w", err)
	}
	var document aiapi.DocumentSchema
	if err := json.Unmarshal(agreement.DocumentJSON, &document); err != nil {
		return fmt.Errorf("decode resolved agreement document: %w", err)
	}
	pdfContent, err := agreementrender.RenderCompletedPDFVersion(agreement.RendererVersion, agreementrender.PDFInput{
		BusinessName: agreement.BusinessName, Title: agreement.Title, BookingSummary: summary,
		ResolvedDocument: document, SchemaVersion: agreement.SchemaVersion,
		RendererVersion: agreement.RendererVersion, ResolvedTermsHash: agreement.ResolvedTermsHash,
		Acceptance: agreementrender.AcceptanceEvidence{
			Method: method, SignerName: agreement.SignerName,
			SignaturePNG: agreement.SignaturePNG, AcceptedAt: *agreement.AcceptedAt,
		},
	})
	if err != nil {
		return fmt.Errorf("render completed agreement PDF: %w", err)
	}
	digest := sha256.Sum256(pdfContent)
	checksum := hex.EncodeToString(digest[:])
	objectKey := path.Join("clients", agreement.ClientID.String(), "agreements", agreement.ID.String(), "completed-"+agreement.ResolvedTermsHash+".pdf")
	if _, err := w.storage.Upload(ctx, pdfContent, objectKey, "application/pdf", w.storage.PrivateBucketName()); err != nil {
		return fmt.Errorf("upload completed agreement PDF: %w", err)
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin completed PDF update: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_instances
		SET pdf_status = 'ready', pdf_r2_key = $2, pdf_sha256 = $3,
			pdf_error_code = '', updated_at = NOW()
		WHERE id = $1
	`, agreement.ID, objectKey, checksum); err != nil {
		return fmt.Errorf("store completed PDF: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,'pdf_ready','system',$3,NOW())
		ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
	`, uuid.New(), agreement.ID, "pdf_ready:"+checksum); err != nil {
		return fmt.Errorf("record completed PDF: %w", err)
	}
	return tx.Commit(ctx)
}

func (w *LifecycleWorker) sendAgreementEmail(ctx context.Context, agreementID uuid.UUID, jobDedupeKey string, completed bool) error {
	agreement, err := w.loadAgreement(ctx, agreementID)
	if err != nil {
		return err
	}
	if completed && agreement.Status != string(domain.AgreementStatusCompleted) {
		return errors.New("completed agreement email is no longer applicable")
	}
	if !completed && agreement.Status != string(domain.AgreementStatusAwaitingCustomer) {
		return errors.New("agreement delivery is no longer applicable")
	}
	if w.mailer == nil || !w.mailer.Enabled() {
		return errors.New("agreement email delivery is not configured")
	}
	if strings.TrimSpace(agreement.SentToEmail) == "" {
		return errors.New("agreement customer email is unavailable")
	}
	token, err := w.tokens.Recover(agreement.ClientID, agreement.ID, agreement.Token)
	if err != nil {
		return fmt.Errorf("recover agreement email link: %w", err)
	}
	link := w.publicBaseURL + "/agreement/" + token
	subject := "Review your " + agreement.Title
	message := "Please review and complete your agreement.\n\nOpen agreement: " + link
	if completed {
		subject = agreement.Title + " completed"
		message = "Your agreement has been completed and recorded.\n\nView agreement: " + link
	}
	if err := w.mailer.Send(ctx, mailer.Message{
		ToEmail: agreement.SentToEmail, ToName: agreement.CustomerName,
		Subject: subject, Text: message,
		MessageID: agreementEmailMessageID(agreement.ID, jobDedupeKey),
	}); err != nil {
		return fmt.Errorf("send agreement email: %w", err)
	}
	eventType := "sent"
	dedupe := "sent:" + jobDedupeKey
	if completed {
		dedupe = "sent:" + jobDedupeKey
	} else if strings.HasPrefix(jobDedupeKey, "resend-email:") {
		eventType = "resent"
		dedupe = "resent:" + jobDedupeKey
	}
	if _, err := w.db.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,$3,'system',$4,NOW())
		ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
	`, uuid.New(), agreement.ID, eventType, dedupe); err != nil {
		return fmt.Errorf("record agreement email delivery: %w", err)
	}
	return nil
}

func agreementEmailMessageID(agreementID uuid.UUID, jobDedupeKey string) string {
	digest := sha256.Sum256([]byte(agreementID.String() + ":" + jobDedupeKey))
	return fmt.Sprintf("<agreement-%x@tellbook.local>", digest)
}

func (w *LifecycleWorker) fail(ctx context.Context, job lifecycleJob, processErr error) error {
	terminal := job.AttemptCount >= job.MaxAttempts
	status := "queued"
	runAt := time.Now().UTC().Add(agreementJobBackoff(job.AttemptCount))
	if terminal {
		status = "failed"
	}
	message := strings.TrimSpace(processErr.Error())
	if len(message) > 500 {
		message = message[:500]
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE agreement_jobs
		SET status = $3, run_at = $4, lease_owner = '', lease_expires_at = NULL,
			error_code = 'processing_failed', error_message = $5, updated_at = NOW()
		WHERE id = $1 AND lease_owner = $2
	`, job.ID, w.workerID, status, runAt, message); err != nil {
		return err
	}
	if terminal && job.Kind == string(domain.AgreementJobRenderCompletedPDF) {
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_instances SET pdf_status = 'failed', pdf_error_code = 'processing_failed', updated_at = NOW()
			WHERE id = $1
		`, job.AgreementID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, metadata, occurred_at)
			VALUES ($1,$2,'pdf_failed','system','pdf_failed',jsonb_build_object('code','processing_failed'),NOW())
			ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
		`, uuid.New(), job.AgreementID); err != nil {
			return err
		}
	}
	if terminal && (job.Kind == string(domain.AgreementJobSendAgreementEmail) || job.Kind == string(domain.AgreementJobSendCompletedEmail)) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, metadata, occurred_at)
			VALUES ($1,$2,'delivery_failed','system',$3,jsonb_build_object('code','processing_failed'),NOW())
			ON CONFLICT (agreement_id, dedupe_key) DO NOTHING
		`, uuid.New(), job.AgreementID, "delivery_failed:"+job.DedupeKey); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func agreementJobBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Duration(1<<(attempt-1)) * 15 * time.Second
	if delay > 15*time.Minute {
		return 15 * time.Minute
	}
	return delay
}
