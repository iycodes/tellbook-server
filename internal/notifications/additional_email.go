package notifications

import (
	"context"
	"errors"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/money"
	"booking/go-server/internal/transactionemail"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type additionalBooking struct {
	createdAt            time.Time
	reservationDeadline  *time.Time
	enabled              bool
	deposit, total, paid int64
	agreementMethod      string
}
type bookingEmailQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadAdditionalBooking(ctx context.Context, q bookingEmailQuerier, id uuid.UUID) (additionalBooking, error) {
	var b additionalBooking
	err := q.QueryRow(ctx, `SELECT b.created_at,b.reservation_expires_at,b.additional_email_reminder_enabled,
 b.deposit_amount_minor,b.total_amount_minor,
 GREATEST(COALESCE((SELECT SUM(p.amount_minor) FROM payments p WHERE p.booking_id=b.id AND p.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0)-COALESCE((SELECT SUM(a.allocation_impact_minor) FROM payment_adjustments a JOIN payments p ON p.id=a.payment_id WHERE p.booking_id=b.id AND a.status='successful'),0),0),
 CASE WHEN b.standalone_signature_required_snapshot THEN 'signature' ELSE COALESCE(b.agreement_confirmation_method_snapshot,'confirmation') END
 FROM bookings b WHERE b.id=$1`, id).Scan(&b.createdAt, &b.reservationDeadline, &b.enabled, &b.deposit, &b.total, &b.paid, &b.agreementMethod)
	return b, err
}
func outstandingStep(s bookingState, b additionalBooking, now time.Time) (transactionemail.ReminderKind, int64, *time.Time) {
	if !providerBookingStatusEligible(s.Status) || !s.StartAt.After(now) || s.ReservationExpiredAt != nil || s.PaymentStatus == "refunded" || s.PaymentStatus == "disputed" {
		return "", 0, nil
	}
	var deadline *time.Time
	// A reservation expires for the initial payment only; a paid deposit clears it.
	if !paymentObligationSatisfied(s.PaymentStatus) {
		deadline = b.reservationDeadline
	}
	if deadline != nil && !deadline.After(now) {
		return "", 0, nil
	}
	if due := b.deposit - b.paid; due > 0 && !paymentObligationSatisfied(s.PaymentStatus) {
		return transactionemail.DepositReminder, due, deadline
	}
	if s.AgreementRequired && s.AgreementStatus != "accepted" && s.AgreementStatus != "signed" {
		return transactionemail.AgreementReminder, 0, nil
	}
	if due := b.total - b.paid; due > 0 && s.PaymentStatus != "paid_in_full" {
		return transactionemail.BalanceReminder, due, deadline
	}
	return "", 0, nil
}
func stepReminderSchedule(created, start time.Time, deadline *time.Time) (time.Time, bool) {
	scheduled := start.Add(-24 * time.Hour)
	limit := start
	if deadline != nil && deadline.Before(limit) {
		limit = *deadline
		if d := deadline.Add(-time.Hour); d.Before(scheduled) {
			scheduled = d
		}
	}
	if earliest := created.Add(time.Hour); scheduled.Before(earliest) {
		scheduled = earliest
	}
	return scheduled, scheduled.Before(limit)
}
func (r *Repository) reconcileAdditionalTx(ctx context.Context, tx pgx.Tx, s bookingState, event *bookingEvent, cache map[string]bool) error {
	if !r.additionalEmails {
		return nil
	}
	b, err := loadAdditionalBooking(ctx, tx, s.ID)
	if err != nil {
		return err
	}
	if event != nil && event.Payload.Status == "completed" && event.Payload.PreviousStatus != "" && event.Payload.PreviousStatus != "completed" && containsNotificationType(eventNotificationTypes(s, *event), "booking_completed") {
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT additional_email_enabled FROM booking_domain_events WHERE id=$1`, event.ID).Scan(&enabled); err != nil {
			return err
		}
		if enabled && s.ConsentPolicyRevision == 1 && s.MarketplaceBookingEmail && s.CustomerEmail != "" {
			c := deliveryCandidate{audience: "customer", channel: "email", notificationType: "booking_completed", templateKey: "booking_completed", scheduledFor: r.now(), destination: s.CustomerEmail, eventSequence: event.Sequence, idempotencyKey: "booking_completed:" + s.ID.String() + ":customer:email"}
			if err = r.insertDeliveryTx(ctx, tx, s, event, c, cache); err != nil {
				return err
			}
		}
	}
	if !b.enabled {
		return nil
	}
	key := "booking_step_reminder:" + s.ID.String() + ":customer:email"
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE idempotency_key=$1)`, key).Scan(&exists); err != nil {
		return err
	}
	if !exists && (event == nil || event.Type != "booking_created") {
		return nil
	}
	kind, _, _ := outstandingStep(s, b, r.now())
	var deadline *time.Time
	if !paymentObligationSatisfied(s.PaymentStatus) {
		deadline = b.reservationDeadline
	}
	scheduled, valid := stepReminderSchedule(b.createdAt, s.StartAt, deadline)

	// Keep one pending/cancelled record even when steps are already complete at creation.
	if !exists && valid && s.CustomerEmail != "" {
		c := deliveryCandidate{audience: "customer", channel: "email", notificationType: "booking_step_reminder", templateKey: "booking_step_reminder", scheduledFor: scheduled, destination: s.CustomerEmail, idempotencyKey: key}
		if err = r.insertDeliveryTx(ctx, tx, s, event, c, cache); err != nil {
			return err
		}
	}
	eligible := valid && kind != "" && s.ConsentPolicyRevision == 1 && s.EmailReminderConsent && s.MarketplaceReminderEmail && s.CustomerEmail != ""
	if eligible {
		c := deliveryCandidate{audience: "customer", channel: "email", notificationType: "booking_step_reminder", templateKey: "booking_step_reminder", scheduledFor: scheduled, destination: s.CustomerEmail, idempotencyKey: key}
		return r.insertDeliveryTx(ctx, tx, s, event, c, cache)
	}
	_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status='cancelled',lease_owner='',lease_expires_at=NULL,completed_at=NOW(),last_error_code='step_no_longer_eligible',updated_at=NOW() WHERE idempotency_key=$1 AND status IN ('pending','retry','processing') AND dispatch_authorized_at IS NULL`, key)
	return err
}
func (r *Repository) additionalAuthorizedTx(ctx context.Context, tx pgx.Tx, d Delivery, s bookingState) (bool, string, error) {
	if !r.additionalEmails || d.Channel != "email" || d.AudienceType != "customer" {
		return false, "additional_emails_disabled", nil
	}
	if d.NotificationType == "booking_completed" {
		return s.Status == "completed", "booking_not_completed", nil
	}
	b, err := loadAdditionalBooking(ctx, tx, s.ID)
	if err != nil {
		return false, "", err
	}
	kind, _, _ := outstandingStep(s, b, r.now())
	var deadline *time.Time
	if !paymentObligationSatisfied(s.PaymentStatus) {
		deadline = b.reservationDeadline
	}
	scheduled, valid := stepReminderSchedule(b.createdAt, s.StartAt, deadline)
	return b.enabled && kind != "" && valid && !scheduled.After(r.now()), "step_no_longer_eligible", nil
}
func (r *Repository) buildAdditionalEmail(ctx context.Context, tx pgx.Tx, d Delivery, data emailTemplateData, start time.Time, timezone, country, currency string) (mailer.Message, error) {
	if !r.additionalEmails {
		return mailer.Message{}, ErrEmailEligibilityChanged
	}
	details := transactionemail.BookingDetails{Event: transactionemail.Event{DeliveryID: d.ID, Recipient: data.RecipientEmail, OccurredAt: r.now()}, Service: data.ServiceTitle, Provider: data.ProviderName, StartsAt: start, Timezone: timezone, BookingURL: data.ActionURL}
	var eventID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1`, d.BookingID).Scan(&eventID); err != nil {
		return mailer.Message{}, err
	}
	s, err := loadBookingStateTx(ctx, tx, eventID)
	if err != nil {
		return mailer.Message{}, err
	}
	ok, _, err := r.deliveryAuthorizedTx(ctx, tx, d, s)
	if err != nil {
		return mailer.Message{}, err
	}
	if !ok {
		return mailer.Message{}, ErrEmailEligibilityChanged
	}
	if data.Type == "booking_completed" {
		message, err := transactionemail.RenderCompletion(transactionemail.CompletionInput{BookingDetails: details, BookingCompleted: s.Status == "completed"})
		if err != nil {
			return mailer.Message{}, invalidEmailContent(err)
		}
		return message, nil
	}
	b, err := loadAdditionalBooking(ctx, tx, s.ID)
	if err != nil {
		return mailer.Message{}, err
	}
	kind, due, deadline := outstandingStep(s, b, r.now())
	market, found := markets.DefaultCatalog().Lookup(country)
	if !found {
		return mailer.Message{}, invalidEmailContent(ErrEmailNotDispatchable)
	}
	var spec money.FormatSpec
	for _, c := range market.Currencies {
		if c.Code == currency {
			spec = money.FormatSpec{CurrencyCode: c.Code, Symbol: c.Symbol, Exponent: c.MinorUnitExponent, DecimalSeparator: c.DecimalSeparator, GroupingSeparator: c.GroupingSeparator, SymbolPosition: c.SymbolPosition, SpaceBetweenSymbol: c.SpaceBetweenSymbol}
			break
		}
	}
	message, err := transactionemail.RenderReminder(transactionemail.ReminderInput{BookingDetails: details, Kind: kind, BookingActive: true, StepOutstanding: true, CheckedAt: r.now(), DueMinor: due, Currency: spec, AgreementMethod: b.agreementMethod, Deadline: deadline})
	if err != nil {
		return mailer.Message{}, invalidEmailContent(err)
	}
	return message, nil
}

// If a reschedule raced the dispatch fence, update the same known-unsent job
// here; the event planner may already have run while it was dispatching.
func (r *Repository) replanUnsentStepTx(ctx context.Context, tx pgx.Tx, bookingID uuid.UUID) error {
	if !r.additionalEmails {
		return nil
	}
	var eventID uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1`, bookingID).Scan(&eventID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	state, err := loadBookingStateTx(ctx, tx, eventID)
	if err != nil {
		return err
	}
	return r.reconcileAdditionalTx(ctx, tx, state, nil, nil)
}
