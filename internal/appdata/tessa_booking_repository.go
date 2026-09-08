package appdata

import (
	"booking/go-server/internal/tessa"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const tessaBookingResultLimit = tessa.MaxSearchResults

type tessaBookingRecord struct {
	ID                          uuid.UUID
	ServiceID                   uuid.NullUUID
	Title                       string
	CustomerName                string
	StartAt                     time.Time
	EndAt                       time.Time
	Timezone                    string
	Status                      string
	PaymentStatus               string
	AgreementStatus             string
	LocationLabel               string
	DurationMinutes             int
	TotalAmountMinor            int64
	CurrencyCode                string
	AgreementTitle              string
	StandaloneSignatureRequired bool
}

func (record tessaBookingRecord) summary() TessaBookingSummary {
	location := timezoneLocation(record.Timezone)
	result := TessaBookingSummary{
		BookingID: record.ID.String(), ServiceTitle: truncateTessaToolText(record.Title, 120),
		CustomerName: truncateTessaToolText(record.CustomerName, 80),
		StartsAt:     record.StartAt.In(location).Format(time.RFC3339),
		EndsAt:       record.EndAt.In(location).Format(time.RFC3339), Timezone: location.String(),
		Status: record.Status, PaymentStatus: record.PaymentStatus,
		AgreementState: record.AgreementStatus, LocationLabel: truncateTessaToolText(record.LocationLabel, 120),
	}
	if record.ServiceID.Valid {
		result.ServiceID = record.ServiceID.UUID.String()
	}
	return result
}

func (record tessaBookingRecord) lifecycle() BookingLifecycle {
	return ResolveBookingLifecycle(PublicBookingSummaryResponse{
		Status: record.Status, PaymentStatus: record.PaymentStatus,
		AgreementStatus: record.AgreementStatus, AgreementTemplateTitle: record.AgreementTitle,
		StandaloneSignatureRequired: record.StandaloneSignatureRequired,
	})
}

// Counts and lists share tenant, overlap, status, literal-search, and upcoming
// predicates. The list's LIMIT is deliberately outside this shared filter.
const tessaBookingFilterSQL = `
	WHERE b.client_id=$1 AND b.start_at<$3 AND b.end_at>$2
		AND (cardinality($4::text[])=0 OR LOWER(BTRIM(b.status))=ANY($4::text[]))
		AND ($5='' OR b.title ILIKE '%'||$5||'%' ESCAPE '\' OR COALESCE(customer.full_name,'') ILIKE '%'||$5||'%' ESCAPE '\'
			OR b.id::text=$5)
		AND ($6 OR LOWER(BTRIM(b.status))<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show']))
		AND ($7::timestamptz IS NULL OR b.start_at >= $7)
		AND CASE $8::text
			WHEN 'any' THEN TRUE
			WHEN 'unpaid' THEN b.payment_status<>ALL(ARRAY['deposit_paid_balance_due','paid_in_full'])
			WHEN 'balance_due' THEN b.payment_status='deposit_paid_balance_due'
			WHEN 'paid_in_full' THEN b.payment_status='paid_in_full'
			ELSE FALSE END
		AND LOWER(BTRIM(b.status))<>ALL($9::text[])
`

type TessaBookingFilter struct {
	Query                      string
	Statuses, ExcludedStatuses []string
	PaymentState               string
	StartsNotBefore            *time.Time
}

func (filter TessaBookingFilter) queryArgs(clientID uuid.UUID, from, to time.Time, includeTerminal bool) []any {
	statusValues := func(values []string) []string {
		result := make([]string, 0, len(values)+1)
		cancelled := false
		for _, value := range values {
			if value == "cancelled" || value == "canceled" {
				cancelled = true
			} else {
				result = append(result, value)
			}
		}
		if cancelled {
			result = append(result, "cancelled", "canceled")
		}
		return result
	}
	state := filter.PaymentState
	if state == "" {
		state = "any"
	}
	return []any{clientID, from.UTC(), to.UTC(), statusValues(filter.Statuses), escapeTessaLikeQuery(filter.Query), includeTerminal, filter.StartsNotBefore, state, statusValues(filter.ExcludedStatuses)}
}

type TessaBookingCount struct {
	From          string `json:"from"`
	To            string `json:"to"`
	Timezone      string `json:"timezone"`
	TotalBookings int64  `json:"total_bookings"`
	Exact         bool   `json:"exact"`
}

func (r *Repository) CountTessaBookings(ctx context.Context, clientID uuid.UUID, from, to time.Time, filter TessaBookingFilter) (TessaBookingCount, error) {
	result := TessaBookingCount{From: from.Format(time.DateOnly), To: to.AddDate(0, 0, -1).Format(time.DateOnly), Timezone: from.Location().String(), Exact: true}
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM bookings b
		LEFT JOIN customers customer ON customer.id=b.customer_id AND customer.client_id=b.client_id
	`+tessaBookingFilterSQL, filter.queryArgs(clientID, from, to, true)...).Scan(&result.TotalBookings)
	if err != nil {
		return TessaBookingCount{}, fmt.Errorf("count Tessa bookings: %w", err)
	}
	return result, nil
}

func (r *Repository) SearchTessaBookings(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
	filter TessaBookingFilter,
	limit int,
	includeTerminal bool,
) (TessaBookingSearchResult, error) {
	if limit < 1 || limit > tessaBookingResultLimit {
		limit = tessaBookingResultLimit
	}
	rows, err := r.db.Query(ctx, `
		SELECT b.id,b.service_id,b.title,COALESCE(customer.full_name,''),b.start_at,b.end_at,
			COALESCE(NULLIF(BTRIM(b.timezone),''),NULLIF(BTRIM(profile.timezone),''),'Africa/Lagos'),
			b.status,b.payment_status,b.agreement_status,b.location_label,b.duration_minutes,
			b.total_amount_minor,b.currency_code,b.agreement_title_snapshot,
			b.standalone_signature_required_snapshot
		FROM bookings b
		LEFT JOIN customers customer ON customer.id=b.customer_id AND customer.client_id=b.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=b.client_id
	`+tessaBookingFilterSQL+` ORDER BY b.start_at,b.id LIMIT $10
	`, append(filter.queryArgs(clientID, from, to, includeTerminal), limit+1)...)
	if err != nil {
		return TessaBookingSearchResult{}, fmt.Errorf("search Tessa bookings: %w", err)
	}
	defer rows.Close()
	records := make([]tessaBookingRecord, 0, limit+1)
	for rows.Next() {
		record, scanErr := scanTessaBookingRecord(rows)
		if scanErr != nil {
			return TessaBookingSearchResult{}, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return TessaBookingSearchResult{}, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	items := make([]TessaBookingSummary, 0, len(records))
	for _, record := range records {
		items = append(items, record.summary())
	}
	location := timezoneLocationFromRange(from)
	return TessaBookingSearchResult{
		From: from.In(location).Format("2006-01-02"), To: to.Add(-time.Nanosecond).In(location).Format("2006-01-02"),
		Timezone: location.String(), Items: items, HasMore: hasMore,
	}, nil
}

func (r *Repository) GetTessaBooking(ctx context.Context, clientID, bookingID uuid.UUID) (TessaBookingDetail, error) {
	record, err := scanTessaBookingRecord(r.db.QueryRow(ctx, `
		SELECT b.id,b.service_id,b.title,COALESCE(customer.full_name,''),b.start_at,b.end_at,
			COALESCE(NULLIF(BTRIM(b.timezone),''),NULLIF(BTRIM(profile.timezone),''),'Africa/Lagos'),
			b.status,b.payment_status,b.agreement_status,b.location_label,b.duration_minutes,
			b.total_amount_minor,b.currency_code,b.agreement_title_snapshot,
			b.standalone_signature_required_snapshot
		FROM bookings b
		LEFT JOIN customers customer ON customer.id=b.customer_id AND customer.client_id=b.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=b.client_id
		WHERE b.id=$1 AND b.client_id=$2
	`, bookingID, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TessaBookingDetail{}, ErrNotFound
	}
	if err != nil {
		return TessaBookingDetail{}, fmt.Errorf("load Tessa booking: %w", err)
	}
	return TessaBookingDetail{
		TessaBookingSummary: record.summary(), DurationMinutes: record.DurationMinutes,
		TotalAmountMinor: record.TotalAmountMinor, CurrencyCode: record.CurrencyCode,
		Lifecycle: record.lifecycle(),
	}, nil
}

func (r *Repository) GetTessaBookingAttentionSummary(
	ctx context.Context,
	clientID uuid.UUID,
	from, to time.Time,
) (TessaBookingAttentionSummary, error) {
	result := TessaBookingAttentionSummary{
		From: from.Format("2006-01-02"), To: to.Add(-time.Nanosecond).Format("2006-01-02"),
		Timezone: timezoneLocationFromRange(from).String(), Examples: []TessaBookingSummary{},
	}
	if err := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE LOWER(BTRIM(status))<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show'])
				AND payment_status<>ALL(ARRAY['deposit_paid_balance_due','paid_in_full']))::int,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(status))<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show'])
				AND payment_status=ANY(ARRAY['deposit_paid_balance_due','paid_in_full'])
				AND (BTRIM(agreement_title_snapshot)<>'' OR standalone_signature_required_snapshot)
				AND agreement_status<>ALL(ARRAY['accepted','signed']))::int,
			COUNT(*) FILTER (WHERE LOWER(BTRIM(status))=ANY(ARRAY['booked','pending'])
				AND payment_status=ANY(ARRAY['deposit_paid_balance_due','paid_in_full'])
				AND ((BTRIM(agreement_title_snapshot)='' AND NOT standalone_signature_required_snapshot)
					OR agreement_status=ANY(ARRAY['accepted','signed'])))::int
		FROM bookings WHERE client_id=$1 AND start_at<$3 AND end_at>$2
	`, clientID, from.UTC(), to.UTC()).Scan(
		&result.AwaitingPayment, &result.AwaitingAgreement, &result.AwaitingProviderConfirmation,
	); err != nil {
		return TessaBookingAttentionSummary{}, fmt.Errorf("summarize Tessa booking attention: %w", err)
	}
	result.Total = result.AwaitingPayment + result.AwaitingAgreement + result.AwaitingProviderConfirmation
	rows, err := r.db.Query(ctx, `
		SELECT b.id,b.service_id,b.title,COALESCE(customer.full_name,''),b.start_at,b.end_at,
			COALESCE(NULLIF(BTRIM(b.timezone),''),NULLIF(BTRIM(profile.timezone),''),'Africa/Lagos'),
			b.status,b.payment_status,b.agreement_status,b.location_label,b.duration_minutes,
			b.total_amount_minor,b.currency_code,b.agreement_title_snapshot,
			b.standalone_signature_required_snapshot
		FROM bookings b
		LEFT JOIN customers customer ON customer.id=b.customer_id AND customer.client_id=b.client_id
		LEFT JOIN client_profiles profile ON profile.client_id=b.client_id
		WHERE b.client_id=$1 AND b.start_at<$3 AND b.end_at>$2
			AND LOWER(BTRIM(b.status))<>ALL(ARRAY['cancelled','canceled','declined','expired','no_show'])
			AND (b.payment_status<>ALL(ARRAY['deposit_paid_balance_due','paid_in_full'])
				OR ((BTRIM(b.agreement_title_snapshot)<>'' OR b.standalone_signature_required_snapshot)
					AND b.agreement_status<>ALL(ARRAY['accepted','signed']))
				OR LOWER(BTRIM(b.status))=ANY(ARRAY['booked','pending']))
		ORDER BY b.start_at,b.id LIMIT 8
	`, clientID, from.UTC(), to.UTC())
	if err != nil {
		return TessaBookingAttentionSummary{}, fmt.Errorf("load Tessa attention examples: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		record, scanErr := scanTessaBookingRecord(rows)
		if scanErr != nil {
			return TessaBookingAttentionSummary{}, scanErr
		}
		result.Examples = append(result.Examples, record.summary())
	}
	return result, rows.Err()
}

func (r *Repository) GetTessaAvailability(
	ctx context.Context,
	clientID uuid.UUID,
	serviceID uuid.UUID,
	serviceQuery string,
	from time.Time,
	days int,
) (TessaAvailabilityResult, error) {
	type service struct {
		ID     uuid.UUID
		Title  string
		Handle string
	}
	query := `
		SELECT service.id,service.title,profile.handle_slug
		FROM services service INNER JOIN client_profiles profile ON profile.client_id=service.client_id
		WHERE service.client_id=$1 AND service.status='published' AND service.is_active
			AND NOT COALESCE(service.is_hidden,FALSE) AND ($2::uuid='00000000-0000-0000-0000-000000000000' OR service.id=$2)
			AND ($3='' OR service.title ILIKE '%'||$3||'%' ESCAPE '\')
		ORDER BY service.sort_order,service.title,service.id LIMIT 4
	`
	rows, err := r.db.Query(ctx, query, clientID, serviceID, escapeTessaLikeQuery(serviceQuery))
	if err != nil {
		return TessaAvailabilityResult{}, fmt.Errorf("list Tessa availability services: %w", err)
	}
	services := make([]service, 0, 4)
	for rows.Next() {
		var item service
		if err := rows.Scan(&item.ID, &item.Title, &item.Handle); err != nil {
			rows.Close()
			return TessaAvailabilityResult{}, err
		}
		services = append(services, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return TessaAvailabilityResult{}, err
	}
	rows.Close()
	if (serviceID != uuid.Nil || serviceQuery != "") && len(services) == 0 {
		return TessaAvailabilityResult{}, ErrNotFound
	}
	hasMoreServices := len(services) > 3
	if hasMoreServices {
		services = services[:3]
	}
	location := timezoneLocationFromRange(from)
	result := TessaAvailabilityResult{
		From: from.In(location).Format("2006-01-02"), To: from.In(location).AddDate(0, 0, days-1).Format("2006-01-02"),
		Timezone: location.String(), Services: []TessaServiceAvailability{}, HasMore: hasMoreServices,
	}
	remainingSlots := 12
	for _, item := range services {
		availability, err := r.BookingApplication().SearchAvailability(ctx, SearchBookingAvailabilityCommand{
			ProviderHandle: item.Handle, ServiceID: item.ID, From: &from, Days: days,
		})
		if err != nil {
			return TessaAvailabilityResult{}, err
		}
		serviceResult := TessaServiceAvailability{
			ServiceID: item.ID.String(), ServiceTitle: truncateTessaToolText(item.Title, 120),
			DurationMinutes: availability.DurationMinutes, Dates: []TessaAvailabilityDay{},
		}
		availableSlotCount := 0
		for _, day := range availability.Dates {
			availableSlotCount += len(day.Slots)
		}
		if availableSlotCount > remainingSlots {
			result.HasMore = true
		}
		for _, day := range availability.Dates {
			if remainingSlots == 0 {
				break
			}
			dayResult := TessaAvailabilityDay{Date: day.Date, Slots: []TessaAvailabilitySlot{}}
			for _, slot := range day.Slots {
				if remainingSlots == 0 {
					break
				}
				dayResult.Slots = append(dayResult.Slots, TessaAvailabilitySlot{StartsAt: slot.StartAt, Label: slot.Label})
				remainingSlots--
			}
			if len(dayResult.Slots) > 0 {
				serviceResult.Dates = append(serviceResult.Dates, dayResult)
			}
		}
		result.Services = append(result.Services, serviceResult)
	}
	return result, nil
}

func scanTessaBookingRecord(scanner interface{ Scan(...any) error }) (tessaBookingRecord, error) {
	var record tessaBookingRecord
	err := scanner.Scan(
		&record.ID, &record.ServiceID, &record.Title, &record.CustomerName, &record.StartAt, &record.EndAt,
		&record.Timezone, &record.Status, &record.PaymentStatus, &record.AgreementStatus,
		&record.LocationLabel, &record.DurationMinutes, &record.TotalAmountMinor, &record.CurrencyCode,
		&record.AgreementTitle, &record.StandaloneSignatureRequired,
	)
	return record, err
}

func timezoneLocationFromRange(value time.Time) *time.Location {
	if value.Location() == nil {
		return time.UTC
	}
	return value.Location()
}
