package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maximumBackfillBatchSize = 5_000

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) &&
		!errors.Is(err, os.ErrNotExist) {
		exitError("load environment", err)
	}

	action := flag.String("action", "status", "status, rebuild, repair, drain, or verify")
	providerID := flag.String("provider-id", "", "provider UUID for repair")
	afterProviderID := flag.String("after-provider-id", "", "exclusive UUID cursor for a resumable rebuild")
	batchSize := flag.Int("batch-size", 500, "provider batch size")
	pause := flag.Duration("pause", 25*time.Millisecond, "pause between rebuild batches")
	flag.Parse()

	if *batchSize < 1 || *batchSize > maximumBackfillBatchSize {
		exitError("batch-size must be between 1 and 5000", nil)
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		exitError("DATABASE_URL is required", nil)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		exitError("open database", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		exitError("ping database", err)
	}

	switch strings.ToLower(strings.TrimSpace(*action)) {
	case "status":
		err = printStatus(ctx, pool)
	case "rebuild":
		err = enqueueRebuild(ctx, pool, *afterProviderID, *batchSize, *pause)
	case "repair":
		err = enqueueRepair(ctx, pool, *providerID)
	case "drain":
		err = drain(ctx, pool, *batchSize)
	case "verify":
		err = verify(ctx, pool)
	default:
		exitError("unknown action", fmt.Errorf("%q", *action))
	}
	if err != nil {
		exitError(*action, err)
	}
}

func printStatus(ctx context.Context, pool *pgxpool.Pool) error {
	var pending, activeLeases, providers, services, availabilityDays, countRows int64
	var countRefreshPending bool
	var oldestLagSeconds *float64
	err := pool.QueryRow(ctx, `
			SELECT
				COUNT(*) FILTER (WHERE requested_revision > applied_revision),
				COUNT(*) FILTER (WHERE lease_expires_at > NOW()),
				EXTRACT(EPOCH FROM (NOW() - MIN(updated_at) FILTER (WHERE requested_revision > applied_revision))),
			(SELECT COUNT(*) FROM marketplace_provider_documents),
			(SELECT COUNT(*) FROM marketplace_service_documents),
			(SELECT COUNT(*) FROM marketplace_service_availability_days),
				(SELECT COUNT(*) FROM marketplace_discovery_counts),
				(SELECT requested_revision > applied_revision FROM marketplace_discovery_count_jobs WHERE singleton)
			FROM marketplace_discovery_jobs
		`).Scan(&pending, &activeLeases, &oldestLagSeconds, &providers, &services, &availabilityDays, &countRows, &countRefreshPending)
	if err != nil {
		return err
	}
	lag := "0"
	if oldestLagSeconds != nil {
		lag = fmt.Sprintf("%.3f", *oldestLagSeconds)
	}
	fmt.Printf("pending=%d active_leases=%d count_refresh_pending=%t oldest_lag_seconds=%s provider_documents=%d service_documents=%d availability_days=%d count_rows=%d\n",
		pending, activeLeases, countRefreshPending, lag, providers, services, availabilityDays, countRows)
	return nil
}

func enqueueRebuild(
	ctx context.Context,
	pool *pgxpool.Pool,
	rawAfter string,
	batchSize int,
	pause time.Duration,
) error {
	after := uuid.Nil
	if strings.TrimSpace(rawAfter) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(rawAfter))
		if err != nil {
			return fmt.Errorf("parse after-provider-id: %w", err)
		}
		after = parsed
	}
	total := 0
	for {
		rows, err := pool.Query(ctx, `
			SELECT client_id
			FROM client_profiles
			WHERE client_id > $1
			ORDER BY client_id
			LIMIT $2
		`, after, batchSize)
		if err != nil {
			return fmt.Errorf("list rebuild providers: %w", err)
		}
		ids := make([]uuid.UUID, 0, batchSize)
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(ids) == 0 {
			break
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO marketplace_discovery_jobs (
				client_id, requested_revision, refresh_counts, refresh_services, refresh_availability
			)
			SELECT provider_id, 1, true, true, true
			FROM UNNEST($1::uuid[]) AS providers(provider_id)
			ON CONFLICT (client_id) DO UPDATE
			SET requested_revision = marketplace_discovery_jobs.requested_revision + 1,
				refresh_counts = true,
				refresh_services = true,
				refresh_availability = true,
				availability_from = NULL,
				availability_to = NULL,
				available_at = NOW(),
				updated_at = NOW()
		`, ids); err != nil {
			return fmt.Errorf("enqueue rebuild batch: %w", err)
		}
		if _, err := pool.Exec(ctx, `SELECT pg_notify('tellbook_worker_core', 'marketplace_discovery')`); err != nil {
			return fmt.Errorf("notify discovery worker: %w", err)
		}
		after = ids[len(ids)-1]
		total += len(ids)
		fmt.Printf("enqueued=%d last_provider_id=%s\n", total, after)
		if pause > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
	}
	return nil
}

func enqueueRepair(ctx context.Context, pool *pgxpool.Pool, rawProviderID string) error {
	providerID, err := uuid.Parse(strings.TrimSpace(rawProviderID))
	if err != nil {
		return fmt.Errorf("provider-id is required and must be a UUID: %w", err)
	}
	command, err := pool.Exec(ctx, `
		INSERT INTO marketplace_discovery_jobs (
			client_id, requested_revision, refresh_counts, refresh_services, refresh_availability
		)
		SELECT client_id, 1, true, true, true FROM client_profiles WHERE client_id = $1
		ON CONFLICT (client_id) DO UPDATE
		SET requested_revision = marketplace_discovery_jobs.requested_revision + 1,
			refresh_counts = true,
			refresh_services = true,
			refresh_availability = true,
			availability_from = NULL,
			availability_to = NULL,
			available_at = NOW(),
			updated_at = NOW()
	`, providerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("provider %s was not found", providerID)
	}
	_, err = pool.Exec(ctx, `SELECT pg_notify('tellbook_worker_core', 'marketplace_discovery')`)
	if err == nil {
		fmt.Printf("enqueued_provider_id=%s\n", providerID)
	}
	return err
}

func drain(ctx context.Context, pool *pgxpool.Pool, batchSize int) error {
	worker := appdata.NewMarketplaceDiscoveryWorker(
		appdata.NewRepository(pool),
		slog.Default(),
		appdata.MarketplaceDiscoveryWorkerConfig{BatchSize: batchSize},
	)
	total := 0
	for {
		enqueued, err := worker.AdvanceAvailabilityHorizon(ctx)
		if err != nil {
			return err
		}
		for {
			processed, err := worker.ProcessBatch(ctx)
			if err != nil {
				return err
			}
			total += processed
			if processed == 0 {
				if err := worker.FlushMarketplaceDiscoveryCounts(ctx); err != nil {
					return err
				}
				break
			}
		}
		if enqueued == 0 {
			fmt.Printf("processed=%d\n", total)
			return nil
		}
	}
}

func verify(ctx context.Context, pool *pgxpool.Pool) error {
	var orphanProviders, privacyViolations, revisionViolations, horizonGaps int64
	var expiredCandidates, refreshCursorMismatches, countMismatches int64
	var providerChecksum, serviceChecksum, availabilityChecksum string
	err := pool.QueryRow(ctx, `
		WITH expected_counts AS (
			SELECT 'category'::text AS dimension, provider.category_id AS dimension_id,
				COUNT(DISTINCT provider.client_id)::int AS provider_count
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
		), count_mismatches AS (
			(SELECT * FROM expected_counts
			 EXCEPT SELECT dimension, dimension_id, provider_count FROM marketplace_discovery_counts)
			UNION ALL
			(SELECT dimension, dimension_id, provider_count FROM marketplace_discovery_counts
			 EXCEPT SELECT * FROM expected_counts)
		)
		SELECT
			(SELECT COUNT(*) FROM marketplace_provider_documents provider
			 WHERE NOT EXISTS (SELECT 1 FROM marketplace_service_documents service WHERE service.provider_id = provider.client_id)),
			(SELECT COUNT(*) FROM marketplace_service_documents service
			 JOIN marketplace_provider_documents provider ON provider.client_id = service.provider_id
			 WHERE (provider.location_visibility = 'approximate' AND service.map_precision <> 'approximate' AND service.fulfillment_mode <> 'virtual')
			    OR (service.map_precision = 'none' AND service.public_geog IS NOT NULL)),
			(SELECT COUNT(*) FROM marketplace_provider_documents provider
			 JOIN marketplace_discovery_jobs job ON job.client_id = provider.client_id
			 WHERE provider.document_revision <> job.applied_revision),
			(SELECT COUNT(*) FROM marketplace_provider_documents provider
			 WHERE EXISTS (
				SELECT 1 FROM marketplace_service_documents service
				WHERE service.provider_id = provider.client_id
				  AND NOT EXISTS (
					SELECT 1 FROM marketplace_service_availability_days day
					WHERE day.service_id = service.service_id
					  AND day.local_date = (NOW() AT TIME ZONE provider.timezone)::date + 30
				  )
			 )),
			(SELECT COUNT(*) FROM marketplace_service_availability_days day
			 WHERE day.has_available_slot AND day.first_available_at < NOW()
			   AND day.refresh_after < NOW() - INTERVAL '30 seconds'),
			(SELECT COUNT(*) FROM marketplace_provider_documents provider
			 WHERE ROW(provider.availability_refresh_after, provider.availability_refresh_date)
			   IS DISTINCT FROM (
				SELECT ROW(day.refresh_after, day.local_date)
				FROM marketplace_service_availability_days day
				WHERE day.provider_id = provider.client_id
				ORDER BY day.refresh_after, day.local_date, day.service_id
				LIMIT 1
			   )),
			(SELECT COUNT(*) FROM count_mismatches),
			(SELECT MD5(COALESCE(SUM(hashtextextended(ROW(client_id, document_revision, source_updated_at)::text, 0))::text, '0'))
			 FROM marketplace_provider_documents),
			(SELECT MD5(COALESCE(SUM(hashtextextended(ROW(service_id, provider_id, document_revision, source_updated_at)::text, 0))::text, '0'))
			 FROM marketplace_service_documents),
			(SELECT MD5(COALESCE(SUM(hashtextextended(ROW(service_id, local_date, first_available_at, refresh_after, document_revision)::text, 0))::text, '0'))
			 FROM marketplace_service_availability_days)
	`).Scan(
		&orphanProviders, &privacyViolations, &revisionViolations, &horizonGaps,
		&expiredCandidates, &refreshCursorMismatches, &countMismatches,
		&providerChecksum, &serviceChecksum, &availabilityChecksum,
	)
	if err != nil {
		return err
	}
	fmt.Printf("orphan_providers=%d privacy_violations=%d revision_violations=%d horizon_gaps=%d expired_candidates=%d refresh_cursor_mismatches=%d count_mismatches=%d provider_checksum=%s service_checksum=%s availability_checksum=%s\n",
		orphanProviders, privacyViolations, revisionViolations, horizonGaps, expiredCandidates, refreshCursorMismatches, countMismatches,
		providerChecksum, serviceChecksum, availabilityChecksum)
	if orphanProviders != 0 || privacyViolations != 0 || revisionViolations != 0 || horizonGaps != 0 ||
		expiredCandidates != 0 || refreshCursorMismatches != 0 || countMismatches != 0 {
		return errors.New("discovery projection invariants failed")
	}
	return nil
}

func exitError(operation string, err error) {
	if err == nil {
		fmt.Fprintln(os.Stderr, operation)
	} else {
		fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
	}
	os.Exit(1)
}
