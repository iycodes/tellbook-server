package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrMarketplaceBookingOwned = errors.New("marketplace booking belongs to another customer")

type marketplaceBookingListCursor struct {
	StartsAt time.Time `json:"starts_at"`
	ID       uuid.UUID `json:"id"`
}

func (r *Repository) ListMarketplaceCustomerBookings(
	ctx context.Context,
	customerID uuid.UUID,
	status MarketplaceBookingStatus,
	rawCursor string,
	limit int,
	includeCounts bool,
) (MarketplaceBookingListResponse, error) {
	fingerprint := keysetFilterFingerprint("marketplace-customer-bookings", customerID.String(), string(status))
	var cursor marketplaceBookingListCursor
	if err := decodeKeysetCursor(rawCursor, fingerprint, &cursor); err != nil {
		return MarketplaceBookingListResponse{}, err
	}
	var cursorStartsAt any
	var cursorID any
	if strings.TrimSpace(rawCursor) != "" {
		if cursor.StartsAt.IsZero() || cursor.ID == uuid.Nil {
			return MarketplaceBookingListResponse{}, ErrInvalidKeysetCursor
		}
		cursorStartsAt, cursorID = cursor.StartsAt.UTC(), cursor.ID
	}
	rows, err := r.db.Query(ctx, `
		WITH page AS MATERIALIZED (
			SELECT b.*
			FROM bookings b
			WHERE b.marketplace_customer_id=$1
			  AND (
				($2='upcoming' AND b.status NOT IN ('cancelled','canceled','declined','expired') AND b.end_at >= NOW()) OR
				($2='past' AND b.status NOT IN ('cancelled','canceled','declined','expired') AND b.end_at < NOW()) OR
				($2='cancelled' AND b.status IN ('cancelled','canceled','declined','expired'))
			  )
			  AND ($3::timestamptz IS NULL OR
				($2='upcoming' AND (b.start_at, b.id) > ($3, $4::uuid)) OR
				($2<>'upcoming' AND (b.start_at, b.id) < ($3, $4::uuid)))
			ORDER BY
				CASE WHEN $2='upcoming' THEN b.start_at END ASC,
				CASE WHEN $2<>'upcoming' THEN b.start_at END DESC,
				CASE WHEN $2='upcoming' THEN b.id END ASC,
				CASE WHEN $2<>'upcoming' THEN b.id END DESC
			LIMIT $5
		)
		SELECT b.id, b.status, COALESCE(b.service_id::text, ''), b.title,
			COALESCE(b.image_url, service.image_url, ''), b.client_id,
			profile.business_name, handle.handle_slug, COALESCE(profile.avatar_url, ''),
			b.start_at, b.end_at, b.timezone, b.duration_minutes, b.location_label,
			b.fulfillment_mode, b.total_amount_minor,
			GREATEST(COALESCE(payment_totals.gross_paid_minor, 0) - COALESCE(payment_totals.adjusted_minor, 0), 0),
			b.currency_code, b.payment_status, b.agreement_status,
			CASE
				WHEN COALESCE(payment_totals.attention_refund_request, FALSE) THEN 'attention_required'
				WHEN COALESCE(payment_totals.failed_refund_request, FALSE) THEN 'failed'
				WHEN COALESCE(payment_totals.pending_adjustment, FALSE)
					OR COALESCE(payment_totals.pending_refund_request, FALSE) THEN 'pending'
				WHEN COALESCE(payment_totals.adjusted_minor, 0) > 0
					AND GREATEST(COALESCE(payment_totals.gross_paid_minor, 0) - COALESCE(payment_totals.adjusted_minor, 0), 0) = 0 THEN 'refunded'
				WHEN COALESCE(payment_totals.adjusted_minor, 0) > 0 THEN 'partially_refunded'
				ELSE ''
			END,
			COALESCE(latest_review.status, '')
		FROM page b
		INNER JOIN client_profiles profile ON profile.client_id=b.client_id
		INNER JOIN client_profile_handles handle ON handle.client_id=b.client_id
		LEFT JOIN services service ON service.id=b.service_id
		LEFT JOIN LATERAL (
			SELECT
				COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
					WHERE payment.booking_id=b.id AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')), 0) AS gross_paid_minor,
				COALESCE((SELECT SUM(adjustment.allocation_impact_minor) FROM payment_adjustments adjustment
					INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=b.id AND adjustment.status='successful'), 0) AS adjusted_minor,
				EXISTS (SELECT 1 FROM payment_adjustments adjustment
					INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=b.id AND adjustment.status='pending') AS pending_adjustment,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status IN ('queued','processing')) AS pending_refund_request,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status='manual_review') AS attention_refund_request,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status='failed') AS failed_refund_request
		) payment_totals ON TRUE
		LEFT JOIN LATERAL (
			SELECT review.status FROM provider_reviews review
			WHERE review.booking_id=b.id
			ORDER BY review.created_at DESC, review.id DESC LIMIT 1
		) latest_review ON TRUE
		ORDER BY
			CASE WHEN $2='upcoming' THEN b.start_at END ASC,
			CASE WHEN $2<>'upcoming' THEN b.start_at END DESC,
			CASE WHEN $2='upcoming' THEN b.id END ASC,
			CASE WHEN $2<>'upcoming' THEN b.id END DESC
	`, customerID, status, cursorStartsAt, cursorID, limit+1)
	if err != nil {
		return MarketplaceBookingListResponse{}, fmt.Errorf("list marketplace customer bookings: %w", err)
	}
	defer rows.Close()

	items := make([]MarketplaceBookingListItem, 0, limit)
	for rows.Next() {
		var item MarketplaceBookingListItem
		var total, paid int64
		if err := rows.Scan(
			&item.ID, &item.Status, &item.ServiceID, &item.ServiceTitle, &item.ServiceImageURL,
			&item.ProviderID, &item.ProviderName, &item.ProviderHandle, &item.ProviderAvatarURL,
			&item.StartsAt, &item.EndsAt, &item.Timezone, &item.DurationMinutes,
			&item.LocationLabel, &item.FulfillmentMode, &total, &paid, &item.CurrencyCode,
			&item.PaymentStatus, &item.AgreementStatus, &item.RefundStatus, &item.ReviewStatus,
		); err != nil {
			return MarketplaceBookingListResponse{}, fmt.Errorf("scan marketplace customer booking: %w", err)
		}
		item.StatusGroup = status
		item.TotalAmountMinor = money.Minor(total)
		item.NetPaidAmountMinor = money.Minor(paid)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MarketplaceBookingListResponse{}, fmt.Errorf("iterate marketplace customer bookings: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	response := MarketplaceBookingListResponse{Items: items}
	if hasMore {
		last := items[len(items)-1]
		lastID, err := uuid.Parse(last.ID)
		if err != nil {
			return MarketplaceBookingListResponse{}, fmt.Errorf("encode marketplace booking cursor: %w", err)
		}
		response.NextCursor, err = encodeKeysetCursor(fingerprint, marketplaceBookingListCursor{StartsAt: last.StartsAt.UTC(), ID: lastID})
		if err != nil {
			return MarketplaceBookingListResponse{}, fmt.Errorf("encode marketplace booking cursor: %w", err)
		}
	}
	if includeCounts {
		counts, err := r.marketplaceBookingCounts(ctx, customerID)
		if err != nil {
			return MarketplaceBookingListResponse{}, err
		}
		response.Counts = &counts
	}
	return response, nil
}

func (r *Repository) marketplaceBookingCounts(ctx context.Context, customerID uuid.UUID) (MarketplaceBookingCounts, error) {
	var counts MarketplaceBookingCounts
	if err := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status NOT IN ('cancelled','canceled','declined','expired') AND end_at >= NOW())::int,
			COUNT(*) FILTER (WHERE status NOT IN ('cancelled','canceled','declined','expired') AND end_at < NOW())::int,
			COUNT(*) FILTER (WHERE status IN ('cancelled','canceled','declined','expired'))::int
		FROM bookings WHERE marketplace_customer_id=$1
	`, customerID).Scan(&counts.Upcoming, &counts.Past, &counts.Cancelled); err != nil {
		return MarketplaceBookingCounts{}, fmt.Errorf("count marketplace customer bookings: %w", err)
	}
	return counts, nil
}

func (r *Repository) GetMarketplaceCustomerBooking(
	ctx context.Context,
	customerID, bookingID uuid.UUID,
) (MarketplaceBookingDetail, error) {
	var detail MarketplaceBookingDetail
	var total, paid int64
	var servicePublished bool
	var agreementRequired bool
	var agreementCompletedAt *time.Time
	var cancellationNoticeMinutes, rescheduleNoticeMinutes int
	var cancellationRefundBPS, rescheduleFeeMinor int64
	var automatedReschedule bool
	err := r.db.QueryRow(ctx, `
		SELECT b.id, b.status, COALESCE(b.service_id::text, ''), b.title,
			COALESCE(b.image_url, service.image_url, ''), b.client_id,
			profile.business_name, handle.handle_slug, COALESCE(profile.avatar_url, ''),
			b.start_at, b.end_at, b.timezone, b.duration_minutes, b.location_label,
			b.fulfillment_mode, b.total_amount_minor,
			GREATEST(COALESCE(payment_totals.gross_paid_minor, 0) - COALESCE(payment_totals.adjusted_minor, 0), 0),
			b.currency_code, b.payment_status, b.agreement_status,
			CASE
				WHEN COALESCE(payment_totals.attention_refund_request, FALSE) THEN 'attention_required'
				WHEN COALESCE(payment_totals.failed_refund_request, FALSE) THEN 'failed'
				WHEN COALESCE(payment_totals.pending_adjustment, FALSE)
					OR COALESCE(payment_totals.pending_refund_request, FALSE) THEN 'pending'
				WHEN COALESCE(payment_totals.adjusted_minor, 0) > 0
					AND GREATEST(COALESCE(payment_totals.gross_paid_minor, 0) - COALESCE(payment_totals.adjusted_minor, 0), 0) = 0 THEN 'refunded'
				WHEN COALESCE(payment_totals.adjusted_minor, 0) > 0 THEN 'partially_refunded'
				ELSE ''
			END,
			COALESCE(review.status, ''), b.public_token, b.source, customer.full_name,
			customer.email, customer.phone, b.notes, COALESCE(service.prep_aftercare_instructions, ''),
			b.cancellation_policy_snapshot, b.lateness_policy_snapshot,
			b.provider_latitude::double precision, b.provider_longitude::double precision,
			b.customer_latitude::double precision, b.customer_longitude::double precision,
			b.virtual_delivery_label, COALESCE(b.virtual_join_url, ''), COALESCE(b.virtual_instructions, ''),
			COALESCE(agreement.title_snapshot, b.agreement_title_snapshot),
			COALESCE(agreement.status, b.agreement_status), agreement.completed_at,
			COALESCE(agreement.pdf_status='ready', FALSE),
			COALESCE(conversation.id::text, ''), COALESCE(review.id::text, ''),
			COALESCE(service.status='published' AND service.is_active AND NOT service.is_hidden, FALSE),
			(NULLIF(BTRIM(b.agreement_title_snapshot), '') IS NOT NULL OR b.standalone_signature_required_snapshot),
			b.cancellation_notice_minutes_snapshot, b.cancellation_refund_bps_snapshot,
			b.reschedule_notice_minutes_snapshot, b.reschedule_fee_minor_snapshot,
			b.automated_reschedule_snapshot
		FROM bookings b
		INNER JOIN client_profiles profile ON profile.client_id=b.client_id
		INNER JOIN client_profile_handles handle ON handle.client_id=b.client_id
		INNER JOIN customers customer ON customer.id=b.customer_id
		LEFT JOIN services service ON service.id=b.service_id
		LEFT JOIN LATERAL (
			SELECT
				COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
					WHERE payment.booking_id=b.id AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')), 0) AS gross_paid_minor,
				COALESCE((SELECT SUM(adjustment.allocation_impact_minor) FROM payment_adjustments adjustment
					INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=b.id AND adjustment.status='successful'), 0) AS adjusted_minor,
				EXISTS (SELECT 1 FROM payment_adjustments adjustment
					INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=b.id AND adjustment.status='pending') AS pending_adjustment,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status IN ('queued','processing')) AS pending_refund_request,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status='manual_review') AS attention_refund_request,
				EXISTS (SELECT 1 FROM booking_refund_requests request
					WHERE request.booking_id=b.id AND request.status='failed') AS failed_refund_request
		) payment_totals ON TRUE
		LEFT JOIN LATERAL (
			SELECT instance.title_snapshot, instance.status, instance.completed_at, instance.pdf_status
			FROM agreement_instances instance WHERE instance.booking_id=b.id
			ORDER BY instance.created_at DESC LIMIT 1
		) agreement ON TRUE
		LEFT JOIN inbox_conversation_bookings conversation_link ON conversation_link.booking_id=b.id
		LEFT JOIN inbox_conversations conversation ON conversation.id=conversation_link.conversation_id
		LEFT JOIN LATERAL (
			SELECT provider_review.id, provider_review.status
			FROM provider_reviews provider_review WHERE provider_review.booking_id=b.id
			ORDER BY provider_review.created_at DESC LIMIT 1
		) review ON TRUE
		WHERE b.id=$1 AND b.marketplace_customer_id=$2
	`, bookingID, customerID).Scan(
		&detail.ID, &detail.Status, &detail.ServiceID, &detail.ServiceTitle, &detail.ServiceImageURL,
		&detail.ProviderID, &detail.ProviderName, &detail.ProviderHandle, &detail.ProviderAvatarURL,
		&detail.StartsAt, &detail.EndsAt, &detail.Timezone, &detail.DurationMinutes,
		&detail.LocationLabel, &detail.FulfillmentMode, &total, &paid, &detail.CurrencyCode,
		&detail.PaymentStatus, &detail.AgreementStatus, &detail.RefundStatus, &detail.ReviewStatus,
		&detail.BookingToken, &detail.Source, &detail.CustomerName, &detail.CustomerEmail,
		&detail.CustomerPhone, &detail.Notes, &detail.Preparation, &detail.CancellationPolicy,
		&detail.LatenessPolicy, &detail.ProviderLatitude, &detail.ProviderLongitude,
		&detail.CustomerLatitude, &detail.CustomerLongitude, &detail.VirtualDeliveryLabel,
		&detail.VirtualJoinURL, &detail.VirtualInstructions, &detail.Agreement.Title,
		&detail.Agreement.Status, &agreementCompletedAt, &detail.Agreement.PDFAvailable,
		&detail.ConversationID, &detail.ReviewID, &servicePublished, &agreementRequired,
		&cancellationNoticeMinutes, &cancellationRefundBPS, &rescheduleNoticeMinutes,
		&rescheduleFeeMinor, &automatedReschedule,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MarketplaceBookingDetail{}, ErrNotFound
	}
	if err != nil {
		return MarketplaceBookingDetail{}, fmt.Errorf("get marketplace customer booking: %w", err)
	}
	detail.StatusGroup = marketplaceBookingStatusGroup(detail.Status, detail.EndsAt, time.Now())
	detail.TotalAmountMinor = money.Minor(total)
	detail.NetPaidAmountMinor = money.Minor(paid)
	detail.Payment = MarketplaceBookingPayment{
		Status: detail.PaymentStatus, TotalAmountMinor: money.Minor(total),
		NetPaidAmountMinor: money.Minor(paid), OutstandingAmountMinor: money.Minor(max64(total-paid, 0)),
		CurrencyCode: detail.CurrencyCode, ReceiptAvailable: paid > 0,
	}
	detail.Agreement.CompletedAt = agreementCompletedAt
	cancelled := detail.StatusGroup == MarketplaceBookingCancelled
	agreementSatisfied := !agreementRequired || detail.AgreementStatus == "accepted" || detail.AgreementStatus == "signed" || detail.Agreement.Status == "completed"
	paymentSatisfied := initialBookingObligationSatisfied(detail.PaymentStatus)
	if !paymentSatisfied || !agreementSatisfied {
		detail.VirtualJoinURL = ""
		detail.VirtualInstructions = ""
	}
	permissions := bookingdomain.Permissions(detail.Status, detail.StartsAt, detail.EndsAt, time.Now(), bookingdomain.ChangePolicy{
		CancellationNoticeMinutes: cancellationNoticeMinutes, CancellationRefundBPS: cancellationRefundBPS,
		RescheduleNoticeMinutes: rescheduleNoticeMinutes, RescheduleFeeMinor: rescheduleFeeMinor,
		AutomatedReschedule: automatedReschedule,
	})
	detail.AllowedActions = MarketplaceBookingAllowedActions{
		AddToCalendar:   paymentSatisfied && agreementSatisfied && !cancelled,
		Reschedule:      permissions.CustomerReschedule && servicePublished,
		Cancel:          permissions.CustomerCancel,
		PayBalance:      paid < total && !cancelled,
		DownloadReceipt: paid > 0,
		ViewAgreement:   strings.TrimSpace(detail.Agreement.Title) != "",
		GetDirections:   detail.FulfillmentMode != "virtual" && marketplaceBookingHasCoordinates(detail),
		Rebook:          servicePublished,
		Review:          detail.Status == "completed" && detail.EndsAt.Before(time.Now()) && detail.ReviewID == "",
		Message:         true,
		CheckIn:         false,
	}
	paymentHistory, refundHistory, changeHistory, err := r.loadBookingHistories(ctx, bookingID)
	if err != nil {
		return MarketplaceBookingDetail{}, err
	}
	detail.PaymentHistory = paymentHistory
	detail.RefundHistory = refundHistory
	detail.ChangeHistory = changeHistory
	return detail, nil
}

func (r *Repository) ClaimMarketplaceBooking(ctx context.Context, customerID uuid.UUID, bookingToken string) (uuid.UUID, error) {
	bookingToken = strings.TrimSpace(bookingToken)
	if bookingToken == "" {
		return uuid.Nil, ErrNotFound
	}
	var bookingID uuid.UUID
	var ownerID uuid.NullUUID
	if err := r.db.QueryRow(ctx, `
		SELECT id, marketplace_customer_id FROM bookings WHERE public_token=$1
	`, bookingToken).Scan(&bookingID, &ownerID); errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	} else if err != nil {
		return uuid.Nil, fmt.Errorf("load marketplace booking claim: %w", err)
	}
	if ownerID.Valid {
		if ownerID.UUID == customerID {
			return bookingID, nil
		}
		return uuid.Nil, ErrMarketplaceBookingOwned
	}
	command, err := r.db.Exec(ctx, `
		UPDATE bookings SET marketplace_customer_id=$1, updated_at=NOW()
		WHERE id=$2 AND marketplace_customer_id IS NULL
	`, customerID, bookingID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("claim marketplace booking: %w", err)
	}
	if command.RowsAffected() != 1 {
		return uuid.Nil, ErrMarketplaceBookingOwned
	}
	return bookingID, nil
}

func (r *Repository) GetMarketplaceBookingReceipt(
	ctx context.Context,
	customerID, bookingID uuid.UUID,
) (MarketplaceBookingReceipt, error) {
	var receipt MarketplaceBookingReceipt
	var total, paid, refunded int64
	var issuedAt *time.Time
	err := r.db.QueryRow(ctx, `
		SELECT b.id, profile.business_name, customer.full_name, customer.email, b.title,
			b.start_at, b.timezone, b.currency_code, b.total_amount_minor,
			GREATEST(
				COALESCE((SELECT SUM(payment.amount_minor) FROM payments payment
					WHERE payment.booking_id=b.id AND payment.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0)
				- COALESCE((SELECT SUM(adjustment.allocation_impact_minor) FROM payment_adjustments adjustment
					INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
					WHERE adjusted_payment.booking_id=b.id AND adjustment.status='successful'),0), 0),
			COALESCE((SELECT SUM(adjustment.allocation_impact_minor) FROM payment_adjustments adjustment
				INNER JOIN payments adjusted_payment ON adjusted_payment.id=adjustment.payment_id
				WHERE adjusted_payment.booking_id=b.id AND adjustment.status='successful'),0),
			(SELECT MAX(payment.paid_at) FROM payments payment WHERE payment.booking_id=b.id)
		FROM bookings b
		INNER JOIN client_profiles profile ON profile.client_id=b.client_id
		INNER JOIN customers customer ON customer.id=b.customer_id
		WHERE b.id=$1 AND b.marketplace_customer_id=$2
	`, bookingID, customerID).Scan(
		&receipt.BookingID, &receipt.ProviderName, &receipt.CustomerName, &receipt.CustomerEmail,
		&receipt.ServiceTitle, &receipt.StartsAt, &receipt.Timezone, &receipt.CurrencyCode, &total, &paid, &refunded, &issuedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MarketplaceBookingReceipt{}, ErrNotFound
	}
	if err != nil {
		return MarketplaceBookingReceipt{}, fmt.Errorf("get marketplace booking receipt: %w", err)
	}
	if paid <= 0 || issuedAt == nil {
		return MarketplaceBookingReceipt{}, ErrNotFound
	}
	receipt.ReceiptNumber = "TB-" + strings.ToUpper(strings.ReplaceAll(receipt.BookingID, "-", "")[:12])
	receipt.TotalAmountMinor = money.Minor(total)
	receipt.NetPaidAmountMinor = money.Minor(paid)
	receipt.RefundedAmountMinor = money.Minor(refunded)
	receipt.IssuedAt = issuedAt.UTC()

	rows, err := r.db.Query(ctx, `
		SELECT reference, purpose, method, amount_minor, paid_at
		FROM payments
		WHERE booking_id=$1 AND status IN ('paid','partially_refunded','refunded','disputed','reversed')
		  AND paid_at IS NOT NULL
		ORDER BY paid_at, id
	`, bookingID)
	if err != nil {
		return MarketplaceBookingReceipt{}, fmt.Errorf("list marketplace receipt payments: %w", err)
	}
	defer rows.Close()
	receipt.Payments = make([]MarketplaceBookingReceiptPayment, 0)
	for rows.Next() {
		var payment MarketplaceBookingReceiptPayment
		var amount int64
		if err := rows.Scan(&payment.Reference, &payment.Purpose, &payment.Method, &amount, &payment.PaidAt); err != nil {
			return MarketplaceBookingReceipt{}, fmt.Errorf("scan marketplace receipt payment: %w", err)
		}
		payment.AmountMinor = money.Minor(amount)
		receipt.Payments = append(receipt.Payments, payment)
	}
	return receipt, rows.Err()
}

func marketplaceBookingStatusGroup(status string, endsAt, now time.Time) MarketplaceBookingStatus {
	if status == "cancelled" || status == "canceled" || status == "declined" || status == "expired" {
		return MarketplaceBookingCancelled
	}
	if endsAt.Before(now) {
		return MarketplaceBookingPast
	}
	return MarketplaceBookingUpcoming
}

func marketplaceBookingHasCoordinates(detail MarketplaceBookingDetail) bool {
	if detail.FulfillmentMode == "virtual" {
		return false
	}
	if detail.FulfillmentMode == "customer_location" {
		return detail.CustomerLatitude != nil && detail.CustomerLongitude != nil
	}
	return detail.ProviderLatitude != nil && detail.ProviderLongitude != nil
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
