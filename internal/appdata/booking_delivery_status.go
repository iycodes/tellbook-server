package appdata

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) loadBookingDeliveryStatus(
	ctx context.Context,
	bookingID uuid.UUID,
) (PublicBookingDeliveryStatus, error) {
	status := PublicBookingDeliveryStatus{
		ProviderInApp:    BookingDeliveryChannelStatus{Requested: true, Eligible: true, Status: "accepted"},
		ProviderEmail:    BookingDeliveryChannelStatus{Status: "not_requested"},
		ProviderWhatsApp: BookingDeliveryChannelStatus{Status: "not_requested"},
		CustomerEmail:    BookingDeliveryChannelStatus{Status: "not_requested"},
		CustomerWhatsApp: BookingDeliveryChannelStatus{Status: "not_requested"},
		CustomerSMS:      BookingDeliveryChannelStatus{Status: "not_requested"},
	}
	rows, err := r.db.Query(ctx, `
		SELECT
			booking.email_reminder_consent,
			booking.whatsapp_consent,
			booking.sms_consent,
			COALESCE(delivery.audience_type,''),
			COALESCE(delivery.channel,''),
			COALESCE(delivery.status,'')
		FROM bookings booking
		LEFT JOIN LATERAL (
			SELECT DISTINCT ON (candidate.audience_type,candidate.channel)
				candidate.audience_type,candidate.channel,candidate.status
			FROM notification_deliveries candidate
			WHERE candidate.booking_id=booking.id
			ORDER BY candidate.audience_type,candidate.channel,candidate.updated_at DESC,candidate.id DESC
		) delivery ON TRUE
		WHERE booking.id=$1
	`, bookingID)
	if err != nil {
		return PublicBookingDeliveryStatus{}, fmt.Errorf("load booking delivery status: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var emailRequested, whatsAppRequested, smsRequested bool
		var audience, channel, deliveryStatus string
		if err := rows.Scan(
			&emailRequested, &whatsAppRequested, &smsRequested,
			&audience, &channel, &deliveryStatus,
		); err != nil {
			return PublicBookingDeliveryStatus{}, fmt.Errorf("scan booking delivery status: %w", err)
		}
		if !found {
			status.CustomerEmail.Requested = emailRequested
			status.CustomerWhatsApp.Requested = whatsAppRequested
			status.CustomerSMS.Requested = smsRequested
			found = true
		}
		if audience == "" || channel == "" {
			continue
		}
		item := deliveryChannelStatus(deliveryStatus)
		switch audience + ":" + channel {
		case "provider:email":
			status.ProviderEmail = item
		case "provider:whatsapp":
			status.ProviderWhatsApp = item
		case "customer:email":
			status.CustomerEmail = item
		case "customer:whatsapp":
			status.CustomerWhatsApp = item
		}
	}
	if err := rows.Err(); err != nil {
		return PublicBookingDeliveryStatus{}, fmt.Errorf("iterate booking delivery status: %w", err)
	}
	if !found {
		return PublicBookingDeliveryStatus{}, pgx.ErrNoRows
	}
	return status, nil
}

func deliveryChannelStatus(value string) BookingDeliveryChannelStatus {
	status := value
	switch value {
	case "pending", "processing", "dispatching", "retry":
		status = "pending"
	case "accepted", "sent", "delivered", "read", "failed", "unknown", "manual_review", "cancelled":
	case "deleted":
		status = "failed"
	default:
		status = "not_requested"
	}
	return BookingDeliveryChannelStatus{
		Requested: true,
		Eligible:  status != "not_requested",
		Status:    status,
	}
}
