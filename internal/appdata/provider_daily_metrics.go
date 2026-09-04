package appdata

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const (
	providerDailyMetricsPollInterval = 5 * time.Second
	providerDailyMetricsBatchSize    = 100
)

type providerDailyMetricJob struct {
	clientID     uuid.UUID
	metricDate   string
	currencyCode string
	timezone     string
	revision     int64
}

type ProviderDailyMetricsWorker struct {
	repository *Repository
	logger     *slog.Logger
}

func NewProviderDailyMetricsWorker(repository *Repository, logger *slog.Logger) *ProviderDailyMetricsWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProviderDailyMetricsWorker{repository: repository, logger: logger}
}

func (worker *ProviderDailyMetricsWorker) Start(ctx context.Context) {
	if worker == nil || worker.repository == nil || worker.repository.db == nil {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		for ctx.Err() == nil {
			processed, err := worker.repository.RebuildProviderDailyMetricsBatch(
				ctx,
				providerDailyMetricsBatchSize,
			)
			if err != nil {
				worker.logger.Error("rebuild provider daily metrics failed", "error", err)
				break
			}
			if processed < providerDailyMetricsBatchSize {
				break
			}
		}
		timer.Reset(providerDailyMetricsPollInterval)
	}
}

func (r *Repository) RebuildProviderDailyMetricsBatch(ctx context.Context, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	if limit < 1 || limit > 500 {
		return 0, fmt.Errorf("provider daily metric batch size must be between 1 and 500")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin provider daily metric batch: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT job.client_id,job.metric_date::text,job.currency_code,profile.timezone,job.revision
		FROM provider_daily_metric_jobs job
		JOIN client_profiles profile ON profile.client_id=job.client_id
		WHERE job.metric_date>=CURRENT_DATE-400
		ORDER BY job.enqueued_at,job.client_id,job.metric_date,job.currency_code
		FOR UPDATE OF job SKIP LOCKED
		LIMIT $1
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("claim provider daily metric jobs: %w", err)
	}
	jobs := make([]providerDailyMetricJob, 0, limit)
	for rows.Next() {
		var job providerDailyMetricJob
		if err := rows.Scan(&job.clientID, &job.metricDate, &job.currencyCode, &job.timezone, &job.revision); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan provider daily metric job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate provider daily metric jobs: %w", err)
	}
	rows.Close()
	if len(jobs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit empty provider daily metric batch: %w", err)
		}
		return 0, nil
	}

	clientIDs := make([]uuid.UUID, len(jobs))
	metricDates := make([]string, len(jobs))
	currencyCodes := make([]string, len(jobs))
	timezones := make([]string, len(jobs))
	revisions := make([]int64, len(jobs))
	for index, job := range jobs {
		clientIDs[index] = job.clientID
		metricDates[index] = job.metricDate
		currencyCodes[index] = job.currencyCode
		timezones[index] = job.timezone
		revisions[index] = job.revision
	}

	if _, err := tx.Exec(ctx, rebuildProviderDailyMetricsSQL, clientIDs, metricDates, currencyCodes, timezones); err != nil {
		return 0, fmt.Errorf("rebuild provider daily metrics: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM provider_daily_metric_jobs job
		USING unnest($1::uuid[],$2::text[],$3::text[],$4::bigint[])
			AS completed(client_id,metric_date_text,currency_code,revision)
		WHERE job.client_id=completed.client_id
		  AND job.metric_date=completed.metric_date_text::date
		  AND job.currency_code=completed.currency_code
		  AND job.revision=completed.revision
	`, clientIDs, metricDates, currencyCodes, revisions); err != nil {
		return 0, fmt.Errorf("complete provider daily metric jobs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit provider daily metric batch: %w", err)
	}
	return len(jobs), nil
}

const rebuildProviderDailyMetricsSQL = `
	WITH jobs AS (
		SELECT client_id,metric_date_text::date AS metric_date,currency_code,timezone,
			(metric_date_text::date::timestamp AT TIME ZONE timezone) AS period_start,
			((metric_date_text::date+1)::timestamp AT TIME ZONE timezone) AS period_end
		FROM unnest($1::uuid[],$2::text[],$3::text[],$4::text[])
			AS input(client_id,metric_date_text,currency_code,timezone)
	), booking_rollup AS (
		SELECT job.client_id,job.metric_date,job.currency_code,
			COUNT(booking.id)::int AS total_bookings,
			COUNT(booking.id) FILTER (WHERE LOWER(booking.status)='completed')::int AS completed_bookings,
			COUNT(booking.id) FILTER (
				WHERE LOWER(booking.status) NOT IN ('completed','cancelled','canceled','declined','expired')
			)::int AS scheduled_bookings,
			COUNT(booking.id) FILTER (
				WHERE LOWER(booking.status) IN ('cancelled','canceled','declined','expired')
			)::int AS cancelled_bookings,
			COUNT(booking.id) FILTER (
				WHERE LOWER(booking.payment_status) IN ('deposit_paid','deposit_paid_balance_due','paid_in_full')
			)::int AS secured_bookings,
			COUNT(DISTINCT booking.customer_id)::int AS unique_customers,
			COALESCE(SUM(booking.total_amount_minor) FILTER (
				WHERE LOWER(booking.status) NOT IN ('cancelled','canceled','declined','expired')
			),0)::bigint AS booked_value_minor
		FROM jobs job
		LEFT JOIN bookings booking ON booking.client_id=job.client_id
			AND booking.currency_code=job.currency_code
			AND booking.start_at>=job.period_start AND booking.start_at<job.period_end
		GROUP BY job.client_id,job.metric_date,job.currency_code
	), revenue_rollup AS (
		SELECT job.client_id,job.metric_date,job.currency_code,
			COALESCE(SUM(allocation.gross_amount_minor),0)::bigint AS gross_revenue_minor,
			COALESCE(SUM(allocation.business_net_amount_minor),0)::bigint AS net_revenue_minor,
			COUNT(allocation.id)::int AS payment_count
		FROM jobs job
		LEFT JOIN payments payment ON payment.client_id=job.client_id
			AND payment.currency_code=job.currency_code
			AND payment.paid_at>=job.period_start AND payment.paid_at<job.period_end
		LEFT JOIN payment_allocations allocation ON allocation.payment_id=payment.id
			AND allocation.status<>'reversed'
		GROUP BY job.client_id,job.metric_date,job.currency_code
	)
	INSERT INTO provider_daily_metrics (
		client_id,metric_date,currency_code,total_bookings,completed_bookings,
		scheduled_bookings,cancelled_bookings,secured_bookings,unique_customers,
		booked_value_minor,gross_revenue_minor,net_revenue_minor,payment_count,projected_at
	)
	SELECT booking.client_id,booking.metric_date,booking.currency_code,
		booking.total_bookings,booking.completed_bookings,booking.scheduled_bookings,
		booking.cancelled_bookings,booking.secured_bookings,booking.unique_customers,
		booking.booked_value_minor,revenue.gross_revenue_minor,revenue.net_revenue_minor,
		revenue.payment_count,NOW()
	FROM booking_rollup booking
	JOIN revenue_rollup revenue USING (client_id,metric_date,currency_code)
	ON CONFLICT (client_id,metric_date,currency_code) DO UPDATE SET
		total_bookings=EXCLUDED.total_bookings,
		completed_bookings=EXCLUDED.completed_bookings,
		scheduled_bookings=EXCLUDED.scheduled_bookings,
		cancelled_bookings=EXCLUDED.cancelled_bookings,
		secured_bookings=EXCLUDED.secured_bookings,
		unique_customers=EXCLUDED.unique_customers,
		booked_value_minor=EXCLUDED.booked_value_minor,
		gross_revenue_minor=EXCLUDED.gross_revenue_minor,
		net_revenue_minor=EXCLUDED.net_revenue_minor,
		payment_count=EXCLUDED.payment_count,
		projected_at=NOW()
`
