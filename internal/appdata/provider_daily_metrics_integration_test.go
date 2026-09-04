package appdata

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderDailyMetricsRebuildsCanonicalBookingDay(t *testing.T) {
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

	clientID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	serviceID := insertMarketplaceTestService(t, ctx, pool, clientID, uuid.Nil, "daily-metric-service", "virtual", 25000, nil)
	customerID := uuid.New()
	bookingID := uuid.New()
	paymentID := uuid.New()
	allocationID := uuid.New()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM payment_allocations WHERE id=$1`, allocationID)
		_, _ = pool.Exec(ctx, `DELETE FROM payments WHERE id=$1`, paymentID)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE id=$1`, customerID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id,client_id,full_name,email,created_at,updated_at)
		VALUES ($1,$2,'Metric customer',$3,NOW(),NOW())
	`, customerID, clientID, "metric-"+customerID.String()+"@example.test"); err != nil {
		t.Fatal(err)
	}
	startAt := time.Date(2026, time.September, 1, 23, 30, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id,client_id,customer_id,service_id,title,status,payment_status,start_at,end_at,
			timezone,base_service_amount_minor,discounted_service_amount_minor,
			total_amount_minor,currency_code,country_code,occupied_start_at,
			occupied_end_at,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,'Metric booking','confirmed','unpaid',$5::timestamptz,$5::timestamptz+INTERVAL '1 hour',
			'Africa/Lagos',25000,25000,25000,'NGN','NG',$5::timestamptz,$5::timestamptz+INTERVAL '1 hour',NOW(),NOW()
		)
	`, bookingID, clientID, customerID, serviceID, startAt); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(pool)
	var enqueued int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM provider_daily_metric_jobs WHERE client_id=$1
	`, clientID).Scan(&enqueued); err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("booking mutation enqueued %d daily metric jobs, want 1", enqueued)
	}
	processedOurJob := false
	for range 100 {
		if _, err := repository.RebuildProviderDailyMetricsBatch(ctx, 500); err != nil {
			t.Fatal(err)
		}
		var pending int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM provider_daily_metric_jobs WHERE client_id=$1
		`, clientID).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			processedOurJob = true
			break
		}
	}
	if !processedOurJob {
		t.Fatal("daily metric job remained queued after draining bounded batches")
	}

	var metricDate string
	var total, scheduled int
	var bookedValue int64
	if err := pool.QueryRow(ctx, `
		SELECT metric_date::text,total_bookings,scheduled_bookings,booked_value_minor
		FROM provider_daily_metrics
		WHERE client_id=$1 AND currency_code='NGN'
	`, clientID).Scan(&metricDate, &total, &scheduled, &bookedValue); err != nil {
		t.Fatal(err)
	}
	if metricDate != "2026-09-02" || total != 1 || scheduled != 1 || bookedValue != 25000 {
		t.Fatalf("unexpected initial daily metric: date=%s total=%d scheduled=%d value=%d", metricDate, total, scheduled, bookedValue)
	}
	lagos, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	periodStart := time.Date(2026, time.September, 2, 0, 0, 0, 0, lagos)
	stats, err := repository.getProjectedStatsPeriod(ctx, clientID, StatsOverviewResponse{
		Range: "1d", PeriodStart: periodStart, PeriodEnd: periodStart.AddDate(0, 0, 1), CurrencyCode: "NGN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.TotalBookings != 1 || stats.UniqueCustomerCount != 1 || stats.BookedValueMinor != 25000 {
		t.Fatalf("unexpected projected stats response: %+v", stats)
	}

	if _, err := pool.Exec(ctx, `UPDATE bookings SET status='completed',updated_at=NOW() WHERE id=$1`, bookingID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RebuildProviderDailyMetricsBatch(ctx, 500); err != nil {
		t.Fatal(err)
	}
	var completed int
	if err := pool.QueryRow(ctx, `
		SELECT total_bookings,completed_bookings,scheduled_bookings
		FROM provider_daily_metrics
		WHERE client_id=$1 AND metric_date='2026-09-02' AND currency_code='NGN'
	`, clientID).Scan(&total, &completed, &scheduled); err != nil {
		t.Fatal(err)
	}
	if total != 1 || completed != 1 || scheduled != 0 {
		t.Fatalf("daily metric was incremented instead of recomputed: total=%d completed=%d scheduled=%d", total, completed, scheduled)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO payments (
			id,public_token,booking_id,client_id,customer_id,purpose,provider,method,
			country_code,currency_code,amount_minor,price_snapshot,reference,
			idempotency_key,request_fingerprint,status,paid_at,created_at,updated_at
		) VALUES (
			$1,$2,$3,$4,$5,'full','paystack','card','NG','NGN',25000,'{}'::jsonb,
			$6,$7,$8,'paid',$9,NOW(),NOW()
		)
	`, paymentID, strings.Repeat("p", 64), bookingID, clientID, customerID,
		"metric-payment-"+paymentID.String(), paymentID.String(), strings.Repeat("a", 64), startAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO payment_allocations (
			id,payment_id,client_id,currency_code,gross_amount_minor,
			business_net_amount_minor,policy_version,calculation_snapshot,status,
			created_at,updated_at
		) VALUES ($1,$2,$3,'NGN',25000,25000,'test-v1','{}'::jsonb,'pending',NOW(),NOW())
	`, allocationID, paymentID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.RebuildProviderDailyMetricsBatch(ctx, 500); err != nil {
		t.Fatal(err)
	}
	var grossRevenue, netRevenue int64
	var paymentCount int
	if err := pool.QueryRow(ctx, `
		SELECT gross_revenue_minor,net_revenue_minor,payment_count
		FROM provider_daily_metrics
		WHERE client_id=$1 AND metric_date='2026-09-02' AND currency_code='NGN'
	`, clientID).Scan(&grossRevenue, &netRevenue, &paymentCount); err != nil {
		t.Fatal(err)
	}
	if grossRevenue != 25000 || netRevenue != 25000 || paymentCount != 1 {
		t.Fatalf("unexpected daily revenue metric: gross=%d net=%d payments=%d", grossRevenue, netRevenue, paymentCount)
	}
	revenue, err := repository.getProjectedRevenuePeriod(ctx, clientID, RevenueOverviewResponse{
		Range: "1d", PeriodStart: periodStart, PeriodEnd: periodStart.AddDate(0, 0, 1),
		CurrencyCode: "NGN", RecentCustomerPayments: []RevenuePaymentItem{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if revenue.GrossRevenueMinor != 25000 || revenue.NetRevenueMinor != 25000 ||
		revenue.PaymentCount != 1 || len(revenue.RecentCustomerPayments) != 1 {
		t.Fatalf("unexpected projected revenue response: %+v", revenue)
	}
}
