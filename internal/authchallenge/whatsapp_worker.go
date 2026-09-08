package authchallenge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const maxWhatsAppStatusAttempts = 10

type whatsAppSender interface {
	SendTemplate(context.Context, whatsapp.TemplateMessage) (whatsapp.SendResult, error)
}

type WhatsAppWorker struct {
	service         *Service
	sender          whatsAppSender
	logger          *slog.Logger
	wake            <-chan struct{}
	metrics         WorkerMetrics
	workerID        string
	concurrency     int
	deliveryTimeout time.Duration
	leaseDuration   time.Duration
}

func NewWhatsAppWorker(
	service *Service,
	sender whatsAppSender,
	logger *slog.Logger,
	wake <-chan struct{},
	metrics WorkerMetrics,
	concurrency int,
	timeout time.Duration,
) *WhatsAppWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = defaultWorkerConcurrency
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	lease := timeout + 30*time.Second
	if lease < claimLease {
		lease = claimLease
	}
	return &WhatsAppWorker{
		service: service, sender: sender, logger: logger, wake: wake, metrics: metrics,
		workerID: "auth-code-whatsapp-" + uuid.NewString(), concurrency: concurrency,
		deliveryTimeout: timeout, leaseDuration: lease,
	}
}

func (worker *WhatsAppWorker) Start(ctx context.Context) {
	if worker == nil || worker.service == nil || !worker.service.whatsAppEnabled || worker.sender == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-timer.C:
		}
		moreWork := worker.processStatusCycle(ctx)
		moreWork = worker.processDeliveryCycle(ctx) || moreWork
		if moreWork {
			timer.Reset(0)
		} else {
			timer.Reset(10 * time.Second)
		}
	}
}

func (worker *WhatsAppWorker) processDeliveryCycle(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	jobs, err := worker.service.claimDeliveryJobs(
		ctx, ChannelWhatsApp, worker.workerID, worker.concurrency, worker.leaseDuration,
	)
	if err != nil {
		worker.logger.Error("claim auth code WhatsApp jobs", "error", err)
		return false
	}
	worker.processDeliveryBatch(ctx, jobs)
	return len(jobs) == worker.concurrency
}

func (worker *WhatsAppWorker) processDeliveryBatch(ctx context.Context, jobs []DeliveryJob) {
	var group sync.WaitGroup
	group.Add(len(jobs))
	for _, job := range jobs {
		go func(job DeliveryJob) {
			defer group.Done()
			worker.processDelivery(ctx, job)
		}(job)
	}
	group.Wait()
}

func (worker *WhatsAppWorker) processDelivery(ctx context.Context, job DeliveryJob) {
	worker.observeClaim(job)
	payload, err := worker.service.decryptPayload(job)
	if err != nil || job.TemplateKey != string(whatsapp.TemplateAuthCode) {
		worker.finalizeFailure(ctx, job, mailer.DispositionPermanent, "payload_or_template_invalid", 0)
		return
	}
	now := worker.service.now()
	if !job.Deadline.After(now) {
		worker.finalizeFailure(ctx, job, mailer.DispositionRetryable, "delivery_deadline_expired", 0)
		return
	}
	deadline := now.Add(worker.deliveryTimeout)
	if job.Deadline.Before(deadline) {
		deadline = job.Deadline
	}
	deliveryContext, cancel := context.WithDeadline(ctx, deadline)
	result, err := worker.sender.SendTemplate(deliveryContext, whatsapp.TemplateMessage{
		To:  payload.Destination,
		Key: whatsapp.TemplateAuthCode,
		Values: whatsapp.TemplateValues{
			Body:               map[string]string{"code_instruction": "Use the code " + payload.Code},
			OpaqueCallbackData: job.ID.String(),
		},
	})
	cancel()
	finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer finalizeCancel()
	if err == nil {
		outcome, finalizeErr := worker.service.markDeliveryAccepted(finalizeContext, job, result.MessageID)
		if finalizeErr != nil {
			worker.logger.Error("record accepted auth code WhatsApp", "job_id", job.ID, "error", finalizeErr)
			return
		}
		worker.observeOutcome(job, outcome)
		return
	}
	disposition, code, retryAfter := classifyWhatsAppSendError(err)
	worker.finalizeFailure(finalizeContext, job, disposition, code, retryAfter)
}

func (worker *WhatsAppWorker) finalizeFailure(
	parent context.Context,
	job DeliveryJob,
	disposition mailer.TransportDisposition,
	code string,
	retryAfter time.Duration,
) {
	ctx := parent
	cancel := func() {}
	if _, ok := parent.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(context.WithoutCancel(parent), finalizationTimeout)
	}
	defer cancel()
	outcome, err := worker.service.recordDeliveryFailure(ctx, job, disposition, code, retryAfter)
	if err != nil {
		worker.logger.Error("record auth code WhatsApp failure", "job_id", job.ID, "error", err)
		return
	}
	worker.observeOutcome(job, outcome)
}

func classifyWhatsAppSendError(err error) (mailer.TransportDisposition, string, time.Duration) {
	var requestError *whatsapp.RequestError
	if errors.As(err, &requestError) {
		return mailer.DispositionPermanent, "meta_request_invalid", 0
	}
	var transportError *whatsapp.TransportError
	if errors.As(err, &transportError) {
		if transportError.Ambiguous {
			return mailer.DispositionAmbiguous, "meta_outcome_unknown", 0
		}
		return mailer.DispositionRetryable, "meta_transport", 0
	}
	var graphError *whatsapp.GraphError
	if errors.As(err, &graphError) {
		code := fmt.Sprintf("meta_%s_%d", graphError.Class, graphError.Code)
		if graphError.Class == whatsapp.ErrorClassTransient || graphError.Class == whatsapp.ErrorClassRateLimit {
			return mailer.DispositionRetryable, code, graphError.RetryAfter
		}
		return mailer.DispositionPermanent, code, 0
	}
	return mailer.DispositionAmbiguous, "meta_outcome_unknown", 0
}

type authWhatsAppStatusReceipt struct {
	ID                uuid.UUID
	WAMID             string
	Status            string
	ProviderTimestamp time.Time
	ProviderErrorCode string
	CorrelationID     uuid.UUID
	AttemptCount      int
	LeaseOwner        string
	LeaseToken        uuid.UUID
}

func (worker *WhatsAppWorker) processStatusCycle(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	receipts, err := worker.service.claimWhatsAppStatusReceipts(
		ctx, worker.workerID, worker.concurrency, worker.leaseDuration,
	)
	if err != nil {
		worker.logger.Error("claim auth WhatsApp status receipts", "error", err)
		return false
	}
	var group sync.WaitGroup
	group.Add(len(receipts))
	for _, receipt := range receipts {
		go func(receipt authWhatsAppStatusReceipt) {
			defer group.Done()
			finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
			defer cancel()
			if err := worker.service.applyWhatsAppStatusReceipt(finalizeContext, receipt); err != nil {
				worker.logger.Warn("apply auth WhatsApp status receipt", "receipt_id", receipt.ID, "error", err)
			}
		}(receipt)
	}
	group.Wait()
	return len(receipts) == worker.concurrency
}

func (s *Service) claimWhatsAppStatusReceipts(
	ctx context.Context,
	owner string,
	limit int,
	lease time.Duration,
) ([]authWhatsAppStatusReceipt, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("auth WhatsApp status lease owner is required")
	}
	if limit < 1 || limit > 100 {
		limit = defaultWorkerConcurrency
	}
	if lease <= 0 {
		lease = claimLease
	}
	if err := whatsapp.RouteStatusReceipts(ctx, s.db); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `
		WITH exhausted_candidates AS (
			SELECT receipt.id FROM meta_whatsapp_webhook_receipts receipt
			WHERE receipt.event_kind='status' AND receipt.processing_owner='auth'
			  AND receipt.processing_status IN ('pending','retry','processing')
			  AND (CASE WHEN receipt.processing_status='processing' THEN receipt.lease_expires_at ELSE receipt.available_at END)<=NOW()
			  AND receipt.attempt_count >= $4
			  AND EXISTS (
				SELECT 1 FROM auth_code_delivery_jobs job
				WHERE job.channel='whatsapp' AND job.status<>'processing'
				  AND (job.provider_message_id=receipt.wamid OR job.id::text=receipt.correlation_id)
			  )
			ORDER BY (CASE WHEN receipt.processing_status='processing' THEN receipt.lease_expires_at ELSE receipt.available_at END),receipt.created_at,receipt.id
			FOR UPDATE SKIP LOCKED LIMIT $1
		), exhausted AS (
			UPDATE meta_whatsapp_webhook_receipts receipt
			SET processing_status='dead_letter',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				last_error_code='auth_status_retry_exhausted',processed_at=NOW()
			FROM exhausted_candidates WHERE receipt.id=exhausted_candidates.id
		), claimable AS (
			SELECT receipt.id FROM meta_whatsapp_webhook_receipts receipt
			WHERE receipt.event_kind='status' AND receipt.processing_owner='auth'
			  AND receipt.processing_status IN ('pending','retry','processing')
			  AND (CASE WHEN receipt.processing_status='processing' THEN receipt.lease_expires_at ELSE receipt.available_at END)<=NOW()
			  AND receipt.attempt_count < $4
			  AND EXISTS (
				SELECT 1 FROM auth_code_delivery_jobs job
				WHERE job.channel='whatsapp' AND job.status<>'processing'
				  AND (job.provider_message_id=receipt.wamid OR job.id::text=receipt.correlation_id)
			  )
			ORDER BY (CASE WHEN receipt.processing_status='processing' THEN receipt.lease_expires_at ELSE receipt.available_at END),receipt.created_at,receipt.id
			FOR UPDATE SKIP LOCKED LIMIT $1
		), claimed AS (
			UPDATE meta_whatsapp_webhook_receipts receipt
			SET processing_status='processing',attempt_count=attempt_count+1,lease_owner=$2,
				lease_token=gen_random_uuid(),lease_expires_at=NOW()+($3::bigint*INTERVAL '1 millisecond'),
				last_error_code=''
			FROM claimable WHERE receipt.id=claimable.id
			RETURNING receipt.id,receipt.wamid,receipt.message_status,receipt.provider_timestamp,
				receipt.provider_error_code,receipt.correlation_id,receipt.attempt_count,
				receipt.lease_owner,receipt.lease_token
		)
		SELECT id,wamid,message_status,provider_timestamp,provider_error_code,correlation_id,
			attempt_count,lease_owner,lease_token FROM claimed
	`, limit, owner, lease.Milliseconds(), maxWhatsAppStatusAttempts)
	if err != nil {
		return nil, fmt.Errorf("claim auth WhatsApp status receipts: %w", err)
	}
	defer rows.Close()
	receipts := make([]authWhatsAppStatusReceipt, 0, limit)
	for rows.Next() {
		var receipt authWhatsAppStatusReceipt
		var correlation string
		if err := rows.Scan(
			&receipt.ID, &receipt.WAMID, &receipt.Status, &receipt.ProviderTimestamp,
			&receipt.ProviderErrorCode, &correlation, &receipt.AttemptCount,
			&receipt.LeaseOwner, &receipt.LeaseToken,
		); err != nil {
			return nil, err
		}
		if id, err := uuid.Parse(correlation); err == nil {
			receipt.CorrelationID = id
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

func (s *Service) applyWhatsAppStatusReceipt(ctx context.Context, receipt authWhatsAppStatusReceipt) error {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var locked bool
	if err := tx.QueryRow(ctx, `
		SELECT TRUE FROM meta_whatsapp_webhook_receipts
		WHERE id=$1 AND processing_status='processing' AND lease_owner=$2 AND lease_token=$3
		FOR UPDATE
	`, receipt.ID, receipt.LeaseOwner, receipt.LeaseToken).Scan(&locked); err != nil {
		return err
	}
	var jobID, challengeID uuid.UUID
	var realm, currentStatus string
	var deadline time.Time
	var acceptedAt, sentAt, deliveredAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT id,COALESCE(provider_challenge_id,marketplace_challenge_id),realm,status,
			delivery_deadline,accepted_at,sent_at,delivered_at
		FROM auth_code_delivery_jobs
		WHERE channel='whatsapp' AND status<>'processing'
		  AND (provider_message_id=$1 OR id=$2)
		ORDER BY (provider_message_id=$1) DESC LIMIT 1 FOR UPDATE
	`, receipt.WAMID, receipt.CorrelationID).Scan(
		&jobID, &challengeID, &realm, &currentStatus, &deadline,
		&acceptedAt, &sentAt, &deliveredAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := completeAuthWhatsAppReceipt(ctx, tx, receipt, "auth_delivery_not_visible"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	incomingStatus := receipt.Status
	if incomingStatus == "read" {
		incomingStatus = "delivered"
	}
	if incomingStatus == "deleted" {
		incomingStatus = "failed"
	}
	var currentStatusAt *time.Time
	switch currentStatus {
	case "delivered":
		currentStatusAt = deliveredAt
	case "sent":
		currentStatusAt = sentAt
	}
	apply := shouldApplyAuthWhatsAppStatus(currentStatus, currentStatusAt, incomingStatus, receipt.ProviderTimestamp)
	if apply && (incomingStatus == "sent" || incomingStatus == "delivered") && acceptedAt == nil {
		acceptedTime := receipt.ProviderTimestamp
		if acceptedTime.After(s.now()) {
			acceptedTime = s.now()
		}
		if !deadline.After(acceptedTime) {
			incomingStatus = "expired"
		} else {
			table, _, tableErr := challengeTable(realm)
			if tableErr != nil {
				return tableErr
			}
			query := fmt.Sprintf(`UPDATE %s SET delivery_accepted_at=$2::timestamptz,verify_expires_at=$2::timestamptz+INTERVAL '10 minutes' WHERE id=$1 AND consumed_at IS NULL`, table)
			if _, err := tx.Exec(ctx, query, challengeID, acceptedTime); err != nil {
				return err
			}
			acceptedAt = &acceptedTime
		}
	}
	if apply {
		errorCode := ""
		if incomingStatus == "failed" {
			errorCode = "meta_delivery_failed"
			if strings.TrimSpace(receipt.ProviderErrorCode) != "" {
				errorCode = "meta_failed_" + strings.TrimSpace(receipt.ProviderErrorCode)
			}
		}
		completedAt := any(nil)
		if incomingStatus == "delivered" || incomingStatus == "failed" || incomingStatus == "expired" {
			completedAt = receipt.ProviderTimestamp
		}
		_, err = tx.Exec(ctx, `
			UPDATE auth_code_delivery_jobs SET status=$2::text,
				provider_message_id=CASE WHEN provider_message_id='' THEN $3 ELSE provider_message_id END,
				payload_ciphertext=NULL,payload_nonce=NULL,payload_key_version=NULL,
				error_code=$4,accepted_at=COALESCE(accepted_at,$5::timestamptz),
				sent_at=CASE WHEN $2::text IN ('sent','delivered') THEN COALESCE(sent_at,$6::timestamptz) ELSE sent_at END,
				delivered_at=CASE WHEN $2::text='delivered' THEN COALESCE(delivered_at,$6::timestamptz) ELSE delivered_at END,
				completed_at=COALESCE($7::timestamptz,completed_at),updated_at=NOW()
			WHERE id=$1
		`, jobID, incomingStatus, receipt.WAMID, boundedErrorCode(errorCode), acceptedAt,
			receipt.ProviderTimestamp, completedAt)
		if err != nil {
			return err
		}
	}
	if err := completeAuthWhatsAppReceipt(ctx, tx, receipt, ""); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func shouldApplyAuthWhatsAppStatus(current string, currentAt *time.Time, incoming string, incomingAt time.Time) bool {
	if current == "failed" || current == "expired" {
		return false
	}
	if currentAt != nil && incomingAt.Before(*currentAt) {
		return false
	}
	if incoming == "failed" {
		return current == "accepted" || current == "sent" || current == "delivered" || current == "unknown"
	}
	rank := func(status string) int {
		switch status {
		case "unknown":
			return 0
		case "accepted":
			return 1
		case "sent":
			return 2
		case "delivered":
			return 3
		default:
			return -1
		}
	}
	return rank(incoming) > rank(current)
}

func completeAuthWhatsAppReceipt(ctx context.Context, tx pgx.Tx, receipt authWhatsAppStatusReceipt, code string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE meta_whatsapp_webhook_receipts
		SET processing_status='completed',lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			last_error_code=$4,processed_at=NOW()
		WHERE id=$1 AND processing_status='processing' AND lease_owner=$2 AND lease_token=$3
	`, receipt.ID, receipt.LeaseOwner, receipt.LeaseToken, boundedErrorCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("auth WhatsApp status receipt lease was lost")
	}
	return nil
}

func (worker *WhatsAppWorker) observeClaim(job DeliveryJob) {
	if worker.metrics == nil {
		return
	}
	latency := worker.service.now().Sub(job.NextAttemptAt)
	if latency < 0 {
		latency = 0
	}
	worker.metrics.ObserveAuthCodeDeliveryClaim(job.Realm, job.Channel, latency)
}

func (worker *WhatsAppWorker) observeOutcome(job DeliveryJob, outcome string) {
	if worker.metrics != nil {
		worker.metrics.ObserveAuthCodeDeliveryOutcome(job.Realm, job.Channel, outcome)
	}
}
