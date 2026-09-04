package appdata

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMarketplaceBookingStatusGroup(t *testing.T) {
	now := time.Date(2026, time.August, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		status string
		end    time.Time
		want   MarketplaceBookingStatus
	}{
		{status: "confirmed", end: now.Add(time.Hour), want: MarketplaceBookingUpcoming},
		{status: "completed", end: now.Add(-time.Hour), want: MarketplaceBookingPast},
		{status: "cancelled", end: now.Add(time.Hour), want: MarketplaceBookingCancelled},
		{status: "canceled", end: now.Add(-time.Hour), want: MarketplaceBookingCancelled},
		{status: "declined", end: now.Add(time.Hour), want: MarketplaceBookingCancelled},
	}
	for _, test := range tests {
		if got := marketplaceBookingStatusGroup(test.status, test.end, now); got != test.want {
			t.Fatalf("marketplaceBookingStatusGroup(%q) = %q, want %q", test.status, got, test.want)
		}
	}
}

func TestMarketplaceCustomerBookingOwnershipIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var bookingID uuid.UUID
	var bookingToken string
	var previousOwner uuid.NullUUID
	if err := pool.QueryRow(ctx, `
		SELECT booking.id, booking.public_token, booking.marketplace_customer_id
		FROM bookings booking
		INNER JOIN client_profiles profile ON profile.client_id=booking.client_id
		INNER JOIN client_profile_handles handle ON handle.client_id=booking.client_id
		INNER JOIN customers customer ON customer.id=booking.customer_id
		ORDER BY booking.created_at DESC LIMIT 1
	`).Scan(&bookingID, &bookingToken, &previousOwner); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no booking is available for the ownership integration test")
	} else if err != nil {
		t.Fatal(err)
	}

	firstCustomerID, secondCustomerID := uuid.New(), uuid.New()
	firstEmail := "marketplace-booking-" + firstCustomerID.String() + "@example.com"
	secondEmail := "marketplace-booking-" + secondCustomerID.String() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id, email, email_verified_at) VALUES ($1,$2,NOW()),($3,$4,NOW())
	`, firstCustomerID, firstEmail, secondCustomerID, secondEmail); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previousOwner.Valid {
			_, _ = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=$1 WHERE id=$2`, previousOwner.UUID, bookingID)
		} else {
			_, _ = pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=NULL WHERE id=$1`, bookingID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id IN ($1,$2)`, firstCustomerID, secondCustomerID)
	})
	if _, err := pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=$1 WHERE id=$2`, firstCustomerID, bookingID); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	detail, err := repo.GetMarketplaceCustomerBooking(ctx, firstCustomerID, bookingID)
	if err != nil || detail.ID != bookingID.String() {
		t.Fatalf("owned detail = %+v, error = %v", detail, err)
	}
	statusGroup := marketplaceBookingStatusGroup(detail.Status, detail.EndsAt, time.Now())
	list, err := repo.ListMarketplaceCustomerBookings(ctx, firstCustomerID, statusGroup, "", 20, true)
	if err != nil {
		t.Fatalf("list owned marketplace bookings: %v", err)
	}
	foundOwnedBooking := false
	for _, item := range list.Items {
		if item.ID == bookingID.String() {
			foundOwnedBooking = true
			break
		}
	}
	if !foundOwnedBooking {
		t.Fatalf("owned booking %s was absent from %s list", bookingID, statusGroup)
	}
	providerDetail, err := repo.GetBookingDetails(ctx, uuid.MustParse(detail.ProviderID), bookingID)
	if err != nil || providerDetail.ID != bookingID.String() || providerDetail.PaymentHistory == nil || providerDetail.RefundHistory == nil || providerDetail.ChangeHistory == nil {
		t.Fatalf("provider detail = %+v, error = %v", providerDetail, err)
	}
	if _, err := repo.GetMarketplaceCustomerBooking(ctx, secondCustomerID, bookingID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-customer detail error = %v, want not found", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET marketplace_customer_id=NULL WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	claimedID, err := repo.ClaimMarketplaceBooking(ctx, firstCustomerID, bookingToken)
	if err != nil || claimedID != bookingID {
		t.Fatalf("claim booking = %s, error = %v", claimedID, err)
	}
	if _, err := repo.ClaimMarketplaceBooking(ctx, secondCustomerID, bookingToken); !errors.Is(err, ErrMarketplaceBookingOwned) {
		t.Fatalf("second customer claim error = %v, want owned conflict", err)
	}
}

func TestMarketplaceBookingCoordinatesFollowFulfillment(t *testing.T) {
	latitude, longitude := 6.45, 3.47
	if !marketplaceBookingHasCoordinates(MarketplaceBookingDetail{
		MarketplaceBookingListItem: MarketplaceBookingListItem{FulfillmentMode: "provider_location"},
		ProviderLatitude:           &latitude,
		ProviderLongitude:          &longitude,
	}) {
		t.Fatal("provider-location booking with provider coordinates was not navigable")
	}
	if marketplaceBookingHasCoordinates(MarketplaceBookingDetail{
		MarketplaceBookingListItem: MarketplaceBookingListItem{FulfillmentMode: "customer_location"},
		ProviderLatitude:           &latitude,
		ProviderLongitude:          &longitude,
	}) {
		t.Fatal("customer-location booking incorrectly used provider coordinates")
	}
	if marketplaceBookingHasCoordinates(MarketplaceBookingDetail{
		MarketplaceBookingListItem: MarketplaceBookingListItem{FulfillmentMode: "virtual"},
	}) {
		t.Fatal("virtual booking was marked navigable")
	}
}

func TestMarketplaceCancellationCommandIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var bookingID uuid.UUID
	var previousOwner uuid.NullUUID
	var previousStatus string
	var previousStart, previousEnd, previousOccupiedStart, previousOccupiedEnd, previousUpdatedAt time.Time
	var previousCancelNotice, previousRescheduleNotice int
	var previousRefundBPS int64
	var previousRescheduleFee int64
	var previousAutomated bool
	if err := pool.QueryRow(ctx, `
		SELECT id, marketplace_customer_id, status, start_at, end_at, occupied_start_at,
			occupied_end_at, updated_at, cancellation_notice_minutes_snapshot,
			cancellation_refund_bps_snapshot, reschedule_notice_minutes_snapshot,
			reschedule_fee_minor_snapshot, automated_reschedule_snapshot
		FROM bookings
		WHERE EXISTS (
			SELECT 1 FROM client_profile_handles handle WHERE handle.client_id = bookings.client_id
		)
		ORDER BY created_at DESC LIMIT 1
	`).Scan(
		&bookingID, &previousOwner, &previousStatus, &previousStart, &previousEnd,
		&previousOccupiedStart, &previousOccupiedEnd, &previousUpdatedAt,
		&previousCancelNotice, &previousRefundBPS, &previousRescheduleNotice,
		&previousRescheduleFee, &previousAutomated,
	); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no booking is available for the cancellation integration test")
	} else if err != nil {
		t.Fatal(err)
	}
	var eventCursor int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM booking_domain_events`).Scan(&eventCursor); err != nil {
		t.Fatal(err)
	}
	customerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id, email, email_verified_at) VALUES ($1,$2,NOW())
	`, customerID, "booking-change-"+customerID.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}

	newStart := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	newEnd := newStart.Add(time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET marketplace_customer_id=$2, status='booked', start_at=$3::timestamptz, end_at=$4::timestamptz,
			occupied_start_at=$3::timestamptz-INTERVAL '10 minutes', occupied_end_at=$4::timestamptz+INTERVAL '10 minutes',
			cancellation_notice_minutes_snapshot=1440, cancellation_refund_bps_snapshot=10000,
			reschedule_notice_minutes_snapshot=1440, reschedule_fee_minor_snapshot=0,
			automated_reschedule_snapshot=true, updated_at=NOW()
		WHERE id=$1
	`, bookingID, customerID, newStart, newEnd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM booking_refund_requests WHERE booking_id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_change_commands WHERE booking_id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_change_quotes WHERE booking_id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `
			UPDATE bookings SET marketplace_customer_id=$2, status=$3, start_at=$4, end_at=$5,
				occupied_start_at=$6, occupied_end_at=$7, updated_at=$8,
				cancellation_notice_minutes_snapshot=$9, cancellation_refund_bps_snapshot=$10,
				reschedule_notice_minutes_snapshot=$11, reschedule_fee_minor_snapshot=$12,
				automated_reschedule_snapshot=$13 WHERE id=$1
		`, bookingID, nullableUUID(previousOwner), previousStatus, previousStart, previousEnd,
			previousOccupiedStart, previousOccupiedEnd, previousUpdatedAt, previousCancelNotice,
			previousRefundBPS, previousRescheduleNotice, previousRescheduleFee, previousAutomated)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_domain_events WHERE booking_id=$1 AND sequence>$2`, bookingID, eventCursor)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, customerID)
	})

	repo := NewRepository(pool)
	quoteKey := uuid.New()
	quote, err := repo.CreateMarketplaceCancellationQuote(ctx, customerID, bookingID, quoteKey)
	if err != nil || quote.QuoteToken == "" || quote.Kind != "cancellation" {
		t.Fatalf("quote=%#v error=%v", quote, err)
	}
	replayedQuote, err := repo.CreateMarketplaceCancellationQuote(ctx, customerID, bookingID, quoteKey)
	if err != nil || replayedQuote.QuoteToken != quote.QuoteToken {
		t.Fatalf("replayed quote=%#v error=%v", replayedQuote, err)
	}
	type quoteResult struct {
		quote MarketplaceBookingChangeQuote
		err   error
	}
	concurrentQuoteKey := uuid.New()
	quoteStart := make(chan struct{})
	quoteResults := make(chan quoteResult, 2)
	for range 2 {
		go func() {
			<-quoteStart
			result, resultErr := repo.CreateMarketplaceCancellationQuote(ctx, customerID, bookingID, concurrentQuoteKey)
			quoteResults <- quoteResult{quote: result, err: resultErr}
		}()
	}
	close(quoteStart)
	firstConcurrentQuote := <-quoteResults
	secondConcurrentQuote := <-quoteResults
	if firstConcurrentQuote.err != nil || secondConcurrentQuote.err != nil ||
		firstConcurrentQuote.quote.QuoteToken != secondConcurrentQuote.quote.QuoteToken {
		t.Fatalf("concurrent quotes first=%#v second=%#v", firstConcurrentQuote, secondConcurrentQuote)
	}
	commandKey := uuid.New()
	type commandResult struct {
		response BookingCommandResponse
		err      error
	}
	commandStart := make(chan struct{})
	commandResults := make(chan commandResult, 2)
	for range 2 {
		go func() {
			<-commandStart
			result, resultErr := repo.ApplyMarketplaceBookingChange(ctx, customerID, bookingID, quote.QuoteToken, "cancel", "Test cancellation", commandKey)
			commandResults <- commandResult{response: result, err: resultErr}
		}()
	}
	close(commandStart)
	firstCommand := <-commandResults
	secondCommand := <-commandResults
	if firstCommand.err != nil || secondCommand.err != nil ||
		firstCommand.response.Status != "cancelled" || secondCommand.response.Status != "cancelled" ||
		firstCommand.response.RefundAmountMinor != secondCommand.response.RefundAmountMinor {
		t.Fatalf("concurrent commands first=%#v second=%#v", firstCommand, secondCommand)
	}
}

func nullableUUID(value uuid.NullUUID) any {
	if value.Valid {
		return value.UUID
	}
	return nil
}
