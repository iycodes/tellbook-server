package appdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errMarketplaceDiscoveryJobSuperseded = errors.New("marketplace discovery job was superseded")

const (
	marketplaceDiscoveryDefaultPollInterval = 5 * time.Second
	marketplaceDiscoveryDefaultBatchSize    = 25
	marketplaceDiscoveryMaximumBatchSize    = 100
	marketplaceDiscoveryHorizonInterval     = 15 * time.Second
	marketplaceDiscoveryHorizonBatchSize    = 5_000
	marketplaceDiscoveryExpiredDayBatchSize = 10_000
	marketplaceDiscoveryLeaseDuration       = 30 * time.Second
	marketplaceDiscoveryCountDebounce       = 500 * time.Millisecond
	marketplaceDiscoveryCountMaximumDelay   = 5 * time.Second
)

type MarketplaceDiscoveryWorkerConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

type MarketplaceDiscoveryWorker struct {
	repository   *Repository
	logger       *slog.Logger
	pollInterval time.Duration
	batchSize    int
}

type marketplaceDiscoveryJob struct {
	ClientID            uuid.UUID
	LeaseToken          uuid.UUID
	Revision            int64
	RefreshCounts       bool
	RefreshServices     bool
	RefreshAvailability bool
	AvailabilityFrom    *time.Time
	AvailabilityTo      *time.Time
}

func NewMarketplaceDiscoveryWorker(
	repository *Repository,
	logger *slog.Logger,
	config MarketplaceDiscoveryWorkerConfig,
) *MarketplaceDiscoveryWorker {
	if logger == nil {
		logger = slog.Default()
	}
	if config.PollInterval <= 0 {
		config.PollInterval = marketplaceDiscoveryDefaultPollInterval
	}
	if config.BatchSize <= 0 || config.BatchSize > marketplaceDiscoveryMaximumBatchSize {
		config.BatchSize = marketplaceDiscoveryDefaultBatchSize
	}
	return &MarketplaceDiscoveryWorker{
		repository: repository, logger: logger,
		pollInterval: config.PollInterval, batchSize: config.BatchSize,
	}
}

func (worker *MarketplaceDiscoveryWorker) Start(ctx context.Context, wakes ...<-chan struct{}) {
	if worker == nil || worker.repository == nil || worker.repository.db == nil {
		return
	}
	wake := firstMarketplaceDiscoveryWake(wakes)
	timer := time.NewTimer(0)
	defer timer.Stop()
	horizonTicker := time.NewTicker(marketplaceDiscoveryHorizonInterval)
	defer horizonTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		case <-horizonTicker.C:
			if _, err := worker.AdvanceAvailabilityHorizon(ctx); err != nil {
				worker.logger.Warn("advance marketplace availability horizon failed", "error", err)
			}
		}
		worker.drain(ctx)
		timer.Reset(worker.pollInterval)
	}
}

// AdvanceAvailabilityHorizon enqueues only providers missing their new day 31
// and prunes expired rows in a bounded batch. An advisory transaction lock keeps
// this maintenance singleton across worker replicas; normal projection claims
// remain horizontally scalable.
func (worker *MarketplaceDiscoveryWorker) AdvanceAvailabilityHorizon(ctx context.Context) (int, error) {
	if worker == nil || worker.repository == nil || worker.repository.db == nil {
		return 0, nil
	}
	tx, err := worker.repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin marketplace horizon maintenance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var leader bool
	if err := tx.QueryRow(ctx, `
		SELECT pg_try_advisory_xact_lock(hashtext('marketplace_availability_horizon'))
	`).Scan(&leader); err != nil {
		return 0, fmt.Errorf("claim marketplace horizon maintenance: %w", err)
	}
	if !leader {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return 0, nil
	}

	command, err := tx.Exec(ctx, `
		WITH missing AS (
			SELECT provider.client_id,
				((NOW() AT TIME ZONE provider.timezone)::date + 30) AS missing_date
			FROM marketplace_provider_documents provider
			WHERE EXISTS (
				SELECT 1 FROM marketplace_service_documents service
				WHERE service.provider_id = provider.client_id
				  AND NOT EXISTS (
					SELECT 1 FROM marketplace_service_availability_days day
					WHERE day.service_id = service.service_id
					  AND day.local_date = (NOW() AT TIME ZONE provider.timezone)::date + 30
				  )
			)
			ORDER BY provider.client_id
			LIMIT $1
		), expired_candidate AS (
			SELECT provider.client_id,
				provider.availability_refresh_date AS missing_date
			FROM marketplace_provider_documents provider
			WHERE provider.availability_refresh_after <= NOW()
			  AND provider.availability_refresh_date IS NOT NULL
			ORDER BY provider.availability_refresh_after, provider.client_id
			LIMIT $1
		), target AS (
			SELECT client_id, MIN(missing_date) AS starts_on, MAX(missing_date) AS ends_on
			FROM (
				SELECT * FROM missing
				UNION ALL
				SELECT * FROM expired_candidate
			) candidate
			GROUP BY client_id
			ORDER BY client_id
			LIMIT $1
		)
		INSERT INTO marketplace_discovery_jobs (
			client_id, requested_revision, refresh_counts, refresh_services,
			refresh_availability, availability_from, availability_to
		)
		SELECT client_id, 1, false, false, true, starts_on, ends_on
		FROM target
		ON CONFLICT (client_id) DO UPDATE SET
			requested_revision = marketplace_discovery_jobs.requested_revision + 1,
			refresh_availability = true,
			availability_from = CASE
				WHEN marketplace_discovery_jobs.refresh_availability AND marketplace_discovery_jobs.availability_from IS NULL THEN NULL
				ELSE LEAST(marketplace_discovery_jobs.availability_from, EXCLUDED.availability_from)
			END,
			availability_to = CASE
				WHEN marketplace_discovery_jobs.refresh_availability AND marketplace_discovery_jobs.availability_to IS NULL THEN NULL
				ELSE GREATEST(marketplace_discovery_jobs.availability_to, EXCLUDED.availability_to)
			END,
			available_at = NOW(),
			updated_at = NOW()
	`, marketplaceDiscoveryHorizonBatchSize)
	if err != nil {
		return 0, fmt.Errorf("enqueue marketplace horizon providers: %w", err)
	}
	enqueued := int(command.RowsAffected())

	if _, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT ctid
			FROM marketplace_service_availability_days
			WHERE local_date < (NOW() AT TIME ZONE 'Africa/Lagos')::date
			ORDER BY local_date, service_id
			LIMIT $1
		)
		DELETE FROM marketplace_service_availability_days day
		USING expired
		WHERE day.ctid = expired.ctid
	`, marketplaceDiscoveryExpiredDayBatchSize); err != nil {
		return 0, fmt.Errorf("prune expired marketplace availability days: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit marketplace horizon maintenance: %w", err)
	}
	return enqueued, nil
}

func (worker *MarketplaceDiscoveryWorker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		processed, err := worker.ProcessBatch(ctx)
		if err != nil {
			worker.logger.Warn("project marketplace discovery documents failed", "error", err)
			return
		}
		if processed == 0 {
			return
		}
		worker.logger.Debug("projected marketplace discovery documents", "count", processed)
	}
}

// ProcessBatch projects a bounded number of independently leased jobs. Claiming
// commits before source projection begins, so source-table triggers never wait on
// calendar/document work. The final revision-and-lease CAS rolls the projection
// back if a source mutation arrived while the document transaction was built.
func (worker *MarketplaceDiscoveryWorker) ProcessBatch(ctx context.Context) (int, error) {
	if worker == nil || worker.repository == nil || worker.repository.db == nil {
		return 0, nil
	}
	processed := 0
	for processed < worker.batchSize {
		job, claimed, err := worker.claimMarketplaceDiscoveryJob(ctx)
		if err != nil {
			return processed, err
		}
		if !claimed {
			break
		}
		if err := worker.projectClaimedMarketplaceDiscoveryJob(ctx, job); err != nil {
			worker.releaseMarketplaceDiscoveryLease(ctx, job)
			if errors.Is(err, errMarketplaceDiscoveryJobSuperseded) {
				continue
			}
			return processed, err
		}
		processed++
	}
	countsRefreshed, err := worker.processMarketplaceDiscoveryCounts(ctx)
	if err != nil {
		return processed, err
	}
	if processed == 0 && countsRefreshed {
		return 1, nil
	}
	return processed, nil
}

func (worker *MarketplaceDiscoveryWorker) claimMarketplaceDiscoveryJob(ctx context.Context) (marketplaceDiscoveryJob, bool, error) {
	token := uuid.New()
	var job marketplaceDiscoveryJob
	err := worker.repository.db.QueryRow(ctx, `
		WITH candidate AS (
			SELECT client_id
			FROM marketplace_discovery_jobs
			WHERE requested_revision > applied_revision
			  AND available_at <= NOW()
			  AND (lease_expires_at IS NULL OR lease_expires_at <= NOW())
			ORDER BY available_at, updated_at, client_id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE marketplace_discovery_jobs job
		SET lease_token = $1, lease_expires_at = NOW() + ($2 * INTERVAL '1 second')
		FROM candidate
		WHERE job.client_id = candidate.client_id
		RETURNING job.client_id, job.requested_revision, job.refresh_counts,
			job.refresh_services, job.refresh_availability,
			job.availability_from, job.availability_to
	`, token, marketplaceDiscoveryLeaseDuration.Seconds()).Scan(
		&job.ClientID, &job.Revision, &job.RefreshCounts,
		&job.RefreshServices, &job.RefreshAvailability,
		&job.AvailabilityFrom, &job.AvailabilityTo,
	)
	if err == pgx.ErrNoRows {
		return marketplaceDiscoveryJob{}, false, nil
	}
	if err != nil {
		return marketplaceDiscoveryJob{}, false, fmt.Errorf("claim marketplace discovery job: %w", err)
	}
	job.LeaseToken = token
	return job, true, nil
}

func (worker *MarketplaceDiscoveryWorker) projectClaimedMarketplaceDiscoveryJob(ctx context.Context, job marketplaceDiscoveryJob) error {
	tx, err := worker.repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin marketplace discovery projection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := projectMarketplaceProvider(ctx, tx, job); err != nil {
		return fmt.Errorf("project marketplace provider %s: %w", job.ClientID, err)
	}
	completed, err := tx.Exec(ctx, `
		UPDATE marketplace_discovery_jobs
		SET applied_revision = $2, refresh_counts = false,
			refresh_services = false, refresh_availability = false,
			availability_from = NULL, availability_to = NULL,
			lease_token = NULL, lease_expires_at = NULL, updated_at = NOW()
		WHERE client_id = $1 AND requested_revision = $2 AND lease_token = $3
		  AND EXISTS (SELECT 1 FROM clients WHERE id = $1)
	`, job.ClientID, job.Revision, job.LeaseToken)
	if err != nil {
		return fmt.Errorf("complete marketplace discovery job: %w", err)
	}
	if completed.RowsAffected() == 0 {
		completed, err = tx.Exec(ctx, `
			DELETE FROM marketplace_discovery_jobs
			WHERE client_id = $1 AND requested_revision = $2 AND lease_token = $3
			  AND NOT EXISTS (SELECT 1 FROM clients WHERE id = $1)
		`, job.ClientID, job.Revision, job.LeaseToken)
		if err != nil {
			return fmt.Errorf("complete deleted provider discovery job: %w", err)
		}
	}
	if completed.RowsAffected() != 1 {
		return errMarketplaceDiscoveryJobSuperseded
	}
	if job.RefreshCounts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO marketplace_discovery_count_jobs (
				singleton, requested_revision, applied_revision, available_at
			) VALUES (true, 1, 0, NOW() + ($1 * INTERVAL '1 second'))
			ON CONFLICT (singleton) DO UPDATE SET
				requested_revision = marketplace_discovery_count_jobs.requested_revision + 1,
				available_at = CASE
					WHEN marketplace_discovery_count_jobs.requested_revision = marketplace_discovery_count_jobs.applied_revision
						THEN NOW() + ($1 * INTERVAL '1 second')
					ELSE LEAST(
						NOW() + ($1 * INTERVAL '1 second'),
						marketplace_discovery_count_jobs.updated_at + ($2 * INTERVAL '1 second')
					)
				END,
				updated_at = CASE
					WHEN marketplace_discovery_count_jobs.requested_revision = marketplace_discovery_count_jobs.applied_revision
						THEN NOW()
					ELSE marketplace_discovery_count_jobs.updated_at
				END
		`, marketplaceDiscoveryCountDebounce.Seconds(), marketplaceDiscoveryCountMaximumDelay.Seconds()); err != nil {
			return fmt.Errorf("enqueue marketplace discovery count refresh: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit marketplace discovery projection: %w", err)
	}
	return nil
}

func (worker *MarketplaceDiscoveryWorker) releaseMarketplaceDiscoveryLease(ctx context.Context, job marketplaceDiscoveryJob) {
	if _, err := worker.repository.db.Exec(ctx, `
		UPDATE marketplace_discovery_jobs
		SET lease_token = NULL, lease_expires_at = NULL,
			available_at = NOW() + INTERVAL '1 second', updated_at = NOW()
		WHERE client_id = $1 AND lease_token = $2
	`, job.ClientID, job.LeaseToken); err != nil {
		worker.logger.Warn("release marketplace discovery lease failed", "provider_id", job.ClientID, "error", err)
	}
}

func (worker *MarketplaceDiscoveryWorker) processMarketplaceDiscoveryCounts(ctx context.Context) (bool, error) {
	tx, err := worker.repository.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin marketplace discovery count refresh: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	if err := tx.QueryRow(ctx, `
		SELECT requested_revision
		FROM marketplace_discovery_count_jobs
		WHERE singleton AND requested_revision > applied_revision AND available_at <= NOW()
		FOR UPDATE SKIP LOCKED
	`).Scan(&revision); err != nil {
		if err == pgx.ErrNoRows {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return false, fmt.Errorf("commit empty marketplace count refresh: %w", commitErr)
			}
			return false, nil
		}
		return false, fmt.Errorf("claim marketplace discovery count refresh: %w", err)
	}
	if err := refreshMarketplaceDiscoveryCounts(ctx, tx); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE marketplace_discovery_count_jobs
		SET applied_revision = $1, updated_at = NOW()
		WHERE singleton AND requested_revision = $1
	`, revision); err != nil {
		return false, fmt.Errorf("complete marketplace discovery count refresh: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit marketplace discovery count refresh: %w", err)
	}
	return true, nil
}

// FlushMarketplaceDiscoveryCounts completes a pending coalesced count refresh.
// It is intended for finite admin drains; long-running workers keep the normal
// debounce so a backlog produces one recount instead of one recount per batch.
func (worker *MarketplaceDiscoveryWorker) FlushMarketplaceDiscoveryCounts(ctx context.Context) error {
	if worker == nil || worker.repository == nil || worker.repository.db == nil {
		return nil
	}
	if _, err := worker.repository.db.Exec(ctx, `
		UPDATE marketplace_discovery_count_jobs
		SET available_at = NOW(), updated_at = NOW()
		WHERE singleton AND requested_revision > applied_revision
	`); err != nil {
		return fmt.Errorf("make marketplace discovery count refresh available: %w", err)
	}
	_, err := worker.processMarketplaceDiscoveryCounts(ctx)
	return err
}

func projectMarketplaceProvider(
	ctx context.Context,
	tx pgx.Tx,
	job marketplaceDiscoveryJob,
) error {
	command, err := tx.Exec(ctx, `
		INSERT INTO marketplace_provider_documents (
			client_id, handle_slug, business_name, headline,
			category_id, category_name, avatar_url, hero_image_url,
			verified, review_rating, review_count, completed_bookings,
			location_visibility, timezone, public_location_label,
			document_revision, source_updated_at, projected_at
		)
		SELECT profile.client_id, profile.handle_slug, profile.business_name, profile.headline,
			category.id, category.name, COALESCE(profile.avatar_url, ''), COALESCE(profile.hero_image_url, ''),
			profile.verified, COALESCE(review_summary.rating, 0), COALESCE(review_summary.count, 0),
			COALESCE(booking_summary.count, 0), profile.marketplace_location_visibility,
			profile.timezone, profile.public_location_label, $2,
			GREATEST(
				profile.updated_at,
				category.updated_at,
				COALESCE(review_summary.updated_at, '-infinity'::timestamptz),
				COALESCE(booking_summary.updated_at, '-infinity'::timestamptz)
			), NOW()
		FROM client_profiles profile
		JOIN marketplace_categories category
			ON category.id = profile.marketplace_category_id AND category.is_active
		LEFT JOIN LATERAL (
			SELECT AVG(review.rating)::double precision AS rating, COUNT(*)::int AS count,
				MAX(review.updated_at) AS updated_at
			FROM provider_reviews review
			WHERE review.client_id = profile.client_id AND review.status = 'approved'
		) review_summary ON true
		LEFT JOIN LATERAL (
			SELECT COUNT(*)::int AS count, MAX(booking.updated_at) AS updated_at
			FROM bookings booking
			WHERE booking.client_id = profile.client_id AND booking.status = 'completed'
		) booking_summary ON true
		WHERE profile.client_id = $1
		  AND profile.marketplace_enabled
 AND NOT profile.platform_restricted
		  AND profile.market_configured_at IS NOT NULL
		  AND NULLIF(BTRIM(profile.handle_slug), '') IS NOT NULL
		ON CONFLICT (client_id) DO UPDATE SET
			handle_slug = EXCLUDED.handle_slug,
			business_name = EXCLUDED.business_name,
			headline = EXCLUDED.headline,
			category_id = EXCLUDED.category_id,
			category_name = EXCLUDED.category_name,
			avatar_url = EXCLUDED.avatar_url,
			hero_image_url = EXCLUDED.hero_image_url,
			verified = EXCLUDED.verified,
			review_rating = EXCLUDED.review_rating,
			review_count = EXCLUDED.review_count,
			completed_bookings = EXCLUDED.completed_bookings,
			location_visibility = EXCLUDED.location_visibility,
			timezone = EXCLUDED.timezone,
			public_location_label = EXCLUDED.public_location_label,
			document_revision = EXCLUDED.document_revision,
			source_updated_at = EXCLUDED.source_updated_at,
			projected_at = NOW()
	`, job.ClientID, job.Revision)
	if err != nil {
		return fmt.Errorf("upsert provider document: %w", err)
	}
	if command.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM marketplace_provider_documents WHERE client_id = $1`, job.ClientID); err != nil {
			return fmt.Errorf("remove ineligible provider document: %w", err)
		}
		return nil
	}
	if !job.RefreshServices {
		if job.RefreshAvailability {
			return projectMarketplaceAvailability(ctx, tx, job)
		}
		return nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM marketplace_service_documents WHERE provider_id = $1`, job.ClientID); err != nil {
		return fmt.Errorf("clear provider service documents: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_service_documents (
			service_id, provider_id, slug, title, description, image_url,
			duration_minutes, price_amount_minor, currency_code, fulfillment_mode,
			provider_location_id, location_label, state_region_id, lga_region_id,
			internal_geog, public_geog, public_latitude, public_longitude, map_precision,
			max_travel_distance_meters, sort_order, document_revision,
			source_updated_at, projected_at
		)
		SELECT service.id, service.client_id, service.slug, service.title, service.description,
			COALESCE(service.image_url, ''), service.duration_minutes, service.price_amount_minor,
			service.currency_code, service.fulfillment_mode, service.provider_location_id,
			CASE
				WHEN service.fulfillment_mode = 'virtual' THEN 'Online'
				WHEN provider.location_visibility = 'exact' THEN location.formatted_address
				ELSE COALESCE(NULLIF(location.locality, ''), lga.name, state.name, provider.public_location_label)
			END,
			location.state_region_id, location.lga_region_id,
			CASE WHEN service.fulfillment_mode = 'virtual' THEN NULL ELSE location.geog END,
			public_point.geog,
			CASE WHEN public_point.geog IS NULL THEN NULL ELSE ROUND(ST_Y(public_point.geog::geometry)::numeric, 6) END,
			CASE WHEN public_point.geog IS NULL THEN NULL ELSE ROUND(ST_X(public_point.geog::geometry)::numeric, 6) END,
			CASE
				WHEN service.fulfillment_mode = 'virtual' THEN 'none'
				WHEN provider.location_visibility = 'exact' THEN 'exact'
				ELSE 'approximate'
			END,
			service.max_travel_distance_meters, service.sort_order, $2,
			GREATEST(service.updated_at, COALESCE(location.updated_at, '-infinity'::timestamptz)), NOW()
		FROM services service
		JOIN marketplace_provider_documents provider ON provider.client_id = service.client_id
		LEFT JOIN business_locations location ON location.id = service.provider_location_id
		LEFT JOIN administrative_regions state ON state.id = location.state_region_id
		LEFT JOIN administrative_regions lga ON lga.id = location.lga_region_id
		LEFT JOIN LATERAL (
			SELECT CASE
				WHEN service.fulfillment_mode = 'virtual' THEN NULL::geography
				WHEN provider.location_visibility = 'exact' THEN location.geog
				ELSE COALESCE(
					ST_PointOnSurface(COALESCE(lga.boundary, state.boundary))::geography,
					ST_SnapToGrid(location.geog::geometry, 0.1)::geography
				)
			END AS geog
		) public_point ON true
		WHERE service.client_id = $1
		  AND service.status = 'published'
		  AND service.is_active
		  AND NOT service.is_hidden
		  AND service.duration_minutes > 0
		  AND (
			(service.availability_mode = 'custom' AND EXISTS (
				SELECT 1 FROM service_availability_windows availability_window
				WHERE availability_window.service_id = service.id
				  AND EXTRACT(EPOCH FROM (availability_window.end_time - availability_window.start_time)) / 60 >=
					service.prep_time_minutes + service.duration_minutes + service.buffer_time_minutes
			)) OR
			(service.availability_mode <> 'custom' AND EXISTS (
				SELECT 1 FROM provider_availability_windows availability_window
				WHERE availability_window.client_id = service.client_id
				  AND EXTRACT(EPOCH FROM (availability_window.end_time - availability_window.start_time)) / 60 >=
					service.prep_time_minutes + service.duration_minutes + service.buffer_time_minutes
			))
		  )
		  AND (
			service.fulfillment_mode = 'virtual' OR
			(location.is_active AND location.resolution_status = 'coordinates_resolved' AND
				(provider.location_visibility = 'exact' OR public_point.geog IS NOT NULL))
		  )
	`, job.ClientID, job.Revision); err != nil {
		return fmt.Errorf("insert service documents: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM marketplace_provider_documents provider
		WHERE provider.client_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM marketplace_service_documents service
			WHERE service.provider_id = provider.client_id
		  )
	`, job.ClientID); err != nil {
		return fmt.Errorf("remove provider without searchable services: %w", err)
	}
	if job.RefreshAvailability {
		return projectMarketplaceAvailability(ctx, tx, job)
	}
	return nil
}

func projectMarketplaceAvailability(ctx context.Context, tx pgx.Tx, job marketplaceDiscoveryJob) error {
	if _, err := tx.Exec(ctx, `
		WITH provider_clock AS (
			SELECT client_id, (NOW() AT TIME ZONE timezone)::date AS today
			FROM marketplace_provider_documents
			WHERE client_id = $1
		), bounds AS (
			SELECT GREATEST(COALESCE($2::date, today), today) AS starts_on,
				LEAST(COALESCE($3::date, today + 30), today + 30) AS ends_on
			FROM provider_clock
		)
		DELETE FROM marketplace_service_availability_days availability
		USING bounds
		WHERE availability.provider_id = $1
		  AND availability.local_date BETWEEN bounds.starts_on AND bounds.ends_on
	`, job.ClientID, job.AvailabilityFrom, job.AvailabilityTo); err != nil {
		return fmt.Errorf("clear marketplace availability days: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		WITH provider_clock AS (
			SELECT provider.client_id, provider.timezone,
				profile.concurrent_booking_capacity,
				(NOW() AT TIME ZONE provider.timezone)::date AS today
			FROM marketplace_provider_documents provider
			JOIN client_profiles profile ON profile.client_id = provider.client_id
			WHERE provider.client_id = $1
		), bounds AS (
			SELECT *,
				GREATEST(COALESCE($2::date, today), today) AS starts_on,
				LEAST(COALESCE($3::date, today + 30), today + 30) AS ends_on
			FROM provider_clock
		), service_days AS (
			SELECT document.service_id, document.provider_id, day.local_date,
				service.duration_minutes, service.prep_time_minutes, service.buffer_time_minutes,
				service.minimum_notice_minutes, service.max_bookings_per_day,
				service.availability_mode, bounds.timezone, bounds.concurrent_booking_capacity
			FROM marketplace_service_documents document
			JOIN services service ON service.id = document.service_id
			JOIN bounds ON bounds.client_id = document.provider_id
			CROSS JOIN LATERAL generate_series(
				bounds.starts_on, bounds.ends_on, INTERVAL '1 day'
			) day(local_date)
		)
			INSERT INTO marketplace_service_availability_days (
				service_id, provider_id, local_date, has_available_slot,
				first_available_at, refresh_after, document_revision, projected_at
			)
			SELECT service_day.service_id, service_day.provider_id, service_day.local_date::date,
				next_slot.start_at IS NOT NULL, next_slot.start_at,
				CASE
					WHEN next_slot.start_at IS NULL THEN 'infinity'::timestamptz
					ELSE GREATEST(
						NOW() + INTERVAL '5 seconds',
						next_slot.start_at - make_interval(mins => service_day.minimum_notice_minutes) + INTERVAL '1 second'
					)
				END,
				$4, NOW()
		FROM service_days service_day
		LEFT JOIN LATERAL (
			SELECT slot.start_at
			FROM (
				SELECT day_of_week, start_time, end_time, slot_interval_minutes
				FROM service_availability_windows
				WHERE service_day.availability_mode = 'custom'
				  AND service_id = service_day.service_id
				UNION ALL
				SELECT day_of_week, start_time, end_time, slot_interval_minutes
				FROM provider_availability_windows
				WHERE service_day.availability_mode <> 'custom'
				  AND client_id = service_day.provider_id
			) availability_window
			CROSS JOIN LATERAL generate_series(
				((service_day.local_date::date + availability_window.start_time) AT TIME ZONE service_day.timezone)
					+ make_interval(mins => service_day.prep_time_minutes),
				((service_day.local_date::date + availability_window.end_time) AT TIME ZONE service_day.timezone)
					- make_interval(mins => service_day.duration_minutes + service_day.buffer_time_minutes),
				make_interval(mins => availability_window.slot_interval_minutes)
			) slot(start_at)
			WHERE availability_window.day_of_week = EXTRACT(DOW FROM service_day.local_date)::int
			  AND slot.start_at >= NOW() + make_interval(mins => service_day.minimum_notice_minutes)
			  AND (
				SELECT COUNT(*) FROM bookings busy
				WHERE busy.client_id = service_day.provider_id
				  AND busy.status NOT IN ('cancelled', 'canceled', 'declined', 'expired')
				  AND busy.occupied_start_at < slot.start_at + make_interval(mins => service_day.duration_minutes + service_day.buffer_time_minutes)
				  AND busy.occupied_end_at > slot.start_at - make_interval(mins => service_day.prep_time_minutes)
			  ) < service_day.concurrent_booking_capacity
			  AND (
				service_day.max_bookings_per_day = 0 OR (
					SELECT COUNT(*) FROM bookings daily
					WHERE daily.client_id = service_day.provider_id
					  AND daily.service_id = service_day.service_id
					  AND daily.status NOT IN ('cancelled', 'canceled', 'declined', 'expired')
					  AND daily.start_at >= (service_day.local_date::date AT TIME ZONE service_day.timezone)
					  AND daily.start_at < ((service_day.local_date::date + 1) AT TIME ZONE service_day.timezone)
				) < service_day.max_bookings_per_day
			  )
			ORDER BY slot.start_at
			LIMIT 1
		) next_slot ON true
		ON CONFLICT (service_id, local_date) DO UPDATE SET
				has_available_slot = EXCLUDED.has_available_slot,
				first_available_at = EXCLUDED.first_available_at,
				refresh_after = EXCLUDED.refresh_after,
				document_revision = EXCLUDED.document_revision,
			projected_at = NOW()
	`, job.ClientID, job.AvailabilityFrom, job.AvailabilityTo, job.Revision); err != nil {
		return fmt.Errorf("project marketplace availability days: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE marketplace_provider_documents provider
		SET (availability_refresh_after, availability_refresh_date) = (
			SELECT day.refresh_after, day.local_date
			FROM marketplace_service_availability_days day
			WHERE day.provider_id = $1
			ORDER BY day.refresh_after, day.local_date, day.service_id
			LIMIT 1
		)
		WHERE provider.client_id = $1
	`, job.ClientID); err != nil {
		return fmt.Errorf("update marketplace availability refresh cursor: %w", err)
	}
	return nil
}

func refreshMarketplaceDiscoveryCounts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `DELETE FROM marketplace_discovery_counts`); err != nil {
		return fmt.Errorf("clear marketplace discovery counts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_discovery_counts (dimension, dimension_id, provider_count)
		SELECT 'category', provider.category_id, COUNT(DISTINCT provider.client_id)::int
		FROM marketplace_provider_documents provider
		JOIN marketplace_service_documents service ON service.provider_id = provider.client_id
		GROUP BY provider.category_id
		UNION ALL
		SELECT 'state', service.state_region_id, COUNT(DISTINCT service.provider_id)::int
		FROM marketplace_service_documents service
		WHERE service.state_region_id IS NOT NULL AND service.fulfillment_mode <> 'virtual'
		GROUP BY service.state_region_id
		UNION ALL
		SELECT 'lga', service.lga_region_id, COUNT(DISTINCT service.provider_id)::int
		FROM marketplace_service_documents service
		WHERE service.lga_region_id IS NOT NULL AND service.fulfillment_mode <> 'virtual'
		GROUP BY service.lga_region_id
	`); err != nil {
		return fmt.Errorf("refresh marketplace discovery counts: %w", err)
	}
	return nil
}

func firstMarketplaceDiscoveryWake(wakes []<-chan struct{}) <-chan struct{} {
	if len(wakes) == 0 {
		return nil
	}
	return wakes[0]
}
