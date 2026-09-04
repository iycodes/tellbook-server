package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"sort"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	agreementrender "booking/go-server/internal/agreements/render"
	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/money"
	"booking/go-server/internal/publictoken"

	aiapi "booking/go-server/shared/ai_api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type publicBookingQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type publicBookingServiceInfo struct {
	ID                          uuid.UUID
	ClientID                    uuid.UUID
	SectionID                   uuid.UUID
	Title                       string
	BusinessName                string
	BusinessLocation            string
	ImageURL                    string
	DurationMinutes             int
	PriceAmountMinor            int64
	CountryCode                 string
	CurrencyCode                string
	Locale                      string
	Timezone                    string
	DepositRequired             bool
	DepositType                 string
	DepositAmountMinor          int64
	DepositPercentageBPS        int
	FulfillmentMode             string
	ProviderLocationID          uuid.UUID
	ProviderLocationLabel       string
	ProviderPlaceID             string
	ProviderLatitude            *float64
	ProviderLongitude           *float64
	ProviderResolutionStatus    string
	TravelFeeMinor              int64
	MaxTravelDistanceMeters     *int
	AvailabilityMode            string
	MinimumNoticeMinutes        int
	MaxBookingsPerDay           int
	ConcurrentBookingCapacity   int
	PrepTimeMinutes             int
	BufferTimeMinutes           int
	VirtualDeliveryLabel        string
	VirtualJoinURL              string
	VirtualInstructions         string
	CancellationPolicy          string
	LatenessPolicy              string
	AgreementTiming             string
	AgreementTemplateFamilyID   uuid.UUID
	AgreementTemplateVersionID  uuid.UUID
	AgreementTemplateTitle      string
	AgreementConfirmationMethod domain.ConfirmationMethod
	AgreementDocument           *aiapi.DocumentSchema
	StandaloneSignatureRequired bool
}

func (r *Repository) getPublicServiceForBooking(ctx context.Context, slug string, serviceID uuid.UUID) (publicBookingServiceInfo, error) {
	return getPublicServiceForBooking(ctx, r.db, slug, serviceID)
}

func getPublicServiceForBooking(ctx context.Context, q publicBookingQuerier, slug string, serviceID uuid.UUID) (publicBookingServiceInfo, error) {
	const query = `
		SELECT
			s.id, s.client_id,
			COALESCE(s.section_id, '00000000-0000-0000-0000-000000000000'::uuid),
			s.title, cp.business_name, cp.public_location_label, COALESCE(s.image_url, ''), s.duration_minutes,
			s.price_amount_minor, cp.country_code, s.currency_code, cp.locale, cp.timezone,
			s.deposit_required, s.deposit_type, s.deposit_amount_minor, s.deposit_percentage_bps,
			s.fulfillment_mode,
			COALESCE(bl.id, '00000000-0000-0000-0000-000000000000'::uuid),
			COALESCE(bl.formatted_address, ''), COALESCE(bl.provider_place_id, ''),
			bl.latitude::double precision, bl.longitude::double precision,
			COALESCE(bl.resolution_status, ''),
			s.travel_fee_minor, s.max_travel_distance_meters,
			s.availability_mode, s.minimum_notice_minutes, s.max_bookings_per_day,
			cp.concurrent_booking_capacity,
			s.prep_time_minutes, s.buffer_time_minutes,
			s.virtual_delivery_label, COALESCE(s.virtual_join_url, ''),
			COALESCE(s.virtual_instructions, ''), s.cancellation_policy, s.lateness_policy,
			COALESCE(s.agreement_timing, ''),
			COALESCE(atf.id, '00000000-0000-0000-0000-000000000000'::uuid),
			COALESCE(atv.id, '00000000-0000-0000-0000-000000000000'::uuid),
			COALESCE(atf.title, ''), COALESCE(atf.confirmation_method, ''),
			atv.document_schema, s.standalone_signature_required
		FROM services s
		INNER JOIN client_profiles cp ON cp.client_id = s.client_id
		INNER JOIN client_profile_handles cph
			ON cph.client_id = s.client_id AND cph.handle_slug = $1
		LEFT JOIN business_locations bl
			ON bl.id = s.provider_location_id AND bl.client_id = s.client_id AND bl.is_active
		LEFT JOIN agreement_template_families atf
			ON atf.id = s.agreement_template_family_id
			AND atf.client_id = s.client_id
			AND atf.status = 'published'
		LEFT JOIN agreement_template_versions atv
			ON atv.id = atf.current_published_version_id
			AND atv.family_id = atf.id
			AND atv.state = 'published'
		WHERE s.id = $2
		  AND s.status = 'published'
		  AND COALESCE(s.is_hidden, FALSE) = FALSE
	`

	var service publicBookingServiceInfo
	var method string
	var documentJSON []byte
	if err := q.QueryRow(ctx, query, strings.TrimSpace(slug), serviceID).Scan(
		&service.ID, &service.ClientID, &service.SectionID, &service.Title,
		&service.BusinessName, &service.BusinessLocation, &service.ImageURL, &service.DurationMinutes,
		&service.PriceAmountMinor, &service.CountryCode, &service.CurrencyCode,
		&service.Locale, &service.Timezone, &service.DepositRequired,
		&service.DepositType, &service.DepositAmountMinor,
		&service.DepositPercentageBPS, &service.FulfillmentMode,
		&service.ProviderLocationID, &service.ProviderLocationLabel,
		&service.ProviderPlaceID, &service.ProviderLatitude, &service.ProviderLongitude,
		&service.ProviderResolutionStatus, &service.TravelFeeMinor,
		&service.MaxTravelDistanceMeters, &service.AvailabilityMode,
		&service.MinimumNoticeMinutes, &service.MaxBookingsPerDay,
		&service.ConcurrentBookingCapacity,
		&service.PrepTimeMinutes, &service.BufferTimeMinutes,
		&service.VirtualDeliveryLabel, &service.VirtualJoinURL,
		&service.VirtualInstructions, &service.CancellationPolicy,
		&service.LatenessPolicy, &service.AgreementTiming,
		&service.AgreementTemplateFamilyID, &service.AgreementTemplateVersionID,
		&service.AgreementTemplateTitle, &method, &documentJSON,
		&service.StandaloneSignatureRequired,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return publicBookingServiceInfo{}, ErrNotFound
		}
		return publicBookingServiceInfo{}, fmt.Errorf("get public service for booking: %w", err)
	}
	if service.AgreementTemplateFamilyID != uuid.Nil {
		parsedMethod, err := domain.ParseConfirmationMethod(method)
		if err != nil {
			return publicBookingServiceInfo{}, fmt.Errorf("parse agreement confirmation method: %w", err)
		}
		service.AgreementConfirmationMethod = parsedMethod
		var document aiapi.DocumentSchema
		if err := json.Unmarshal(documentJSON, &document); err != nil {
			return publicBookingServiceInfo{}, fmt.Errorf("decode agreement document: %w", err)
		}
		service.AgreementDocument = &document
	}
	if service.FulfillmentMode != string(bookingdomain.FulfillmentVirtual) && service.ProviderLocationID == uuid.Nil {
		return publicBookingServiceInfo{}, ErrNotFound
	}
	return service, nil
}

func (r *Repository) ListPublicServicesBySlug(ctx context.Context, slug string) ([]PublicServiceItem, error) {
	const query = `
		SELECT
			s.id, s.title, s.slug, s.description, s.category, s.icon_name,
			COALESCE(s.image_url, ''), s.duration_minutes, s.price_amount_minor,
			s.currency_code, s.status, s.cancellation_policy, s.lateness_policy,
			COALESCE(s.agreement_timing, ''),
			COALESCE(atf.confirmation_method, ''), COALESCE(atf.title, ''),
			s.standalone_signature_required,
			s.fulfillment_mode, COALESCE(bl.formatted_address, ''),
			s.virtual_delivery_label, s.minimum_notice_minutes,
			EXISTS (
				SELECT 1 FROM service_short_notice_rules snr WHERE snr.service_id = s.id
			),
			(s.fulfillment_mode = 'virtual' OR
				(bl.id IS NOT NULL AND bl.resolution_status = 'coordinates_resolved'))
			AND (
				(s.availability_mode = 'custom' AND EXISTS (
					SELECT 1 FROM service_availability_windows availability_window
					WHERE availability_window.service_id = s.id
					  AND EXTRACT(EPOCH FROM (availability_window.end_time - availability_window.start_time)) / 60 >=
						s.prep_time_minutes + s.duration_minutes + s.buffer_time_minutes
				)) OR (s.availability_mode <> 'custom' AND EXISTS (
					SELECT 1 FROM provider_availability_windows availability_window
					WHERE availability_window.client_id = s.client_id
					  AND EXTRACT(EPOCH FROM (availability_window.end_time - availability_window.start_time)) / 60 >=
						s.prep_time_minutes + s.duration_minutes + s.buffer_time_minutes
				))
			)
		FROM services s
		INNER JOIN client_profile_handles cph
			ON cph.client_id = s.client_id AND cph.handle_slug = $1
		LEFT JOIN business_locations bl
			ON bl.id = s.provider_location_id AND bl.client_id = s.client_id AND bl.is_active
		LEFT JOIN agreement_template_families atf
			ON atf.id = s.agreement_template_family_id
			AND atf.client_id = s.client_id
			AND atf.status = 'published'
		WHERE s.status IN ('published', 'paused')
		  AND COALESCE(s.is_hidden, FALSE) = FALSE
		ORDER BY s.sort_order ASC, s.created_at ASC
	`
	rows, err := r.db.Query(ctx, query, strings.TrimSpace(slug))
	if err != nil {
		return nil, fmt.Errorf("list public services: %w", err)
	}
	defer rows.Close()

	items := make([]PublicServiceItem, 0)
	for rows.Next() {
		var item PublicServiceItem
		var hasBookableConfiguration bool
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Slug, &item.Description, &item.Category,
			&item.IconName, &item.ImageURL, &item.DurationMinutes,
			&item.StartingPriceAmountMinor, &item.CurrencyCode, &item.Status,
			&item.CancellationPolicy, &item.LatenessPolicy, &item.AgreementTiming,
			&item.AgreementConfirmationMethod,
			&item.AgreementTemplateTitle, &item.StandaloneSignatureRequired,
			&item.FulfillmentMode, &item.ProviderLocationLabel,
			&item.VirtualDeliveryLabel, &item.MinimumNoticeMinutes,
			&item.HasShortNoticePricing,
			&hasBookableConfiguration,
		); err != nil {
			return nil, fmt.Errorf("scan public service: %w", err)
		}
		item.IsBookable = item.Status == "published" && hasBookableConfiguration
		item.FulfillmentLabel = publicFulfillmentLabel(item.FulfillmentMode)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public services: %w", err)
	}
	return items, nil
}

func publicFulfillmentLabel(mode string) string {
	switch bookingdomain.FulfillmentMode(mode) {
	case bookingdomain.FulfillmentProviderLocation:
		return "At the provider's location"
	case bookingdomain.FulfillmentCustomerLocation:
		return "At your location"
	case bookingdomain.FulfillmentVirtual:
		return "Online service"
	default:
		return ""
	}
}

type publicAvailabilityState struct {
	Date     time.Time
	Location *time.Location
	Slots    []bookingdomain.AvailableSlot
	Rules    []bookingdomain.ShortNoticeRule
}

func (r *Repository) searchPublicAvailabilityDay(ctx context.Context, slug string, serviceID uuid.UUID, date time.Time) (PublicAvailabilityResponse, error) {
	service, err := r.getPublicServiceForBooking(ctx, slug, serviceID)
	if err != nil {
		return PublicAvailabilityResponse{}, err
	}
	state, err := loadPublicAvailabilityState(ctx, r.db, service, date, time.Now().UTC())
	if err != nil {
		return PublicAvailabilityResponse{}, err
	}

	slots, err := publicAvailabilitySlots(service, state, time.Now().UTC())
	if err != nil {
		return PublicAvailabilityResponse{}, err
	}

	return PublicAvailabilityResponse{
		ServiceID:       service.ID.String(),
		Date:            state.Date.Format("2006-01-02"),
		Timezone:        service.Timezone,
		CurrencyCode:    service.CurrencyCode,
		DurationMinutes: service.DurationMinutes,
		LocationLabel:   publicServiceLocationLabel(service),
		Slots:           slots,
	}, nil
}

func (r *Repository) searchPublicAvailabilityRange(
	ctx context.Context,
	slug string,
	serviceID uuid.UUID,
	from *time.Time,
	days int,
) (PublicAvailabilityRangeResponse, error) {
	if days < 1 || days > 31 {
		return PublicAvailabilityRangeResponse{}, fmt.Errorf("days must be between 1 and 31")
	}
	service, err := r.getPublicServiceForBooking(ctx, slug, serviceID)
	if err != nil {
		return PublicAvailabilityRangeResponse{}, err
	}
	now := time.Now().UTC()
	states, err := loadPublicAvailabilityRangeStates(ctx, r.db, service, from, days, now)
	if err != nil {
		return PublicAvailabilityRangeResponse{}, err
	}
	dates := make([]PublicAvailabilityDay, 0, len(states))
	for _, state := range states {
		slots, slotErr := publicAvailabilitySlots(service, state, now)
		if slotErr != nil {
			return PublicAvailabilityRangeResponse{}, slotErr
		}
		dates = append(dates, PublicAvailabilityDay{
			Date:  state.Date.Format("2006-01-02"),
			Slots: slots,
		})
	}
	return PublicAvailabilityRangeResponse{
		ServiceID:       service.ID.String(),
		From:            states[0].Date.Format("2006-01-02"),
		Days:            days,
		Timezone:        service.Timezone,
		CurrencyCode:    service.CurrencyCode,
		DurationMinutes: service.DurationMinutes,
		LocationLabel:   publicServiceLocationLabel(service),
		Dates:           dates,
	}, nil
}

func publicAvailabilitySlots(
	service publicBookingServiceInfo,
	state publicAvailabilityState,
	now time.Time,
) ([]PublicAvailabilitySlot, error) {
	slots := make([]PublicAvailabilitySlot, 0, len(state.Slots))
	for _, slot := range state.Slots {
		rule, err := bookingdomain.SelectShortNoticeRule(
			slot.Start, now, service.MinimumNoticeMinutes, state.Rules,
		)
		if err != nil {
			return nil, err
		}
		fee, err := bookingdomain.ShortNoticeFee(service.PriceAmountMinor, rule)
		if err != nil {
			return nil, err
		}
		slots = append(slots, PublicAvailabilitySlot{
			StartAt:                    slot.Start.Format(time.RFC3339),
			Label:                      slot.Start.Format("03:04 PM"),
			BasePriceAmountMinor:       money.Minor(service.PriceAmountMinor),
			ShortNoticeFeeAmountMinor:  money.Minor(fee),
			EstimatedTotalBeforeTravel: money.Minor(service.PriceAmountMinor + fee),
			ShortNoticeApplies:         rule != nil,
		})
	}
	return slots, nil
}

func loadPublicAvailabilityState(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	date time.Time,
	now time.Time,
) (publicAvailabilityState, error) {
	return loadPublicAvailabilityStateExcluding(ctx, q, service, date, now, nil)
}

func loadPublicAvailabilityStateExcluding(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	date time.Time,
	now time.Time,
	excludeBookingID *uuid.UUID,
) (publicAvailabilityState, error) {
	states, err := loadPublicAvailabilityRangeStatesExcluding(ctx, q, service, &date, 1, now, excludeBookingID)
	if err != nil {
		return publicAvailabilityState{}, err
	}
	return states[0], nil
}

func loadPublicAvailabilityRangeStates(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	from *time.Time,
	days int,
	now time.Time,
) ([]publicAvailabilityState, error) {
	return loadPublicAvailabilityRangeStatesExcluding(ctx, q, service, from, days, now, nil)
}

func loadPublicAvailabilityRangeStatesExcluding(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	from *time.Time,
	days int,
	now time.Time,
	excludeBookingID *uuid.UUID,
) ([]publicAvailabilityState, error) {
	location, err := loadLocation(service.Timezone)
	if err != nil {
		return nil, err
	}
	startSource := now.In(location)
	if from != nil {
		startSource = *from
	}
	rangeStart := time.Date(startSource.Year(), startSource.Month(), startSource.Day(), 0, 0, 0, 0, location)
	rangeEnd := rangeStart.AddDate(0, 0, days)

	windowsByDay, err := loadPublicAvailabilityWindowsByDay(ctx, q, service)
	if err != nil {
		return nil, err
	}
	busyRanges, err := loadPublicBusyRanges(ctx, q, service.ClientID, rangeStart, rangeEnd, excludeBookingID)
	if err != nil {
		return nil, err
	}
	bookingCounts, err := loadPublicServiceBookingCounts(ctx, q, service, rangeStart, rangeEnd, excludeBookingID)
	if err != nil {
		return nil, err
	}
	rules, err := loadPublicShortNoticeRules(ctx, q, service.ID)
	if err != nil {
		return nil, err
	}

	states := make([]publicAvailabilityState, 0, days)
	for offset := 0; offset < days; offset++ {
		selectedDate := rangeStart.AddDate(0, 0, offset)
		dayEnd := selectedDate.AddDate(0, 0, 1)
		dayBusyRanges := make([]bookingdomain.OccupiedRange, 0)
		for _, occupied := range busyRanges {
			if occupied.Start.Before(dayEnd) && occupied.End.After(selectedDate) {
				dayBusyRanges = append(dayBusyRanges, occupied)
			}
		}
		slots, generateErr := bookingdomain.GenerateAvailableSlots(bookingdomain.AvailabilityRequest{
			Date:                 selectedDate,
			Now:                  now,
			Location:             location,
			DurationMinutes:      service.DurationMinutes,
			PrepTimeMinutes:      service.PrepTimeMinutes,
			BufferTimeMinutes:    service.BufferTimeMinutes,
			MinimumNoticeMinutes: service.MinimumNoticeMinutes,
			MaxBookingsPerDay:    service.MaxBookingsPerDay,
			ExistingServiceCount: bookingCounts[selectedDate.Format("2006-01-02")],
			ConcurrentCapacity:   service.ConcurrentBookingCapacity,
			Windows:              windowsByDay[selectedDate.Weekday()],
			BusyRanges:           dayBusyRanges,
		})
		if generateErr != nil {
			return nil, fmt.Errorf("generate availability: %w", generateErr)
		}
		states = append(states, publicAvailabilityState{
			Date: selectedDate, Location: location, Slots: slots, Rules: rules,
		})
	}
	return states, nil
}

func loadPublicServiceBookingCounts(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	rangeStart, rangeEnd time.Time,
	excludeBookingID *uuid.UUID,
) (map[string]int, error) {
	rows, err := q.Query(ctx, `
		SELECT TO_CHAR(start_at AT TIME ZONE $5, 'YYYY-MM-DD'), COUNT(*)::int
		FROM bookings
		WHERE client_id = $1 AND service_id = $2
		  AND start_at >= $3 AND start_at < $4
		  AND status NOT IN ('cancelled', 'canceled', 'declined', 'expired')
		  AND ($6::uuid IS NULL OR id <> $6)
		GROUP BY 1
	`, service.ClientID, service.ID, rangeStart, rangeEnd, service.Timezone, excludeBookingID)
	if err != nil {
		return nil, fmt.Errorf("count service bookings: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var date string
		var count int
		if err := rows.Scan(&date, &count); err != nil {
			return nil, fmt.Errorf("scan service booking count: %w", err)
		}
		counts[date] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service booking counts: %w", err)
	}
	return counts, nil
}

func loadPublicAvailabilityWindowsByDay(ctx context.Context, q publicBookingQuerier, service publicBookingServiceInfo) (map[time.Weekday][]bookingdomain.AvailabilityWindow, error) {
	query := `
		SELECT day_of_week, start_time, end_time, slot_interval_minutes
		FROM provider_availability_windows
		WHERE client_id = $1
		ORDER BY day_of_week, start_time ASC
	`
	argument := service.ClientID
	if service.AvailabilityMode == string(bookingdomain.AvailabilityCustom) {
		query = `
			SELECT day_of_week, start_time, end_time, slot_interval_minutes
			FROM service_availability_windows
			WHERE service_id = $1
			ORDER BY day_of_week, start_time ASC
		`
		argument = service.ID
	}
	rows, err := q.Query(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("list availability windows: %w", err)
	}
	defer rows.Close()

	windows := make(map[time.Weekday][]bookingdomain.AvailabilityWindow)
	for rows.Next() {
		var day int
		var startAt time.Time
		var endAt time.Time
		var interval int
		if err := rows.Scan(&day, &startAt, &endAt, &interval); err != nil {
			return nil, fmt.Errorf("scan availability window: %w", err)
		}
		weekday := time.Weekday(day)
		windows[weekday] = append(windows[weekday], bookingdomain.AvailabilityWindow{
			DayOfWeek:           time.Weekday(day),
			StartMinuteOfDay:    startAt.Hour()*60 + startAt.Minute(),
			EndMinuteOfDay:      endAt.Hour()*60 + endAt.Minute(),
			SlotIntervalMinutes: interval,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate availability windows: %w", err)
	}
	return windows, nil
}

func loadPublicBusyRanges(ctx context.Context, q publicBookingQuerier, clientID uuid.UUID, dayStart, dayEnd time.Time, excludeBookingID *uuid.UUID) ([]bookingdomain.OccupiedRange, error) {
	rows, err := q.Query(ctx, `
		SELECT occupied_start_at, occupied_end_at
		FROM bookings
		WHERE client_id = $1
		  AND occupied_start_at < $3 AND occupied_end_at > $2
		  AND status NOT IN ('cancelled', 'canceled', 'declined', 'expired')
		  AND ($4::uuid IS NULL OR id <> $4)
	`, clientID, dayStart, dayEnd, excludeBookingID)
	if err != nil {
		return nil, fmt.Errorf("list busy ranges: %w", err)
	}
	defer rows.Close()
	ranges := make([]bookingdomain.OccupiedRange, 0)
	for rows.Next() {
		var item bookingdomain.OccupiedRange
		if err := rows.Scan(&item.Start, &item.End); err != nil {
			return nil, fmt.Errorf("scan busy range: %w", err)
		}
		ranges = append(ranges, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate busy ranges: %w", err)
	}
	return ranges, nil
}

func loadPublicShortNoticeRules(ctx context.Context, q publicBookingQuerier, serviceID uuid.UUID) ([]bookingdomain.ShortNoticeRule, error) {
	rows, err := q.Query(ctx, `
		SELECT id, threshold_minutes, surcharge_type,
			surcharge_amount_minor, surcharge_percentage_bps
		FROM service_short_notice_rules
		WHERE service_id = $1
		ORDER BY threshold_minutes ASC
	`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("list short-notice rules: %w", err)
	}
	defer rows.Close()
	rules := make([]bookingdomain.ShortNoticeRule, 0)
	for rows.Next() {
		var id uuid.UUID
		var rule bookingdomain.ShortNoticeRule
		if err := rows.Scan(&id, &rule.ThresholdMinutes, &rule.Type, &rule.AmountMinor, &rule.PercentageBasisPoints); err != nil {
			return nil, fmt.Errorf("scan short-notice rule: %w", err)
		}
		rule.ID = id.String()
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate short-notice rules: %w", err)
	}
	return rules, nil
}

func publicServiceLocationLabel(service publicBookingServiceInfo) string {
	switch bookingdomain.FulfillmentMode(service.FulfillmentMode) {
	case bookingdomain.FulfillmentProviderLocation:
		return service.ProviderLocationLabel
	case bookingdomain.FulfillmentCustomerLocation:
		return "Customer location"
	case bookingdomain.FulfillmentVirtual:
		return firstNonEmpty(service.VirtualDeliveryLabel, "Provider will contact you with the online session details")
	default:
		return ""
	}
}

type quoteFulfillmentSnapshot struct {
	LocationLabel         string
	ProviderLocationLabel string
	ProviderPlaceID       string
	ProviderLatitude      *float64
	ProviderLongitude     *float64
	CustomerLocationLabel string
	CustomerPlaceID       string
	CustomerLatitude      *float64
	CustomerLongitude     *float64
	TravelDistanceMeters  *int
	TravelFeeMinor        int64
}

func (r *Repository) createPublicBookingQuote(ctx context.Context, slug string, input CreatePublicBookingQuoteInput) (PublicBookingQuoteResponse, error) {
	idempotencyKey, err := uuid.Parse(strings.TrimSpace(input.IdempotencyKey))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: idempotency_key must be a UUID", ErrInvalidQuoteRequest)
	}
	serviceID, err := uuid.Parse(strings.TrimSpace(input.ServiceID))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: invalid service_id", ErrInvalidQuoteRequest)
	}
	service, err := r.getPublicServiceForBooking(ctx, slug, serviceID)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	if err := r.EnsureClientMarketConfigured(ctx, service.ClientID); err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	email, err := validatePublicBookingContact(input)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	requestedStart, err := time.Parse(time.RFC3339, strings.TrimSpace(input.StartsAt))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: invalid starts_at", ErrInvalidQuoteRequest)
	}
	requestFingerprint, err := publicQuoteRequestFingerprint(input, serviceID, requestedStart, email)
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("fingerprint booking quote: %w", err)
	}
	if replay, found, err := loadIdempotentPublicQuote(ctx, r.db, service.ClientID, idempotencyKey, requestFingerprint); err != nil {
		return PublicBookingQuoteResponse{}, err
	} else if found {
		return replay, nil
	}
	now := time.Now().UTC()
	state, err := loadPublicAvailabilityState(ctx, r.db, service, requestedStart, now)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	var selected bookingdomain.AvailableSlot
	found := false
	for _, slot := range state.Slots {
		if slot.Start.Equal(requestedStart) {
			selected = slot
			found = true
			break
		}
	}
	if !found {
		return PublicBookingQuoteResponse{}, ErrSlotUnavailable
	}

	fulfillment, err := r.resolveQuoteFulfillment(ctx, service, input.CustomerLocationToken)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	rule, err := bookingdomain.SelectShortNoticeRule(selected.Start, now, service.MinimumNoticeMinutes, state.Rules)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	shortNoticeFee, err := bookingdomain.ShortNoticeFee(service.PriceAmountMinor, rule)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}

	resolution, err := r.resolvePublicDiscount(ctx, service, service.PriceAmountMinor, email, input.DiscountCode)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	if strings.TrimSpace(input.DiscountCode) != "" && resolution.CodeError != "" {
		return PublicBookingQuoteResponse{}, fmt.Errorf("%w: %s", ErrDiscountInvalid, resolution.CodeError)
	}
	rawDeposit, err := bookingdomain.CalculateDepositAmount(
		service.PriceAmountMinor, service.DepositRequired,
		bookingdomain.DepositType(service.DepositType), service.DepositAmountMinor,
		service.DepositPercentageBPS,
	)
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("calculate service deposit: %w", err)
	}
	depositDiscount := rawDeposit - resolution.Snapshot.DepositAmountMinor
	if depositDiscount < 0 {
		depositDiscount = 0
	}
	pricing, err := bookingdomain.CalculatePricing(bookingdomain.PricingInput{
		BaseServiceAmountMinor:     service.PriceAmountMinor,
		ServiceDiscountAmountMinor: resolution.Snapshot.DiscountAmountMinor,
		DepositDiscountAmountMinor: depositDiscount,
		ShortNoticeFeeMinor:        shortNoticeFee,
		TravelFeeMinor:             fulfillment.TravelFeeMinor,
		DepositRequired:            service.DepositRequired,
		DepositType:                bookingdomain.DepositType(service.DepositType),
		DepositAmountMinor:         service.DepositAmountMinor,
		DepositPercentageBPS:       service.DepositPercentageBPS,
	})
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("calculate booking quote: %w", err)
	}
	agreementSnapshot, err := buildPublicQuoteAgreementSnapshot(
		service,
		input,
		selected.Start,
		selected.End,
		fulfillment.LocationLabel,
		pricing.FinalTotalMinor,
		pricing.DepositDueMinor,
	)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}

	expiresAt := now.Add(10 * time.Minute)
	minimumNoticeBoundary := selected.Start.Add(-time.Duration(service.MinimumNoticeMinutes) * time.Minute)
	if minimumNoticeBoundary.Before(expiresAt) {
		expiresAt = minimumNoticeBoundary
	}
	if !expiresAt.After(now) {
		return PublicBookingQuoteResponse{}, ErrSlotUnavailable
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("begin booking quote: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, service.ClientID.String()+":"+idempotencyKey.String()); err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("lock booking quote idempotency key: %w", err)
	}
	if replay, found, err := loadIdempotentPublicQuote(ctx, tx, service.ClientID, idempotencyKey, requestFingerprint); err != nil {
		return PublicBookingQuoteResponse{}, err
	} else if found {
		return replay, nil
	}
	if err := reserveQuotePromotionCapacity(ctx, tx, resolution, email); err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	quoteID := uuid.New()
	quoteToken, err := publictoken.New()
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("create quote token: %w", err)
	}
	var promotionID any
	if resolution.Snapshot.PromotionID != uuid.Nil {
		promotionID = resolution.Snapshot.PromotionID
	}
	var ruleID any
	var ruleThreshold any
	ruleType := ""
	var ruleAmount int64
	var rulePercentage int
	if rule != nil {
		parsed, parseErr := uuid.Parse(rule.ID)
		if parseErr != nil {
			return PublicBookingQuoteResponse{}, fmt.Errorf("invalid short-notice rule id: %w", parseErr)
		}
		ruleID = parsed
		ruleThreshold = rule.ThresholdMinutes
		ruleType = string(rule.Type)
		ruleAmount = rule.AmountMinor
		rulePercentage = rule.PercentageBasisPoints
	}
	response := PublicBookingQuoteResponse{
		QuoteToken:                   quoteToken,
		ExpiresAt:                    expiresAt.Format(time.RFC3339),
		ServiceID:                    service.ID.String(),
		ServiceTitle:                 service.Title,
		StartsAt:                     selected.Start.Format(time.RFC3339),
		EndsAt:                       selected.End.Format(time.RFC3339),
		LocationLabel:                fulfillment.LocationLabel,
		FulfillmentMode:              service.FulfillmentMode,
		BaseServiceAmountMinor:       money.Minor(pricing.BaseServiceAmountMinor),
		DiscountAmountMinor:          money.Minor(pricing.ServiceDiscountAmountMinor),
		DiscountName:                 resolution.Snapshot.DiscountName,
		DiscountCode:                 resolution.Snapshot.DiscountCode,
		ShortNoticeFeeMinor:          money.Minor(pricing.ShortNoticeFeeMinor),
		TravelFeeMinor:               money.Minor(pricing.TravelFeeMinor),
		TravelDistanceMeters:         fulfillment.TravelDistanceMeters,
		DiscountedServiceAmountMinor: money.Minor(pricing.DiscountedServiceAmountMinor),
		TotalAmountMinor:             money.Minor(pricing.FinalTotalMinor),
		DepositAmountMinor:           money.Minor(pricing.DepositDueMinor),
		RemainingAmountMinor:         money.Minor(pricing.RemainingBalanceMinor),
		CountryCode:                  service.CountryCode,
		CurrencyCode:                 service.CurrencyCode,
		Timezone:                     service.Timezone,
		Locale:                       service.Locale,
		Agreement:                    agreementSnapshot.response,
		StandaloneSignatureRequired:  service.StandaloneSignatureRequired,
		SlotHeld:                     false,
		AvailabilityRevalidated:      true,
	}
	if rule != nil {
		response.ShortNoticeLabel = "Short-notice fee"
	}
	responseSnapshot, err := json.Marshal(response)
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("encode booking quote response: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO booking_quotes (
			id, public_token, client_id, service_id,
			service_title, business_name, service_image_url, duration_minutes,
			appointment_start_at, appointment_end_at, occupied_start_at, occupied_end_at,
			prep_time_minutes, buffer_time_minutes, timezone, fulfillment_mode, location_label,
			provider_location_label, provider_place_id, provider_latitude, provider_longitude,
			customer_location_label, customer_place_id, customer_latitude, customer_longitude,
			travel_distance_meters, virtual_delivery_label, virtual_join_url, virtual_instructions,
			country_code, currency_code, locale, cancellation_policy, lateness_policy,
			customer_name_snapshot, customer_phone_snapshot, booking_notes_snapshot,
			agreement_template_family_id_snapshot, agreement_template_version_id_snapshot,
			agreement_title_snapshot, agreement_booking_summary_snapshot,
			agreement_resolved_document_snapshot, agreement_schema_version_snapshot,
			agreement_renderer_version_snapshot, agreement_rendered_html_snapshot,
			agreement_resolved_terms_hash_snapshot, agreement_confirmation_method_snapshot,
			agreement_timing_snapshot, standalone_signature_required_snapshot,
			base_service_amount_minor, promotion_id, discount_name, discount_source,
			discount_code, discount_type, discount_percentage_bps, discount_value_minor,
			discount_amount_minor, short_notice_rule_id, short_notice_threshold_minutes,
			short_notice_surcharge_type, short_notice_surcharge_amount_minor,
			short_notice_surcharge_percentage_bps, short_notice_fee_minor, travel_fee_minor,
			discounted_service_amount_minor, total_amount_minor, deposit_amount_minor,
			remaining_amount_minor, customer_email_normalized,
			idempotency_key, request_fingerprint, response_snapshot,
			expires_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,
			$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,$51,$52,$53,$54,$55,$56,
			$57,$58,$59,$60,$61,$62,$63,$64,$65,$66,$67,$68,$69,$70,$71,$72,$73,$74,NOW(),NOW()
		)
	`, quoteID, quoteToken, service.ClientID, service.ID,
		service.Title, service.BusinessName, service.ImageURL, service.DurationMinutes,
		selected.Start, selected.End, selected.OccupiedStart, selected.OccupiedEnd,
		service.PrepTimeMinutes, service.BufferTimeMinutes, service.Timezone,
		service.FulfillmentMode, fulfillment.LocationLabel,
		fulfillment.ProviderLocationLabel, nullIfBlank(fulfillment.ProviderPlaceID),
		fulfillment.ProviderLatitude, fulfillment.ProviderLongitude,
		fulfillment.CustomerLocationLabel, nullIfBlank(fulfillment.CustomerPlaceID),
		fulfillment.CustomerLatitude, fulfillment.CustomerLongitude,
		fulfillment.TravelDistanceMeters, service.VirtualDeliveryLabel,
		nullIfBlank(service.VirtualJoinURL), nullIfBlank(service.VirtualInstructions),
		service.CountryCode, service.CurrencyCode, service.Locale,
		service.CancellationPolicy, service.LatenessPolicy,
		strings.TrimSpace(input.CustomerName), strings.TrimSpace(input.CustomerPhone),
		strings.TrimSpace(input.BookingNotes), agreementSnapshot.familyID,
		agreementSnapshot.versionID, agreementSnapshot.title, agreementSnapshot.bookingSummaryJSON,
		agreementSnapshot.documentJSON, agreementSnapshot.schemaVersion,
		agreementSnapshot.rendererVersion, agreementSnapshot.renderedHTML,
		agreementSnapshot.resolvedTermsHash, agreementSnapshot.confirmationMethod,
		agreementSnapshot.timing, service.StandaloneSignatureRequired,
		service.PriceAmountMinor, promotionID,
		resolution.Snapshot.DiscountName, resolution.Snapshot.DiscountSource,
		resolution.Snapshot.DiscountCode, resolution.Snapshot.DiscountType,
		resolution.Snapshot.DiscountPercentageBPS, resolution.Snapshot.DiscountValueMinor,
		resolution.Snapshot.DiscountAmountMinor, ruleID, ruleThreshold, ruleType,
		ruleAmount, rulePercentage, shortNoticeFee, fulfillment.TravelFeeMinor,
		pricing.DiscountedServiceAmountMinor, pricing.FinalTotalMinor,
		pricing.DepositDueMinor, pricing.RemainingBalanceMinor, email,
		idempotencyKey, requestFingerprint, responseSnapshot, expiresAt,
	); err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("insert booking quote: %w", err)
	}
	if err := insertQuotePromotionReservations(ctx, tx, quoteID, resolution, email); err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("commit booking quote: %w", err)
	}

	return response, nil
}

func publicQuoteRequestFingerprint(input CreatePublicBookingQuoteInput, serviceID uuid.UUID, startsAt time.Time, email string) (string, error) {
	normalized := struct {
		ServiceID             string `json:"service_id"`
		StartsAt              string `json:"starts_at"`
		CustomerName          string `json:"customer_name"`
		CustomerEmail         string `json:"customer_email"`
		CustomerPhone         string `json:"customer_phone"`
		BookingNotes          string `json:"booking_notes"`
		DiscountCode          string `json:"discount_code"`
		CustomerLocationToken string `json:"customer_location_token"`
	}{
		ServiceID: serviceID.String(), StartsAt: startsAt.UTC().Format(time.RFC3339Nano),
		CustomerName: strings.TrimSpace(input.CustomerName), CustomerEmail: email,
		CustomerPhone: strings.TrimSpace(input.CustomerPhone), BookingNotes: strings.TrimSpace(input.BookingNotes),
		DiscountCode:          strings.ToUpper(strings.TrimSpace(input.DiscountCode)),
		CustomerLocationToken: strings.TrimSpace(input.CustomerLocationToken),
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func loadIdempotentPublicQuote(
	ctx context.Context,
	q publicBookingQuerier,
	clientID, idempotencyKey uuid.UUID,
	requestFingerprint string,
) (PublicBookingQuoteResponse, bool, error) {
	var storedFingerprint string
	var storedResponse []byte
	err := q.QueryRow(ctx, `
		SELECT request_fingerprint, response_snapshot
		FROM booking_quotes
		WHERE client_id = $1 AND idempotency_key = $2
	`, clientID, idempotencyKey).Scan(&storedFingerprint, &storedResponse)
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicBookingQuoteResponse{}, false, nil
	}
	if err != nil {
		return PublicBookingQuoteResponse{}, false, fmt.Errorf("load idempotent booking quote: %w", err)
	}
	if storedFingerprint != requestFingerprint {
		return PublicBookingQuoteResponse{}, false, ErrIdempotencyConflict
	}
	var replay PublicBookingQuoteResponse
	if err := json.Unmarshal(storedResponse, &replay); err != nil {
		return PublicBookingQuoteResponse{}, false, fmt.Errorf("decode idempotent booking quote: %w", err)
	}
	replay.IdempotentReplay = true
	return replay, true, nil
}

func validPublicBookingPhone(value string) bool {
	value = strings.TrimSpace(value)
	digits := 0
	for index, character := range value {
		switch {
		case character >= '0' && character <= '9':
			digits++
		case character == '+' && index == 0:
		case character == ' ' || character == '-' || character == '(' || character == ')':
		default:
			return false
		}
	}
	return digits >= 7 && digits <= 15
}

func validatePublicBookingContact(input CreatePublicBookingQuoteInput) (string, error) {
	email := strings.ToLower(strings.TrimSpace(input.CustomerEmail))
	parsedEmail, err := mail.ParseAddress(email)
	if err != nil || strings.ToLower(parsedEmail.Address) != email {
		return "", fmt.Errorf("%w: enter a valid email address", ErrInvalidContact)
	}
	if strings.TrimSpace(input.CustomerName) == "" {
		return "", fmt.Errorf("%w: customer_name is required", ErrInvalidContact)
	}
	if !validPublicBookingPhone(input.CustomerPhone) {
		return "", fmt.Errorf("%w: enter a valid phone number", ErrInvalidContact)
	}
	return email, nil
}

type publicQuoteAgreementSnapshot struct {
	familyID           any
	versionID          any
	title              string
	bookingSummaryJSON []byte
	documentJSON       any
	schemaVersion      any
	rendererVersion    any
	renderedHTML       string
	resolvedTermsHash  string
	confirmationMethod string
	timing             string
	response           *PublicBookingAgreementSnapshot
}

func buildPublicQuoteAgreementSnapshot(
	service publicBookingServiceInfo,
	input CreatePublicBookingQuoteInput,
	startAt, endAt time.Time,
	locationLabel string,
	totalAmountMinor, depositAmountMinor int64,
) (publicQuoteAgreementSnapshot, error) {
	emptySummary, _ := json.Marshal(agreementrender.BookingSummary{})
	result := publicQuoteAgreementSnapshot{bookingSummaryJSON: emptySummary}
	if service.AgreementTemplateFamilyID == uuid.Nil {
		return result, nil
	}
	if service.AgreementTemplateVersionID == uuid.Nil || service.AgreementDocument == nil {
		return publicQuoteAgreementSnapshot{}, fmt.Errorf("published agreement template is incomplete")
	}
	location, err := time.LoadLocation(service.Timezone)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, fmt.Errorf("load service timezone: %w", err)
	}
	startAt = startAt.In(location)
	endAt = endAt.In(location)
	values, err := buildPublicAgreementResolvedVariables(
		service,
		CreatePublicBookingInput{
			FullName: strings.TrimSpace(input.CustomerName), Email: strings.TrimSpace(input.CustomerEmail),
			Phone: strings.TrimSpace(input.CustomerPhone), Notes: strings.TrimSpace(input.BookingNotes),
		},
		startAt,
		endAt,
		locationLabel,
		totalAmountMinor,
		depositAmountMinor,
	)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, err
	}
	totalAmount, err := formatMarketMoney(totalAmountMinor, service.CountryCode, service.CurrencyCode)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, err
	}
	summary := agreementrender.BookingSummary{
		ServiceName: service.Title,
		Date:        startAt.Format("Monday, Jan 2, 2006"),
		Time:        fmt.Sprintf("%s - %s", startAt.Format("03:04 PM"), endAt.Format("03:04 PM")),
		Location:    locationLabel,
		TotalAmount: totalAmount,
	}
	snapshot, err := agreementrender.BuildSnapshot(
		service.AgreementTemplateTitle,
		summary,
		*service.AgreementDocument,
		service.AgreementConfirmationMethod,
		values,
	)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, fmt.Errorf("resolve booking agreement: %w", err)
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, fmt.Errorf("encode agreement booking summary: %w", err)
	}
	documentJSON, err := json.Marshal(snapshot.ResolvedDocument)
	if err != nil {
		return publicQuoteAgreementSnapshot{}, fmt.Errorf("encode resolved agreement: %w", err)
	}
	return publicQuoteAgreementSnapshot{
		familyID: service.AgreementTemplateFamilyID, versionID: service.AgreementTemplateVersionID,
		title: service.AgreementTemplateTitle, bookingSummaryJSON: summaryJSON,
		documentJSON: documentJSON, schemaVersion: snapshot.SchemaVersion,
		rendererVersion: snapshot.RendererVersion, renderedHTML: snapshot.RenderedHTML,
		resolvedTermsHash:  snapshot.ResolvedTermsHash,
		confirmationMethod: string(service.AgreementConfirmationMethod), timing: service.AgreementTiming,
		response: &PublicBookingAgreementSnapshot{
			Title: service.AgreementTemplateTitle, RenderedHTML: snapshot.RenderedHTML,
			ConfirmationMethod: string(service.AgreementConfirmationMethod),
			Timing:             service.AgreementTiming, ResolvedTermsHash: snapshot.ResolvedTermsHash,
		},
	}, nil
}

func (r *Repository) resolveQuoteFulfillment(ctx context.Context, service publicBookingServiceInfo, customerToken string) (quoteFulfillmentSnapshot, error) {
	snapshot := quoteFulfillmentSnapshot{
		ProviderLocationLabel: service.ProviderLocationLabel,
		ProviderPlaceID:       service.ProviderPlaceID,
		ProviderLatitude:      service.ProviderLatitude,
		ProviderLongitude:     service.ProviderLongitude,
	}
	switch bookingdomain.FulfillmentMode(service.FulfillmentMode) {
	case bookingdomain.FulfillmentProviderLocation:
		snapshot.LocationLabel = service.ProviderLocationLabel
	case bookingdomain.FulfillmentCustomerLocation:
		if strings.TrimSpace(customerToken) == "" {
			return quoteFulfillmentSnapshot{}, ErrLocationRequired
		}
		location, err := r.loadResolvedLocation(ctx, customerToken)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return quoteFulfillmentSnapshot{}, ErrLocationRequired
			}
			return quoteFulfillmentSnapshot{}, err
		}
		snapshot.LocationLabel = location.FormattedAddress
		snapshot.CustomerLocationLabel = location.FormattedAddress
		snapshot.CustomerPlaceID = location.ProviderPlaceID
		snapshot.CustomerLatitude = location.Latitude
		snapshot.CustomerLongitude = location.Longitude
		snapshot.TravelFeeMinor = service.TravelFeeMinor
		if service.MaxTravelDistanceMeters != nil {
			if snapshot.ProviderLatitude == nil || snapshot.ProviderLongitude == nil {
				if r.googleMapsAPIKey == "" {
					return quoteFulfillmentSnapshot{}, ErrLocationNotAllowed
				}
				resolved, geocodeErr := r.googleGeocodeAddress(ctx, service.ProviderLocationLabel)
				if geocodeErr != nil {
					return quoteFulfillmentSnapshot{}, ErrLocationNotAllowed
				}
				snapshot.ProviderLocationLabel = resolved.FormattedAddress
				snapshot.ProviderLatitude = &resolved.Latitude
				snapshot.ProviderLongitude = &resolved.Longitude
			}
			if snapshot.CustomerLatitude == nil || snapshot.CustomerLongitude == nil {
				return quoteFulfillmentSnapshot{}, ErrLocationNotAllowed
			}
			distance := haversineDistanceMeters(
				*snapshot.ProviderLatitude, *snapshot.ProviderLongitude,
				*snapshot.CustomerLatitude, *snapshot.CustomerLongitude,
			)
			snapshot.TravelDistanceMeters = &distance
			if distance > *service.MaxTravelDistanceMeters {
				return quoteFulfillmentSnapshot{}, ErrOutsideServiceArea
			}
		}
	case bookingdomain.FulfillmentVirtual:
		snapshot = quoteFulfillmentSnapshot{
			LocationLabel: firstNonEmpty(service.VirtualDeliveryLabel, "Provider will contact you with the online session details"),
		}
	default:
		return quoteFulfillmentSnapshot{}, fmt.Errorf("invalid fulfillment mode")
	}
	return snapshot, nil
}

func reserveQuotePromotionCapacity(ctx context.Context, tx pgx.Tx, resolution promotionResolution, customerEmail string) error {
	promotions := make([]*promotionCandidate, 0, 2)
	if resolution.AutomaticPromotion != nil {
		promotions = append(promotions, resolution.AutomaticPromotion)
	}
	if resolution.CodePromotion != nil {
		promotions = append(promotions, resolution.CodePromotion)
	}
	sort.Slice(promotions, func(i, j int) bool { return promotions[i].ID.String() < promotions[j].ID.String() })
	for _, promotion := range promotions {
		if err := tx.QueryRow(ctx, `SELECT id FROM promotions WHERE id = $1 FOR UPDATE`, promotion.ID).Scan(new(uuid.UUID)); err != nil {
			return fmt.Errorf("lock promotion capacity: %w", err)
		}
		var total int
		if err := tx.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM promotion_redemptions WHERE promotion_id = $1) +
				(SELECT COUNT(*) FROM booking_quote_promotions bqp
				 INNER JOIN booking_quotes bq ON bq.id = bqp.booking_quote_id
				 WHERE bqp.promotion_id = $1 AND bq.consumed_at IS NULL AND bq.expires_at > NOW())
		`, promotion.ID).Scan(&total); err != nil {
			return fmt.Errorf("count reserved promotion capacity: %w", err)
		}
		if promotion.MaxRedemptions > 0 && total >= promotion.MaxRedemptions {
			return ErrPromotionUnavailable
		}
		if promotion.MaxRedemptionsPerCustomer > 0 {
			var customerTotal int
			if err := tx.QueryRow(ctx, `
				SELECT
					(SELECT COUNT(*) FROM promotion_redemptions
					 WHERE promotion_id = $1 AND LOWER(customer_email) = LOWER($2)) +
					(SELECT COUNT(*) FROM booking_quote_promotions bqp
					 INNER JOIN booking_quotes bq ON bq.id = bqp.booking_quote_id
					 WHERE bqp.promotion_id = $1
					   AND bqp.customer_email_normalized = LOWER($2)
					   AND bq.consumed_at IS NULL AND bq.expires_at > NOW())
			`, promotion.ID, customerEmail).Scan(&customerTotal); err != nil {
				return fmt.Errorf("count customer promotion capacity: %w", err)
			}
			if customerTotal >= promotion.MaxRedemptionsPerCustomer {
				return ErrPromotionUnavailable
			}
		}
	}
	return nil
}

func insertQuotePromotionReservations(ctx context.Context, tx pgx.Tx, quoteID uuid.UUID, resolution promotionResolution, email string) error {
	insert := func(promotion *promotionCandidate, amount int64) error {
		if promotion == nil || amount <= 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO booking_quote_promotions (
				booking_quote_id, promotion_id, customer_email_normalized,
				code_used, discount_amount_minor, currency_code, created_at
			) VALUES ($1,$2,$3,$4,$5,$6,NOW())
		`, quoteID, promotion.ID, email, promotion.Code, amount, promotion.CurrencyCode); err != nil {
			return fmt.Errorf("reserve quote promotion: %w", err)
		}
		return nil
	}
	if err := insert(resolution.AutomaticPromotion, resolution.AutomaticAmountMinor); err != nil {
		return err
	}
	return insert(resolution.CodePromotion, resolution.CodeAmountMinor)
}

func haversineDistanceMeters(latitudeA, longitudeA, latitudeB, longitudeB float64) int {
	const earthRadiusMeters = 6371000.0
	toRadians := func(value float64) float64 { return value * math.Pi / 180 }
	latA := toRadians(latitudeA)
	latB := toRadians(latitudeB)
	deltaLat := toRadians(latitudeB - latitudeA)
	deltaLongitude := toRadians(longitudeB - longitudeA)
	a := math.Sin(deltaLat/2)*math.Sin(deltaLat/2) +
		math.Cos(latA)*math.Cos(latB)*math.Sin(deltaLongitude/2)*math.Sin(deltaLongitude/2)
	return int(math.Round(earthRadiusMeters * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))))
}

func nullUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}
