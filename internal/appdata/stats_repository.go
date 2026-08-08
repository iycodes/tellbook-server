package appdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetStatsOverview(
	ctx context.Context,
	clientID uuid.UUID,
	rangeName string,
	days int,
) (StatsOverviewResponse, error) {
	if days <= 0 {
		return StatsOverviewResponse{}, errors.New("stats range must be positive")
	}

	var currencyCode, timezone string
	if err := r.db.QueryRow(ctx, `
		SELECT currency_code, timezone
		FROM client_profiles
		WHERE client_id = $1
	`, clientID).Scan(&currencyCode, &timezone); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StatsOverviewResponse{}, ErrNotFound
		}
		return StatsOverviewResponse{}, fmt.Errorf("get stats market: %w", err)
	}

	location, err := time.LoadLocation(timezone)
	if err != nil {
		return StatsOverviewResponse{}, fmt.Errorf("load stats timezone: %w", err)
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	periodStart := today.AddDate(0, 0, -(days - 1))
	periodEnd := today.AddDate(0, 0, 1)

	response := StatsOverviewResponse{
		Range: rangeName, PeriodStart: periodStart, PeriodEnd: periodEnd,
		CurrencyCode: currencyCode,
	}
	var mismatchedCurrencyCount int
	if err := r.db.QueryRow(ctx, `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE LOWER(status) = 'completed')::int,
			COUNT(*) FILTER (
				WHERE LOWER(status) NOT IN ('completed', 'cancelled', 'canceled')
			)::int,
			COUNT(*) FILTER (WHERE LOWER(status) IN ('cancelled', 'canceled'))::int,
			COUNT(*) FILTER (
				WHERE LOWER(payment_status) IN ('deposit_paid', 'deposit_paid_balance_due', 'paid_in_full')
			)::int,
			COUNT(DISTINCT customer_id)::int,
			COALESCE(SUM(total_amount_minor) FILTER (
				WHERE LOWER(status) NOT IN ('cancelled', 'canceled')
			), 0)::bigint,
			COUNT(*) FILTER (WHERE currency_code <> $4)::int
		FROM bookings
		WHERE client_id = $1
		  AND start_at >= $2
		  AND start_at < $3
	`, clientID, periodStart, periodEnd, currencyCode).Scan(
		&response.TotalBookings,
		&response.CompletedBookings,
		&response.ScheduledBookings,
		&response.CancelledBookings,
		&response.SecuredBookings,
		&response.UniqueCustomerCount,
		&response.BookedValueMinor,
		&mismatchedCurrencyCount,
	); err != nil {
		return StatsOverviewResponse{}, fmt.Errorf("get stats overview: %w", err)
	}
	if mismatchedCurrencyCount > 0 {
		return StatsOverviewResponse{}, errors.New("stats bookings contain mixed currencies")
	}

	return response, nil
}
