package appdata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	agreementservice "booking/go-server/internal/agreements/service"
	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db               *pgxpool.Pool
	httpClient       *http.Client
	googleMapsAPIKey string
	agreementTokens  *agreementservice.PublicTokenManager
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{
		db:         db,
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

func (r *Repository) ConfigureGoogleMaps(apiKey string) {
	r.googleMapsAPIKey = strings.TrimSpace(apiKey)
}

func (r *Repository) ConfigureAgreementTokens(manager *agreementservice.PublicTokenManager) {
	r.agreementTokens = manager
}

func (r *Repository) GetDashboard(ctx context.Context, clientID uuid.UUID) (DashboardResponse, error) {
	profile, err := r.getDashboardProfile(ctx, clientID)
	if err != nil {
		return DashboardResponse{}, err
	}

	stats, err := r.getDashboardStats(ctx, clientID)
	if err != nil {
		return DashboardResponse{}, err
	}

	attention, err := r.getTopAttentionItem(ctx, clientID)
	if err != nil {
		return DashboardResponse{}, err
	}

	bookings, err := r.getTodayBookings(ctx, clientID)
	if err != nil {
		return DashboardResponse{}, err
	}

	return DashboardResponse{
		Profile:       profile,
		Stats:         stats,
		AttentionItem: attention,
		TodayBookings: bookings,
	}, nil
}

func (r *Repository) ListBookings(ctx context.Context, clientID uuid.UUID) ([]BookingItem, error) {
	const query = `
		SELECT
			b.id,
			b.customer_id,
			b.title,
			b.start_at,
			b.end_at,
			c.full_name,
			b.status,
			b.payment_status,
			b.agreement_status,
			COALESCE(s.icon_name, ''),
			b.location_label,
			b.notes
		FROM bookings b
		LEFT JOIN customers c ON c.id = b.customer_id
		LEFT JOIN services s ON s.id = b.service_id
		WHERE b.client_id = $1
		ORDER BY b.start_at ASC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list bookings: %w", err)
	}
	defer rows.Close()

	bookings := make([]BookingItem, 0)
	for rows.Next() {
		var item BookingItem
		var id, customerID uuid.UUID
		if err := rows.Scan(
			&id,
			&customerID,
			&item.Title,
			&item.StartAt,
			&item.EndAt,
			&item.CustomerName,
			&item.Status,
			&item.PaymentStatus,
			&item.AgreementStatus,
			&item.IconName,
			&item.LocationLabel,
			&item.Notes,
		); err != nil {
			return nil, fmt.Errorf("scan booking: %w", err)
		}
		item.ID = id.String()
		item.CustomerID = customerID.String()
		bookings = append(bookings, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate bookings: %w", err)
	}

	return bookings, nil
}

type bookingInsightRecord struct {
	ServiceID     string
	StartAt       time.Time
	EndAt         time.Time
	Status        string
	PaymentStatus string
}

type scheduleAvailabilityWindow struct {
	DayOfWeek    int
	StartMinutes int
	EndMinutes   int
}

type scheduleOptimizationService struct {
	ID               string
	Title            string
	DurationMinutes  int
	PrepMinutes      int
	BufferMinutes    int
	AvailabilityMode string
	MaxPerDay        int
}

func (r *Repository) GetBookingOptimizationInsight(ctx context.Context, clientID uuid.UUID) (*BookingOptimizationInsight, error) {
	timezone := "Africa/Lagos"
	if err := r.db.QueryRow(
		ctx,
		`SELECT COALESCE(NULLIF(timezone, ''), 'Africa/Lagos') FROM client_profiles WHERE client_id = $1`,
		clientID,
	).Scan(&timezone); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get schedule timezone: %w", err)
	}

	location, err := loadLocation(timezone)
	if err != nil {
		return nil, err
	}

	const bookingsQuery = `
		SELECT
			COALESCE(service_id::text, ''),
			start_at,
			end_at,
			status,
			payment_status
		FROM bookings
		WHERE client_id = $1
		  AND end_at >= NOW()
		  AND start_at <= NOW() + INTERVAL '21 days'
		  AND status NOT IN ('cancelled', 'canceled')
		ORDER BY start_at ASC
	`

	rows, err := r.db.Query(ctx, bookingsQuery, clientID)
	if err != nil {
		return nil, fmt.Errorf("list booking optimization records: %w", err)
	}
	defer rows.Close()

	records := make([]bookingInsightRecord, 0)
	for rows.Next() {
		var record bookingInsightRecord
		if err := rows.Scan(
			&record.ServiceID,
			&record.StartAt,
			&record.EndAt,
			&record.Status,
			&record.PaymentStatus,
		); err != nil {
			return nil, fmt.Errorf("scan booking optimization record: %w", err)
		}
		records = append(records, record)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate booking optimization records: %w", err)
	}

	windowRows, err := r.db.Query(
		ctx,
		`SELECT day_of_week, start_time, end_time FROM provider_availability_windows WHERE client_id = $1 ORDER BY day_of_week ASC, start_time ASC`,
		clientID,
	)
	if err != nil {
		return nil, fmt.Errorf("list provider availability windows: %w", err)
	}
	defer windowRows.Close()

	windows := make([]scheduleAvailabilityWindow, 0)
	for windowRows.Next() {
		var startAt time.Time
		var endAt time.Time
		var item scheduleAvailabilityWindow
		if err := windowRows.Scan(&item.DayOfWeek, &startAt, &endAt); err != nil {
			return nil, fmt.Errorf("scan availability window: %w", err)
		}
		item.StartMinutes = startAt.Hour()*60 + startAt.Minute()
		item.EndMinutes = endAt.Hour()*60 + endAt.Minute()
		windows = append(windows, item)
	}
	if err := windowRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate availability windows: %w", err)
	}

	serviceRows, err := r.db.Query(
		ctx,
		`SELECT
			id::text,
			title,
			duration_minutes,
			prep_time_minutes,
			buffer_time_minutes,
			availability_mode,
			COALESCE(max_bookings_per_day, 0)
		FROM services
		WHERE client_id = $1
		  AND status = 'published'
		  AND COALESCE(is_hidden, FALSE) = FALSE`,
		clientID,
	)
	if err != nil {
		return nil, fmt.Errorf("list optimization services: %w", err)
	}
	defer serviceRows.Close()

	services := make([]scheduleOptimizationService, 0)
	for serviceRows.Next() {
		var item scheduleOptimizationService
		if err := serviceRows.Scan(
			&item.ID,
			&item.Title,
			&item.DurationMinutes,
			&item.PrepMinutes,
			&item.BufferMinutes,
			&item.AvailabilityMode,
			&item.MaxPerDay,
		); err != nil {
			return nil, fmt.Errorf("scan optimization service: %w", err)
		}
		services = append(services, item)
	}
	if err := serviceRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate optimization services: %w", err)
	}

	insight := buildBookingOptimizationInsight(records, windows, services, location, time.Now().In(location))
	return insight, nil
}

func buildBookingOptimizationInsight(
	records []bookingInsightRecord,
	windows []scheduleAvailabilityWindow,
	services []scheduleOptimizationService,
	location *time.Location,
	now time.Time,
) *BookingOptimizationInsight {
	type localizedBooking struct {
		ServiceID     string
		StartAt       time.Time
		EndAt         time.Time
		Status        string
		PaymentStatus string
		BookedMinutes int
	}

	openMinutesByWeekday := make(map[time.Weekday]int)
	for _, window := range windows {
		weekday := time.Weekday(window.DayOfWeek)
		duration := window.EndMinutes - window.StartMinutes
		if duration > 0 {
			openMinutesByWeekday[weekday] += duration
		}
	}

	upcoming := make([]localizedBooking, 0, len(records))
	for _, record := range records {
		startLocal := record.StartAt.In(location)
		endLocal := record.EndAt.In(location)
		if endLocal.Before(now) {
			continue
		}
		upcoming = append(upcoming, localizedBooking{
			ServiceID:     record.ServiceID,
			StartAt:       startLocal,
			EndAt:         endLocal,
			Status:        record.Status,
			PaymentStatus: record.PaymentStatus,
			BookedMinutes: int(math.Round(endLocal.Sub(startLocal).Minutes())),
		})
	}

	if len(upcoming) == 0 {
		return &BookingOptimizationInsight{
			OptimizationsAvailable: false,
			Items:                  []BookingOptimizationItem{},
		}
	}

	byDay := make(map[string][]localizedBooking)
	dayServiceCounts := make(map[string]map[string]int)
	pendingSoonCount := 0

	for _, booking := range upcoming {
		dayKey := booking.StartAt.Format("2006-01-02")
		byDay[dayKey] = append(byDay[dayKey], booking)

		if booking.ServiceID != "" {
			counts := dayServiceCounts[dayKey]
			if counts == nil {
				counts = make(map[string]int)
				dayServiceCounts[dayKey] = counts
			}
			counts[booking.ServiceID] += 1
		}

		status := strings.ToLower(strings.TrimSpace(booking.Status + " " + booking.PaymentStatus))
		if strings.Contains(status, "pending") && booking.StartAt.Before(now.Add(7*24*time.Hour)) {
			pendingSoonCount += 1
		}
	}

	items := make([]BookingOptimizationItem, 0, 4)

	type candidateService struct {
		ID            string
		Title         string
		TotalMinutes  int
		MaxPerDay     int
		LastBookingAt int
		HasCutoff     bool
	}

	bestGap := struct {
		found       bool
		gapMinutes  int
		dayLabel    string
		startLabel  string
		endLabel    string
		serviceID   string
		serviceName string
	}{}

	bestOverload := struct {
		found        bool
		utilization  float64
		dayLabel     string
		bookingCount int
	}{}

	bestRuleConflict := struct {
		found       bool
		serviceID   string
		serviceName string
		maxPerDay   int
		physicalCap int
	}{}

	for dayKey, dayBookings := range byDay {
		sort.Slice(dayBookings, func(i, j int) bool {
			return dayBookings[i].StartAt.Before(dayBookings[j].StartAt)
		})

		weekday := dayBookings[0].StartAt.Weekday()
		dayLabel := dayBookings[0].StartAt.Format("Monday")
		openMinutes := openMinutesByWeekday[weekday]
		bookedMinutes := 0
		for _, booking := range dayBookings {
			if booking.BookedMinutes > 0 {
				bookedMinutes += booking.BookedMinutes
			}
		}

		if openMinutes > 0 && len(dayBookings) >= 3 {
			utilization := float64(bookedMinutes) / float64(openMinutes)
			if utilization >= 0.85 && (!bestOverload.found || utilization > bestOverload.utilization) {
				bestOverload = struct {
					found        bool
					utilization  float64
					dayLabel     string
					bookingCount int
				}{
					found:        true,
					utilization:  utilization,
					dayLabel:     dayLabel,
					bookingCount: len(dayBookings),
				}
			}
		}

		for index := 0; index < len(dayBookings)-1; index += 1 {
			current := dayBookings[index]
			next := dayBookings[index+1]
			gapMinutes := int(math.Round(next.StartAt.Sub(current.EndAt).Minutes()))
			if gapMinutes < 75 {
				continue
			}

			bestFit := candidateService{}
			for _, service := range services {
				totalMinutes := service.DurationMinutes + service.PrepMinutes + service.BufferMinutes
				if totalMinutes <= 0 || totalMinutes > gapMinutes {
					continue
				}

				if service.MaxPerDay > 0 && dayServiceCounts[dayKey][service.ID] >= service.MaxPerDay {
					continue
				}

				if bestFit.ID == "" || totalMinutes < bestFit.TotalMinutes {
					bestFit = candidateService{
						ID:           service.ID,
						Title:        service.Title,
						TotalMinutes: totalMinutes,
						MaxPerDay:    service.MaxPerDay,
					}
				}
			}

			if bestFit.ID == "" {
				continue
			}

			if !bestGap.found || gapMinutes > bestGap.gapMinutes {
				bestGap = struct {
					found       bool
					gapMinutes  int
					dayLabel    string
					startLabel  string
					endLabel    string
					serviceID   string
					serviceName string
				}{
					found:       true,
					gapMinutes:  gapMinutes,
					dayLabel:    dayLabel,
					startLabel:  current.EndAt.Format("3:04 PM"),
					endLabel:    next.StartAt.Format("3:04 PM"),
					serviceID:   bestFit.ID,
					serviceName: bestFit.Title,
				}
			}
		}
	}

	for _, service := range services {
		totalMinutes := service.DurationMinutes + service.PrepMinutes + service.BufferMinutes
		if totalMinutes <= 0 || service.MaxPerDay <= 0 {
			continue
		}

		bestCapacity := 0
		for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
			openMinutes := openMinutesByWeekday[weekday]
			if openMinutes <= 0 {
				continue
			}
			capacity := openMinutes / totalMinutes
			if capacity > bestCapacity {
				bestCapacity = capacity
			}
		}

		if bestCapacity > 0 && service.MaxPerDay > bestCapacity {
			if !bestRuleConflict.found || service.MaxPerDay-bestCapacity > bestRuleConflict.maxPerDay-bestRuleConflict.physicalCap {
				bestRuleConflict = struct {
					found       bool
					serviceID   string
					serviceName string
					maxPerDay   int
					physicalCap int
				}{
					found:       true,
					serviceID:   service.ID,
					serviceName: service.Title,
					maxPerDay:   service.MaxPerDay,
					physicalCap: bestCapacity,
				}
			}
		}
	}

	if bestRuleConflict.found {
		items = append(items, BookingOptimizationItem{
			Kind:        "rule_conflict",
			Severity:    "high",
			Title:       "Service limit is higher than your current capacity",
			Body:        fmt.Sprintf("%s is set to %d bookings per day, but your current availability windows realistically fit about %d.", bestRuleConflict.serviceName, bestRuleConflict.maxPerDay, bestRuleConflict.physicalCap),
			ActionLabel: "Review max bookings per day",
			ServiceID:   bestRuleConflict.serviceID,
			ServiceName: bestRuleConflict.serviceName,
		})
	}

	if bestOverload.found {
		utilizationLabel := fmt.Sprintf("%d%%", int(math.Round(bestOverload.utilization*100)))
		items = append(items, BookingOptimizationItem{
			Kind:        "overloaded_day",
			Severity:    "high",
			Title:       "One upcoming day is almost fully booked",
			Body:        fmt.Sprintf("%s is already at about %s utilization with %d bookings scheduled. Review prep and buffer time before it gets tighter.", bestOverload.dayLabel, utilizationLabel, bestOverload.bookingCount),
			ActionLabel: "Review that day",
			DayLabel:    bestOverload.dayLabel,
		})
	}

	if bestGap.found {
		hoursLabel := fmt.Sprintf("%.1f", float64(bestGap.gapMinutes)/60)
		if bestGap.gapMinutes%60 == 0 {
			hoursLabel = fmt.Sprintf("%d", bestGap.gapMinutes/60)
		}
		items = append(items, BookingOptimizationItem{
			Kind:        "bookable_gap",
			Severity:    "medium",
			Title:       "There is a real bookable gap in your schedule",
			Body:        fmt.Sprintf("There is a %s-hour opening on %s between %s and %s. %s can fit into that window under your current rules.", hoursLabel, bestGap.dayLabel, bestGap.startLabel, bestGap.endLabel, bestGap.serviceName),
			ActionLabel: "Review gap",
			DayLabel:    bestGap.dayLabel,
			StartLabel:  bestGap.startLabel,
			EndLabel:    bestGap.endLabel,
			ServiceID:   bestGap.serviceID,
			ServiceName: bestGap.serviceName,
		})
	}

	if pendingSoonCount >= 2 {
		items = append(items, BookingOptimizationItem{
			Kind:        "pending_cleanup",
			Severity:    "low",
			Title:       "Pending bookings are obscuring open capacity",
			Body:        fmt.Sprintf("You have %d upcoming bookings still marked pending within the next week. Clearing those first will make your schedule availability more reliable.", pendingSoonCount),
			ActionLabel: "Review pending bookings",
		})
	}

	if len(items) == 0 {
		return &BookingOptimizationInsight{
			OptimizationsAvailable: false,
			Items:                  []BookingOptimizationItem{},
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		severityRank := func(severity string) int {
			switch severity {
			case "high":
				return 0
			case "medium":
				return 1
			default:
				return 2
			}
		}
		return severityRank(items[i].Severity) < severityRank(items[j].Severity)
	})

	if len(items) > 3 {
		items = items[:3]
	}

	summary := fmt.Sprintf("%d solid schedule optimization", len(items))
	if len(items) != 1 {
		summary += "s"
	}
	summary += " found."

	return &BookingOptimizationInsight{
		OptimizationsAvailable: true,
		Summary:                summary,
		Items:                  items,
	}
}

func (r *Repository) GetBookingDetails(ctx context.Context, clientID, bookingID uuid.UUID) (BookingDetailsResponse, error) {
	const query = `
		SELECT
			b.id,
			b.status,
			b.title,
			COALESCE(NULLIF(b.stylist_name, ''), cp.business_name),
			b.start_at,
			b.end_at,
			b.base_service_amount_minor,
			b.duration_minutes,
			b.total_amount_minor,
			b.currency_code,
			b.payment_status,
			b.agreement_status,
			b.notes,
			b.location_label,
			COALESCE(b.image_url, s.image_url, '')
		FROM bookings b
		INNER JOIN client_profiles cp ON cp.client_id = b.client_id
		LEFT JOIN services s ON s.id = b.service_id
		WHERE b.client_id = $1 AND b.id = $2
	`

	var response BookingDetailsResponse
	var id uuid.UUID
	var startAt time.Time
	var endAt time.Time
	var rateAmount int64
	var durationMinutes int
	var totalAmount int64
	if err := r.db.QueryRow(ctx, query, clientID, bookingID).Scan(
		&id,
		&response.Status,
		&response.Title,
		&response.Stylist,
		&startAt,
		&endAt,
		&rateAmount,
		&durationMinutes,
		&totalAmount,
		&response.CurrencyCode,
		&response.PaymentStatus,
		&response.AgreementStatus,
		&response.Notes,
		&response.Location,
		&response.ImageURL,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BookingDetailsResponse{}, ErrNotFound
		}
		return BookingDetailsResponse{}, fmt.Errorf("get booking details: %w", err)
	}

	response.ID = id.String()
	response.DateLabel = startAt.Format("Monday, Jan 2")
	response.TimeLabel = fmt.Sprintf("%s - %s", startAt.Format("03:04 PM"), endAt.Format("03:04 PM"))
	response.BaseServiceAmountMinor = money.Minor(rateAmount)
	response.DurationLabel = fmt.Sprintf("%d min", durationMinutes)
	response.TotalAmountMinor = money.Minor(totalAmount)

	return response, nil
}

func (r *Repository) ListCustomers(ctx context.Context, clientID uuid.UUID) ([]CustomerItem, error) {
	const query = `
		SELECT
			c.id,
			c.full_name,
			c.email,
			c.phone,
			COALESCE(c.avatar_url, ''),
			c.tier_label,
			c.status_label,
			c.badge_label,
			c.badge_tone,
			c.tags,
			c.private_notes,
			COALESCE(c.last_seen_at, c.created_at),
			COALESCE(booking_summary.has_upcoming_booking, FALSE),
			COALESCE(booking_summary.has_completed_booking, FALSE),
			booking_summary.next_booking_at,
			booking_summary.last_completed_booking_at
		FROM customers c
		LEFT JOIN LATERAL (
			SELECT
				COUNT(*) FILTER (
					WHERE b.start_at >= NOW()
					  AND b.status NOT IN ('cancelled', 'canceled')
				) > 0 AS has_upcoming_booking,
				COUNT(*) FILTER (
					WHERE b.end_at < NOW()
					  AND b.status NOT IN ('cancelled', 'canceled')
				) > 0 AS has_completed_booking,
				MIN(b.start_at) FILTER (
					WHERE b.start_at >= NOW()
					  AND b.status NOT IN ('cancelled', 'canceled')
				) AS next_booking_at,
				MAX(b.end_at) FILTER (
					WHERE b.end_at < NOW()
					  AND b.status NOT IN ('cancelled', 'canceled')
				) AS last_completed_booking_at
			FROM bookings b
			WHERE b.client_id = $1 AND b.customer_id = c.id
		) AS booking_summary ON TRUE
		WHERE c.client_id = $1
		ORDER BY
			COALESCE(booking_summary.has_upcoming_booking, FALSE) DESC,
			booking_summary.next_booking_at ASC NULLS LAST,
			c.full_name ASC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()

	customers := make([]CustomerItem, 0)
	for rows.Next() {
		var item CustomerItem
		var id uuid.UUID
		if err := rows.Scan(
			&id,
			&item.FullName,
			&item.Email,
			&item.Phone,
			&item.AvatarURL,
			&item.TierLabel,
			&item.StatusLabel,
			&item.BadgeLabel,
			&item.BadgeTone,
			&item.Tags,
			&item.PrivateNotes,
			&item.LastSeenAt,
			&item.HasUpcomingBooking,
			&item.HasCompletedBooking,
			&item.NextBookingAt,
			&item.LastCompletedBookingAt,
		); err != nil {
			return nil, fmt.Errorf("scan customer: %w", err)
		}
		item.ID = id.String()
		customers = append(customers, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate customers: %w", err)
	}

	return customers, nil
}

func (r *Repository) GetCustomerDetails(ctx context.Context, clientID, customerID uuid.UUID) (CustomerDetailsResponse, error) {
	const customerQuery = `
		SELECT
			id,
			full_name,
			COALESCE(NULLIF(tier_label, ''), status_label, 'Client'),
			email,
			phone,
			COALESCE(avatar_url, ''),
			private_notes
		FROM customers
		WHERE client_id = $1 AND id = $2
	`

	var response CustomerDetailsResponse
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, customerQuery, clientID, customerID).Scan(
		&id,
		&response.Name,
		&response.Tier,
		&response.Email,
		&response.Phone,
		&response.ImageURL,
		&response.Notes,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CustomerDetailsResponse{}, ErrNotFound
		}
		return CustomerDetailsResponse{}, fmt.Errorf("get customer details: %w", err)
	}
	response.ID = id.String()

	upcomingQuery := `
		SELECT
			title,
			start_at,
			duration_minutes,
			payment_status,
			agreement_status
		FROM bookings
		WHERE client_id = $1 AND customer_id = $2 AND start_at >= NOW()
		ORDER BY start_at ASC
		LIMIT 1
	`
	var upcomingTitle string
	var upcomingStart time.Time
	var upcomingDuration int
	if err := r.db.QueryRow(ctx, upcomingQuery, clientID, customerID).Scan(
		&upcomingTitle,
		&upcomingStart,
		&upcomingDuration,
		&response.PaymentStatus,
		&response.AgreementStatus,
	); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return CustomerDetailsResponse{}, fmt.Errorf("get upcoming booking for customer: %w", err)
		}
	} else {
		response.NextBooking = &CustomerUpcomingBooking{
			DateLabel: upcomingStart.Format("Monday, Jan 2 • 03:04 PM"),
			Title:     upcomingTitle,
			Duration:  fmt.Sprintf("%d minutes", upcomingDuration),
		}
	}

	if response.PaymentStatus == "" || response.AgreementStatus == "" {
		latestQuery := `
			SELECT payment_status, agreement_status
			FROM bookings
			WHERE client_id = $1 AND customer_id = $2
			ORDER BY start_at DESC
			LIMIT 1
		`
		var paymentStatus string
		var agreementStatus string
		if err := r.db.QueryRow(ctx, latestQuery, clientID, customerID).Scan(&paymentStatus, &agreementStatus); err == nil {
			if response.PaymentStatus == "" {
				response.PaymentStatus = paymentStatus
			}
			if response.AgreementStatus == "" {
				response.AgreementStatus = agreementStatus
			}
		}
	}
	if response.PaymentStatus == "" {
		response.PaymentStatus = "No payments yet"
	}
	if response.AgreementStatus == "" {
		response.AgreementStatus = "Pending"
	}

	historyQuery := `
		SELECT
			b.id,
			b.title,
			b.start_at,
			b.total_amount_minor,
			b.currency_code,
			COALESCE(s.icon_name, 'event')
		FROM bookings b
		LEFT JOIN services s ON s.id = b.service_id
		WHERE b.client_id = $1 AND b.customer_id = $2
		ORDER BY b.start_at DESC
		LIMIT 10
	`
	rows, err := r.db.Query(ctx, historyQuery, clientID, customerID)
	if err != nil {
		return CustomerDetailsResponse{}, fmt.Errorf("list customer history: %w", err)
	}
	defer rows.Close()

	response.History = make([]CustomerBookingHistoryItem, 0)
	for rows.Next() {
		var item CustomerBookingHistoryItem
		var bookingID uuid.UUID
		var bookedAt time.Time
		var totalAmount int64
		if err := rows.Scan(&bookingID, &item.Service, &bookedAt, &totalAmount, &item.CurrencyCode, &item.Icon); err != nil {
			return CustomerDetailsResponse{}, fmt.Errorf("scan customer history: %w", err)
		}
		item.ID = bookingID.String()
		item.Date = bookedAt.Format("Jan 2, 2006")
		item.AmountMinor = money.Minor(totalAmount)
		response.History = append(response.History, item)
	}
	if err := rows.Err(); err != nil {
		return CustomerDetailsResponse{}, fmt.Errorf("iterate customer history: %w", err)
	}

	return response, nil
}

func (r *Repository) GetNotifications(ctx context.Context, clientID uuid.UUID) (NotificationsResponse, error) {
	const query = `
		SELECT
			id,
			type,
			severity,
			title,
			description,
			action_label,
			action_route,
			COALESCE(image_url, ''),
			COALESCE(icon_name, ''),
			COALESCE(icon_tone, ''),
			created_at
		FROM notifications
		WHERE client_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return NotificationsResponse{}, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	response := NotificationsResponse{
		ActionRequired: make([]NotificationItem, 0),
		Today:          make([]NotificationItem, 0),
	}
	now := time.Now().UTC()
	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	for rows.Next() {
		var item NotificationItem
		var id uuid.UUID
		if err := rows.Scan(
			&id,
			&item.Type,
			&item.Severity,
			&item.Title,
			&item.Description,
			&item.ActionLabel,
			&item.ActionRoute,
			&item.ImageURL,
			&item.IconName,
			&item.IconTone,
			&item.CreatedAt,
		); err != nil {
			return NotificationsResponse{}, fmt.Errorf("scan notification: %w", err)
		}
		item.ID = id.String()

		if item.Severity == "urgent" {
			response.ActionRequired = append(response.ActionRequired, item)
			continue
		}

		if !item.CreatedAt.Before(startOfToday) {
			response.Today = append(response.Today, item)
		}
	}

	if err := rows.Err(); err != nil {
		return NotificationsResponse{}, fmt.Errorf("iterate notifications: %w", err)
	}

	return response, nil
}

func (r *Repository) ListAutomationSettings(ctx context.Context, clientID uuid.UUID) ([]AutomationSettingItem, error) {
	const query = `
		SELECT id, automation_key, title, description, action_label, enabled
		FROM automation_settings
		WHERE client_id = $1
		ORDER BY title ASC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list automation settings: %w", err)
	}
	defer rows.Close()

	items := make([]AutomationSettingItem, 0)
	for rows.Next() {
		var item AutomationSettingItem
		var id uuid.UUID
		if err := rows.Scan(&id, &item.Key, &item.Title, &item.Description, &item.ActionLabel, &item.Enabled); err != nil {
			return nil, fmt.Errorf("scan automation setting: %w", err)
		}
		item.ID = id.String()
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automation settings: %w", err)
	}

	return items, nil
}

func (r *Repository) UpdateAutomationSetting(ctx context.Context, clientID uuid.UUID, key string, enabled bool) error {
	const query = `
		UPDATE automation_settings
		SET enabled = $3, updated_at = NOW()
		WHERE client_id = $1 AND automation_key = $2
	`

	commandTag, err := r.db.Exec(ctx, query, clientID, key, enabled)
	if err != nil {
		return fmt.Errorf("update automation setting: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}

func (r *Repository) GetClientProfile(ctx context.Context, clientID uuid.UUID) (ClientProfileResponse, error) {
	const query = `
		SELECT
			c.full_name,
			c.email,
			c.bio,
			COALESCE(cp.business_name, c.full_name),
			COALESCE(cp.handle_slug, ''),
			COALESCE(cp.category, ''),
			COALESCE(cp.headline, ''),
			COALESCE(cp.short_bio, ''),
			COALESCE(cp.public_profile_about, ''),
			COALESCE(cp.booking_page_intro, ''),
			COALESCE(cp.public_location_label, ''),
			COALESCE(cp.city, ''),
			COALESCE(cp.region, ''),
			COALESCE(cp.timezone, ''),
			COALESCE(cp.locale, ''),
			COALESCE(cp.country_code, ''),
			COALESCE(cp.avatar_url, ''),
			COALESCE(cp.hero_image_url, ''),
			COALESCE(cp.verified, FALSE),
			COALESCE(cp.currency_code, ''),
			(cp.market_configured_at IS NOT NULL)
		FROM clients c
		LEFT JOIN client_profiles cp ON cp.client_id = c.id
		WHERE c.id = $1
	`

	var profile ClientProfileResponse
	if err := r.db.QueryRow(ctx, query, clientID).Scan(
		&profile.FullName,
		&profile.Email,
		&profile.Bio,
		&profile.BusinessName,
		&profile.HandleSlug,
		&profile.Category,
		&profile.Headline,
		&profile.ShortBio,
		&profile.PublicProfileAbout,
		&profile.BookingPageIntro,
		&profile.Location,
		&profile.City,
		&profile.Region,
		&profile.Timezone,
		&profile.Locale,
		&profile.CountryCode,
		&profile.AvatarURL,
		&profile.HeroImageURL,
		&profile.Verified,
		&profile.CurrencyCode,
		&profile.MarketConfigured,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ClientProfileResponse{}, ErrNotFound
		}
		return ClientProfileResponse{}, fmt.Errorf("get client profile: %w", err)
	}

	return profile, nil
}

func (r *Repository) UpdateClientProfile(ctx context.Context, clientID uuid.UUID, input UpdateClientProfileInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin profile update: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var currentHandle string
	profileExists := true
	if err := tx.QueryRow(
		ctx,
		`SELECT handle_slug FROM client_profiles WHERE client_id = $1 FOR UPDATE`,
		clientID,
	).Scan(&currentHandle); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lock client profile: %w", err)
		}
		profileExists = false
	}

	handleSlug := currentHandle
	if input.HandleSlug != nil {
		handleSlug, err = normalizeHandleSlug(*input.HandleSlug)
		if err != nil {
			return err
		}
	} else if !profileExists {
		handleSlug = uuid.NewString()
	}

	if err := claimClientProfileHandle(ctx, tx, clientID, handleSlug); err != nil {
		return err
	}

	const query = `
		INSERT INTO client_profiles (
			client_id, business_name, handle_slug, category, headline, short_bio, public_profile_about, booking_page_intro,
			public_location_label, city, region, hero_image_url, verified, created_at, updated_at
		)
		VALUES ($1,$2,$12,$10,$4,$3,$5,$6,$7,$8,$9,$11,FALSE,NOW(),NOW())
		ON CONFLICT (client_id) DO UPDATE SET
			business_name = EXCLUDED.business_name,
			handle_slug = EXCLUDED.handle_slug,
			category = EXCLUDED.category,
			headline = EXCLUDED.headline,
			short_bio = EXCLUDED.short_bio,
			public_profile_about = EXCLUDED.public_profile_about,
			booking_page_intro = EXCLUDED.booking_page_intro,
			public_location_label = EXCLUDED.public_location_label,
			city = EXCLUDED.city,
			region = EXCLUDED.region,
			hero_image_url = EXCLUDED.hero_image_url,
			updated_at = NOW()
	`

	if _, err := tx.Exec(
		ctx,
		query,
		clientID,
		strings.TrimSpace(input.BusinessName),
		strings.TrimSpace(input.ShortBio),
		strings.TrimSpace(input.Headline),
		strings.TrimSpace(input.PublicProfileAbout),
		strings.TrimSpace(input.BookingPageIntro),
		strings.TrimSpace(input.Location),
		strings.TrimSpace(input.City),
		strings.TrimSpace(input.Region),
		strings.TrimSpace(input.HeroImageURL),
		strings.TrimSpace(input.Category),
		handleSlug,
	); err != nil {
		return fmt.Errorf("update client profile: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit profile update: %w", err)
	}
	return nil
}

func (r *Repository) UpdateClientMarket(ctx context.Context, clientID uuid.UUID, input UpdateClientMarketInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin market update: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var currentCountry sql.NullString
	var currentCurrency sql.NullString
	var marketConfigured bool
	if err := tx.QueryRow(
		ctx,
		`
			SELECT country_code, currency_code, market_configured_at IS NOT NULL
			FROM client_profiles
			WHERE client_id = $1
			FOR UPDATE
		`,
		clientID,
	).Scan(&currentCountry, &currentCurrency, &marketConfigured); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock client market: %w", err)
	}

	countryChanged := marketConfigured && currentCountry.String != input.CountryCode
	currencyChanged := marketConfigured && currentCurrency.String != input.CurrencyCode
	if countryChanged || currencyChanged {
		var hasBookingOrFinancialHistory bool
		if err := tx.QueryRow(
			ctx,
			`
				SELECT
					EXISTS (SELECT 1 FROM bookings WHERE client_id = $1)
					OR EXISTS (SELECT 1 FROM payments WHERE client_id = $1)
					OR EXISTS (SELECT 1 FROM payouts WHERE client_id = $1)
			`,
			clientID,
		).Scan(&hasBookingOrFinancialHistory); err != nil {
			return fmt.Errorf("check market history lock: %w", err)
		}
		if hasBookingOrFinancialHistory {
			return ErrMarketLocked
		}
	}

	if currencyChanged {
		var hasMoneyConfiguration bool
		if err := tx.QueryRow(
			ctx,
			`
				SELECT
					EXISTS (
						SELECT 1
						FROM services
						WHERE client_id = $1
						  AND (
							price_amount_minor <> 0
							OR compare_price_amount_minor <> 0
							OR deposit_amount_minor <> 0
							OR travel_fee_minor <> 0
						  )
					)
					OR EXISTS (
						SELECT 1
						FROM promotions
						WHERE client_id = $1
						  AND (
							minimum_spend_minor <> 0
							OR discount_value_minor <> 0
						  )
					)
			`,
			clientID,
		).Scan(&hasMoneyConfiguration); err != nil {
			return fmt.Errorf("check currency configuration lock: %w", err)
		}
		if hasMoneyConfiguration {
			return ErrMarketLocked
		}
	}

	if countryChanged || currencyChanged {
		if _, err := tx.Exec(
			ctx,
			`
					UPDATE payout_destinations
					SET status = 'invalidated', is_default = FALSE, updated_at = NOW()
				WHERE client_id = $1
			`,
			clientID,
		); err != nil {
			return fmt.Errorf("invalidate payout destinations after market change: %w", err)
		}
	}

	tag, err := tx.Exec(
		ctx,
		`
			UPDATE client_profiles
			SET
				country_code = $2,
				currency_code = $3,
				timezone = $4,
				locale = $5,
				market_configured_at = NOW(),
				updated_at = NOW()
			WHERE client_id = $1
		`,
		clientID,
		input.CountryCode,
		input.CurrencyCode,
		input.Timezone,
		input.Locale,
	)
	if err != nil {
		return fmt.Errorf("update client market: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if currencyChanged {
		if _, err := tx.Exec(
			ctx,
			`
				UPDATE services
				SET currency_code = $2, updated_at = NOW()
				WHERE client_id = $1
			`,
			clientID,
			input.CurrencyCode,
		); err != nil {
			return fmt.Errorf("update service currency snapshots: %w", err)
		}
		if _, err := tx.Exec(
			ctx,
			`
				UPDATE promotions
				SET currency_code = $2, updated_at = NOW()
				WHERE client_id = $1
			`,
			clientID,
			input.CurrencyCode,
		); err != nil {
			return fmt.Errorf("update promotion currency snapshots: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit market update: %w", err)
	}
	return nil
}

func (r *Repository) EnsureClientMarketConfigured(ctx context.Context, clientID uuid.UUID) error {
	var configured bool
	if err := r.db.QueryRow(
		ctx,
		`
			SELECT EXISTS (
				SELECT 1
				FROM client_profiles
				WHERE client_id = $1
				  AND market_configured_at IS NOT NULL
			)
		`,
		clientID,
	).Scan(&configured); err != nil {
		return fmt.Errorf("check client market configuration: %w", err)
	}
	if !configured {
		return ErrMarketNotConfigured
	}
	return nil
}

func (r *Repository) CheckHandleSlugAvailability(ctx context.Context, clientID uuid.UUID, value string) (string, bool, error) {
	handleSlug, err := normalizeHandleSlug(value)
	if err != nil {
		return "", false, err
	}

	var available bool
	if err := r.db.QueryRow(
		ctx,
		`SELECT NOT EXISTS (
			SELECT 1
			FROM client_profile_handles
			WHERE handle_slug = $1
			  AND client_id <> $2
		)`,
		handleSlug,
		clientID,
	).Scan(&available); err != nil {
		return "", false, fmt.Errorf("check profile handle availability: %w", err)
	}

	return handleSlug, available, nil
}

func claimClientProfileHandle(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, handleSlug string) error {
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO client_profile_handles (handle_slug, client_id, created_at, updated_at)
		 VALUES ($1, $2, NOW(), NOW())
		 ON CONFLICT (handle_slug) DO NOTHING`,
		handleSlug,
		clientID,
	); err != nil {
		return fmt.Errorf("claim profile handle: %w", err)
	}

	var ownerID uuid.UUID
	if err := tx.QueryRow(
		ctx,
		`SELECT client_id FROM client_profile_handles WHERE handle_slug = $1`,
		handleSlug,
	).Scan(&ownerID); err != nil {
		return fmt.Errorf("load profile handle owner: %w", err)
	}
	if ownerID != clientID {
		return ErrHandleSlugTaken
	}

	return nil
}

func (r *Repository) GetPublicProfileBySlug(ctx context.Context, slug string) (PublicProfileResponse, error) {
	profile, err := r.getPublicProfile(ctx, slug)
	if err != nil {
		return PublicProfileResponse{}, err
	}

	services, err := r.ListPublicServicesBySlug(ctx, slug)
	if err != nil {
		return PublicProfileResponse{}, err
	}

	portfolio, err := r.listPublicPortfolioBySlug(ctx, slug)
	if err != nil {
		return PublicProfileResponse{}, err
	}

	if len(services) > 2 {
		services = services[:2]
	}

	return PublicProfileResponse{
		Profile:          profile,
		FeaturedServices: services,
		Portfolio:        portfolio,
	}, nil
}

func (r *Repository) getDashboardProfile(ctx context.Context, clientID uuid.UUID) (DashboardProfile, error) {
	const query = `
		SELECT
			c.id,
			c.full_name,
			cp.business_name,
			COALESCE(cp.avatar_url, c.cover_image_url, ''),
			cp.category,
			cp.headline,
			cp.public_location_label,
			cp.review_rating::float8,
			cp.review_count,
			cp.verified
		FROM clients c
		INNER JOIN client_profiles cp ON cp.client_id = c.id
		WHERE c.id = $1
	`

	var profile DashboardProfile
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, query, clientID).Scan(
		&id,
		&profile.FullName,
		&profile.BusinessName,
		&profile.AvatarURL,
		&profile.Category,
		&profile.Headline,
		&profile.LocationLabel,
		&profile.ReviewRating,
		&profile.ReviewCount,
		&profile.Verified,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DashboardProfile{}, ErrNotFound
		}
		return DashboardProfile{}, fmt.Errorf("get dashboard profile: %w", err)
	}
	profile.ClientID = id.String()
	return profile, nil
}

func (r *Repository) getDashboardStats(ctx context.Context, clientID uuid.UUID) (DashboardStats, error) {
	const query = `
		SELECT
			COUNT(booking.id)::int,
			COALESCE(SUM(booking.total_amount_minor), 0)::bigint,
			COALESCE(profile.currency_code, ''),
			COUNT(*) FILTER (
				WHERE booking.id IS NOT NULL
				  AND booking.currency_code <> profile.currency_code
			)::int
		FROM client_profiles AS profile
		LEFT JOIN bookings AS booking
			ON booking.client_id = profile.client_id
		   AND booking.start_at::date = CURRENT_DATE
		WHERE profile.client_id = $1
		GROUP BY profile.currency_code
	`

	var stats DashboardStats
	var mismatchedCurrencyCount int
	if err := r.db.QueryRow(ctx, query, clientID).Scan(
		&stats.TodayBookingsCount,
		&stats.ProjectedRevenue,
		&stats.CurrencyCode,
		&mismatchedCurrencyCount,
	); err != nil {
		return DashboardStats{}, fmt.Errorf("get dashboard stats: %w", err)
	}
	if mismatchedCurrencyCount > 0 {
		return DashboardStats{}, fmt.Errorf("dashboard bookings contain mixed currencies")
	}
	return stats, nil
}

func (r *Repository) getTopAttentionItem(ctx context.Context, clientID uuid.UUID) (*NotificationItem, error) {
	const query = `
		SELECT
			id,
			type,
			severity,
			title,
			description,
			action_label,
			action_route,
			COALESCE(image_url, ''),
			COALESCE(icon_name, ''),
			COALESCE(icon_tone, ''),
			created_at
		FROM notifications
		WHERE client_id = $1
		ORDER BY (CASE WHEN severity = 'urgent' THEN 0 ELSE 1 END), created_at DESC
		LIMIT 1
	`

	var item NotificationItem
	var id uuid.UUID
	err := r.db.QueryRow(ctx, query, clientID).Scan(
		&id,
		&item.Type,
		&item.Severity,
		&item.Title,
		&item.Description,
		&item.ActionLabel,
		&item.ActionRoute,
		&item.ImageURL,
		&item.IconName,
		&item.IconTone,
		&item.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get top attention item: %w", err)
	}

	item.ID = id.String()
	return &item, nil
}

func (r *Repository) getTodayBookings(ctx context.Context, clientID uuid.UUID) ([]DashboardBookingItem, error) {
	const query = `
		SELECT
			b.id,
			b.title,
			b.start_at,
			c.full_name,
			b.status,
			b.payment_status,
			COALESCE(s.icon_name, '')
		FROM bookings b
		LEFT JOIN customers c ON c.id = b.customer_id
		LEFT JOIN services s ON s.id = b.service_id
		WHERE b.client_id = $1
		  AND b.start_at::date = CURRENT_DATE
		ORDER BY b.start_at ASC
		LIMIT 6
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("get today bookings: %w", err)
	}
	defer rows.Close()

	items := make([]DashboardBookingItem, 0)
	for rows.Next() {
		var item DashboardBookingItem
		var id uuid.UUID
		if err := rows.Scan(
			&id,
			&item.Title,
			&item.StartAt,
			&item.CustomerName,
			&item.Status,
			&item.PaymentStatus,
			&item.IconName,
		); err != nil {
			return nil, fmt.Errorf("scan today booking: %w", err)
		}
		item.ID = id.String()
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate today bookings: %w", err)
	}

	return items, nil
}

func (r *Repository) getPublicProfile(ctx context.Context, slug string) (PublicProfile, error) {
	const query = `
		SELECT
			cp.client_id,
			cp.business_name,
			cp.handle_slug,
			cp.category,
			cp.headline,
			cp.short_bio,
			cp.public_profile_about,
			cp.booking_page_intro,
			cp.public_location_label,
			COALESCE(cp.hero_image_url, ''),
			COALESCE(cp.avatar_url, ''),
			cp.verified,
			cp.years_experience,
			cp.review_rating::float8,
			cp.review_count,
			COALESCE(cp.country_code, ''),
			COALESCE(cp.currency_code, ''),
			COALESCE(cp.timezone, ''),
			COALESCE(cp.locale, '')
		FROM client_profiles cp
		INNER JOIN client_profile_handles cph
			ON cph.client_id = cp.client_id
		   AND cph.handle_slug = $1
	`

	var profile PublicProfile
	var id uuid.UUID
	if err := r.db.QueryRow(ctx, query, slug).Scan(
		&id,
		&profile.BusinessName,
		&profile.HandleSlug,
		&profile.Category,
		&profile.Headline,
		&profile.ShortBio,
		&profile.PublicProfileAbout,
		&profile.BookingPageIntro,
		&profile.LocationLabel,
		&profile.HeroImageURL,
		&profile.AvatarURL,
		&profile.Verified,
		&profile.YearsExperience,
		&profile.ReviewRating,
		&profile.ReviewCount,
		&profile.CountryCode,
		&profile.CurrencyCode,
		&profile.Timezone,
		&profile.Locale,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PublicProfile{}, ErrNotFound
		}
		return PublicProfile{}, fmt.Errorf("get public profile: %w", err)
	}
	profile.ClientID = id.String()
	return profile, nil
}

func (r *Repository) listPublicPortfolioBySlug(ctx context.Context, slug string) ([]PublicPortfolioItem, error) {
	const query = `
		SELECT
			p.id,
			COALESCE(p.image_url, ''),
			COALESCE(p.title, '')
		FROM provider_portfolio_items p
		INNER JOIN client_profile_handles cph
			ON cph.client_id = p.client_id
		   AND cph.handle_slug = $1
		ORDER BY p.sort_order ASC, p.created_at ASC
	`

	rows, err := r.db.Query(ctx, query, slug)
	if err != nil {
		return nil, fmt.Errorf("list public portfolio: %w", err)
	}
	defer rows.Close()

	items := make([]PublicPortfolioItem, 0)
	for rows.Next() {
		var item PublicPortfolioItem
		var id uuid.UUID
		if err := rows.Scan(&id, &item.ImageURL, &item.Caption); err != nil {
			return nil, fmt.Errorf("scan public portfolio item: %w", err)
		}
		item.ID = id.String()
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public portfolio items: %w", err)
	}

	return items, nil
}

func upsertPublicCustomer(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, input CreatePublicBookingInput) (uuid.UUID, error) {
	email := strings.TrimSpace(strings.ToLower(input.Email))
	phone := strings.TrimSpace(input.Phone)
	fullName := strings.TrimSpace(input.FullName)
	if fullName == "" {
		return uuid.Nil, fmt.Errorf("full_name is required")
	}
	if email == "" {
		return uuid.Nil, fmt.Errorf("email is required")
	}

	const findQuery = `
		SELECT id
		FROM customers
		WHERE client_id = $1 AND (email = $2 OR ($3 <> '' AND phone = $3))
		ORDER BY created_at ASC
		LIMIT 1
	`
	var customerID uuid.UUID
	err := tx.QueryRow(ctx, findQuery, clientID, email, phone).Scan(&customerID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("find public customer: %w", err)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		customerID = uuid.New()
		const insertQuery = `
			INSERT INTO customers (
				id, client_id, full_name, email, phone, tier_label, status_label, badge_label,
				badge_tone, tags, private_notes, last_seen_at, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,'New Client','Public booking','','',ARRAY['New'], '', NOW(), NOW(), NOW())
		`
		if _, err := tx.Exec(ctx, insertQuery, customerID, clientID, fullName, email, phone); err != nil {
			return uuid.Nil, fmt.Errorf("insert public customer: %w", err)
		}
		return customerID, nil
	}

	const updateQuery = `
		UPDATE customers
		SET full_name = $3, email = $4, phone = $5, last_seen_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND client_id = $2
	`
	if _, err := tx.Exec(ctx, updateQuery, customerID, clientID, fullName, email, phone); err != nil {
		return uuid.Nil, fmt.Errorf("update public customer: %w", err)
	}
	return customerID, nil
}

func loadLocation(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}
	return location, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func initialBookingPaymentState(totalAmountMinor, depositAmountMinor int64) string {
	if depositAmountMinor > 0 && depositAmountMinor < totalAmountMinor {
		return string(payments.BookingPaymentDepositPending)
	}
	return string(payments.BookingPaymentFullPending)
}

func initialBookingObligationSatisfied(status string) bool {
	return status == string(payments.BookingPaymentDepositPaidBalance) ||
		status == string(payments.BookingPaymentPaidInFull)
}

func buildPublicAgreementResolvedVariables(
	service publicBookingServiceInfo,
	input CreatePublicBookingInput,
	startAt time.Time,
	endAt time.Time,
	locationLabel string,
	totalAmountMinor int64,
	depositAmountMinor int64,
) (map[string]string, error) {
	location := firstNonEmpty(locationLabel, service.ProviderLocationLabel)
	businessLocation := firstNonEmpty(service.BusinessLocation, service.ProviderLocationLabel)
	customerName := strings.TrimSpace(input.FullName)
	if depositAmountMinor < 0 {
		depositAmountMinor = 0
	}
	if depositAmountMinor > totalAmountMinor {
		depositAmountMinor = totalAmountMinor
	}
	remainingAmountMinor := totalAmountMinor - depositAmountMinor
	startLabel := startAt.Format("03:04 PM")
	endLabel := endAt.Format("03:04 PM")
	dateLabel := startAt.Format("Monday, Jan 2, 2006")
	durationMinutes := fmt.Sprintf("%d", service.DurationMinutes)
	durationLabel := fmt.Sprintf("%d mins", service.DurationMinutes)
	totalAmount, err := formatMarketMoney(totalAmountMinor, service.CountryCode, service.CurrencyCode)
	if err != nil {
		return nil, err
	}
	depositAmount, err := formatMarketMoney(depositAmountMinor, service.CountryCode, service.CurrencyCode)
	if err != nil {
		return nil, err
	}
	remainingAmount, err := formatMarketMoney(remainingAmountMinor, service.CountryCode, service.CurrencyCode)
	if err != nil {
		return nil, err
	}

	values := map[string]string{
		"BUSINESS_NAME":       service.BusinessName,
		"BUSINESS_LOCATION":   businessLocation,
		"CUSTOMER_NAME":       customerName,
		"CUSTOMER_EMAIL":      strings.TrimSpace(input.Email),
		"CUSTOMER_PHONE":      strings.TrimSpace(input.Phone),
		"SERVICE_NAME":        service.Title,
		"BOOKING_DATE":        dateLabel,
		"BOOKING_START_TIME":  startLabel,
		"BOOKING_END_TIME":    endLabel,
		"BOOKING_TIME_RANGE":  fmt.Sprintf("%s - %s", startLabel, endLabel),
		"BOOKING_LOCATION":    location,
		"TOTAL_AMOUNT":        totalAmount,
		"DEPOSIT_AMOUNT":      depositAmount,
		"REMAINING_AMOUNT":    remainingAmount,
		"DURATION_MINUTES":    durationMinutes,
		"SERVICE_DURATION":    durationLabel,
		"BOOKING_NOTES":       strings.TrimSpace(input.Notes),
		"CANCELLATION_POLICY": strings.TrimSpace(service.CancellationPolicy),
		"LATENESS_POLICY":     strings.TrimSpace(service.LatenessPolicy),
	}

	return values, nil
}

func slugify(value string) string {
	slug := normalizeSlug(value)
	if slug == "" {
		return uuid.NewString()
	}

	return slug
}

func normalizeHandleSlug(value string) (string, error) {
	slug := normalizeSlug(value)
	if slug == "" || len(slug) > 64 {
		return "", ErrInvalidHandleSlug
	}

	return slug, nil
}

func normalizeSlug(value string) string {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return ""
	}

	var builder strings.Builder
	lastWasDash := false
	for _, char := range trimmed {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastWasDash = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastWasDash = false
		default:
			if lastWasDash || builder.Len() == 0 {
				continue
			}
			builder.WriteByte('-')
			lastWasDash = true
		}
	}

	slug := strings.Trim(builder.String(), "-")
	return slug
}

type publicBookingPaymentContext struct {
	BookingID     uuid.UUID
	BookingToken  string
	ClientID      uuid.UUID
	CustomerID    uuid.UUID
	ServiceTitle  string
	CountryCode   string
	CurrencyCode  string
	CustomerName  string
	CustomerEmail string
	CustomerPhone string
	HandleSlug    string
}

func (r *Repository) getPublicBookingPaymentContext(ctx context.Context, bookingToken string) (publicBookingPaymentContext, error) {
	const query = `
		SELECT
			b.id,
			b.public_token,
			b.client_id,
			b.customer_id,
			b.title,
				b.country_code,
			b.currency_code,
			COALESCE(c.full_name, ''),
			COALESCE(c.email, ''),
			COALESCE(c.phone, ''),
			cp.handle_slug
		FROM bookings b
		INNER JOIN customers c ON c.id = b.customer_id
		INNER JOIN client_profiles cp ON cp.client_id = b.client_id
		WHERE b.public_token = $1
	`

	var paymentContext publicBookingPaymentContext
	if err := r.db.QueryRow(ctx, query, strings.TrimSpace(bookingToken)).Scan(
		&paymentContext.BookingID,
		&paymentContext.BookingToken,
		&paymentContext.ClientID,
		&paymentContext.CustomerID,
		&paymentContext.ServiceTitle,
		&paymentContext.CountryCode,
		&paymentContext.CurrencyCode,
		&paymentContext.CustomerName,
		&paymentContext.CustomerEmail,
		&paymentContext.CustomerPhone,
		&paymentContext.HandleSlug,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return publicBookingPaymentContext{}, ErrNotFound
		}
		return publicBookingPaymentContext{}, fmt.Errorf("get public booking payment context: %w", err)
	}

	return paymentContext, nil
}
