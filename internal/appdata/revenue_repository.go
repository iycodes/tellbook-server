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
	return r.getProjectedRevenuePeriod(ctx, clientID, response)
}

func (r *Repository) getProjectedRevenuePeriod(
	ctx context.Context,
	clientID uuid.UUID,
	response RevenueOverviewResponse,
) (RevenueOverviewResponse, error) {
	if err := r.db.QueryRow(ctx, `
		SELECT
			COALESCE((
				SELECT SUM(wallet.business_net_amount_minor)
				FROM payment_allocations wallet
				WHERE wallet.client_id=$1
				  AND wallet.currency_code=$4
				  AND wallet.status='eligible'
				  AND wallet.available_for_payout_at<=NOW()
			),0)::bigint,
			COALESCE(SUM(gross_revenue_minor),0)::bigint,
			COALESCE(SUM(net_revenue_minor),0)::bigint,
			COALESCE(SUM(payment_count),0)::int
		FROM provider_daily_metrics
		WHERE client_id=$1
		  AND currency_code=$4
		  AND metric_date >= $2::date
		  AND metric_date < $3::date
	`, clientID, response.PeriodStart.Format("2006-01-02"), response.PeriodEnd.Format("2006-01-02"), response.CurrencyCode).Scan(
		&response.WalletBalanceMinor,
		&response.GrossRevenueMinor,
		&response.NetRevenueMinor,
		&response.PaymentCount,
	); err != nil {
		return RevenueOverviewResponse{}, fmt.Errorf("get projected revenue totals: %w", err)
	}
	if response.PaymentCount > 0 {
		response.AveragePaymentMinor = response.NetRevenueMinor / money.Minor(response.PaymentCount)
	}
	recent, err := r.listRecentRevenuePayments(ctx, clientID, response)
	if err != nil {
		return RevenueOverviewResponse{}, err
	}
	response.RecentCustomerPayments = recent
	return response, nil
}

func (r *Repository) getRevenuePeriod(
	ctx context.Context,
	clientID uuid.UUID,
	response RevenueOverviewResponse,
	includeRecent bool,
) (RevenueOverviewResponse, error) {
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
		`, clientID, response.PeriodStart, response.PeriodEnd, response.CurrencyCode).Scan(
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
	if !includeRecent {
		return response, nil
	}
	recent, err := r.listRecentRevenuePayments(ctx, clientID, response)
	if err != nil {
		return RevenueOverviewResponse{}, err
	}
	response.RecentCustomerPayments = recent
	return response, nil
}

func (r *Repository) listRecentRevenuePayments(
	ctx context.Context,
	clientID uuid.UUID,
	response RevenueOverviewResponse,
) ([]RevenuePaymentItem, error) {
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
	`, clientID, response.PeriodStart, response.PeriodEnd, response.CurrencyCode)
	if err != nil {
		return nil, fmt.Errorf("list recent revenue: %w", err)
	}
	defer rows.Close()

	items := make([]RevenuePaymentItem, 0, 10)
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
			return nil, fmt.Errorf("scan recent revenue: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent revenue: %w", err)
	}
	return items, nil
}
