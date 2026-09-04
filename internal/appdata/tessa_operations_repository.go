package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/payments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const tessaOperationalResultLimit = 8

func (r *Repository) SearchTessaServices(
	ctx context.Context,
	clientID uuid.UUID,
	query, status string,
	limit int,
) (TessaServiceSearchResult, error) {
	if limit < 1 || limit > tessaOperationalResultLimit {
		limit = tessaOperationalResultLimit
	}
	query = escapeTessaLikeQuery(query)
	rows, err := r.db.Query(ctx, `
		SELECT service.id,service.title,service.status,COALESCE(service.is_hidden,FALSE),
			service.duration_minutes,service.price_amount_minor,service.currency_code,
			service.fulfillment_mode,COALESCE(section.name,'')
		FROM services service
		LEFT JOIN service_sections section
			ON section.id=service.section_id AND section.client_id=service.client_id
		WHERE service.client_id=$1
			AND ($2='' OR service.title ILIKE '%'||$2||'%' ESCAPE '\' OR service.id::text=$2)
			AND ($3='' OR service.status=$3)
		ORDER BY service.sort_order,service.created_at DESC,service.id
		LIMIT $4
	`, clientID, query, status, limit+1)
	if err != nil {
		return TessaServiceSearchResult{}, fmt.Errorf("search Tessa services: %w", err)
	}
	defer rows.Close()
	items := make([]TessaServiceSummary, 0, limit+1)
	for rows.Next() {
		var item TessaServiceSummary
		if err := rows.Scan(
			&item.ServiceID, &item.Name, &item.Status, &item.Hidden,
			&item.DurationMinutes, &item.PriceAmountMinor, &item.CurrencyCode,
			&item.FulfillmentMode, &item.SectionName,
		); err != nil {
			return TessaServiceSearchResult{}, fmt.Errorf("scan Tessa service: %w", err)
		}
		item.Name = truncateTessaToolText(item.Name, 120)
		item.SectionName = truncateTessaToolText(item.SectionName, 80)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return TessaServiceSearchResult{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return TessaServiceSearchResult{Items: items, HasMore: hasMore}, nil
}

func (r *Repository) GetTessaService(
	ctx context.Context,
	clientID, serviceID uuid.UUID,
) (TessaServiceDetail, error) {
	managed, err := r.GetManagedServiceDetails(ctx, clientID, serviceID)
	if err != nil {
		return TessaServiceDetail{}, err
	}
	return TessaServiceDetail{
		TessaServiceSummary: TessaServiceSummary{
			ServiceID: managed.ID, Name: truncateTessaToolText(managed.Name, 120),
			Status: managed.Status, Hidden: managed.IsHidden, DurationMinutes: managed.DurationMinutes,
			PriceAmountMinor: int64(managed.Pricing.PriceAmountMinor), CurrencyCode: managed.CurrencyCode,
			FulfillmentMode: managed.Fulfillment.Mode,
			SectionName:     truncateTessaToolText(managed.SectionName, 80),
		},
		Description:     truncateTessaToolText(managed.Description, 500),
		DepositRequired: managed.Pricing.DepositRequired, DepositType: managed.Pricing.DepositType,
		DepositAmountMinor:   int64(managed.Pricing.DepositAmountMinor),
		DepositPercentageBPS: managed.Pricing.DepositPercentageBPS,
		AvailabilityMode:     managed.Availability.Mode,
		MinimumNoticeMinutes: managed.Availability.MinimumNoticeMinutes,
		MaxBookingsPerDay:    managed.Availability.MaxBookingsPerDay,
		LocationLabel:        truncateTessaToolText(managed.Fulfillment.ProviderLocationLabel, 120),
	}, nil
}

func (r *Repository) SearchTessaCustomers(
	ctx context.Context,
	clientID uuid.UUID,
	query string,
	limit int,
) (TessaCustomerSearchResult, error) {
	if limit < 1 || limit > tessaOperationalResultLimit {
		limit = tessaOperationalResultLimit
	}
	query = escapeTessaLikeQuery(query)
	rows, err := r.db.Query(ctx, `
		WITH candidate_customers AS MATERIALIZED (
			SELECT id,full_name,tier_label,status_label
			FROM customers
			WHERE client_id=$1
				AND ($2='' OR LOWER(full_name) LIKE '%'||LOWER($2)||'%' ESCAPE '\' OR id::text=$2)
			ORDER BY CASE WHEN id::text=$2 THEN 0 ELSE 1 END,updated_at DESC,id
			LIMIT $4
		)
		SELECT customer.id,customer.full_name,customer.tier_label,customer.status_label,
			COALESCE(summary.has_upcoming,FALSE),COALESCE(summary.has_completed,FALSE),
			summary.next_booking_at,summary.last_completed_at
		FROM candidate_customers customer
		LEFT JOIN LATERAL (
			SELECT
				BOOL_OR(booking.start_at>=NOW()
					AND LOWER(booking.status)<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show'])) AS has_upcoming,
				BOOL_OR(booking.end_at<NOW() AND LOWER(booking.status)='completed') AS has_completed,
				MIN(booking.start_at) FILTER (WHERE booking.start_at>=NOW()
					AND LOWER(booking.status)<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show'])) AS next_booking_at,
				MAX(booking.end_at) FILTER (WHERE booking.end_at<NOW()
					AND LOWER(booking.status)='completed') AS last_completed_at
			FROM bookings booking
			WHERE booking.client_id=$1 AND booking.customer_id=customer.id
		) summary ON TRUE
		ORDER BY COALESCE(summary.has_upcoming,FALSE) DESC,summary.next_booking_at NULLS LAST,
			customer.full_name,customer.id
		LIMIT $3
	`, clientID, query, limit+1, 64)
	if err != nil {
		return TessaCustomerSearchResult{}, fmt.Errorf("search Tessa customers: %w", err)
	}
	defer rows.Close()
	items := make([]TessaCustomerSummary, 0, limit+1)
	for rows.Next() {
		var item TessaCustomerSummary
		var next, last *time.Time
		if err := rows.Scan(
			&item.CustomerID, &item.Name, &item.Tier, &item.Status,
			&item.HasUpcomingBooking, &item.HasCompletedBooking, &next, &last,
		); err != nil {
			return TessaCustomerSearchResult{}, fmt.Errorf("scan Tessa customer: %w", err)
		}
		item.Name = truncateTessaToolText(item.Name, 80)
		item.Tier = truncateTessaToolText(item.Tier, 40)
		item.Status = truncateTessaToolText(item.Status, 40)
		if next != nil {
			item.NextBookingAt = next.UTC().Format(time.RFC3339)
		}
		if last != nil {
			item.LastCompletedBookingAt = last.UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return TessaCustomerSearchResult{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return TessaCustomerSearchResult{Items: items, HasMore: hasMore}, nil
}

func (r *Repository) GetTessaCustomerBookingSummary(
	ctx context.Context,
	clientID, customerID uuid.UUID,
) (TessaCustomerBookingSummary, error) {
	search, err := r.SearchTessaCustomers(ctx, clientID, customerID.String(), 1)
	if err != nil {
		return TessaCustomerBookingSummary{}, err
	}
	if len(search.Items) == 0 || search.Items[0].CustomerID != customerID.String() {
		return TessaCustomerBookingSummary{}, ErrNotFound
	}
	result := TessaCustomerBookingSummary{Customer: search.Items[0], RecentBookings: []TessaBookingSummary{}}
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*)::int,
			COUNT(*) FILTER (WHERE start_at>=NOW() AND LOWER(status)<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show']))::int,
			COUNT(*) FILTER (WHERE LOWER(status)='completed')::int,
			COUNT(*) FILTER (WHERE LOWER(status)=ANY(ARRAY['cancelled','canceled','declined','expired','no_show']))::int
		FROM bookings WHERE client_id=$1 AND customer_id=$2
	`, clientID, customerID).Scan(
		&result.TotalBookings, &result.UpcomingBookings,
		&result.CompletedBookings, &result.CancelledBookings,
	); err != nil {
		return TessaCustomerBookingSummary{}, fmt.Errorf("summarize Tessa customer bookings: %w", err)
	}
	rows, err := r.db.Query(ctx, `
		SELECT booking.id,booking.service_id,booking.title,'',booking.start_at,booking.end_at,
			COALESCE(NULLIF(BTRIM(booking.timezone),''),NULLIF(BTRIM(profile.timezone),''),'Africa/Lagos'),
			booking.status,booking.payment_status,booking.agreement_status,booking.location_label,
			booking.duration_minutes,booking.total_amount_minor,booking.currency_code,
			booking.agreement_title_snapshot,booking.standalone_signature_required_snapshot
		FROM bookings booking
		LEFT JOIN client_profiles profile ON profile.client_id=booking.client_id
		WHERE booking.client_id=$1 AND booking.customer_id=$2
		ORDER BY booking.start_at DESC,booking.id DESC LIMIT 5
	`, clientID, customerID)
	if err != nil {
		return TessaCustomerBookingSummary{}, fmt.Errorf("load Tessa customer bookings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanTessaBookingRecord(rows)
		if scanErr != nil {
			return TessaCustomerBookingSummary{}, scanErr
		}
		result.RecentBookings = append(result.RecentBookings, record.summary())
	}
	return result, rows.Err()
}

func (r *Repository) GetTessaPaymentSummary(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
) (TessaPaymentSummary, error) {
	var currencyCode string
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(profile.currency_code,'')
		FROM clients client LEFT JOIN client_profiles profile ON profile.client_id=client.id
		WHERE client.id=$1
	`, clientID).Scan(&currencyCode); errors.Is(err, pgx.ErrNoRows) {
		return TessaPaymentSummary{}, ErrNotFound
	} else if err != nil {
		return TessaPaymentSummary{}, fmt.Errorf("load Tessa payment market: %w", err)
	}
	location := timezoneLocationFromRange(from)
	if currencyCode == "" {
		return TessaPaymentSummary{
			From:       from.In(location).Format("2006-01-02"),
			To:         to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
			Configured: false,
		}, nil
	}
	period, err := r.getRevenuePeriod(ctx, clientID, RevenueOverviewResponse{
		Range: "custom", PeriodStart: from, PeriodEnd: to, CurrencyCode: currencyCode,
		RecentCustomerPayments: []RevenuePaymentItem{},
	}, false)
	if err != nil {
		return TessaPaymentSummary{}, err
	}
	return TessaPaymentSummary{
		Configured: true,
		From:       from.In(location).Format("2006-01-02"), To: to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
		CurrencyCode: period.CurrencyCode, WalletBalanceMinor: int64(period.WalletBalanceMinor),
		GrossRevenueMinor: int64(period.GrossRevenueMinor), NetRevenueMinor: int64(period.NetRevenueMinor),
		AveragePaymentMinor: int64(period.AveragePaymentMinor), PaymentCount: period.PaymentCount,
	}, nil
}

func (r *Repository) GetTessaBookingPaymentStatus(
	ctx context.Context,
	clientID, bookingID uuid.UUID,
) (TessaBookingPaymentStatus, error) {
	booking, err := r.GetTessaBooking(ctx, clientID, bookingID)
	if err != nil {
		return TessaBookingPaymentStatus{}, err
	}
	return TessaBookingPaymentStatus{
		BookingID: booking.BookingID, ServiceTitle: booking.ServiceTitle,
		CustomerName: booking.CustomerName, PaymentStatus: booking.PaymentStatus,
		TotalAmountMinor: booking.TotalAmountMinor, CurrencyCode: booking.CurrencyCode,
	}, nil
}

func (r *Repository) GetTessaPayoutSummary(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
) (TessaPayoutSummary, error) {
	var currencyCode string
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(profile.currency_code,'')
		FROM clients client LEFT JOIN client_profiles profile ON profile.client_id=client.id
		WHERE client.id=$1
	`, clientID).Scan(&currencyCode); errors.Is(err, pgx.ErrNoRows) {
		return TessaPayoutSummary{}, ErrNotFound
	} else if err != nil {
		return TessaPayoutSummary{}, fmt.Errorf("load Tessa payout market: %w", err)
	}
	location := timezoneLocationFromRange(from)
	base := TessaPayoutSummary{
		From:        from.In(location).Format("2006-01-02"),
		To:          to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
		BalanceAsOf: time.Now().In(location).Format(time.RFC3339),
	}
	if currencyCode == "" {
		return base, nil
	}
	summary, err := payments.NewLedgerRepository(r.db).GetPayoutSummary(ctx, clientID, currencyCode, from, to)
	if err != nil {
		return TessaPayoutSummary{}, fmt.Errorf("load Tessa payout summary: %w", err)
	}
	base.MarketConfigured = true
	base.DestinationConfigured = summary.ActiveDestinationCount > 0
	base.ActiveDestinationCount = summary.ActiveDestinationCount
	base.CurrencyCode = summary.CurrencyCode
	base.AvailableAmountMinor = int64(summary.AvailableAmountMinor)
	base.PendingSettlementAmountMinor = int64(summary.PendingSettlementAmountMinor)
	base.PayoutInProgressAmountMinor = int64(summary.PayoutInProgressAmountMinor)
	base.PeriodPaidOutAmountMinor = int64(summary.PeriodPaidOutAmountMinor)
	base.EligibleAllocationCount = summary.EligibleAllocationCount
	base.PeriodPayoutCount = summary.PeriodPayoutCount
	return base, nil
}

func (r *Repository) GetTessaBookingMetrics(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
	comparePrevious bool,
) (TessaBookingMetrics, error) {
	var currencyCode string
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(profile.currency_code,'')
		FROM clients client LEFT JOIN client_profiles profile ON profile.client_id=client.id
		WHERE client.id=$1
	`, clientID).Scan(&currencyCode); errors.Is(err, pgx.ErrNoRows) {
		return TessaBookingMetrics{}, ErrNotFound
	} else if err != nil {
		return TessaBookingMetrics{}, fmt.Errorf("load Tessa metrics market: %w", err)
	}
	if currencyCode == "" {
		location := timezoneLocationFromRange(from)
		return TessaBookingMetrics{
			Configured: false,
			Current: TessaBookingMetricPeriod{
				From: from.In(location).Format("2006-01-02"),
				To:   to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
			},
		}, nil
	}
	current, err := r.getStatsPeriod(ctx, clientID, StatsOverviewResponse{
		Range: "custom", PeriodStart: from, PeriodEnd: to, CurrencyCode: currencyCode,
	})
	if err != nil {
		return TessaBookingMetrics{}, err
	}
	result := TessaBookingMetrics{
		Configured:   true,
		CurrencyCode: currencyCode,
		Current:      tessaMetricPeriod(from, to, current),
	}
	if !comparePrevious {
		return result, nil
	}
	days := tessaCalendarDays(from, to)
	previousFrom := from.AddDate(0, 0, -days)
	previousTo := from
	previous, err := r.getStatsPeriod(ctx, clientID, StatsOverviewResponse{
		Range: "previous", PeriodStart: previousFrom, PeriodEnd: previousTo, CurrencyCode: currencyCode,
	})
	if err != nil {
		return TessaBookingMetrics{}, err
	}
	previousPeriod := tessaMetricPeriod(previousFrom, previousTo, previous)
	result.Previous = &previousPeriod
	result.Delta = &TessaBookingMetricDelta{
		TotalBookings:     current.TotalBookings - previous.TotalBookings,
		CompletedBookings: current.CompletedBookings - previous.CompletedBookings,
		CancelledBookings: current.CancelledBookings - previous.CancelledBookings,
		UniqueCustomers:   current.UniqueCustomerCount - previous.UniqueCustomerCount,
		BookedValueMinor:  int64(current.BookedValueMinor - previous.BookedValueMinor),
	}
	return result, nil
}

func (r *Repository) GetTessaInboxSummary(
	ctx context.Context,
	clientID uuid.UUID,
) (TessaInboxSummary, error) {
	result := TessaInboxSummary{Recent: []TessaInboxConversationSummary{}}
	if err := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE conversation.disabled_at IS NULL AND state.archived_at IS NULL)::int,
			COUNT(*) FILTER (WHERE conversation.disabled_at IS NULL AND state.archived_at IS NULL AND EXISTS (
				SELECT 1 FROM inbox_messages message WHERE message.conversation_id=conversation.id
				AND message.sequence>COALESCE(state.last_read_sequence,0)
				AND message.sender_type NOT IN ('provider','ai')
			))::int,
			COUNT(*) FILTER (WHERE conversation.disabled_at IS NULL AND state.archived_at IS NULL AND EXISTS (
				SELECT 1 FROM inbox_ai_conversation_controls control
				WHERE control.conversation_id=conversation.id AND control.state='handoff'
			))::int
		FROM inbox_conversations conversation
		LEFT JOIN inbox_participant_states state ON state.conversation_id=conversation.id
			AND state.participant_type='provider' AND state.participant_id=$1
		WHERE conversation.client_id=$1
	`, clientID).Scan(&result.OpenConversations, &result.UnreadConversations, &result.HandoffRequests); err != nil {
		return TessaInboxSummary{}, fmt.Errorf("summarize Tessa inbox: %w", err)
	}
	unread, err := r.GetProviderInboxUnreadCount(ctx, clientID)
	if err != nil {
		return TessaInboxSummary{}, err
	}
	result.UnreadMessages = unread.UnreadTotal
	page, err := r.ListProviderConversations(ctx, clientID, 5, "", "all", "")
	if err != nil {
		return TessaInboxSummary{}, err
	}
	for _, conversation := range page.Items {
		item := TessaInboxConversationSummary{
			ConversationID: conversation.ID,
			CustomerName:   truncateTessaToolText(conversation.Counterparty.Name, 80),
			UnreadCount:    conversation.UnreadCount, HandoffNeeded: conversation.ProviderHandoffRequested,
		}
		if conversation.LastMessageAt != nil {
			item.LastMessageAt = conversation.LastMessageAt.UTC().Format(time.RFC3339)
		}
		result.Recent = append(result.Recent, item)
	}
	return result, nil
}

func (r *Repository) GetTessaReviewSummary(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
) (TessaReviewSummary, error) {
	location := timezoneLocationFromRange(from)
	result := TessaReviewSummary{
		From: from.In(location).Format("2006-01-02"), To: to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
		Breakdown: make(map[int]int, 5), Recent: []TessaReviewItem{},
	}
	var one, two, three, four, five int
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(AVG(rating),0)::double precision,COUNT(*)::int,
			COUNT(*) FILTER (WHERE rating=1)::int,COUNT(*) FILTER (WHERE rating=2)::int,
			COUNT(*) FILTER (WHERE rating=3)::int,COUNT(*) FILTER (WHERE rating=4)::int,
			COUNT(*) FILTER (WHERE rating=5)::int
		FROM provider_reviews
		WHERE client_id=$1 AND status='approved' AND created_at>=$2 AND created_at<$3
	`, clientID, from.UTC(), to.UTC()).Scan(&result.Rating, &result.Count, &one, &two, &three, &four, &five); err != nil {
		return TessaReviewSummary{}, fmt.Errorf("summarize Tessa reviews: %w", err)
	}
	result.Breakdown[1], result.Breakdown[2], result.Breakdown[3] = one, two, three
	result.Breakdown[4], result.Breakdown[5] = four, five
	rows, err := r.db.Query(ctx, `
		SELECT review.id,review.author_name,COALESCE(service.title,''),review.rating,
			review.review_text,review.created_at
		FROM provider_reviews review
		LEFT JOIN services service ON service.id=review.service_id AND service.client_id=review.client_id
		WHERE review.client_id=$1 AND review.status='approved' AND review.created_at>=$2 AND review.created_at<$3
		ORDER BY review.created_at DESC,review.id DESC LIMIT 5
	`, clientID, from.UTC(), to.UTC())
	if err != nil {
		return TessaReviewSummary{}, fmt.Errorf("load recent Tessa reviews: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item TessaReviewItem
		var createdAt time.Time
		if err := rows.Scan(
			&item.ReviewID, &item.AuthorName, &item.ServiceTitle,
			&item.Rating, &item.ReviewExcerpt, &createdAt,
		); err != nil {
			return TessaReviewSummary{}, fmt.Errorf("scan recent Tessa review: %w", err)
		}
		item.AuthorName = truncateTessaToolText(publicReviewAuthorName(item.AuthorName), 80)
		item.ServiceTitle = truncateTessaToolText(item.ServiceTitle, 120)
		item.ReviewExcerpt = truncateTessaToolText(item.ReviewExcerpt, 240)
		item.CreatedAt = createdAt.In(location).Format(time.RFC3339)
		result.Recent = append(result.Recent, item)
	}
	return result, rows.Err()
}

func (r *Repository) GetTessaPublicProfileStatus(
	ctx context.Context,
	clientID uuid.UUID,
) (TessaPublicProfileStatus, error) {
	profile, err := r.GetClientProfile(ctx, clientID)
	if err != nil {
		return TessaPublicProfileStatus{}, err
	}
	result := TessaPublicProfileStatus{
		BusinessName: truncateTessaToolText(profile.BusinessName, 120),
		HandleSlug:   profile.HandleSlug, MarketConfigured: profile.MarketConfigured,
		Verified: profile.Verified, MissingFields: []string{},
	}
	if result.HandleSlug != "" {
		result.PublicPath = "/p/" + result.HandleSlug
	}
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(profile.marketplace_enabled,FALSE),
			(SELECT COUNT(*)::int FROM services service WHERE service.client_id=$1
				AND service.status='published' AND service.is_active AND NOT COALESCE(service.is_hidden,FALSE)),
			(SELECT COUNT(*)::int FROM business_locations location WHERE location.client_id=$1 AND location.is_active)
		FROM clients client
		LEFT JOIN client_profiles profile ON profile.client_id=client.id
		WHERE client.id=$1
	`, clientID).Scan(&result.MarketplaceEnabled, &result.PublishedServices, &result.BusinessLocations); errors.Is(err, pgx.ErrNoRows) {
		return TessaPublicProfileStatus{}, ErrNotFound
	} else if err != nil {
		return TessaPublicProfileStatus{}, fmt.Errorf("load Tessa public profile status: %w", err)
	}
	checks := []struct {
		missing bool
		name    string
	}{
		{strings.TrimSpace(profile.BusinessName) == "", "business_name"},
		{strings.TrimSpace(profile.HandleSlug) == "", "handle"},
		{strings.TrimSpace(profile.Category) == "", "category"},
		{strings.TrimSpace(profile.Headline) == "", "headline"},
		{strings.TrimSpace(profile.ShortBio) == "", "short_bio"},
		{strings.TrimSpace(profile.Location) == "", "location"},
		{!profile.MarketConfigured, "market"},
		{result.PublishedServices == 0, "published_service"},
	}
	for _, check := range checks {
		if check.missing {
			result.MissingFields = append(result.MissingFields, check.name)
		}
	}
	return result, nil
}

func escapeTessaLikeQuery(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(value))
}

func truncateTessaToolText(value string, maximumBytes int) string {
	value = strings.TrimSpace(value)
	if maximumBytes < 1 || len(value) <= maximumBytes {
		return value
	}
	end := maximumBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return strings.TrimSpace(value[:end])
}

func tessaMetricPeriod(from, to time.Time, value StatsOverviewResponse) TessaBookingMetricPeriod {
	location := timezoneLocationFromRange(from)
	return TessaBookingMetricPeriod{
		From: from.In(location).Format("2006-01-02"),
		To:   to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
		Metrics: TessaBookingMetricSet{
			TotalBookings: value.TotalBookings, CompletedBookings: value.CompletedBookings,
			ScheduledBookings: value.ScheduledBookings, CancelledBookings: value.CancelledBookings,
			SecuredBookings: value.SecuredBookings, UniqueCustomers: value.UniqueCustomerCount,
			BookedValueMinor: int64(value.BookedValueMinor),
		},
	}
}
