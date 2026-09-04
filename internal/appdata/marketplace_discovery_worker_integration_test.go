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

func TestMarketplaceDiscoveryProjectionIsRevisionSafeAndPrivacyPreserving(t *testing.T) {
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

	_, stateID, lgaID := insertMarketplaceProjectionRegions(t, ctx, pool)
	providerID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	locationID := insertMarketplaceTestLocation(
		t, ctx, pool, providerID,
		"14 Exact Private Street", "Projection locality", 6.451234, 3.471234,
	)
	if _, err := pool.Exec(ctx, `
		UPDATE business_locations
		SET state_region_id = $2, lga_region_id = $3
		WHERE id = $1
	`, locationID, stateID, lgaID); err != nil {
		t.Fatal(err)
	}
	serviceID := insertMarketplaceTestService(
		t, ctx, pool, providerID, locationID,
		"projection-service", "provider_location", 25000, nil,
	)
	insertMarketplaceTestSchedule(t, ctx, pool, providerID, time.Now().Weekday())

	var requestedBefore int64
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision FROM marketplace_discovery_jobs WHERE client_id = $1
	`, providerID).Scan(&requestedBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET headline = 'Projection revision one' WHERE client_id = $1`, providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET headline = 'Projection revision two' WHERE client_id = $1`, providerID); err != nil {
		t.Fatal(err)
	}
	var requestedAfter int64
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision FROM marketplace_discovery_jobs WHERE client_id = $1
	`, providerID).Scan(&requestedAfter); err != nil {
		t.Fatal(err)
	}
	if requestedAfter != requestedBefore+2 {
		t.Fatalf("requested revision = %d, want %d after two coalesced updates", requestedAfter, requestedBefore+2)
	}

	processMarketplaceProjectionProvider(t, ctx, pool, providerID)

	var appliedRevision, documentRevision int64
	var refreshCounts bool
	if err := pool.QueryRow(ctx, `
		SELECT job.applied_revision, job.refresh_counts, provider.document_revision
		FROM marketplace_discovery_jobs job
		JOIN marketplace_provider_documents provider ON provider.client_id = job.client_id
		WHERE job.client_id = $1
	`, providerID).Scan(&appliedRevision, &refreshCounts, &documentRevision); err != nil {
		t.Fatal(err)
	}
	if appliedRevision != requestedAfter || documentRevision != requestedAfter || refreshCounts {
		t.Fatalf("projection revisions applied=%d document=%d requested=%d refresh_counts=%t",
			appliedRevision, documentRevision, requestedAfter, refreshCounts)
	}

	var mapPrecision, locationLabel string
	var internalLatitude, internalLongitude, publicLatitude, publicLongitude float64
	if err := pool.QueryRow(ctx, `
		SELECT map_precision, location_label,
			ST_Y(internal_geog::geometry), ST_X(internal_geog::geometry),
			public_latitude::double precision, public_longitude::double precision
		FROM marketplace_service_documents
		WHERE service_id = $1
	`, serviceID).Scan(
		&mapPrecision, &locationLabel,
		&internalLatitude, &internalLongitude, &publicLatitude, &publicLongitude,
	); err != nil {
		t.Fatal(err)
	}
	if mapPrecision != "approximate" || locationLabel != "Projection locality" {
		t.Fatalf("public location precision=%q label=%q", mapPrecision, locationLabel)
	}
	if internalLatitude == publicLatitude && internalLongitude == publicLongitude {
		t.Fatalf("approximate public point exposed the internal point %.6f,%.6f", internalLatitude, internalLongitude)
	}
	var availabilityDays int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM marketplace_service_availability_days WHERE service_id = $1
	`, serviceID).Scan(&availabilityDays); err != nil {
		t.Fatal(err)
	}
	if availabilityDays != 31 {
		t.Fatalf("projected availability days = %d, want the bounded 31-day horizon", availabilityDays)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_service_availability_days day
		SET first_available_at = NOW() - INTERVAL '1 minute',
			refresh_after = '-infinity', has_available_slot = true
		WHERE day.service_id = $1
		  AND day.local_date = (NOW() AT TIME ZONE 'Africa/Lagos')::date
	`, serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_provider_documents
		SET availability_refresh_after = '-infinity',
			availability_refresh_date = (NOW() AT TIME ZONE timezone)::date
		WHERE client_id = $1
	`, providerID); err != nil {
		t.Fatal(err)
	}
	maintenanceWorker := NewMarketplaceDiscoveryWorker(
		NewRepository(pool), nil, MarketplaceDiscoveryWorkerConfig{BatchSize: 1},
	)
	if enqueued, err := maintenanceWorker.AdvanceAvailabilityHorizon(ctx); err != nil || enqueued < 1 {
		t.Fatalf("advance expired current-day availability enqueued=%d error=%v", enqueued, err)
	}
	processMarketplaceProjectionProvider(t, ctx, pool, providerID)
	var expiredCandidates int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM marketplace_service_availability_days
		WHERE service_id = $1 AND has_available_slot AND first_available_at < NOW()
	`, serviceID).Scan(&expiredCandidates); err != nil {
		t.Fatal(err)
	}
	if expiredCandidates != 0 {
		var firstAvailable, projectedAt time.Time
		var requestedRevision, appliedRevision int64
		if err := pool.QueryRow(ctx, `
			SELECT day.first_available_at, day.projected_at, job.requested_revision, job.applied_revision
			FROM marketplace_service_availability_days day
			JOIN marketplace_discovery_jobs job ON job.client_id = day.provider_id
			WHERE day.service_id = $1 AND day.has_available_slot AND day.first_available_at < NOW()
			LIMIT 1
		`, serviceID).Scan(&firstAvailable, &projectedAt, &requestedRevision, &appliedRevision); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("expired projected candidates=%d first=%s projected=%s revisions=%d/%d",
			expiredCandidates, firstAvailable, projectedAt, requestedRevision, appliedRevision)
	}

	assertMarketplaceCategoryCountMatchesDocuments(t, ctx, pool, creativeMarketplaceCategoryID)

	if _, err := pool.Exec(ctx, `DELETE FROM clients WHERE id = $1`, providerID); err != nil {
		t.Fatal(err)
	}
	var deletionPending bool
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision > applied_revision
		FROM marketplace_discovery_jobs WHERE client_id = $1
	`, providerID).Scan(&deletionPending); err != nil {
		t.Fatal(err)
	}
	if !deletionPending {
		t.Fatal("provider deletion did not leave a count-refresh tombstone")
	}
	processMarketplaceProjectionProvider(t, ctx, pool, providerID)
	var tombstones int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM marketplace_discovery_jobs WHERE client_id = $1`, providerID).Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if tombstones != 0 {
		t.Fatalf("processed deletion tombstones = %d, want 0", tombstones)
	}
	assertMarketplaceCategoryCountMatchesDocuments(t, ctx, pool, creativeMarketplaceCategoryID)
}

func TestMarketplaceDiscoveryLeaseDoesNotBlockSourceMutation(t *testing.T) {
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
	providerID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_discovery_jobs SET available_at = '-infinity' WHERE client_id = $1
	`, providerID); err != nil {
		t.Fatal(err)
	}
	worker := NewMarketplaceDiscoveryWorker(NewRepository(pool), nil, MarketplaceDiscoveryWorkerConfig{BatchSize: 1})
	job, claimed, err := worker.claimMarketplaceDiscoveryJob(ctx)
	if err != nil || !claimed || job.ClientID != providerID {
		t.Fatalf("claimed job=%+v claimed=%t error=%v", job, claimed, err)
	}
	mutationContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := pool.Exec(mutationContext, `
		UPDATE client_profiles SET headline = 'Mutation while projection is leased' WHERE client_id = $1
	`, providerID); err != nil {
		t.Fatalf("source mutation waited on the projection lease: %v", err)
	}
	worker.releaseMarketplaceDiscoveryLease(ctx, job)
}

func TestMarketplaceDiscoveryCountRefreshCoalescesCompletedJobs(t *testing.T) {
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
	firstProviderID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	secondProviderID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_discovery_jobs SET available_at = '-infinity'
		WHERE client_id = ANY($1::uuid[])
	`, []uuid.UUID{firstProviderID, secondProviderID}); err != nil {
		t.Fatal(err)
	}
	worker := NewMarketplaceDiscoveryWorker(NewRepository(pool), nil, MarketplaceDiscoveryWorkerConfig{BatchSize: 2})
	processed, err := worker.ProcessBatch(ctx)
	if err != nil || processed != 2 {
		t.Fatalf("processed count-affecting jobs=%d error=%v, want 2", processed, err)
	}
	var countJobRows int
	var revisionGap int64
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*), MAX(requested_revision - applied_revision)
		FROM marketplace_discovery_count_jobs
	`).Scan(&countJobRows, &revisionGap); err != nil {
		t.Fatal(err)
	}
	if countJobRows != 1 || revisionGap < 2 {
		t.Fatalf("count job rows=%d revision_gap=%d, want one coalesced job covering both projections", countJobRows, revisionGap)
	}
	if err := worker.FlushMarketplaceDiscoveryCounts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision - applied_revision FROM marketplace_discovery_count_jobs WHERE singleton
	`).Scan(&revisionGap); err != nil {
		t.Fatal(err)
	}
	if revisionGap != 0 {
		t.Fatalf("count refresh revision gap=%d after flush, want zero", revisionGap)
	}
}

func insertMarketplaceProjectionRegions(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	countryID, stateID, lgaID := uuid.New(), uuid.New(), uuid.New()
	suffix := strings.ToUpper(strings.ReplaceAll(countryID.String()[:8], "-", ""))
	if _, err := pool.Exec(ctx, `
		INSERT INTO administrative_regions (
			id, parent_id, country_code, level, code, slug, name, boundary,
			source, source_version, source_feature_id, source_license
		) VALUES
			($1,NULL,'NG','country',$4,$5,'Projection country',NULL,'test','1',$4,'test'),
			($2,$1,'NG','state',$6,$7,'Projection state',
			 ST_Multi(ST_GeomFromText('POLYGON((3 6,4 6,4 7,3 7,3 6))',4326)),'test','1',$6,'test'),
			($3,$2,'NG','lga',$8,$9,'Projection LGA',
			 ST_Multi(ST_GeomFromText('POLYGON((3.2 6.2,3.8 6.2,3.8 6.8,3.2 6.8,3.2 6.2))',4326)),'test','1',$8,'test')
	`, countryID, stateID, lgaID,
		"T"+suffix, "projection-country-"+strings.ToLower(suffix),
		"S"+suffix, "projection-state-"+strings.ToLower(suffix),
		"L"+suffix, "projection-lga-"+strings.ToLower(suffix)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, id := range []uuid.UUID{lgaID, stateID, countryID} {
			if _, err := pool.Exec(ctx, `DELETE FROM administrative_regions WHERE id = $1`, id); err != nil {
				t.Errorf("delete projection region %s: %v", id, err)
			}
		}
	})
	return countryID, stateID, lgaID
}

func processMarketplaceProjectionProvider(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	providerID uuid.UUID,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_discovery_jobs SET available_at = '-infinity' WHERE client_id = $1
	`, providerID); err != nil {
		t.Fatal(err)
	}
	worker := NewMarketplaceDiscoveryWorker(
		NewRepository(pool), nil, MarketplaceDiscoveryWorkerConfig{BatchSize: 1},
	)
	processed, err := worker.ProcessBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if processed != 1 {
		t.Fatalf("processed discovery jobs = %d, want 1", processed)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_discovery_count_jobs SET available_at = '-infinity'
		WHERE requested_revision > applied_revision
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.processMarketplaceDiscoveryCounts(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertMarketplaceCategoryCountMatchesDocuments(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	categoryID uuid.UUID,
) {
	t.Helper()
	var maintained, actual int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT provider_count FROM marketplace_discovery_counts
			WHERE dimension = 'category' AND dimension_id = $1
		), 0), (
			SELECT COUNT(DISTINCT provider.client_id)::int
			FROM marketplace_provider_documents provider
			JOIN marketplace_service_documents service ON service.provider_id = provider.client_id
			WHERE provider.category_id = $1
		)
	`, categoryID).Scan(&maintained, &actual); err != nil {
		t.Fatal(err)
	}
	if maintained != actual {
		t.Fatalf("maintained category count = %d, document count = %d", maintained, actual)
	}
}
