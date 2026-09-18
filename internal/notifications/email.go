package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type emailTemplateData struct {
	ProviderContactPhone string
	Audience             string
	Type                 string
	RecipientEmail       string
	RecipientName        string
	CustomerName         string
	ProviderName         string
	ServiceTitle         string
	BookingStatus        string
	When                 string
	Location             string
	Total                string
	Paid                 string
	Due                  string
	ActionURL            string
}

type emailCopy struct {
	Subject  string
	Headline string
	Body     string
	Action   string
}

var (
	ErrEmailEligibilityChanged = errors.New("email eligibility changed before SMTP")
	ErrEmailNotDispatchable    = errors.New("email delivery is no longer dispatchable")
	ErrEmailDestinationMoved   = errors.New("email destination changed after authorization")
)

type emailContentError struct{ cause error }

func (e *emailContentError) Error() string { return e.cause.Error() }
func (e *emailContentError) Unwrap() error { return e.cause }

func invalidEmailContent(err error) error { return &emailContentError{cause: err} }

var emailTemplateRegistry = map[string]emailCopy{
	"customer:customer_booking_received": {
		Subject: "We received your booking", Headline: "Your booking is in",
		Body: "We have your booking. You can follow any remaining payment, agreement, and confirmation steps on your booking page.", Action: "View booking",
	},
	"provider:provider_new_booking": {
		Subject: "You have a new secured booking", Headline: "A new booking is ready",
		Body: "The customer has completed the required payment and agreement steps. Review the details and confirm the appointment if needed.", Action: "Review booking",
	},
	"customer:customer_booking_secured": {
		Subject: "Your booking is secured", Headline: "Your booking steps are complete",
		Body: "Your required payment and agreement steps are complete. Check your booking page for the provider’s confirmation and anything else you need to do.", Action: "View booking",
	},
	"provider:appointment_reminder": {
		Subject: "Upcoming appointment reminder", Headline: "An appointment is coming up",
		Body: "A little preparation goes a long way. Here are the details for your upcoming appointment; your booking page has the latest updates.", Action: "View booking",
	},
	"customer:appointment_reminder": {
		Subject: "Your appointment is coming up", Headline: "A quick appointment reminder",
		Body: "Your appointment is getting closer. Here’s a handy reminder of when, where, and who you’re booked with.", Action: "View booking",
	},
	"provider:booking_rescheduled": {
		Subject: "A booking was rescheduled", Headline: "The appointment time changed",
		Body: "The booking now has a different appointment time. Review the current details below.", Action: "Review booking",
	},
	"customer:booking_rescheduled": {
		Subject: "Your booking was rescheduled", Headline: "Your appointment time changed",
		Body: "Your booking now has a different appointment time. Review the current details below.", Action: "View booking",
	},
	"provider:booking_cancelled": {
		Subject: "A booking was cancelled", Headline: "Booking cancelled",
		Body: "This booking is no longer active. You can review the cancellation and any payment updates on the booking page.", Action: "Review booking",
	},
	"customer:booking_cancelled": {
		Subject: "Your booking was cancelled", Headline: "Booking cancelled",
		Body: "This booking is no longer active. Your booking page has the cancellation details and any applicable payment updates.", Action: "View booking",
	},
	"provider:booking_expired": {
		Subject: "A booking reservation expired", Headline: "Reservation expired",
		Body: "The reservation window ended before the required booking steps were completed.", Action: "Review booking",
	},
	"customer:booking_expired": {
		Subject: "Your booking reservation expired", Headline: "Reservation expired",
		Body: "The reservation window ended before the required booking steps were completed.", Action: "View booking",
	},
	"provider:payment_satisfied": {
		Subject: "Booking payment requirement satisfied", Headline: "Payment confirmed",
		Body: "The required payment for this booking is complete. The current booking balance is below; any remaining balance is shown separately.", Action: "Review booking",
	},
	"customer:payment_satisfied": {
		Subject: "Your booking payment is confirmed", Headline: "Payment confirmed",
		Body: "Your required payment is complete. You’ll find the current booking balance below, including any amount still remaining.", Action: "View booking",
	},
	"provider:payment_failed": {
		Subject: "Booking payment needs attention", Headline: "Payment needs attention",
		Body: "A payment attempt for this booking did not complete successfully.", Action: "Review booking",
	},
	"customer:payment_failed": {
		Subject: "Your booking payment needs attention", Headline: "Payment needs attention",
		Body: "A payment attempt for this booking did not complete successfully. Open the booking for the current next step.", Action: "View booking",
	},
	"provider:payment_refunded": {
		Subject: "A booking payment was refunded", Headline: "Payment refunded",
		Body: "A refund has been recorded for this booking. The balance below reflects the current net payment; open the booking for refund details.", Action: "Review booking",
	},
	"customer:payment_refunded": {
		Subject: "Your booking payment was refunded", Headline: "Payment refunded",
		Body: "A refund has been recorded for your booking. The balance below reflects the current net payment; open your booking for refund details.", Action: "View booking",
	},
	"provider:payment_action_required": {
		Subject: "Booking payment action is required", Headline: "Payment action required",
		Body: "There’s a payment update that needs your attention. Open the booking to review the details and the next step.", Action: "Review booking",
	},
	"customer:payment_action_required": {
		Subject: "Your booking payment requires attention", Headline: "Payment action required",
		Body: "Your booking’s payment state requires attention. Open the booking for the current next step.", Action: "View booking",
	},
}

func (r *Repository) BuildEmailMessage(
	ctx context.Context,
	delivery Delivery,
	clientBaseURL string,
	marketplaceBaseURL string,
) (mailer.Message, error) {
	// Read new notice content and eligibility from one coherent snapshot.
	var query bookingEmailQuerier = r.db
	var tx pgx.Tx
	if delivery.NotificationType == "booking_step_reminder" || delivery.NotificationType == "booking_completed" {
		var err error
		tx, err = r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return mailer.Message{}, err
		}
		defer tx.Rollback(ctx)
		query = tx
	}
	var data emailTemplateData
	var bookingID uuid.UUID
	var publicToken, timezone, countryCode, currencyCode string
	var startsAt time.Time
	var totalMinor, paidMinor int64
	var owned bool
	err := query.QueryRow(ctx, `
			SELECT delivery.audience_type,delivery.notification_type,booking.id,booking.public_token,booking.status,
			CASE WHEN delivery.audience_type='provider' THEN COALESCE(lower(btrim(client.email)), '')
			     ELSE COALESCE(booking.customer_email_snapshot,'') END,
			CASE WHEN delivery.audience_type='provider' THEN booking.stylist_name
			     ELSE customer.full_name END,
			customer.full_name,booking.stylist_name,booking.title,booking.start_at,
			booking.timezone,booking.location_label,booking.country_code,booking.currency_code,
			booking.total_amount_minor,
			GREATEST(COALESCE(payment_totals.gross_paid_minor,0)-COALESCE(payment_totals.adjusted_minor,0),0),
			booking.marketplace_customer_id IS NOT NULL,
			CASE WHEN delivery.audience_type='customer' THEN COALESCE(profile.booking_contact_phone,'') ELSE '' END
		FROM notification_deliveries delivery
		JOIN bookings booking ON booking.id=delivery.booking_id
		JOIN clients client ON client.id=booking.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=booking.client_id
		JOIN customers customer ON customer.id=booking.customer_id
		LEFT JOIN LATERAL (
			SELECT
				COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
					WHERE payment.booking_id=booking.id
					  AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0) gross_paid_minor,
				COALESCE((SELECT SUM(adjustment.allocation_impact_minor)
					FROM payment_adjustments adjustment
					JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=booking.id AND adjustment.status='successful'),0) adjusted_minor
		) payment_totals ON TRUE
		WHERE delivery.id=$1 AND delivery.channel='email' AND delivery.status='dispatching'
	`, delivery.ID).Scan(
		&data.Audience, &data.Type, &bookingID, &publicToken, &data.BookingStatus,
		&data.RecipientEmail, &data.RecipientName, &data.CustomerName, &data.ProviderName,
		&data.ServiceTitle, &startsAt, &timezone, &data.Location, &countryCode, &currencyCode,
		&totalMinor, &paidMinor, &owned, &data.ProviderContactPhone,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return mailer.Message{}, ErrEmailNotDispatchable
	}
	if err != nil {
		return mailer.Message{}, fmt.Errorf("load email delivery context: %w", err)
	}
	if !hmacEqual(r.destinationFingerprint("email", data.RecipientEmail), delivery.DestinationHMAC) {
		return mailer.Message{}, ErrEmailDestinationMoved
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return mailer.Message{}, invalidEmailContent(fmt.Errorf("invalid booking timezone: %w", err))
	}
	data.When = startsAt.In(location).Format("Monday, 2 January 2006 at 3:04 PM MST")
	if strings.TrimSpace(data.Location) == "" {
		data.Location = "See TellBook for location details"
	}
	data.Total, err = formatEmailMoney(totalMinor, countryCode, currencyCode)
	if err != nil {
		return mailer.Message{}, invalidEmailContent(err)
	}
	data.Paid, err = formatEmailMoney(paidMinor, countryCode, currencyCode)
	if err != nil {
		return mailer.Message{}, invalidEmailContent(err)
	}
	data.Due, err = formatEmailMoney(max(totalMinor-paidMinor, 0), countryCode, currencyCode)
	if err != nil {
		return mailer.Message{}, invalidEmailContent(err)
	}
	data.ActionURL = emailBookingActionURL(
		data.Audience, owned, bookingID, publicToken, clientBaseURL, marketplaceBaseURL,
	)
	if data.Type == "booking_step_reminder" || data.Type == "booking_completed" {
		return r.buildAdditionalEmail(ctx, tx, delivery, data, startsAt, timezone, countryCode, currencyCode)
	}
	return renderEmailTemplate(delivery.ID, data)
}

func emailBookingActionURL(
	audience string,
	owned bool,
	bookingID uuid.UUID,
	publicToken string,
	clientBaseURL string,
	marketplaceBaseURL string,
) string {
	if audience == "provider" {
		return strings.TrimRight(clientBaseURL, "/") + "/bookings?booking=" + url.QueryEscape(bookingID.String())
	}
	if owned {
		return strings.TrimRight(marketplaceBaseURL, "/") + "/bookings?booking=" + url.QueryEscape(bookingID.String())
	}
	return strings.TrimRight(marketplaceBaseURL, "/") + "/bookings#claim=" + url.QueryEscape(publicToken)
}

func renderEmailTemplate(deliveryID uuid.UUID, data emailTemplateData) (mailer.Message, error) {
	copy, ok := emailTemplateRegistry[data.Audience+":"+data.Type]
	if !ok {
		return mailer.Message{}, invalidEmailContent(fmt.Errorf("unsupported email template %s:%s", data.Audience, data.Type))
	}
	bookingStatus := strings.ToLower(strings.TrimSpace(data.BookingStatus))
	if data.Audience == "provider" && data.Type == "appointment_reminder" &&
		(bookingStatus == "booked" || bookingStatus == "pending") {
		copy.Subject = "A booking is awaiting your confirmation"
		copy.Headline = "Confirmation is still required"
		copy.Body = "This appointment is approaching, but it is still awaiting your confirmation. Open TellBook to review and confirm the booking before its scheduled time."
	}
	greetingName := data.RecipientName
	if strings.TrimSpace(greetingName) == "" {
		greetingName = "there"
	}
	contactText := ""
	if data.Audience == "customer" && data.ProviderContactPhone != "" {
		contactText = "\nProvider contact: " + data.ProviderContactPhone
	}
	textBody := fmt.Sprintf(
		"Hi %s,\n\n%s\n\nService: %s\nCustomer: %s\nProvider: %s\nWhen: %s\nLocation: %s\nBooking total: %s\nNet paid: %s\nRemaining balance: %s\n\n%s: %s\n\n— TellBook",
		greetingName, copy.Body, data.ServiceTitle, data.CustomerName, data.ProviderName,
		data.When, data.Location+contactText, data.Total, data.Paid, data.Due, copy.Action, data.ActionURL,
	)
	htmlBody, err := renderNotificationHTML(data, copy, greetingName)
	if err != nil {
		return mailer.Message{}, invalidEmailContent(fmt.Errorf("render notification email: %w", err))
	}
	return mailer.Message{
		ToEmail: data.RecipientEmail, ToName: data.RecipientName, Subject: copy.Subject,
		Text: textBody, HTML: htmlBody,
		MessageID: "<notification-" + deliveryID.String() + "@mail.tellbook.app>",
	}, nil
}

func formatEmailMoney(amount int64, countryCode, currencyCode string) (string, error) {
	market, ok := markets.DefaultCatalog().Lookup(countryCode)
	if !ok {
		return "", fmt.Errorf("unsupported booking market %q", countryCode)
	}
	for _, currency := range market.Currencies {
		if currency.Code == currencyCode {
			return money.Format(amount, money.FormatSpec{
				CurrencyCode: currency.Code, Symbol: currency.Symbol,
				Exponent: currency.MinorUnitExponent, DecimalSeparator: currency.DecimalSeparator,
				GroupingSeparator: currency.GroupingSeparator, SymbolPosition: currency.SymbolPosition,
				SpaceBetweenSymbol: currency.SpaceBetweenSymbol,
			})
		}
	}
	return "", fmt.Errorf("currency %q is not configured for market %q", currencyCode, countryCode)
}
