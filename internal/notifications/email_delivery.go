package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	defaultEmailConcurrency  = 4
	maxDeliveryAttempts      = 8
	emailFinalizationTimeout = 5 * time.Second
)

type EmailWorkerMetrics interface {
	ObserveNotificationEmailClaim(template string, latency time.Duration)
	ObserveNotificationEmailOutcome(template, outcome string)
}

type EmailWorker struct {
	repository         *Repository
	sender             mailer.Sender
	logger             *slog.Logger
	wake               <-chan struct{}
	metrics            EmailWorkerMetrics
	workerID           string
	concurrency        int
	deliveryTimeout    time.Duration
	clientBaseURL      string
	marketplaceBaseURL string
}

func NewEmailWorker(
	repository *Repository,
	sender mailer.Sender,
	logger *slog.Logger,
	wake <-chan struct{},
	metrics EmailWorkerMetrics,
	concurrency int,
	deliveryTimeout time.Duration,
	clientBaseURL string,
	marketplaceBaseURL string,
) *EmailWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = defaultEmailConcurrency
	}
	if deliveryTimeout <= 0 {
		deliveryTimeout = 30 * time.Second
	}
	return &EmailWorker{
		repository: repository, sender: sender, logger: logger, wake: wake, metrics: metrics,
		workerID: "notification-email-" + uuid.NewString(), concurrency: concurrency,
		deliveryTimeout: deliveryTimeout, clientBaseURL: strings.TrimRight(clientBaseURL, "/"),
		marketplaceBaseURL: strings.TrimRight(marketplaceBaseURL, "/"),
	}
}

func (worker *EmailWorker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil || worker.sender == nil || !worker.sender.Enabled() {
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
		worker.drain(ctx)
		timer.Reset(20 * time.Second)
	}
}

func (worker *EmailWorker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		deliveries, err := worker.repository.ClaimDeliveries(
			ctx, "email", worker.workerID, defaultClaimBatch, defaultLeaseDuration,
		)
		if err != nil {
			worker.logger.Error("claim email notification deliveries", "error", err)
			return
		}
		worker.processBatch(ctx, deliveries)
		if len(deliveries) < defaultClaimBatch {
			return
		}
	}
}

func (worker *EmailWorker) processBatch(ctx context.Context, deliveries []Delivery) {
	for start := 0; start < len(deliveries); start += worker.concurrency {
		end := min(start+worker.concurrency, len(deliveries))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, delivery := range deliveries[start:end] {
			go func(delivery Delivery) {
				defer group.Done()
				worker.processOne(ctx, delivery)
			}(delivery)
		}
		group.Wait()
	}
}

func (worker *EmailWorker) processOne(ctx context.Context, claimed Delivery) {
	template := emailMetricTemplate(claimed)
	claimLatency := worker.repository.now().Sub(claimed.ScheduledFor)
	if claimLatency < 0 {
		claimLatency = 0
	}
	worker.observeClaim(template, claimLatency)
	delivery, err := worker.repository.AuthorizeDispatch(ctx, claimed)
	if errors.Is(err, ErrDispatchNotAuthorized) {
		worker.observeOutcome(template, "cancelled")
		return
	}
	if err != nil {
		finalizeContext, finalizeCancel := newEmailFinalizationContext(ctx)
		defer finalizeCancel()
		if releaseErr := worker.repository.ReleaseClaimedDelivery(finalizeContext, claimed, "dispatch_fence_failed"); releaseErr != nil {
			worker.logger.Warn("release email delivery after dispatch fence failure", "delivery_id", claimed.ID, "error", releaseErr)
		} else {
			worker.observeOutcome(template, "retry")
		}
		return
	}
	message, err := worker.repository.BuildEmailMessage(
		ctx, delivery, worker.clientBaseURL, worker.marketplaceBaseURL,
	)
	if err != nil {
		if errors.Is(err, ErrEmailNotDispatchable) {
			return
		}
		if errors.Is(err, ErrEmailDestinationMoved) || errors.Is(err, ErrEmailEligibilityChanged) {
			finalizeContext, finalizeCancel := newEmailFinalizationContext(ctx)
			defer finalizeCancel()
			code := "destination_changed"
			if errors.Is(err, ErrEmailEligibilityChanged) {
				code = "eligibility_changed_before_smtp"
			}
			if cancelErr := worker.repository.CancelAuthorizedDelivery(finalizeContext, delivery.ID, code); cancelErr != nil {
				worker.logger.Error("cancel redirected email delivery", "delivery_id", delivery.ID, "error", cancelErr)
			} else {
				worker.observeOutcome(template, "cancelled")
			}
			return
		}
		var contentError *emailContentError
		disposition := mailer.DispositionRetryable
		code := "email_context_load_failed"
		if errors.As(err, &contentError) {
			disposition = mailer.DispositionPermanent
			code = "email_render_failed"
		}
		finalizeContext, finalizeCancel := newEmailFinalizationContext(ctx)
		defer finalizeCancel()
		if recordErr := worker.repository.RecordEmailFailure(finalizeContext, delivery, disposition, false, code); recordErr != nil {
			worker.logger.Error("record email preparation failure", "delivery_id", delivery.ID, "error", recordErr)
		} else {
			worker.observeOutcome(template, emailDispositionOutcome(disposition, delivery.AttemptCount))
		}
		return
	}
	deliveryContext, cancel := context.WithTimeout(ctx, worker.deliveryTimeout)
	err = worker.sender.Send(deliveryContext, message)
	cancel()
	finalizeContext, finalizeCancel := newEmailFinalizationContext(ctx)
	defer finalizeCancel()
	if err == nil {
		if completeErr := worker.repository.MarkEmailAccepted(finalizeContext, delivery.ID); completeErr != nil {
			worker.logger.Error("record accepted email delivery", "delivery_id", delivery.ID, "error", completeErr)
		} else {
			worker.observeOutcome(template, "accepted")
		}
		return
	}
	disposition, suppress := mailer.ClassifyTransportError(err)
	code := "smtp_transient"
	switch disposition {
	case mailer.DispositionPermanent:
		code = "smtp_permanent"
	case mailer.DispositionAmbiguous:
		code = "smtp_outcome_unknown"
	}
	if recordErr := worker.repository.RecordEmailFailure(finalizeContext, delivery, disposition, suppress, code); recordErr != nil {
		worker.logger.Error("record email delivery failure", "delivery_id", delivery.ID, "error", recordErr)
	} else {
		worker.observeOutcome(template, emailDispositionOutcome(disposition, delivery.AttemptCount))
	}
}

func newEmailFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), emailFinalizationTimeout)
}

func (worker *EmailWorker) observeClaim(template string, latency time.Duration) {
	if worker.metrics != nil {
		worker.metrics.ObserveNotificationEmailClaim(template, latency)
	}
}

func (worker *EmailWorker) observeOutcome(template, outcome string) {
	if worker.metrics != nil {
		worker.metrics.ObserveNotificationEmailOutcome(template, outcome)
	}
}

func emailMetricTemplate(delivery Delivery) string {
	key := delivery.AudienceType + ":" + delivery.NotificationType
	if key == "customer:booking_step_reminder" || key == "customer:booking_completed" {
		return key
	}
	if _, ok := emailTemplateRegistry[key]; !ok {
		return "other"
	}
	return key
}

func emailDispositionOutcome(disposition mailer.TransportDisposition, attempt int) string {
	switch disposition {
	case mailer.DispositionAmbiguous:
		return "manual_review"
	case mailer.DispositionPermanent:
		return "failed"
	case mailer.DispositionRetryable:
		if attempt >= maxDeliveryAttempts {
			return "retry_exhausted"
		}
		return "retry"
	default:
		return "manual_review"
	}
}

func (r *Repository) CancelAuthorizedDelivery(ctx context.Context, deliveryID uuid.UUID, code string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var bookingID uuid.UUID
	var kind, channel string
	err = tx.QueryRow(ctx, `
		UPDATE notification_deliveries
		SET status='cancelled',reconcile_after=NULL,last_error_code=$2,
            dispatch_authorized_at=CASE WHEN notification_type IN ('booking_step_reminder','booking_completed') AND channel='email' THEN NULL ELSE dispatch_authorized_at END,
            authorized_booking_event_sequence=CASE WHEN notification_type IN ('booking_step_reminder','booking_completed') AND channel='email' THEN NULL ELSE authorized_booking_event_sequence END,
            authorized_preference_revision=CASE WHEN notification_type IN ('booking_step_reminder','booking_completed') AND channel='email' THEN NULL ELSE authorized_preference_revision END,
			provider_status='cancelled',provider_status_at=NOW(),completed_at=NOW(),updated_at=NOW()
		WHERE id=$1 AND status='dispatching' RETURNING booking_id,notification_type,channel
	`, deliveryID, boundedCode(code)).Scan(&bookingID, &kind, &channel)
	if err != nil {
		return fmt.Errorf("cancel authorized notification delivery: %w", err)
	}
	if kind == "booking_step_reminder" && channel == "email" {
		if err = r.replanUnsentStepTx(ctx, tx, bookingID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) MarkEmailAccepted(ctx context.Context, deliveryID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='accepted',accepted_at=NOW(),completed_at=NOW(),reconcile_after=NULL,
			provider_status='accepted',provider_status_at=NOW(),provider_error_code='',
			last_error_code='',updated_at=NOW()
		WHERE id=$1 AND channel='email' AND status='dispatching'
	`, deliveryID)
	if err != nil {
		return fmt.Errorf("record accepted email delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("email delivery is no longer dispatching")
	}
	return nil
}

func (r *Repository) ReleaseClaimedDelivery(ctx context.Context, delivery Delivery, code string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='retry',next_attempt_at=$3,lease_owner='',lease_expires_at=NULL,
			last_error_code=$4,updated_at=NOW()
		WHERE id=$1 AND status='processing' AND lease_owner=$2
	`, delivery.ID, delivery.LeaseOwner, deliveryRetryAt(r.now(), delivery.ID, delivery.AttemptCount), boundedCode(code))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("notification delivery lease was lost")
	}
	return nil
}

func (r *Repository) RecordEmailFailure(
	ctx context.Context,
	delivery Delivery,
	disposition mailer.TransportDisposition,
	suppressDestination bool,
	code string,
) error {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	status := "failed"
	var completedAt any = r.now()
	var nextAttemptAt any = r.now()
	if disposition == mailer.DispositionRetryable && delivery.AttemptCount < maxDeliveryAttempts {
		status = "retry"
		completedAt = nil
		nextAttemptAt = deliveryRetryAt(r.now(), delivery.ID, delivery.AttemptCount)
	} else if disposition == mailer.DispositionAmbiguous {
		status = "manual_review"
		completedAt = nil
	}
	if disposition == mailer.DispositionRetryable && delivery.AttemptCount >= maxDeliveryAttempts {
		code = "smtp_retry_exhausted"
		suppressDestination = false
	}
	tag, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status=$2,next_attempt_at=$3,lease_owner='',lease_expires_at=NULL,
			dispatch_authorized_at=CASE WHEN $2='retry' THEN NULL ELSE dispatch_authorized_at END,
			authorized_booking_event_sequence=CASE WHEN $2='retry' THEN NULL ELSE authorized_booking_event_sequence END,
			authorized_preference_revision=CASE WHEN $2='retry' THEN NULL ELSE authorized_preference_revision END,
			reconcile_after=NULL,last_error_code=$4,provider_error_code=$4,
			provider_status=$2,provider_status_at=NOW(),completed_at=$5,updated_at=NOW()
		WHERE id=$1 AND channel='email' AND status='dispatching'
	`, delivery.ID, status, nextAttemptAt, boundedCode(code), completedAt)
	if err != nil {
		return fmt.Errorf("record email delivery failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("email delivery is no longer dispatching")
	}
	if suppressDestination && disposition == mailer.DispositionPermanent {
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_contact_suppressions (
				channel,destination_hmac,reason,source,created_at,updated_at
			) VALUES ('email',$1,'invalid_address','provider_response',NOW(),NOW())
			ON CONFLICT (channel,destination_hmac) DO UPDATE SET
				reason=CASE
					WHEN notification_contact_suppressions.reason IN ('manual','hard_bounce')
					THEN notification_contact_suppressions.reason
					ELSE EXCLUDED.reason END,
				source=CASE
					WHEN notification_contact_suppressions.reason IN ('manual','hard_bounce')
					THEN notification_contact_suppressions.source
					ELSE EXCLUDED.source END,
				updated_at=NOW()
		`, delivery.DestinationHMAC); err != nil {
			return fmt.Errorf("suppress rejected email destination: %w", err)
		}
	}
	return tx.Commit(ctx)
}

func deliveryRetryAt(now time.Time, deliveryID uuid.UUID, attempt int) time.Time {
	return mailer.RetryAt(now, deliveryID, attempt)
}
