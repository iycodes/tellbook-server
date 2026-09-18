package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ListMarketplaceCategories(ctx context.Context) ([]MarketplaceCategory, error) {
	rows, err := r.db.Query(ctx, `
		SELECT category.id, category.slug, category.name, category.description,
			category.image_url, COALESCE(discovery_count.provider_count, 0)
		FROM marketplace_categories category
		LEFT JOIN marketplace_discovery_counts discovery_count
			ON discovery_count.dimension = 'category'
			AND discovery_count.dimension_id = category.id
		WHERE category.is_active
		ORDER BY category.sort_order, category.name
	`)
	if err != nil {
		return nil, fmt.Errorf("list marketplace categories: %w", err)
	}
	defer rows.Close()
	items := make([]MarketplaceCategory, 0)
	for rows.Next() {
		var item MarketplaceCategory
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Description, &item.ImageURL, &item.ProviderCount); err != nil {
			return nil, fmt.Errorf("scan marketplace category: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ListMarketplaceRegions(ctx context.Context, level string, parentID *uuid.UUID) ([]MarketplaceRegion, error) {
	rows, err := r.db.Query(ctx, `
		SELECT region.id, COALESCE(region.parent_id::text, ''), region.level, region.slug, region.name,
			COALESCE(discovery_count.provider_count, 0)
		FROM administrative_regions region
		LEFT JOIN marketplace_discovery_counts discovery_count
			ON discovery_count.dimension = $1
			AND discovery_count.dimension_id = region.id
		WHERE region.country_code = 'NG' AND region.level = $1
		  AND ($2::uuid IS NULL OR region.parent_id = $2)
		ORDER BY region.name
	`, level, parentID)
	if err != nil {
		return nil, fmt.Errorf("list marketplace regions: %w", err)
	}
	defer rows.Close()
	items := make([]MarketplaceRegion, 0)
	for rows.Next() {
		var item MarketplaceRegion
		if err := rows.Scan(&item.ID, &item.ParentID, &item.Level, &item.Slug, &item.Name, &item.ProviderCount); err != nil {
			return nil, fmt.Errorf("scan marketplace region: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) GetMarketplaceHome(ctx context.Context, token string, stateID, lgaID, categoryID *uuid.UUID, limit int) (MarketplaceHomeResponse, error) {
	if categoryID != nil {
		var exists bool
		if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM marketplace_categories WHERE id = $1 AND is_active)`, categoryID).Scan(&exists); err != nil {
			return MarketplaceHomeResponse{}, fmt.Errorf("validate marketplace category: %w", err)
		}
		if !exists {
			return MarketplaceHomeResponse{}, ErrMarketplaceCategory
		}
	}
	if lgaID != nil {
		var parentID uuid.UUID
		if err := r.db.QueryRow(ctx, `SELECT parent_id FROM administrative_regions WHERE id = $1 AND level = 'lga'`, lgaID).Scan(&parentID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return MarketplaceHomeResponse{}, ErrMarketplaceRegion
			}
			return MarketplaceHomeResponse{}, fmt.Errorf("validate marketplace LGA: %w", err)
		}
		if stateID != nil && *stateID != parentID {
			return MarketplaceHomeResponse{}, ErrMarketplaceRegion
		}
		stateID = &parentID
	}
	if stateID != nil {
		var exists bool
		if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM administrative_regions WHERE id = $1 AND level = 'state' AND country_code = 'NG')`, stateID).Scan(&exists); err != nil {
			return MarketplaceHomeResponse{}, fmt.Errorf("validate marketplace state: %w", err)
		}
		if !exists {
			return MarketplaceHomeResponse{}, ErrMarketplaceRegion
		}
	}
	locationContext := MarketplaceLocationContext{Mode: "all", Label: "Nigeria"}
	radius := 0
	if strings.TrimSpace(token) != "" {
		resolved, err := r.loadResolvedLocation(ctx, token)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return MarketplaceHomeResponse{}, ErrNotFound
			}
			return MarketplaceHomeResponse{}, err
		}
		if resolved.Latitude == nil {
			return MarketplaceHomeResponse{}, fmt.Errorf("location token has no coordinates")
		}
		if !strings.EqualFold(resolved.CountryCode, "NG") {
			return MarketplaceHomeResponse{}, ErrMarketplaceCountry
		}
		locationContext = MarketplaceLocationContext{Mode: "nearby", Label: resolved.FormattedAddress, StateRegionID: uuidString(resolved.StateRegionID), LGARegionID: uuidString(resolved.LGARegionID), ResolutionStatus: resolved.ResolutionStatus}
		radius = 15000
	} else if lgaID != nil || stateID != nil {
		id := stateID
		locationContext.Mode = "region"
		if lgaID != nil {
			id = lgaID
			locationContext.LGARegionID = lgaID.String()
			locationContext.StateRegionID = stateID.String()
		} else {
			locationContext.StateRegionID = stateID.String()
		}
		if err := r.db.QueryRow(ctx, `SELECT name FROM administrative_regions WHERE id = $1`, id).Scan(&locationContext.Label); err != nil {
			return MarketplaceHomeResponse{}, ErrNotFound
		}
	}
	providers, err := r.listMarketplaceProviders(ctx, token, stateID, lgaID, categoryID, radius, limit)
	if err != nil {
		return MarketplaceHomeResponse{}, err
	}
	if token != "" && len(providers) == 0 {
		radius = 50000
		providers, err = r.listMarketplaceProviders(ctx, token, stateID, lgaID, categoryID, radius, limit)
		if err != nil {
			return MarketplaceHomeResponse{}, err
		}
	}
	locationContext.SearchRadiusM = radius
	return MarketplaceHomeResponse{Providers: providers, Location: locationContext}, nil
}

func (r *Repository) listMarketplaceProviders(ctx context.Context, token string, stateID, lgaID, categoryID *uuid.UUID, radius, limit int) ([]MarketplaceProvider, error) {
	rows, err := r.db.Query(ctx, `
		WITH visitor AS (
			SELECT geog FROM resolved_locations WHERE public_token = NULLIF($1, '') AND expires_at > NOW()
		)
		SELECT provider.client_id, provider.handle_slug, provider.business_name, provider.headline,
			provider.category_id, provider.category_name, provider.avatar_url, provider.hero_image_url,
			provider.verified, provider.review_rating, provider.review_count,
			service.location_label,
			CASE
				WHEN service.public_distance_meters IS NULL THEN NULL
				WHEN provider.location_visibility = 'approximate'
					THEN (ROUND(service.public_distance_meters / 1000) * 1000)::int
				ELSE ROUND(service.public_distance_meters)::int
			END,
			service.service_id, service.slug, service.title, service.duration_minutes,
			service.price_amount_minor, service.currency_code, service.fulfillment_mode,
			provider.completed_bookings,
			ARRAY_REMOVE(ARRAY[
				CASE WHEN provider.verified THEN 'Verified' END,
				CASE service.fulfillment_mode
					WHEN 'virtual' THEN 'Online'
					WHEN 'customer_location' THEN 'Comes to you'
				END
			], NULL),
			provider.document_revision,
			service.document_revision
		FROM marketplace_provider_documents provider
 JOIN client_profiles eligibility ON eligibility.client_id=provider.client_id AND NOT eligibility.platform_restricted
		LEFT JOIN visitor ON TRUE
		CROSS JOIN LATERAL (
			SELECT candidate.*,
				CASE WHEN visitor.geog IS NULL OR candidate.public_geog IS NULL THEN NULL
					ELSE ST_Distance(visitor.geog, candidate.public_geog) END AS public_distance_meters
			FROM marketplace_service_documents candidate
			WHERE candidate.provider_id = provider.client_id
			  AND ($2::uuid IS NULL OR candidate.fulfillment_mode = 'virtual' OR candidate.state_region_id = $2)
			  AND ($3::uuid IS NULL OR candidate.fulfillment_mode = 'virtual' OR candidate.lga_region_id = $3)
			  AND ($4 = 0 OR candidate.fulfillment_mode = 'virtual' OR
				(visitor.geog IS NOT NULL AND (
					(candidate.fulfillment_mode = 'customer_location' AND candidate.internal_geog IS NOT NULL AND
					 ST_DWithin(visitor.geog, candidate.internal_geog, COALESCE(candidate.max_travel_distance_meters, $4))) OR
					(candidate.fulfillment_mode <> 'customer_location' AND candidate.public_geog IS NOT NULL AND
					 ST_DWithin(visitor.geog, candidate.public_geog, $4))
				)))
			ORDER BY public_distance_meters NULLS LAST,
				candidate.price_amount_minor, candidate.sort_order, candidate.service_id LIMIT 1
		) service
		WHERE ($5::uuid IS NULL OR provider.category_id = $5)
		ORDER BY service.public_distance_meters NULLS LAST,
			provider.review_rating DESC, provider.review_count DESC, provider.client_id
		LIMIT $6
	`, strings.TrimSpace(token), stateID, lgaID, radius, categoryID, limit)
	if err != nil {
		return nil, fmt.Errorf("list marketplace providers: %w", err)
	}
	defer rows.Close()
	items := make([]MarketplaceProvider, 0)
	for rows.Next() {
		var item MarketplaceProvider
		if err := rows.Scan(&item.ID, &item.HandleSlug, &item.BusinessName, &item.Headline, &item.CategoryID, &item.CategoryName,
			&item.AvatarURL, &item.HeroImageURL, &item.Verified, &item.ReviewRating, &item.ReviewCount,
			&item.LocationLabel, &item.DistanceMeters, &item.ServiceID, &item.ServiceSlug, &item.ServiceTitle,
			&item.DurationMinutes, &item.PriceAmountMinor, &item.CurrencyCode, &item.FulfillmentMode,
			&item.CompletedBookings, &item.Badges, &item.providerDocumentRevision,
			&item.serviceDocumentRevision); err != nil {
			return nil, fmt.Errorf("scan marketplace provider: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type marketplaceSearchPoint struct {
	Latitude  float64
	Longitude float64
}

func (r *Repository) validateMarketplaceSearchScope(ctx context.Context, token string, stateID, lgaID, categoryID *uuid.UUID, radius int) (MarketplaceLocationContext, *marketplaceSearchPoint, error) {
	if categoryID != nil {
		var exists bool
		if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM marketplace_categories WHERE id=$1 AND is_active)`, categoryID).Scan(&exists); err != nil {
			return MarketplaceLocationContext{}, nil, fmt.Errorf("validate marketplace category: %w", err)
		}
		if !exists {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceCategory
		}
	}
	if lgaID != nil {
		var parentID uuid.UUID
		if err := r.db.QueryRow(ctx, `SELECT parent_id FROM administrative_regions WHERE id=$1 AND level='lga' AND country_code='NG'`, lgaID).Scan(&parentID); err != nil {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceRegion
		}
		if stateID != nil && *stateID != parentID {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceRegion
		}
		stateID = &parentID
	}
	if stateID != nil {
		var exists bool
		if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrative_regions WHERE id=$1 AND level='state' AND country_code='NG')`, stateID).Scan(&exists); err != nil {
			return MarketplaceLocationContext{}, nil, fmt.Errorf("validate marketplace state: %w", err)
		}
		if !exists {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceRegion
		}
	}

	location := MarketplaceLocationContext{Mode: "all", Label: "Nigeria"}
	if strings.TrimSpace(token) != "" {
		resolved, err := r.loadResolvedLocation(ctx, token)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return MarketplaceLocationContext{}, nil, ErrNotFound
			}
			return MarketplaceLocationContext{}, nil, err
		}
		if resolved.Latitude == nil || resolved.Longitude == nil {
			return MarketplaceLocationContext{}, nil, ErrNotFound
		}
		if !strings.EqualFold(resolved.CountryCode, "NG") {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceCountry
		}
		return MarketplaceLocationContext{
			Mode: "nearby", Label: resolved.FormattedAddress,
			StateRegionID: uuidString(resolved.StateRegionID), LGARegionID: uuidString(resolved.LGARegionID),
			SearchRadiusM: radius, ResolutionStatus: resolved.ResolutionStatus,
		}, &marketplaceSearchPoint{Latitude: *resolved.Latitude, Longitude: *resolved.Longitude}, nil
	}
	if lgaID != nil || stateID != nil {
		regionID := stateID
		location.Mode = "region"
		location.StateRegionID = stateID.String()
		if lgaID != nil {
			regionID = lgaID
			location.LGARegionID = lgaID.String()
		}
		if err := r.db.QueryRow(ctx, `SELECT name FROM administrative_regions WHERE id=$1`, regionID).Scan(&location.Label); err != nil {
			return MarketplaceLocationContext{}, nil, ErrMarketplaceRegion
		}
	}
	return location, nil, nil
}

func (r *Repository) GetMarketplaceProfileSettings(ctx context.Context, clientID uuid.UUID) (MarketplaceProfileSettings, error) {
	var settings MarketplaceProfileSettings
	if err := r.db.QueryRow(ctx, `SELECT marketplace_enabled, COALESCE(marketplace_category_id::text, ''), marketplace_location_visibility FROM client_profiles WHERE client_id = $1`, clientID).
		Scan(&settings.Enabled, &settings.CategoryID, &settings.LocationVisibility); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return settings, ErrNotFound
		}
		return settings, fmt.Errorf("get marketplace settings: %w", err)
	}
	categories, err := r.ListMarketplaceCategories(ctx)
	if err != nil {
		return settings, err
	}
	settings.Categories = categories
	settings.Readiness, err = r.marketplaceReadiness(ctx, clientID, settings.CategoryID)
	return settings, err
}

func (r *Repository) UpdateMarketplaceProfileSettings(ctx context.Context, clientID uuid.UUID, input UpdateMarketplaceProfileSettingsInput) (MarketplaceProfileSettings, error) {
	visibility := strings.TrimSpace(input.LocationVisibility)
	if visibility != "approximate" && visibility != "exact" {
		return MarketplaceProfileSettings{}, fmt.Errorf("location_visibility must be approximate or exact")
	}
	categoryID, err := uuid.Parse(strings.TrimSpace(input.CategoryID))
	if err != nil {
		return MarketplaceProfileSettings{}, fmt.Errorf("valid category_id is required")
	}
	var active bool
	if err := r.db.QueryRow(ctx, `SELECT is_active FROM marketplace_categories WHERE id = $1`, categoryID).Scan(&active); err != nil || !active {
		return MarketplaceProfileSettings{}, fmt.Errorf("category is unavailable")
	}
	readiness, err := r.marketplaceReadiness(ctx, clientID, categoryID.String())
	if err != nil {
		return MarketplaceProfileSettings{}, err
	}
	if input.Enabled && !readiness.Ready {
		return MarketplaceProfileSettings{}, ErrMarketplaceNotReady
	}
	command, err := r.db.Exec(ctx, `UPDATE client_profiles SET marketplace_enabled=$2, marketplace_category_id=$3, marketplace_location_visibility=$4, updated_at=NOW() WHERE client_id=$1`, clientID, input.Enabled, categoryID, visibility)
	if err != nil {
		return MarketplaceProfileSettings{}, fmt.Errorf("update marketplace settings: %w", err)
	}
	if command.RowsAffected() == 0 {
		return MarketplaceProfileSettings{}, ErrNotFound
	}
	return r.GetMarketplaceProfileSettings(ctx, clientID)
}

func (r *Repository) marketplaceReadiness(ctx context.Context, clientID uuid.UUID, categoryID string) (MarketplaceReadiness, error) {
	var marketConfigured, hasHandle, hasService, hasBookableSchedule bool
	err := r.db.QueryRow(ctx, `
		WITH eligible_service AS (
			SELECT service.*
			FROM services service
			LEFT JOIN business_locations location ON location.id=service.provider_location_id
			WHERE service.client_id=$1 AND service.status='published'
			  AND service.is_active AND NOT service.is_hidden
			  AND (service.fulfillment_mode='virtual' OR
				(location.is_active AND location.resolution_status='coordinates_resolved'))
		)
		SELECT market_configured_at IS NOT NULL, NULLIF(BTRIM(handle_slug), '') IS NOT NULL,
		EXISTS (SELECT 1 FROM eligible_service),
		EXISTS (
			SELECT 1 FROM eligible_service service
			WHERE (service.availability_mode='custom' AND EXISTS (
				SELECT 1 FROM service_availability_windows availability_window
				WHERE availability_window.service_id=service.id
				  AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
					service.prep_time_minutes+service.duration_minutes+service.buffer_time_minutes
			)) OR (service.availability_mode<>'custom' AND EXISTS (
				SELECT 1 FROM provider_availability_windows availability_window
				WHERE availability_window.client_id=service.client_id
				  AND EXTRACT(EPOCH FROM (availability_window.end_time-availability_window.start_time))/60 >=
					service.prep_time_minutes+service.duration_minutes+service.buffer_time_minutes
			))
		)
		FROM client_profiles profile WHERE client_id=$1
	`, clientID).Scan(&marketConfigured, &hasHandle, &hasService, &hasBookableSchedule)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MarketplaceReadiness{}, ErrNotFound
		}
		return MarketplaceReadiness{}, err
	}
	reasons := make([]string, 0)
	if !marketConfigured {
		reasons = append(reasons, "Configure your business country, currency, timezone, and locale.")
	}
	if strings.TrimSpace(categoryID) == "" {
		reasons = append(reasons, "Choose a marketplace category.")
	}
	if !hasHandle {
		reasons = append(reasons, "Set a public profile handle.")
	}
	if !hasService {
		reasons = append(reasons, "Publish a virtual service or a physical service with a resolved business location.")
	} else if !hasBookableSchedule {
		reasons = append(reasons, "Add availability hours long enough for at least one published service, including its preparation and buffer time.")
	}
	return MarketplaceReadiness{Ready: len(reasons) == 0, BlockingReasons: reasons}, nil
}
