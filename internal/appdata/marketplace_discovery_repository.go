package appdata

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (r *Repository) SearchMarketplaceProviders(
	ctx context.Context,
	input MarketplaceProviderSearchInput,
) (MarketplaceProviderSearchResponse, error) {
	locationContext, visitorPoint, err := r.validateMarketplaceSearchScope(
		ctx,
		input.LocationToken,
		input.StateID,
		input.LGAID,
		input.CategoryID,
		input.RadiusMeters,
	)
	if err != nil {
		return MarketplaceProviderSearchResponse{}, err
	}
	if input.LGAID != nil && input.StateID == nil {
		var parentID uuid.UUID
		if err := r.db.QueryRow(ctx, `
			SELECT parent_id FROM administrative_regions
			WHERE id = $1 AND level = 'lga'
		`, input.LGAID).Scan(&parentID); err != nil {
			return MarketplaceProviderSearchResponse{}, ErrMarketplaceRegion
		}
		input.StateID = &parentID
	}
	if err := validateMarketplaceAvailabilityDate(input.AvailableOn); err != nil {
		return MarketplaceProviderSearchResponse{}, err
	}
	var visitorLongitude, visitorLatitude *float64
	if visitorPoint != nil {
		visitorLongitude = &visitorPoint.Longitude
		visitorLatitude = &visitorPoint.Latitude
	}
	var cursorPrimary, cursorSecondary, cursorTertiary any
	var cursorProviderID any
	if input.Cursor != nil {
		providerID, err := uuid.Parse(input.Cursor.ProviderID)
		if err != nil || !validMarketplaceSortKey(input.Cursor.Primary) ||
			!validMarketplaceSortKey(input.Cursor.Secondary) ||
			!validMarketplaceSortKey(input.Cursor.Tertiary) {
			return MarketplaceProviderSearchResponse{}, ErrInvalidKeysetCursor
		}
		cursorPrimary = input.Cursor.Primary
		cursorSecondary = input.Cursor.Secondary
		cursorTertiary = input.Cursor.Tertiary
		cursorProviderID = providerID.String()
	}

	rows, err := r.db.Query(ctx, `
		WITH visitor AS (
			SELECT CASE
				WHEN $1::double precision IS NULL OR $2::double precision IS NULL THEN NULL::geography
				ELSE ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography
			END AS geog
		), candidate_services AS (
			SELECT provider.client_id AS provider_id,
				provider.handle_slug, provider.business_name, provider.headline,
				provider.category_id, provider.category_name,
				provider.avatar_url, provider.hero_image_url, provider.verified,
				provider.review_rating, provider.review_count,
				provider.completed_bookings, provider.location_visibility,
				provider.document_revision AS provider_document_revision,
				service.service_id, service.slug AS service_slug, service.title AS service_title,
				service.duration_minutes, service.price_amount_minor, service.currency_code,
				service.fulfillment_mode, service.location_label,
				service.public_latitude, service.public_longitude, service.map_precision,
				service.sort_order, service.document_revision AS service_document_revision,
				availability.first_available_at AS next_available_at,
				COALESCE(availability.document_revision, 0) AS availability_document_revision,
				CASE WHEN visitor.geog IS NULL OR service.public_geog IS NULL THEN NULL
					ELSE ST_Distance(visitor.geog, service.public_geog) END AS public_distance_meters
			FROM marketplace_provider_documents provider
			JOIN marketplace_service_documents service ON service.provider_id = provider.client_id
			LEFT JOIN visitor ON true
			LEFT JOIN LATERAL (
				SELECT day.first_available_at, day.document_revision
				FROM marketplace_service_availability_days day
					WHERE day.service_id = service.service_id
					  AND day.has_available_slot
					  AND day.first_available_at >= NOW()
				  AND ($12::date IS NULL OR day.local_date = $12)
				ORDER BY day.local_date, day.first_available_at
				LIMIT 1
			) availability ON true
			WHERE ($3::uuid IS NULL OR service.fulfillment_mode = 'virtual' OR service.state_region_id = $3)
			  AND ($4::uuid IS NULL OR service.fulfillment_mode = 'virtual' OR service.lga_region_id = $4)
			  AND ($5::uuid IS NULL OR provider.category_id = $5)
			  AND provider.review_rating >= $8
			  AND ($9::bigint IS NULL OR service.price_amount_minor >= $9)
			  AND ($10::bigint IS NULL OR service.price_amount_minor <= $10)
			  AND ($11 = '' OR service.fulfillment_mode = $11)
			  AND ($12::date IS NULL OR availability.first_available_at IS NOT NULL)
			  AND ($7 = '' OR
				provider.search_vector @@ websearch_to_tsquery('simple', $7) OR
				service.search_vector @@ websearch_to_tsquery('simple', $7))
			  AND ($6 = 0 OR service.fulfillment_mode = 'virtual' OR (
				visitor.geog IS NOT NULL AND (
					(service.fulfillment_mode = 'customer_location' AND service.internal_geog IS NOT NULL AND
					 ST_DWithin(visitor.geog, service.internal_geog, COALESCE(service.max_travel_distance_meters, $6))) OR
					(service.fulfillment_mode <> 'customer_location' AND service.public_geog IS NOT NULL AND
					 ST_DWithin(visitor.geog, service.public_geog, $6))
				)
			  ))
		), ranked_services AS (
			SELECT candidate.*,
				ROW_NUMBER() OVER (
					PARTITION BY candidate.provider_id
					ORDER BY candidate.public_distance_meters NULLS LAST,
						candidate.price_amount_minor, candidate.sort_order, candidate.service_id
				) AS service_rank
			FROM candidate_services candidate
		), provider_results AS (
			SELECT ranked.*,
				CASE $13
					WHEN 'rating' THEN -ranked.review_rating::numeric
					WHEN 'price' THEN ranked.price_amount_minor::numeric
					ELSE COALESCE(ROUND(ranked.public_distance_meters), 1e12)::numeric
				END AS primary_sort,
				CASE $13
					WHEN 'rating' THEN -ranked.review_count::numeric
					ELSE -ranked.review_rating::numeric
				END AS secondary_sort,
				CASE $13
					WHEN 'rating' THEN COALESCE(ROUND(ranked.public_distance_meters), 1e12)::numeric
					WHEN 'price' THEN COALESCE(ROUND(ranked.public_distance_meters), 1e12)::numeric
					ELSE -ranked.review_count::numeric
				END AS tertiary_sort
			FROM ranked_services ranked
			WHERE ranked.service_rank = 1
		)
		SELECT result.provider_id, handle_slug, business_name, headline,
			category_id, category_name, avatar_url, hero_image_url,
			verified, review_rating, review_count, location_label,
			CASE
				WHEN public_distance_meters IS NULL THEN NULL
				WHEN location_visibility = 'approximate'
					THEN (ROUND(public_distance_meters / 1000) * 1000)::int
				ELSE ROUND(public_distance_meters)::int
			END AS distance_meters,
			service_id, service_slug, service_title, duration_minutes,
			price_amount_minor, currency_code, fulfillment_mode, completed_bookings,
			next_available_at, public_latitude::double precision, public_longitude::double precision,
			map_precision, provider_document_revision, service_document_revision,
			availability_document_revision,
			ARRAY_REMOVE(ARRAY[
				CASE WHEN verified THEN 'Verified' END,
				CASE fulfillment_mode
					WHEN 'virtual' THEN 'Online'
					WHEN 'customer_location' THEN 'Comes to you'
				END
			], NULL),
			result.primary_sort::text, result.secondary_sort::text, result.tertiary_sort::text
		FROM provider_results result
		WHERE $14::numeric IS NULL
		   OR result.primary_sort > $14::numeric
		   OR (result.primary_sort = $14::numeric AND result.secondary_sort > $15::numeric)
		   OR (result.primary_sort = $14::numeric AND result.secondary_sort = $15::numeric
		       AND result.tertiary_sort > $16::numeric)
		   OR (result.primary_sort = $14::numeric AND result.secondary_sort = $15::numeric
		       AND result.tertiary_sort = $16::numeric AND result.provider_id > $17::uuid)
		ORDER BY result.primary_sort, result.secondary_sort, result.tertiary_sort, result.provider_id
		LIMIT $18
	`,
		visitorLongitude, visitorLatitude, input.StateID, input.LGAID, input.CategoryID,
		input.RadiusMeters, strings.TrimSpace(input.Query), input.MinimumRating,
		input.MinimumPriceMinor, input.MaximumPriceMinor, input.FulfillmentMode,
		input.AvailableOn, input.Sort,
		cursorPrimary, cursorSecondary, cursorTertiary, cursorProviderID, input.Limit+1,
	)
	if err != nil {
		return MarketplaceProviderSearchResponse{}, fmt.Errorf("search marketplace discovery documents: %w", err)
	}
	defer rows.Close()

	response := MarketplaceProviderSearchResponse{
		Items: make([]MarketplaceProvider, 0, input.Limit), Location: locationContext,
	}
	cursors := make([]MarketplaceProviderSearchCursor, 0, input.Limit+1)
	for rows.Next() {
		var item MarketplaceProvider
		var latitude, longitude *float64
		var mapPrecision string
		var cursor MarketplaceProviderSearchCursor
		if err := rows.Scan(
			&item.ID, &item.HandleSlug, &item.BusinessName, &item.Headline,
			&item.CategoryID, &item.CategoryName, &item.AvatarURL, &item.HeroImageURL,
			&item.Verified, &item.ReviewRating, &item.ReviewCount, &item.LocationLabel,
			&item.DistanceMeters, &item.ServiceID, &item.ServiceSlug, &item.ServiceTitle,
			&item.DurationMinutes, &item.PriceAmountMinor, &item.CurrencyCode, &item.FulfillmentMode,
			&item.CompletedBookings, &item.NextAvailableAt, &latitude, &longitude, &mapPrecision,
			&item.providerDocumentRevision, &item.serviceDocumentRevision,
			&item.availabilityDocumentRevision, &item.Badges,
			&cursor.Primary, &cursor.Secondary, &cursor.Tertiary,
		); err != nil {
			return MarketplaceProviderSearchResponse{}, fmt.Errorf("scan marketplace discovery result: %w", err)
		}
		if latitude != nil && longitude != nil {
			item.MapPoint = &MarketplaceMapPoint{
				Latitude: *latitude, Longitude: *longitude, Precision: mapPrecision,
			}
		}
		cursor.ProviderID = item.ID
		response.Items = append(response.Items, item)
		cursors = append(cursors, cursor)
	}
	if err := rows.Err(); err != nil {
		return MarketplaceProviderSearchResponse{}, fmt.Errorf("iterate marketplace discovery results: %w", err)
	}
	if len(response.Items) > input.Limit {
		response.Items = response.Items[:input.Limit]
		cursors = cursors[:input.Limit]
		cursor := cursors[len(cursors)-1]
		response.nextCursor = &cursor
	}
	return response, nil
}

func validateMarketplaceAvailabilityDate(value *time.Time) error {
	if value == nil {
		return nil
	}
	location, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		return fmt.Errorf("load marketplace timezone: %w", err)
	}
	today := time.Now().In(location)
	startsOn := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	requested := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, location)
	if requested.Before(startsOn) || requested.After(startsOn.AddDate(0, 0, 30)) {
		return ErrMarketplaceAvailabilityRange
	}
	return nil
}

func validMarketplaceSortKey(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	index := 0
	if value[0] == '-' {
		index++
		if index == len(value) {
			return false
		}
	}
	digitsBeforeDecimal := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		digitsBeforeDecimal++
		index++
	}
	if digitsBeforeDecimal == 0 {
		return false
	}
	if index == len(value) {
		return true
	}
	if value[index] != '.' {
		return false
	}
	index++
	digitsAfterDecimal := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		digitsAfterDecimal++
		index++
	}
	return digitsAfterDecimal > 0 && index == len(value)
}

func marketplaceSearchFingerprint(input MarketplaceProviderSearchInput) string {
	locationToken := strings.TrimSpace(input.LocationToken)
	locationHash := ""
	if locationToken != "" {
		sum := sha256.Sum256([]byte(locationToken))
		locationHash = fmt.Sprintf("%x", sum[:])
	}
	availableOn := ""
	if input.AvailableOn != nil {
		availableOn = input.AvailableOn.Format("2006-01-02")
	}
	return keysetFilterFingerprint(
		"marketplace-search-v1",
		input.Query,
		locationHash,
		marketplaceUUIDFingerprint(input.StateID),
		marketplaceUUIDFingerprint(input.LGAID),
		marketplaceUUIDFingerprint(input.CategoryID),
		strconv.Itoa(input.RadiusMeters),
		strconv.FormatFloat(input.MinimumRating, 'f', -1, 64),
		marketplaceInt64Fingerprint(input.MinimumPriceMinor),
		marketplaceInt64Fingerprint(input.MaximumPriceMinor),
		input.FulfillmentMode,
		availableOn,
		input.Sort,
	)
}

func marketplaceUUIDFingerprint(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func marketplaceInt64Fingerprint(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func (r *Repository) loadMarketplaceProjectedNextAvailability(
	ctx context.Context,
	items []MarketplaceProvider,
) error {
	serviceIDs := make([]uuid.UUID, 0, len(items))
	itemByService := make(map[uuid.UUID]int, len(items))
	for index := range items {
		serviceID, err := uuid.Parse(items[index].ServiceID)
		if err != nil {
			return fmt.Errorf("parse marketplace service ID: %w", err)
		}
		serviceIDs = append(serviceIDs, serviceID)
		itemByService[serviceID] = index
	}
	rows, err := r.db.Query(ctx, `
		SELECT service_id, MIN(first_available_at)
		FROM marketplace_service_availability_days
		WHERE service_id = ANY($1::uuid[]) AND has_available_slot
		  AND first_available_at >= NOW()
		GROUP BY service_id
	`, serviceIDs)
	if err != nil {
		return fmt.Errorf("load projected marketplace next availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var serviceID uuid.UUID
		var nextAvailableAt time.Time
		if err := rows.Scan(&serviceID, &nextAvailableAt); err != nil {
			return fmt.Errorf("scan projected marketplace next availability: %w", err)
		}
		if index, ok := itemByService[serviceID]; ok {
			items[index].NextAvailableAt = &nextAvailableAt
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate projected marketplace next availability: %w", err)
	}
	return nil
}
