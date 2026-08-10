package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
		); err != nil {
			return nil, fmt.Errorf("scan public service: %w", err)
		}
		item.IsBookable = item.Status == "published"
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

func (r *Repository) GetPublicAvailability(ctx context.Context, slug string, serviceID uuid.UUID, date time.Time) (PublicAvailabilityResponse, error) {
	service, err := r.getPublicServiceForBooking(ctx, slug, serviceID)
	if err != nil {
		return PublicAvailabilityResponse{}, err
	}
	state, err := loadPublicAvailabilityState(ctx, r.db, service, date, time.Now().UTC())
	if err != nil {
		return PublicAvailabilityResponse{}, err
	}

	slots := make([]PublicAvailabilitySlot, 0, len(state.Slots))
	for _, slot := range state.Slots {
		rule, err := bookingdomain.SelectShortNoticeRule(
			slot.Start, time.Now().UTC(), service.MinimumNoticeMinutes, state.Rules,
		)
		if err != nil {
			return PublicAvailabilityResponse{}, err
		}
		fee, err := bookingdomain.ShortNoticeFee(service.PriceAmountMinor, rule)
		if err != nil {
			return PublicAvailabilityResponse{}, err
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

func loadPublicAvailabilityState(
	ctx context.Context,
	q publicBookingQuerier,
	service publicBookingServiceInfo,
	date time.Time,
	now time.Time,
) (publicAvailabilityState, error) {
	location, err := loadLocation(service.Timezone)
	if err != nil {
		return publicAvailabilityState{}, err
	}
	selectedDate := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, location)
	dayEnd := selectedDate.AddDate(0, 0, 1)

	windows, err := loadPublicAvailabilityWindows(ctx, q, service, selectedDate.Weekday())
	if err != nil {
		return publicAvailabilityState{}, err
	}
	busyRanges, err := loadPublicBusyRanges(ctx, q, service.ClientID, selectedDate, dayEnd)
	if err != nil {
		return publicAvailabilityState{}, err
	}
	var existingServiceCount int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*)::int
		FROM bookings
		WHERE client_id = $1 AND service_id = $2
		  AND start_at >= $3 AND start_at < $4
		  AND status NOT IN ('cancelled', 'canceled')
	`, service.ClientID, service.ID, selectedDate, dayEnd).Scan(&existingServiceCount); err != nil {
		return publicAvailabilityState{}, fmt.Errorf("count service bookings: %w", err)
	}

	slots, err := bookingdomain.GenerateAvailableSlots(bookingdomain.AvailabilityRequest{
		Date:                 selectedDate,
		Now:                  now,
		Location:             location,
		DurationMinutes:      service.DurationMinutes,
		PrepTimeMinutes:      service.PrepTimeMinutes,
		BufferTimeMinutes:    service.BufferTimeMinutes,
		MinimumNoticeMinutes: service.MinimumNoticeMinutes,
		MaxBookingsPerDay:    service.MaxBookingsPerDay,
		ExistingServiceCount: existingServiceCount,
		Windows:              windows,
		BusyRanges:           busyRanges,
	})
	if err != nil {
		return publicAvailabilityState{}, fmt.Errorf("generate availability: %w", err)
	}
	rules, err := loadPublicShortNoticeRules(ctx, q, service.ID)
	if err != nil {
		return publicAvailabilityState{}, err
	}
	return publicAvailabilityState{Date: selectedDate, Location: location, Slots: slots, Rules: rules}, nil
}

func loadPublicAvailabilityWindows(ctx context.Context, q publicBookingQuerier, service publicBookingServiceInfo, weekday time.Weekday) ([]bookingdomain.AvailabilityWindow, error) {
	query := `
		SELECT day_of_week, start_time, end_time, slot_interval_minutes
		FROM provider_availability_windows
		WHERE client_id = $1 AND day_of_week = $2
		ORDER BY start_time ASC
	`
	argument := service.ClientID
	if service.AvailabilityMode == string(bookingdomain.AvailabilityCustom) {
		query = `
			SELECT day_of_week, start_time, end_time, slot_interval_minutes
			FROM service_availability_windows
			WHERE service_id = $1 AND day_of_week = $2
			ORDER BY start_time ASC
		`
		argument = service.ID
	}
	rows, err := q.Query(ctx, query, argument, int(weekday))
	if err != nil {
		return nil, fmt.Errorf("list availability windows: %w", err)
	}
	defer rows.Close()

	windows := make([]bookingdomain.AvailabilityWindow, 0)
	for rows.Next() {
		var day int
		var startAt time.Time
		var endAt time.Time
		var interval int
		if err := rows.Scan(&day, &startAt, &endAt, &interval); err != nil {
			return nil, fmt.Errorf("scan availability window: %w", err)
		}
		windows = append(windows, bookingdomain.AvailabilityWindow{
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

func loadPublicBusyRanges(ctx context.Context, q publicBookingQuerier, clientID uuid.UUID, dayStart, dayEnd time.Time) ([]bookingdomain.OccupiedRange, error) {
	rows, err := q.Query(ctx, `
		SELECT occupied_start_at, occupied_end_at
		FROM bookings
		WHERE client_id = $1
		  AND occupied_start_at < $3 AND occupied_end_at > $2
		  AND status NOT IN ('cancelled', 'canceled')
	`, clientID, dayStart, dayEnd)
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

func (r *Repository) CreatePublicBookingQuote(ctx context.Context, slug string, input CreatePublicBookingQuoteInput) (PublicBookingQuoteResponse, error) {
	serviceID, err := uuid.Parse(strings.TrimSpace(input.ServiceID))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("invalid service_id")
	}
	service, err := r.getPublicServiceForBooking(ctx, slug, serviceID)
	if err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	if err := r.EnsureClientMarketConfigured(ctx, service.ClientID); err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	email := strings.ToLower(strings.TrimSpace(input.CustomerEmail))
	if email == "" {
		return PublicBookingQuoteResponse{}, fmt.Errorf("customer_email is required")
	}
	if strings.TrimSpace(input.CustomerName) == "" {
		return PublicBookingQuoteResponse{}, fmt.Errorf("customer_name is required")
	}
	if strings.TrimSpace(input.CustomerPhone) == "" {
		return PublicBookingQuoteResponse{}, fmt.Errorf("customer_phone is required")
	}
	requestedStart, err := time.Parse(time.RFC3339, strings.TrimSpace(input.StartsAt))
	if err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("invalid starts_at")
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
			remaining_amount_minor, customer_email_normalized, expires_at, created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,
			$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,$51,$52,$53,$54,$55,$56,
			$57,$58,$59,$60,$61,$62,$63,$64,$65,$66,$67,$68,$69,$70,$71,NOW(),NOW()
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
		pricing.DepositDueMinor, pricing.RemainingBalanceMinor, email, expiresAt,
	); err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("insert booking quote: %w", err)
	}
	if err := insertQuotePromotionReservations(ctx, tx, quoteID, resolution, email); err != nil {
		return PublicBookingQuoteResponse{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PublicBookingQuoteResponse{}, fmt.Errorf("commit booking quote: %w", err)
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
	}
	if rule != nil {
		response.ShortNoticeLabel = "Short-notice fee"
	}
	return response, nil
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
				address, latitude, longitude, geocodeErr := r.googleGeocodeAddress(ctx, service.ProviderLocationLabel)
				if geocodeErr != nil {
					return quoteFulfillmentSnapshot{}, ErrLocationNotAllowed
				}
				snapshot.ProviderLocationLabel = address
				snapshot.ProviderLatitude = &latitude
				snapshot.ProviderLongitude = &longitude
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
