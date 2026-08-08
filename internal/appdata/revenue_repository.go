package appdata

import (
	"context"
	"errors"
	"fmt"
	"time"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) GetRevenueOverview(
	ctx context.Context,
	clientID uuid.UUID,
	rangeName string,
	days int,
) (RevenueOverviewResponse, error) {
	if days <= 0 {
		return RevenueOverviewResponse{}, errors.New("revenue range must be positive")
	}

	var currencyCode, timezone string
	if err := r.db.QueryRow(ctx, `
		SELECT currency_code, timezone
		FROM client_profiles
		WHERE client_id = $1
	`, clientID).Scan(&currencyCode, &timezone); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RevenueOverviewResponse{}, ErrNotFound
		}
		return RevenueOverviewResponse{}, fmt.Errorf("get revenue market: %w", err)
	}

	location, err := time.LoadLocation(timezone)
	if err != nil {
		return RevenueOverviewResponse{}, fmt.Errorf("load revenue timezone: %w", err)
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	periodStart := today.AddDate(0, 0, -(days - 1))
	periodEnd := today.AddDate(0, 0, 1)

	response := RevenueOverviewResponse{
		Range: rangeName, PeriodStart: periodStart, PeriodEnd: periodEnd,
		CurrencyCode: currencyCode, RecentCustomerPayments: []RevenuePaymentItem{},
	}
	if err := r.db.QueryRow(ctx, `
			SELECT
				COALESCE((
					SELECT SUM(wallet.business_net_amount_minor)
					FROM payment_allocations wallet
					WHERE wallet.client_id = $1
					  AND wallet.currency_code = $4
					  AND wallet.status = 'eligible'
					  AND wallet.available_for_payout_at <= NOW()
				), 0)::bigint,
				COALESCE(SUM(pa.gross_amount_minor), 0)::bigint,
				COALESCE(SUM(pa.business_net_amount_minor), 0)::bigint,
				COUNT(*)::int
		FROM payments p
		JOIN payment_allocations pa ON pa.payment_id = p.id
		WHERE p.client_id = $1
		  AND p.paid_at >= $2
		  AND p.paid_at < $3
			  AND p.currency_code = $4
			  AND pa.status <> 'reversed'
		`, clientID, periodStart, periodEnd, currencyCode).Scan(
		&response.WalletBalanceMinor,
		&response.GrossRevenueMinor,
		&response.NetRevenueMinor,
		&response.PaymentCount,
	); err != nil {
		return RevenueOverviewResponse{}, fmt.Errorf("get revenue totals: %w", err)
	}
	if response.PaymentCount > 0 {
		response.AveragePaymentMinor = response.NetRevenueMinor / money.Minor(response.PaymentCount)
	}

	rows, err := r.db.Query(ctx, `
		SELECT
			p.id,
			COALESCE(NULLIF(BTRIM(c.full_name), ''), 'Customer'),
			COALESCE(NULLIF(BTRIM(b.title), ''), NULLIF(BTRIM(s.title), ''), 'Service'),
			pa.gross_amount_minor,
			pa.business_net_amount_minor,
			p.method,
			p.provider,
			p.paid_at
		FROM payments p
		JOIN payment_allocations pa ON pa.payment_id = p.id
		JOIN bookings b ON b.id = p.booking_id
		JOIN customers c ON c.id = p.customer_id
		LEFT JOIN services s ON s.id = b.service_id
		WHERE p.client_id = $1
		  AND p.paid_at >= $2
		  AND p.paid_at < $3
		  AND p.currency_code = $4
		  AND pa.status <> 'reversed'
		ORDER BY p.paid_at DESC, p.id DESC
		LIMIT 10
	`, clientID, periodStart, periodEnd, currencyCode)
	if err != nil {
		return RevenueOverviewResponse{}, fmt.Errorf("list recent revenue: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var item RevenuePaymentItem
		if err := rows.Scan(
			&item.ID,
			&item.CustomerName,
			&item.ServiceName,
			&item.GrossMinor,
			&item.NetMinor,
			&item.Method,
			&item.Provider,
			&item.PaidAt,
		); err != nil {
			return RevenueOverviewResponse{}, fmt.Errorf("scan recent revenue: %w", err)
		}
		response.RecentCustomerPayments = append(response.RecentCustomerPayments, item)
	}
	if err := rows.Err(); err != nil {
		return RevenueOverviewResponse{}, fmt.Errorf("iterate recent revenue: %w", err)
	}

	return response, nil
}
