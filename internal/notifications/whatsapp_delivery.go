package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	defaultWhatsAppConcurrency  = 4
	whatsAppFinalizationTimeout = 5 * time.Second
	whatsAppUnknownReviewDelay  = 15 * time.Minute
	maxWhatsAppStatusAttempts   = 10
)

var (
	ErrWhatsAppNotDispatchable  = errors.New("WhatsApp delivery is no longer dispatchable")
	ErrWhatsAppDestinationMoved = errors.New("WhatsApp destination changed after authorization")
)

type whatsAppContentError struct{ cause error }

func (e *whatsAppContentError) Error() string { return e.cause.Error() }
func (e *whatsAppContentError) Unwrap() error { return e.cause }

func invalidWhatsAppContent(err error) error { return &whatsAppContentError{cause: err} }

type whatsAppSender interface {
	SendTemplate(context.Context, whatsapp.TemplateMessage) (whatsapp.SendResult, error)
}

type WhatsAppWorkerMetrics interface {
	ObserveNotificationWhatsAppClaim(template string, latency time.Duration)
	ObserveNotificationWhatsAppOutcome(template, outcome string)
	ObserveNotificationWhatsAppStatus(status, outcome string, lag time.Duration)
}

type WhatsAppWorker struct {
	repository      *Repository
	sender          whatsAppSender
	logger          *slog.Logger
	wake            <-chan struct{}
	metrics         WhatsAppWorkerMetrics
	workerID        string
	concurrency     int
	deliveryTimeout time.Duration
	outboundEnabled bool
}

func NewWhatsAppWorker(
	repository *Repository,
	sender whatsAppSender,
	logger *slog.Logger,
	wake <-chan struct{},
	metrics WhatsAppWorkerMetrics,
	concurrency int,
	deliveryTimeout time.Duration,
	outboundEnabled bool,
) *WhatsAppWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = defaultWhatsAppConcurrency
	}
	if deliveryTimeout <= 0 {
		deliveryTimeout = 15 * time.Second
	}
	return &WhatsAppWorker{
		repository: repository, sender: sender, logger: logger, wake: wake, metrics: metrics,
		workerID: "notification-whatsapp-" + uuid.NewString(), concurrency: concurrency,
		deliveryTimeout: deliveryTimeout, outboundEnabled: outboundEnabled,
	}
}

func (worker *WhatsAppWorker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil {
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
		if worker.outboundEnabled && worker.sender != nil {
			moreWork = worker.processDeliveryCycle(ctx) || moreWork
		}
		if moreWork {
			timer.Reset(0)
		} else {
			timer.Reset(20 * time.Second)
		}
	}
}

func (worker *WhatsAppWorker) processDeliveryCycle(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	deliveries, err := worker.repository.ClaimDeliveries(
		ctx, "whatsapp", worker.workerID, defaultClaimBatch, defaultLeaseDuration,
	)
	if err != nil {
		worker.logger.Error("claim WhatsApp notification deliveries", "error", err)
		return false
	}
	worker.processDeliveryBatch(ctx, deliveries)
	return len(deliveries) == defaultClaimBatch
}

func (worker *WhatsAppWorker) processDeliveryBatch(ctx context.Context, deliveries []Delivery) {
	for start := 0; start < len(deliveries); start += worker.concurrency {
		end := min(start+worker.concurrency, len(deliveries))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, delivery := range deliveries[start:end] {
			go func(delivery Delivery) {
				defer group.Done()
				worker.processDelivery(ctx, delivery)
			}(delivery)
		}
		group.Wait()
	}
}

func (worker *WhatsAppWorker) processDelivery(ctx context.Context, claimed Delivery) {
	template := whatsAppMetricTemplate(claimed.TemplateKey)
	latency := worker.repository.now().Sub(claimed.ScheduledFor)
	if latency < 0 {
		latency = 0
	}
	worker.observeClaim(template, latency)
	delivery, err := worker.repository.AuthorizeDispatch(ctx, claimed)
	if errors.Is(err, ErrDispatchNotAuthorized) {
		worker.observeOutcome(template, "cancelled")
		return
	}
	if err != nil {
		finalizeContext, cancel := newWhatsAppFinalizationContext(ctx)
		defer cancel()
		if releaseErr := worker.repository.ReleaseClaimedDelivery(finalizeContext, claimed, "dispatch_fence_failed"); releaseErr != nil {
			worker.logger.Warn("release WhatsApp delivery after dispatch fence failure", "delivery_id", claimed.ID, "error", releaseErr)
		} else {
			worker.observeOutcome(template, "retry")
		}
		return
	}
	message, err := worker.repository.BuildWhatsAppMessage(ctx, delivery)
	if err != nil {
		if errors.Is(err, ErrWhatsAppNotDispatchable) {
			return
		}
		finalizeContext, cancel := newWhatsAppFinalizationContext(ctx)
		defer cancel()
		if errors.Is(err, ErrWhatsAppDestinationMoved) {
			if cancelErr := worker.repository.CancelAuthorizedDelivery(finalizeContext, delivery.ID, "destination_changed"); cancelErr != nil {
				worker.logger.Error("cancel redirected WhatsApp delivery", "delivery_id", delivery.ID, "error", cancelErr)
			} else {
				worker.observeOutcome(template, "cancelled")
			}
			return
		}
		var contentError *whatsAppContentError
		disposition := whatsAppFailureRetryable
		code := "whatsapp_context_load_failed"
		if errors.As(err, &contentError) {
			disposition = whatsAppFailurePermanent
			code = "whatsapp_template_invalid"
		}
		worker.recordFailure(finalizeContext, delivery, disposition, code, 0, template)
		return
	}
	deliveryContext, cancel := context.WithTimeout(ctx, worker.deliveryTimeout)
	result, err := worker.sender.SendTemplate(deliveryContext, message)
	cancel()
	finalizeContext, finalizeCancel := newWhatsAppFinalizationContext(ctx)
	defer finalizeCancel()
	if err == nil {
		if recordErr := worker.repository.MarkWhatsAppAccepted(finalizeContext, delivery.ID, result.MessageID); recordErr != nil {
			worker.logger.Error("record accepted WhatsApp delivery", "delivery_id", delivery.ID, "error", recordErr)
		} else {
			worker.observeOutcome(template, "accepted")
		}
		return
	}
	disposition, code, retryAfter := classifyWhatsAppSendError(err)
	worker.recordFailure(finalizeContext, delivery, disposition, code, retryAfter, template)
}

func (worker *WhatsAppWorker) recordFailure(
	ctx context.Context,
	delivery Delivery,
	disposition whatsAppFailureDisposition,
	code string,
	retryAfter time.Duration,
	template string,
) {
	if err := worker.repository.RecordWhatsAppFailure(ctx, delivery, disposition, code, retryAfter); err != nil {
		worker.logger.Error("record WhatsApp delivery failure", "delivery_id", delivery.ID, "error", err)
		return
	}
	worker.observeOutcome(template, whatsAppDispositionOutcome(disposition, delivery.AttemptCount))
}

func newWhatsAppFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), whatsAppFinalizationTimeout)
}

func (r *Repository) BuildWhatsAppMessage(ctx context.Context, delivery Delivery) (whatsapp.TemplateMessage, error) {
	var audience, templateKey, destination, providerName, customerName, serviceTitle string
	var timezone, locationLabel, countryCode, currencyCode string
	var providerContactPhone, bookingToken string
	var customerOwned bool
	var bookingID uuid.UUID
	var startsAt time.Time
	var totalMinor, paidMinor int64
	err := r.db.QueryRow(ctx, `
		SELECT delivery.audience_type,delivery.template_key,booking.id,
			CASE WHEN delivery.audience_type='provider' THEN COALESCE(preference.whatsapp_e164,'')
			     ELSE COALESCE(booking.customer_whatsapp_e164_snapshot,'') END,
			booking.stylist_name,customer.full_name,booking.title,booking.start_at,
			booking.timezone,booking.location_label,booking.country_code,booking.currency_code,
			booking.total_amount_minor,
			GREATEST(COALESCE(payment_totals.gross_paid_minor,0)-COALESCE(payment_totals.adjusted_minor,0),0),
			COALESCE(profile.booking_contact_phone,''),booking.public_token,booking.marketplace_customer_id IS NOT NULL
		FROM notification_deliveries delivery
		JOIN bookings booking ON booking.id=delivery.booking_id
		JOIN customers customer ON customer.id=booking.customer_id
		LEFT JOIN client_profiles profile ON profile.client_id=booking.client_id
		LEFT JOIN provider_notification_preferences preference ON preference.client_id=booking.client_id
		LEFT JOIN LATERAL (
			SELECT
				COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
					WHERE payment.booking_id=booking.id
					  AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0) gross_paid_minor,
				COALESCE((SELECT SUM(adjustment.allocation_impact_minor)
					FROM payment_adjustments adjustment
					JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=booking.id AND adjustment.status='successful'),0) adjusted_minor
		) payment_totals ON delivery.template_key IN ('provider_new_booking','provider_booking_reminder','user_reminder')
		WHERE delivery.id=$1 AND delivery.channel='whatsapp' AND delivery.status='dispatching'
	`, delivery.ID).Scan(
		&audience, &templateKey, &bookingID, &destination, &providerName, &customerName,
		&serviceTitle, &startsAt, &timezone, &locationLabel, &countryCode, &currencyCode,
		&totalMinor, &paidMinor,
		&providerContactPhone, &bookingToken, &customerOwned,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return whatsapp.TemplateMessage{}, ErrWhatsAppNotDispatchable
	}
	if err != nil {
		return whatsapp.TemplateMessage{}, fmt.Errorf("load WhatsApp delivery context: %w", err)
	}
	if !hmacEqual(r.destinationFingerprint("whatsapp", destination), delivery.DestinationHMAC) {
		return whatsapp.TemplateMessage{}, ErrWhatsAppDestinationMoved
	}
	if _, err := whatsapp.NormalizeE164(destination); err != nil {
		return whatsapp.TemplateMessage{}, invalidWhatsAppContent(err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return whatsapp.TemplateMessage{}, invalidWhatsAppContent(fmt.Errorf("invalid booking timezone: %w", err))
	}
	when := startsAt.In(location).Format("Monday, 2 January 2006 at 3:04 PM MST")
	if strings.TrimSpace(locationLabel) == "" {
		locationLabel = "See TellBook for location details"
	}
	key := whatsapp.TemplateKey(templateKey)
	var paid, due string
	if key == whatsapp.TemplateProviderNewBooking || key == whatsapp.TemplateProviderBookingReminder || key == whatsapp.TemplateUserReminder {
		paid, err = formatEmailMoney(paidMinor, countryCode, currencyCode)
		if err != nil {
			return whatsapp.TemplateMessage{}, invalidWhatsAppContent(err)
		}
		due, err = formatEmailMoney(max(totalMinor-paidMinor, 0), countryCode, currencyCode)
		if err != nil {
			return whatsapp.TemplateMessage{}, invalidWhatsAppContent(err)
		}
	}
	values := whatsapp.TemplateValues{OpaqueCallbackData: delivery.ID.String()}
	switch {
	case audience == "customer" && key == whatsapp.TemplateUserReminder:
		if providerContactPhone == "" {
			if err := r.CancelAuthorizedDelivery(ctx, delivery.ID, "provider_contact_unavailable"); err != nil {
				return whatsapp.TemplateMessage{}, err
			}
			return whatsapp.TemplateMessage{}, ErrWhatsAppNotDispatchable
		}
		suffix := "#claim=" + bookingToken
		if customerOwned {
			suffix = "?booking=" + bookingID.String()
		}
		values.Button = map[string]string{"booking_route_suffix": suffix}
		values.Body = map[string]string{
			"customer_name": customerName, "service_title": serviceTitle, "provider_name": providerName,
			"appointment_datetime": when, "booking_location": locationLabel,
			"provider_contact_phone": providerContactPhone, "amount_due": due,
		}
	case audience == "provider" && key == whatsapp.TemplateProviderNewBooking:
		values.Button = map[string]string{"booking_route_suffix": "?booking=" + bookingID.String()}
		values.Body = map[string]string{
			"provider_name": providerName, "customer_name": customerName,
			"service_title": serviceTitle, "appointment_datetime": when,
			"amount_paid": paid, "amount_due": due,
		}
	case audience == "provider" && key == whatsapp.TemplateProviderBookingReminder:
		values.Button = map[string]string{"booking_route_suffix": "?booking=" + bookingID.String()}
		values.Body = map[string]string{
			"provider_name": providerName, "service_title": serviceTitle,
			"customer_name": customerName, "appointment_datetime": when,
			"booking_location": locationLabel, "amount_due": due,
		}
	case (audience == "provider" || audience == "customer") && key == whatsapp.TemplateBookingStatusUpdate:
		recipientName := customerName
		if audience == "provider" {
			recipientName = providerName
		}
		values.Body = map[string]string{
			"recipient_name": recipientName, "booking_reference": bookingDisplayReference(bookingID),
			"update_summary": bookingUpdateSummary(delivery.NotificationType),
			"service_title":  serviceTitle, "appointment_datetime": when,
		}
	default:
		return whatsapp.TemplateMessage{}, invalidWhatsAppContent(
			fmt.Errorf("unsupported WhatsApp template %s:%s", audience, templateKey),
		)
	}
	return whatsapp.TemplateMessage{To: destination, Key: key, Values: values}, nil
}

func bookingDisplayReference(bookingID uuid.UUID) string {
	return "TB-" + strings.ToUpper(strings.ReplaceAll(bookingID.String(), "-", "")[:12])
}

func bookingUpdateSummary(notificationType string) string {
	return map[string]string{
		"booking_rescheduled":     "Appointment rescheduled",
		"booking_cancelled":       "Booking cancelled",
		"booking_expired":         "Booking expired",
		"payment_satisfied":       "Payment requirement satisfied",
		"payment_failed":          "Payment needs attention",
		"payment_refunded":        "Payment refunded",
		"payment_action_required": "Payment action required",
	}[notificationType]
}

type whatsAppFailureDisposition string

const (
	whatsAppFailureRetryable whatsAppFailureDisposition = "retryable"
	whatsAppFailurePermanent whatsAppFailureDisposition = "permanent"
	whatsAppFailureUnknown   whatsAppFailureDisposition = "unknown"
)

func classifyWhatsAppSendError(err error) (whatsAppFailureDisposition, string, time.Duration) {
	var requestError *whatsapp.RequestError
	if errors.As(err, &requestError) {
		return whatsAppFailurePermanent, "meta_request_invalid", 0
	}
	var transportError *whatsapp.TransportError
	if errors.As(err, &transportError) {
		if transportError.Ambiguous {
			return whatsAppFailureUnknown, "meta_outcome_unknown", 0
		}
		return whatsAppFailureRetryable, "meta_transport", 0
	}
	var graphError *whatsapp.GraphError
	if errors.As(err, &graphError) {
		code := fmt.Sprintf("meta_%s_%d", graphError.Class, graphError.Code)
		switch graphError.Class {
		case whatsapp.ErrorClassTransient, whatsapp.ErrorClassRateLimit:
			return whatsAppFailureRetryable, code, graphError.RetryAfter
		default:
			return whatsAppFailurePermanent, code, 0
		}
	}
	return whatsAppFailureUnknown, "meta_outcome_unknown", 0
}

func (r *Repository) MarkWhatsAppAccepted(ctx context.Context, deliveryID uuid.UUID, wamid string) error {
	wamid = strings.TrimSpace(wamid)
	if wamid == "" || len(wamid) > 512 {
		return errors.New("WhatsApp provider message ID is invalid")
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET status='accepted',provider_message_id=$2,accepted_at=NOW(),reconcile_after=NULL,
			provider_status='accepted',provider_status_at=NOW(),provider_error_code='',
			last_error_code='',updated_at=NOW()
		WHERE id=$1 AND channel='whatsapp' AND status='dispatching'
	`, deliveryID, wamid)
	if err != nil {
		return fmt.Errorf("record accepted WhatsApp delivery: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	tag, err = r.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET accepted_at=COALESCE(accepted_at,NOW()),updated_at=NOW()
		WHERE id=$1 AND channel='whatsapp' AND provider_message_id=$2
		  AND status IN ('accepted','sent','delivered','read','failed','deleted')
	`, deliveryID, wamid)
	if err != nil {
		return fmt.Errorf("complete callback-first WhatsApp acceptance: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return errors.New("WhatsApp delivery is no longer dispatching")
}

func (r *Repository) RecordWhatsAppFailure(
	ctx context.Context,
	delivery Delivery,
	disposition whatsAppFailureDisposition,
	code string,
	retryAfter time.Duration,
) error {
	status := "failed"
	var completedAt any = r.now()
	var reconcileAfter any
	nextAttemptAt := r.now()
	if disposition == whatsAppFailureRetryable && delivery.AttemptCount < maxDeliveryAttempts {
		status = "retry"
		completedAt = nil
		nextAttemptAt = whatsAppRetryAt(r.now(), delivery.ID, delivery.AttemptCount, retryAfter)
	} else if disposition == whatsAppFailureUnknown {
		status = "unknown"
		completedAt = nil
		reconcileAfter = r.now().Add(whatsAppUnknownReviewDelay)
	} else if disposition == whatsAppFailureRetryable {
		code = "meta_retry_exhausted"
	}
	tag, err := r.db.Exec(ctx, `
		UPDATE notification_deliveries
		SET status=$2,next_attempt_at=$3,lease_owner='',lease_expires_at=NULL,
			dispatch_authorized_at=CASE WHEN $2='retry' THEN NULL ELSE dispatch_authorized_at END,
			authorized_booking_event_sequence=CASE WHEN $2='retry' THEN NULL ELSE authorized_booking_event_sequence END,
			authorized_preference_revision=CASE WHEN $2='retry' THEN NULL ELSE authorized_preference_revision END,
			reconcile_after=$4,last_error_code=$5,provider_error_code=$5,
			provider_status=$2,provider_status_at=NOW(),completed_at=$6,updated_at=NOW()
		WHERE id=$1 AND channel='whatsapp' AND status='dispatching'
	`, delivery.ID, status, nextAttemptAt, reconcileAfter, boundedCode(code), completedAt)
	if err != nil {
		return fmt.Errorf("record WhatsApp delivery failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("WhatsApp delivery is no longer dispatching")
	}
	return nil
}

func whatsAppRetryAt(now time.Time, deliveryID uuid.UUID, attempt int, retryAfter time.Duration) time.Time {
	retryAt := deliveryRetryAt(now, deliveryID, attempt)
	if retryAfter > 30*time.Minute {
		retryAfter = 30 * time.Minute
	}
	if providerRetryAt := now.Add(retryAfter); providerRetryAt.After(retryAt) {
		return providerRetryAt
	}
	return retryAt
}

func whatsAppMetricTemplate(key string) string {
	switch whatsapp.TemplateKey(strings.TrimSpace(key)) {
	case whatsapp.TemplateProviderNewBooking, whatsapp.TemplateProviderBookingReminder,
		whatsapp.TemplateUserReminder, whatsapp.TemplateBookingStatusUpdate:
		return strings.TrimSpace(key)
	default:
		return "other"
	}
}

func whatsAppDispositionOutcome(disposition whatsAppFailureDisposition, attempt int) string {
	switch disposition {
	case whatsAppFailureUnknown:
		return "unknown"
	case whatsAppFailurePermanent:
		return "failed"
	case whatsAppFailureRetryable:
		if attempt >= maxDeliveryAttempts {
			return "retry_exhausted"
		}
		return "retry"
	default:
		return "other"
	}
}

func (worker *WhatsAppWorker) observeClaim(template string, latency time.Duration) {
	if worker.metrics != nil {
		worker.metrics.ObserveNotificationWhatsAppClaim(template, latency)
	}
}

func (worker *WhatsAppWorker) observeOutcome(template, outcome string) {
	if worker.metrics != nil {
		worker.metrics.ObserveNotificationWhatsAppOutcome(template, outcome)
	}
}
