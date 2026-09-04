package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/money"
	"booking/go-server/internal/publictoken"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrBookingActionNotAllowed = errors.New("booking action is not allowed")
	ErrBookingChangeExpired    = errors.New("booking change quote expired")
	ErrBookingChangeStale      = errors.New("booking changed after quote")
	ErrBookingChangeConsumed   = errors.New("booking change quote consumed")
	ErrBookingIdempotency      = errors.New("idempotency key was already used for another request")
	ErrAutomatedReschedule     = errors.New("booking policy does not support automated rescheduling")
)

const bookingChangeQuoteLifetime = 10 * time.Minute

type bookingChangeRecord struct {
	ID                        uuid.UUID
	ClientID                  uuid.UUID
	MarketplaceCustomerID     uuid.UUID
	ServiceID                 uuid.UUID
	ProviderHandle            string
	Status                    string
	StartsAt                  time.Time
	EndsAt                    time.Time
	OccupiedStartsAt          time.Time
	OccupiedEndsAt            time.Time
	UpdatedAt                 time.Time
	Timezone                  string
	DurationMinutes           int
	PrepTimeMinutes           int
	BufferTimeMinutes         int
	CurrencyCode              string
	NetPaidMinor              int64
	PaymentStatus             string
	AgreementStatus           string
	AgreementRequired         bool
	CancellationNoticeMinutes int
	CancellationRefundBPS     int64
	RescheduleNoticeMinutes   int
	RescheduleFeeMinor        int64
	AutomatedReschedule       bool
}

func (record bookingChangeRecord) policy() bookingdomain.ChangePolicy {
	return bookingdomain.ChangePolicy{
		CancellationNoticeMinutes: record.CancellationNoticeMinutes,
		CancellationRefundBPS:     record.CancellationRefundBPS,
		RescheduleNoticeMinutes:   record.RescheduleNoticeMinutes,
		RescheduleFeeMinor:        record.RescheduleFeeMinor,
		AutomatedReschedule:       record.AutomatedReschedule,
	}
}

type bookingChangeQuoteRecord struct {
	ID                       uuid.UUID
	PublicToken              string
	BookingID                uuid.UUID
	MarketplaceCustomerID    uuid.UUID
	Kind                     string
	BookingUpdatedAt         time.Time
	CurrentStartsAt          time.Time
	CurrentEndsAt            time.Time
	ProposedStartsAt         *time.Time
	ProposedEndsAt           *time.Time
	ProposedOccupiedStartsAt *time.Time
	ProposedOccupiedEndsAt   *time.Time
	RefundAmountMinor        int64
	RetainedAmountMinor      int64
	FeeAmountMinor           int64
	CurrencyCode             string
	PolicyMessage            string
	ExpiresAt                time.Time
	ConsumedAt               *time.Time
}

func (r *Repository) CreateMarketplaceCancellationQuote(
	ctx context.Context,
	customerID, bookingID, idempotencyKey uuid.UUID,
) (MarketplaceBookingChangeQuote, error) {
	return r.createMarketplaceBookingChangeQuote(ctx, customerID, bookingID, idempotencyKey, "cancellation", nil)
}

func (r *Repository) CreateMarketplaceRescheduleQuote(
	ctx context.Context,
	customerID, bookingID, idempotencyKey uuid.UUID,
	proposedStartsAt time.Time,
) (MarketplaceBookingChangeQuote, error) {
	return r.createMarketplaceBookingChangeQuote(ctx, customerID, bookingID, idempotencyKey, "reschedule", &proposedStartsAt)
}

func (r *Repository) GetMarketplaceRescheduleAvailability(
	ctx context.Context,
	customerID, bookingID uuid.UUID,
	from *time.Time,
	days int,
) (PublicAvailabilityRangeResponse, error) {
	if days < 1 || days > 31 {
		return PublicAvailabilityRangeResponse{}, fmt.Errorf("days must be between 1 and 31")
	}
	booking, err := loadMarketplaceBookingChangeRecord(ctx, r.db, customerID, bookingID, false)
	if err != nil {
		return PublicAvailabilityRangeResponse{}, err
	}
	permissions := bookingdomain.Permissions(booking.Status, booking.StartsAt, booking.EndsAt, time.Now().UTC(), booking.policy())
	if !permissions.CustomerReschedule {
		return PublicAvailabilityRangeResponse{}, ErrBookingActionNotAllowed
	}
	service, err := getPublicServiceForBooking(ctx, r.db, booking.ProviderHandle, booking.ServiceID)
	if err != nil {
		return PublicAvailabilityRangeResponse{}, err
	}
	service.DurationMinutes = booking.DurationMinutes
	service.PrepTimeMinutes = booking.PrepTimeMinutes
	service.BufferTimeMinutes = booking.BufferTimeMinutes
	now := time.Now().UTC()
	states, err := loadPublicAvailabilityRangeStatesExcluding(ctx, r.db, service, from, days, now, &booking.ID)
	if err != nil {
		return PublicAvailabilityRangeResponse{}, err
	}
	dates := make([]PublicAvailabilityDay, 0, len(states))
	for _, state := range states {
		slots, err := publicAvailabilitySlots(service, state, now)
		if err != nil {
			return PublicAvailabilityRangeResponse{}, err
		}
		dates = append(dates, PublicAvailabilityDay{Date: state.Date.Format("2006-01-02"), Slots: slots})
	}
	return PublicAvailabilityRangeResponse{
		ServiceID: service.ID.String(), From: states[0].Date.Format("2006-01-02"), Days: days,
		Timezone: service.Timezone, CurrencyCode: service.CurrencyCode,
		DurationMinutes: booking.DurationMinutes, LocationLabel: publicServiceLocationLabel(service), Dates: dates,
	}, nil
}

func (r *Repository) createMarketplaceBookingChangeQuote(
	ctx context.Context,
	customerID, bookingID, idempotencyKey uuid.UUID,
	kind string,
	proposedStartsAt *time.Time,
) (MarketplaceBookingChangeQuote, error) {
	fingerprint := bookingChangeFingerprint(bookingID, kind, proposedStartsAt)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return MarketplaceBookingChangeQuote{}, fmt.Errorf("begin booking change quote: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockBookingChangeIdempotency(ctx, tx, "quote:"+kind, customerID, idempotencyKey); err != nil {
		return MarketplaceBookingChangeQuote{}, err
	}
	if existing, existingFingerprint, err := loadBookingChangeQuoteByIdempotency(ctx, tx, customerID, kind, idempotencyKey); err == nil {
		if existingFingerprint != fingerprint || existing.BookingID != bookingID {
			return MarketplaceBookingChangeQuote{}, ErrBookingIdempotency
		}
		response := bookingChangeQuoteResponse(existing)
		return response, tx.Commit(ctx)
	} else if !errors.Is(err, ErrNotFound) {
		return MarketplaceBookingChangeQuote{}, err
	}

	booking, err := loadMarketplaceBookingChangeRecord(ctx, tx, customerID, bookingID, false)
	if err != nil {
		return MarketplaceBookingChangeQuote{}, err
	}
	now := time.Now().UTC()
	permissions := bookingdomain.Permissions(booking.Status, booking.StartsAt, booking.EndsAt, now, booking.policy())
	quote := bookingChangeQuoteRecord{
		ID: uuid.New(), BookingID: booking.ID, MarketplaceCustomerID: customerID, Kind: kind,
		BookingUpdatedAt: booking.UpdatedAt, CurrentStartsAt: booking.StartsAt, CurrentEndsAt: booking.EndsAt,
		CurrencyCode: booking.CurrencyCode, ExpiresAt: now.Add(bookingChangeQuoteLifetime),
	}
	if kind == "cancellation" {
		if !permissions.CustomerCancel {
			return MarketplaceBookingChangeQuote{}, ErrBookingActionNotAllowed
		}
		quote.RefundAmountMinor = bookingdomain.CancellationRefund(booking.NetPaidMinor, booking.StartsAt, now, booking.policy())
		quote.RetainedAmountMinor = booking.NetPaidMinor - quote.RefundAmountMinor
		quote.PolicyMessage = cancellationPolicyMessage(booking.NetPaidMinor, quote.RefundAmountMinor)
	} else {
		if !booking.AutomatedReschedule {
			return MarketplaceBookingChangeQuote{}, ErrAutomatedReschedule
		}
		if !permissions.CustomerReschedule || proposedStartsAt == nil || !proposedStartsAt.After(now) {
			return MarketplaceBookingChangeQuote{}, ErrBookingActionNotAllowed
		}
		service, err := getPublicServiceForBooking(ctx, tx, booking.ProviderHandle, booking.ServiceID)
		if err != nil {
			return MarketplaceBookingChangeQuote{}, err
		}
		service.DurationMinutes = booking.DurationMinutes
		service.PrepTimeMinutes = booking.PrepTimeMinutes
		service.BufferTimeMinutes = booking.BufferTimeMinutes
		state, err := loadPublicAvailabilityStateExcluding(ctx, tx, service, *proposedStartsAt, now, &booking.ID)
		if err != nil {
			return MarketplaceBookingChangeQuote{}, err
		}
		var selected *bookingdomain.AvailableSlot
		for index := range state.Slots {
			if state.Slots[index].Start.Equal(proposedStartsAt.UTC()) {
				selected = &state.Slots[index]
				break
			}
		}
		if selected == nil {
			return MarketplaceBookingChangeQuote{}, ErrSlotUnavailable
		}
		quote.ProposedStartsAt = &selected.Start
		quote.ProposedEndsAt = &selected.End
		quote.ProposedOccupiedStartsAt = &selected.OccupiedStart
		quote.ProposedOccupiedEndsAt = &selected.OccupiedEnd
		quote.FeeAmountMinor = booking.RescheduleFeeMinor
		if quote.FeeAmountMinor == 0 {
			quote.PolicyMessage = "Your original booking price, including its short-notice pricing, is retained. No change fee applies."
		} else {
			quote.PolicyMessage = "Your original booking price, including its short-notice pricing, is retained. The change fee shown comes from the policy agreed at booking."
		}
	}

	token, err := publictoken.New()
	if err != nil {
		return MarketplaceBookingChangeQuote{}, fmt.Errorf("create booking change token: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO booking_change_quotes (
			id, public_token, booking_id, marketplace_customer_id, kind, idempotency_key,
			request_fingerprint, booking_updated_at, current_start_at, current_end_at,
			proposed_start_at, proposed_end_at, proposed_occupied_start_at, proposed_occupied_end_at,
			refund_amount_minor, retained_amount_minor, fee_amount_minor, currency_code,
			policy_message, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
	`, quote.ID, token, quote.BookingID, customerID, kind, idempotencyKey, fingerprint,
		quote.BookingUpdatedAt, quote.CurrentStartsAt, quote.CurrentEndsAt,
		quote.ProposedStartsAt, quote.ProposedEndsAt, quote.ProposedOccupiedStartsAt, quote.ProposedOccupiedEndsAt,
		quote.RefundAmountMinor, quote.RetainedAmountMinor, quote.FeeAmountMinor,
		quote.CurrencyCode, quote.PolicyMessage, quote.ExpiresAt)
	if err != nil {
		return MarketplaceBookingChangeQuote{}, fmt.Errorf("create booking change quote: %w", err)
	}
	response := bookingChangeQuoteResponse(quote)
	response.QuoteToken = token
	if err := tx.Commit(ctx); err != nil {
		return MarketplaceBookingChangeQuote{}, fmt.Errorf("commit booking change quote: %w", err)
	}
	return response, nil
}

func (r *Repository) ApplyMarketplaceBookingChange(
	ctx context.Context,
	customerID, bookingID uuid.UUID,
	quoteToken, command, reason string,
	idempotencyKey uuid.UUID,
) (BookingCommandResponse, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BookingCommandResponse{}, fmt.Errorf("begin booking change: %w", err)
	}
	defer tx.Rollback(ctx)
	commandFingerprint := bookingCommandFingerprint(bookingID, command, quoteToken, reason)
	if err := lockBookingChangeIdempotency(ctx, tx, "command:customer", customerID, idempotencyKey); err != nil {
		return BookingCommandResponse{}, err
	}
	if response, found, err := loadIdempotentBookingCommand(ctx, tx, "customer", customerID, bookingID, command, commandFingerprint, idempotencyKey); err != nil {
		return BookingCommandResponse{}, err
	} else if found {
		return response, tx.Commit(ctx)
	}
	quote, err := lockBookingChangeQuote(ctx, tx, customerID, bookingID, strings.TrimSpace(quoteToken))
	if err != nil {
		return BookingCommandResponse{}, err
	}
	expectedKind := map[string]string{"cancel": "cancellation", "reschedule": "reschedule"}[command]
	if expectedKind == "" || quote.Kind != expectedKind {
		return BookingCommandResponse{}, ErrBookingActionNotAllowed
	}
	now := time.Now().UTC()
	if quote.ConsumedAt != nil {
		return BookingCommandResponse{}, ErrBookingChangeConsumed
	}
	if !quote.ExpiresAt.After(now) {
		return BookingCommandResponse{}, ErrBookingChangeExpired
	}
	booking, err := loadMarketplaceBookingChangeRecord(ctx, tx, customerID, bookingID, true)
	if err != nil {
		return BookingCommandResponse{}, err
	}
	if !booking.UpdatedAt.Equal(quote.BookingUpdatedAt) {
		return BookingCommandResponse{}, ErrBookingChangeStale
	}
	permissions := bookingdomain.Permissions(booking.Status, booking.StartsAt, booking.EndsAt, now, booking.policy())
	response := BookingCommandResponse{
		BookingID: booking.ID.String(), Status: booking.Status, StartsAt: booking.StartsAt,
		EndsAt: booking.EndsAt, RefundAmountMinor: money.Minor(quote.RefundAmountMinor), CurrencyCode: booking.CurrencyCode,
	}
	if command == "cancel" {
		if !permissions.CustomerCancel {
			return BookingCommandResponse{}, ErrBookingActionNotAllowed
		}
		if _, err := tx.Exec(ctx, `UPDATE bookings SET status='cancelled', updated_at=NOW() WHERE id=$1`, booking.ID); err != nil {
			return BookingCommandResponse{}, fmt.Errorf("cancel booking: %w", err)
		}
		response.Status = "cancelled"
	} else {
		if !permissions.CustomerReschedule || quote.ProposedStartsAt == nil {
			return BookingCommandResponse{}, ErrBookingActionNotAllowed
		}
		lockKey := booking.ClientID.String() + ":" + quote.ProposedStartsAt.In(mustLocation(booking.Timezone)).Format("2006-01-02")
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return BookingCommandResponse{}, fmt.Errorf("lock reschedule day: %w", err)
		}
		service, err := getPublicServiceForBooking(ctx, tx, booking.ProviderHandle, booking.ServiceID)
		if err != nil {
			return BookingCommandResponse{}, err
		}
		service.DurationMinutes = booking.DurationMinutes
		service.PrepTimeMinutes = booking.PrepTimeMinutes
		service.BufferTimeMinutes = booking.BufferTimeMinutes
		state, err := loadPublicAvailabilityStateExcluding(ctx, tx, service, *quote.ProposedStartsAt, now, &booking.ID)
		if err != nil {
			return BookingCommandResponse{}, err
		}
		var selected *bookingdomain.AvailableSlot
		for index := range state.Slots {
			if state.Slots[index].Start.Equal(*quote.ProposedStartsAt) {
				selected = &state.Slots[index]
				break
			}
		}
		if selected == nil {
			return BookingCommandResponse{}, ErrSlotUnavailable
		}
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET start_at=$2, end_at=$3, occupied_start_at=$4,
				occupied_end_at=$5, updated_at=NOW() WHERE id=$1
		`, booking.ID, selected.Start, selected.End, selected.OccupiedStart, selected.OccupiedEnd); err != nil {
			return BookingCommandResponse{}, fmt.Errorf("reschedule booking: %w", err)
		}
		response.StartsAt, response.EndsAt = selected.Start, selected.End
	}
	commandID := uuid.New()
	if quote.RefundAmountMinor > 0 {
		response.RefundStatus = "queued"
	}
	if err := insertBookingCommand(ctx, tx, commandID, booking.ID, "customer", customerID, command, commandFingerprint, idempotencyKey, &quote.ID, reason, response); err != nil {
		return BookingCommandResponse{}, err
	}
	if quote.RefundAmountMinor > 0 {
		if err := insertBookingRefundRequest(ctx, tx, booking.ID, commandID, quote.RefundAmountMinor, booking.CurrencyCode, reason); err != nil {
			return BookingCommandResponse{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE booking_change_quotes SET consumed_at=NOW() WHERE id=$1`, quote.ID); err != nil {
		return BookingCommandResponse{}, fmt.Errorf("consume booking change quote: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BookingCommandResponse{}, fmt.Errorf("commit booking change: %w", err)
	}
	return response, nil
}

func (r *Repository) ApplyProviderBookingCommand(
	ctx context.Context,
	clientID, bookingID uuid.UUID,
	command, reason string,
	idempotencyKey uuid.UUID,
) (BookingCommandResponse, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BookingCommandResponse{}, fmt.Errorf("begin provider booking command: %w", err)
	}
	defer tx.Rollback(ctx)
	commandFingerprint := bookingCommandFingerprint(bookingID, command, "", reason)
	if err := lockBookingChangeIdempotency(ctx, tx, "command:provider", clientID, idempotencyKey); err != nil {
		return BookingCommandResponse{}, err
	}
	if response, found, err := loadIdempotentBookingCommand(ctx, tx, "provider", clientID, bookingID, command, commandFingerprint, idempotencyKey); err != nil {
		return BookingCommandResponse{}, err
	} else if found {
		return response, tx.Commit(ctx)
	}
	booking, err := loadProviderBookingChangeRecord(ctx, tx, clientID, bookingID)
	if err != nil {
		return BookingCommandResponse{}, err
	}
	permissions := bookingdomain.Permissions(booking.Status, booking.StartsAt, booking.EndsAt, time.Now().UTC(), booking.policy())
	allowed := map[string]bool{
		"confirm": permissions.ProviderConfirm && providerConfirmationObligationsSatisfied(booking), "decline": permissions.ProviderDecline,
		"complete": permissions.ProviderComplete, "mark_no_show": permissions.ProviderNoShow,
	}[command]
	if !allowed {
		return BookingCommandResponse{}, ErrBookingActionNotAllowed
	}
	status := map[string]string{
		"confirm": "confirmed", "decline": "declined", "complete": "completed", "mark_no_show": "no_show",
	}[command]
	if _, err := tx.Exec(ctx, `UPDATE bookings SET status=$2, updated_at=NOW() WHERE id=$1`, booking.ID, status); err != nil {
		return BookingCommandResponse{}, fmt.Errorf("apply provider booking command: %w", err)
	}
	refundAmount := int64(0)
	if command == "decline" {
		refundAmount = booking.NetPaidMinor
	}
	response := BookingCommandResponse{
		BookingID: booking.ID.String(), Status: status, StartsAt: booking.StartsAt, EndsAt: booking.EndsAt,
		RefundAmountMinor: money.Minor(refundAmount), CurrencyCode: booking.CurrencyCode,
	}
	if refundAmount > 0 {
		response.RefundStatus = "queued"
	}
	commandID := uuid.New()
	if err := insertBookingCommand(ctx, tx, commandID, booking.ID, "provider", clientID, command, commandFingerprint, idempotencyKey, nil, reason, response); err != nil {
		return BookingCommandResponse{}, err
	}
	if refundAmount > 0 {
		if err := insertBookingRefundRequest(ctx, tx, booking.ID, commandID, refundAmount, booking.CurrencyCode, reason); err != nil {
			return BookingCommandResponse{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return BookingCommandResponse{}, fmt.Errorf("commit provider booking command: %w", err)
	}
	return response, nil
}

func loadMarketplaceBookingChangeRecord(ctx context.Context, q publicBookingQuerier, customerID, bookingID uuid.UUID, forUpdate bool) (bookingChangeRecord, error) {
	query := bookingChangeRecordQuery + ` WHERE b.id=$1 AND b.marketplace_customer_id=$2`
	if forUpdate {
		query += ` FOR UPDATE OF b`
	}
	return scanBookingChangeRecord(q.QueryRow(ctx, query, bookingID, customerID))
}

func loadProviderBookingChangeRecord(ctx context.Context, q publicBookingQuerier, clientID, bookingID uuid.UUID) (bookingChangeRecord, error) {
	return scanBookingChangeRecord(q.QueryRow(ctx, bookingChangeRecordQuery+` WHERE b.id=$1 AND b.client_id=$2 FOR UPDATE OF b`, bookingID, clientID))
}

const bookingChangeRecordQuery = `
	SELECT b.id, b.client_id, COALESCE(b.marketplace_customer_id, '00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(b.service_id, '00000000-0000-0000-0000-000000000000'::uuid), handle.handle_slug,
		b.status, b.start_at, b.end_at, b.occupied_start_at, b.occupied_end_at, b.updated_at,
		b.timezone, b.duration_minutes, b.prep_time_minutes, b.buffer_time_minutes, b.currency_code,
		GREATEST(COALESCE(payment_totals.gross_paid_minor,0)-COALESCE(payment_totals.adjusted_minor,0),0),
		b.payment_status, b.agreement_status,
		(NULLIF(BTRIM(b.agreement_title_snapshot), '') IS NOT NULL OR b.standalone_signature_required_snapshot),
		b.cancellation_notice_minutes_snapshot, b.cancellation_refund_bps_snapshot,
		b.reschedule_notice_minutes_snapshot, b.reschedule_fee_minor_snapshot,
		b.automated_reschedule_snapshot
	FROM bookings b
	INNER JOIN client_profile_handles handle ON handle.client_id=b.client_id
	LEFT JOIN LATERAL (
		SELECT
			COALESCE((SELECT SUM(p.amount_minor) FROM payments p WHERE p.booking_id=b.id
				AND p.status IN ('paid','partially_refunded','refunded','disputed','reversed')),0) gross_paid_minor,
			COALESCE((SELECT SUM(a.allocation_impact_minor) FROM payment_adjustments a
				INNER JOIN payments ap ON ap.id=a.payment_id WHERE ap.booking_id=b.id
				AND a.status IN ('pending','successful')),0) adjusted_minor
	) payment_totals ON TRUE`

func scanBookingChangeRecord(row pgx.Row) (bookingChangeRecord, error) {
	var record bookingChangeRecord
	if err := row.Scan(
		&record.ID, &record.ClientID, &record.MarketplaceCustomerID, &record.ServiceID, &record.ProviderHandle,
		&record.Status, &record.StartsAt, &record.EndsAt, &record.OccupiedStartsAt, &record.OccupiedEndsAt,
		&record.UpdatedAt, &record.Timezone, &record.DurationMinutes, &record.PrepTimeMinutes,
		&record.BufferTimeMinutes, &record.CurrencyCode, &record.NetPaidMinor,
		&record.PaymentStatus, &record.AgreementStatus, &record.AgreementRequired,
		&record.CancellationNoticeMinutes, &record.CancellationRefundBPS,
		&record.RescheduleNoticeMinutes, &record.RescheduleFeeMinor, &record.AutomatedReschedule,
	); errors.Is(err, pgx.ErrNoRows) {
		return bookingChangeRecord{}, ErrNotFound
	} else if err != nil {
		return bookingChangeRecord{}, fmt.Errorf("load booking change record: %w", err)
	}
	return record, nil
}

func loadBookingChangeQuoteByIdempotency(ctx context.Context, q publicBookingQuerier, customerID uuid.UUID, kind string, key uuid.UUID) (bookingChangeQuoteRecord, string, error) {
	row := q.QueryRow(ctx, bookingChangeQuoteSelect+`
		WHERE marketplace_customer_id=$1 AND kind=$2 AND idempotency_key=$3
	`, customerID, kind, key)
	return scanBookingChangeQuote(row)
}

const bookingChangeQuoteSelect = `
	SELECT id, booking_id, marketplace_customer_id, kind, request_fingerprint, booking_updated_at,
		current_start_at, current_end_at, proposed_start_at, proposed_end_at,
		proposed_occupied_start_at, proposed_occupied_end_at, refund_amount_minor,
		retained_amount_minor, fee_amount_minor, currency_code, policy_message, expires_at, consumed_at,
		public_token
	FROM booking_change_quotes`

func lockBookingChangeQuote(ctx context.Context, tx pgx.Tx, customerID, bookingID uuid.UUID, token string) (bookingChangeQuoteRecord, error) {
	quote, _, err := scanBookingChangeQuote(tx.QueryRow(ctx, bookingChangeQuoteSelect+`
		WHERE marketplace_customer_id=$1 AND booking_id=$2 AND public_token=$3 FOR UPDATE
	`, customerID, bookingID, token))
	return quote, err
}

func scanBookingChangeQuote(row pgx.Row) (bookingChangeQuoteRecord, string, error) {
	var quote bookingChangeQuoteRecord
	var fingerprint string
	if err := row.Scan(
		&quote.ID, &quote.BookingID, &quote.MarketplaceCustomerID, &quote.Kind, &fingerprint,
		&quote.BookingUpdatedAt, &quote.CurrentStartsAt, &quote.CurrentEndsAt,
		&quote.ProposedStartsAt, &quote.ProposedEndsAt, &quote.ProposedOccupiedStartsAt,
		&quote.ProposedOccupiedEndsAt, &quote.RefundAmountMinor, &quote.RetainedAmountMinor,
		&quote.FeeAmountMinor, &quote.CurrencyCode, &quote.PolicyMessage, &quote.ExpiresAt,
		&quote.ConsumedAt, &quote.PublicToken,
	); errors.Is(err, pgx.ErrNoRows) {
		return bookingChangeQuoteRecord{}, "", ErrNotFound
	} else if err != nil {
		return bookingChangeQuoteRecord{}, "", fmt.Errorf("load booking change quote: %w", err)
	}
	return quote, fingerprint, nil
}

func bookingChangeQuoteResponse(quote bookingChangeQuoteRecord) MarketplaceBookingChangeQuote {
	return MarketplaceBookingChangeQuote{
		QuoteToken: quote.PublicToken, BookingID: quote.BookingID.String(), Kind: quote.Kind,
		CurrentStartsAt: quote.CurrentStartsAt, CurrentEndsAt: quote.CurrentEndsAt,
		ProposedStartsAt: quote.ProposedStartsAt, ProposedEndsAt: quote.ProposedEndsAt,
		RefundAmountMinor: money.Minor(quote.RefundAmountMinor), RetainedAmountMinor: money.Minor(quote.RetainedAmountMinor),
		FeeAmountMinor: money.Minor(quote.FeeAmountMinor), CurrencyCode: quote.CurrencyCode,
		PolicyMessage: quote.PolicyMessage, ExpiresAt: quote.ExpiresAt,
	}
}

func bookingChangeFingerprint(bookingID uuid.UUID, kind string, proposedStartsAt *time.Time) string {
	value := bookingID.String() + "|" + kind
	if proposedStartsAt != nil {
		value += "|" + proposedStartsAt.UTC().Format(time.RFC3339Nano)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func bookingCommandFingerprint(bookingID uuid.UUID, command, quoteToken, reason string) string {
	value := strings.Join([]string{
		bookingID.String(), strings.TrimSpace(command), strings.TrimSpace(quoteToken), strings.TrimSpace(reason),
	}, "|")
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func lockBookingChangeIdempotency(ctx context.Context, tx pgx.Tx, namespace string, actorID, key uuid.UUID) error {
	lockKey := strings.Join([]string{"booking-change", namespace, actorID.String(), key.String()}, ":")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock booking change idempotency: %w", err)
	}
	return nil
}

func providerConfirmationObligationsSatisfied(booking bookingChangeRecord) bool {
	return initialBookingObligationSatisfied(booking.PaymentStatus) &&
		bookingAgreementObligationSatisfied(booking.AgreementRequired, booking.AgreementStatus)
}

func bookingAgreementObligationSatisfied(required bool, status string) bool {
	if !required {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "signed", "completed":
		return true
	default:
		return false
	}
}

func cancellationPolicyMessage(netPaid, refund int64) string {
	switch {
	case netPaid <= 0:
		return "No payment has been collected, so there is no refund to process."
	case refund > 0:
		return "The refundable amount is calculated from the policy agreed when this booking was created."
	default:
		return "This cancellation is outside the refundable policy window. No automatic refund is due."
	}
}

func loadIdempotentBookingCommand(ctx context.Context, tx pgx.Tx, actorType string, actorID, bookingID uuid.UUID, command, fingerprint string, key uuid.UUID) (BookingCommandResponse, bool, error) {
	var storedBookingID uuid.UUID
	var storedCommand string
	var storedFingerprint string
	var payload []byte
	err := tx.QueryRow(ctx, `
		SELECT booking_id, command, request_fingerprint, response_snapshot FROM booking_change_commands
		WHERE actor_type=$1 AND actor_id=$2 AND idempotency_key=$3
	`, actorType, actorID, key).Scan(&storedBookingID, &storedCommand, &storedFingerprint, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return BookingCommandResponse{}, false, nil
	}
	if err != nil {
		return BookingCommandResponse{}, false, fmt.Errorf("load idempotent booking command: %w", err)
	}
	if storedBookingID != bookingID || storedCommand != command || storedFingerprint != fingerprint {
		return BookingCommandResponse{}, false, ErrBookingIdempotency
	}
	var response BookingCommandResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return BookingCommandResponse{}, false, fmt.Errorf("decode booking command response: %w", err)
	}
	return response, true, nil
}

func insertBookingCommand(ctx context.Context, tx pgx.Tx, id, bookingID uuid.UUID, actorType string, actorID uuid.UUID, command, fingerprint string, idempotencyKey uuid.UUID, quoteID *uuid.UUID, reason string, response BookingCommandResponse) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode booking command response: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_change_commands (
			id, booking_id, actor_type, actor_id, command, idempotency_key,
			quote_id, reason, request_fingerprint, response_snapshot
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
	`, id, bookingID, actorType, actorID, command, idempotencyKey, quoteID, strings.TrimSpace(reason), fingerprint, payload); err != nil {
		return fmt.Errorf("record booking command: %w", err)
	}
	return nil
}

func insertBookingRefundRequest(ctx context.Context, tx pgx.Tx, bookingID, commandID uuid.UUID, amount int64, currency, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_refund_requests (
			id, booking_id, command_id, amount_minor, currency_code, reason
		) VALUES ($1,$2,$3,$4,$5,$6)
	`, uuid.New(), bookingID, commandID, amount, currency, strings.TrimSpace(reason)); err != nil {
		return fmt.Errorf("queue booking refund: %w", err)
	}
	return nil
}

func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}
