package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ListBusinessLocations(ctx context.Context, clientID uuid.UUID) ([]BusinessLocationItem, error) {
	rows, err := r.db.Query(ctx, businessLocationSelect+`
		WHERE location.client_id = $1
		ORDER BY location.is_active DESC, location.is_primary DESC, location.created_at ASC
	`, clientID)
	if err != nil {
		return nil, fmt.Errorf("list business locations: %w", err)
	}
	defer rows.Close()

	items := make([]BusinessLocationItem, 0)
	for rows.Next() {
		item, err := scanBusinessLocation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate business locations: %w", err)
	}
	return items, nil
}

const businessLocationSelect = `
		SELECT
			location.id,
			location.label,
			location.formatted_address,
			COALESCE(location.provider_place_id, ''),
			location.latitude::double precision,
			location.longitude::double precision,
			location.address_source,
			location.resolution_status,
			COALESCE(location.country_code, ''),
			COALESCE(location.state_region_id::text, ''),
			COALESCE(state.name, ''),
			COALESCE(location.lga_region_id::text, ''),
			COALESCE(lga.name, ''),
			location.locality,
			location.timezone,
			location.is_primary,
			location.is_active
		FROM business_locations location
		LEFT JOIN administrative_regions state ON state.id = location.state_region_id
		LEFT JOIN administrative_regions lga ON lga.id = location.lga_region_id
`

func (r *Repository) CreateBusinessLocation(ctx context.Context, clientID uuid.UUID, input UpsertBusinessLocationInput) (BusinessLocationItem, error) {
	normalized, err := normalizeBusinessLocationInput(input)
	if err != nil {
		return BusinessLocationItem{}, err
	}
	normalized, err = r.enrichBusinessLocation(ctx, normalized)
	if err != nil {
		return BusinessLocationItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BusinessLocationItem{}, fmt.Errorf("begin create business location: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := enforceProviderCollectionLimit(
		ctx,
		tx,
		clientID,
		`SELECT COUNT(*) FROM business_locations WHERE client_id = $1 AND is_active`,
		MaxProviderBusinessLocations,
		ErrBusinessLocationLimitReached,
	); err != nil {
		return BusinessLocationItem{}, err
	}

	var hasActiveLocation bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM business_locations WHERE client_id = $1 AND is_active
		)
	`, clientID).Scan(&hasActiveLocation); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("check existing business locations: %w", err)
	}
	if !hasActiveLocation {
		normalized.IsPrimary = true
	}
	if normalized.IsPrimary {
		if _, err := tx.Exec(ctx, `
			UPDATE business_locations
			SET is_primary = FALSE, updated_at = NOW()
			WHERE client_id = $1 AND is_primary
		`, clientID); err != nil {
			return BusinessLocationItem{}, fmt.Errorf("clear primary business location: %w", err)
		}
	}

	id := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO business_locations (
			id, client_id, label, formatted_address, provider_place_id,
			latitude, longitude, address_source, resolution_status, timezone,
			country_code, state_region_id, lga_region_id, locality,
			is_primary, is_active, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,TRUE,NOW(),NOW())
	`, id, clientID, normalized.Label, normalized.FormattedAddress,
		nullIfBlank(normalized.ProviderPlaceID), normalized.Latitude, normalized.Longitude,
		normalized.AddressSource, normalized.ResolutionStatus, normalized.Timezone,
		nullIfBlank(normalized.CountryCode), normalized.StateRegionID, normalized.LGARegionID,
		normalized.Locality, normalized.IsPrimary); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("create business location: %w", err)
	}
	item, err := queryBusinessLocation(ctx, tx, businessLocationSelect+`
		WHERE location.client_id = $1 AND location.id = $2
	`, clientID, id)
	if err != nil {
		return BusinessLocationItem{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("commit create business location: %w", err)
	}
	return item, nil
}

func (r *Repository) UpdateBusinessLocation(ctx context.Context, clientID, locationID uuid.UUID, input UpsertBusinessLocationInput) (BusinessLocationItem, error) {
	normalized, err := normalizeBusinessLocationInput(input)
	if err != nil {
		return BusinessLocationItem{}, err
	}
	normalized, err = r.enrichBusinessLocation(ctx, normalized)
	if err != nil {
		return BusinessLocationItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BusinessLocationItem{}, fmt.Errorf("begin update business location: %w", err)
	}
	defer tx.Rollback(ctx)

	var currentPrimary bool
	if err := tx.QueryRow(ctx, `
		SELECT is_primary
		FROM business_locations
		WHERE client_id = $1 AND id = $2 AND is_active
		FOR UPDATE
	`, clientID, locationID).Scan(&currentPrimary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BusinessLocationItem{}, ErrNotFound
		}
		return BusinessLocationItem{}, fmt.Errorf("lock business location: %w", err)
	}
	if currentPrimary && !normalized.IsPrimary {
		return BusinessLocationItem{}, fmt.Errorf("select another primary location before demoting this one")
	}
	if normalized.IsPrimary {
		if _, err := tx.Exec(ctx, `
			UPDATE business_locations
			SET is_primary = FALSE, updated_at = NOW()
			WHERE client_id = $1 AND id <> $2 AND is_primary
		`, clientID, locationID); err != nil {
			return BusinessLocationItem{}, fmt.Errorf("replace primary business location: %w", err)
		}
	}

	commandTag, err := tx.Exec(ctx, `
		UPDATE business_locations
		SET
			label = $3,
			formatted_address = $4,
			provider_place_id = $5,
			latitude = $6,
			longitude = $7,
			address_source = $8,
			resolution_status = $9,
			timezone = $10,
			is_primary = $11,
			country_code = $12,
			state_region_id = $13,
			lga_region_id = $14,
			locality = $15,
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2 AND is_active
	`, clientID, locationID, normalized.Label, normalized.FormattedAddress,
		nullIfBlank(normalized.ProviderPlaceID), normalized.Latitude, normalized.Longitude,
		normalized.AddressSource, normalized.ResolutionStatus, normalized.Timezone,
		normalized.IsPrimary, nullIfBlank(normalized.CountryCode), normalized.StateRegionID,
		normalized.LGARegionID, normalized.Locality)
	if err != nil {
		return BusinessLocationItem{}, fmt.Errorf("update business location: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return BusinessLocationItem{}, ErrNotFound
	}
	item, err := queryBusinessLocation(ctx, tx, businessLocationSelect+`
		WHERE location.client_id = $1 AND location.id = $2
	`, clientID, locationID)
	if err != nil {
		return BusinessLocationItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("commit update business location: %w", err)
	}
	return item, nil
}

func (r *Repository) ArchiveBusinessLocation(ctx context.Context, clientID, locationID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin archive business location: %w", err)
	}
	defer tx.Rollback(ctx)

	var isPrimary bool
	if err := tx.QueryRow(ctx, `
		SELECT is_primary
		FROM business_locations
		WHERE client_id = $1 AND id = $2 AND is_active
		FOR UPDATE
	`, clientID, locationID).Scan(&isPrimary); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock business location: %w", err)
	}

	var publishedReferences int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM services
		WHERE client_id = $1 AND provider_location_id = $2 AND status = 'published'
	`, clientID, locationID).Scan(&publishedReferences); err != nil {
		return fmt.Errorf("check business location usage: %w", err)
	}
	if publishedReferences > 0 {
		return ErrLocationInUse
	}

	if _, err := tx.Exec(ctx, `
		UPDATE business_locations
		SET is_active = FALSE, is_primary = FALSE, updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`, clientID, locationID); err != nil {
		return fmt.Errorf("archive business location: %w", err)
	}

	if isPrimary {
		if _, err := tx.Exec(ctx, `
			UPDATE business_locations
			SET is_primary = TRUE, updated_at = NOW()
			WHERE id = (
				SELECT id
				FROM business_locations
				WHERE client_id = $1 AND is_active
				ORDER BY created_at ASC
				LIMIT 1
			)
		`, clientID); err != nil {
			return fmt.Errorf("promote replacement business location: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit archive business location: %w", err)
	}
	return nil
}

type normalizedBusinessLocationInput struct {
	Label            string
	FormattedAddress string
	ProviderPlaceID  string
	Latitude         *float64
	Longitude        *float64
	AddressSource    string
	ResolutionStatus string
	CountryCode      string
	StateRegionID    *uuid.UUID
	LGARegionID      *uuid.UUID
	Locality         string
	Timezone         string
	IsPrimary        bool
}

func (r *Repository) enrichBusinessLocation(ctx context.Context, input normalizedBusinessLocationInput) (normalizedBusinessLocationInput, error) {
	var resolved resolvedAddress
	var err error
	if input.AddressSource == "google_place" && input.ProviderPlaceID != "" {
		if r.googleMapsAPIKey == "" {
			return input, fmt.Errorf("map location resolution is unavailable")
		}
		resolved, err = r.googlePlaceDetails(ctx, input.ProviderPlaceID)
		if err != nil {
			return input, err
		}
	} else if input.Latitude == nil && r.googleMapsAPIKey != "" {
		resolved, err = r.googleGeocodeAddress(ctx, input.FormattedAddress)
		if err != nil {
			return input, nil
		}
	} else if input.AddressSource == "current_location" && input.Latitude != nil && r.googleMapsAPIKey != "" {
		resolved, err = r.googleReverseGeocode(ctx, *input.Latitude, *input.Longitude)
		if err != nil {
			resolved = resolvedAddress{}
		}
	}
	if resolved.FormattedAddress != "" {
		input.FormattedAddress = resolved.FormattedAddress
		input.Latitude = &resolved.Latitude
		input.Longitude = &resolved.Longitude
		input.CountryCode = strings.ToUpper(resolved.CountryCode)
		input.Locality = resolved.Locality
		input.ResolutionStatus = "coordinates_resolved"
	}
	if input.Latitude == nil || input.Longitude == nil {
		return input, nil
	}
	assignment, err := r.assignAdministrativeRegions(ctx, *input.Latitude, *input.Longitude, input.Locality)
	if err != nil {
		return input, err
	}
	if assignment.CountryCode != "" {
		input.CountryCode = assignment.CountryCode
	}
	input.StateRegionID = assignment.StateRegionID
	input.LGARegionID = assignment.LGARegionID
	input.Locality = assignment.Locality
	return input, nil
}

func normalizeBusinessLocationInput(input UpsertBusinessLocationInput) (normalizedBusinessLocationInput, error) {
	label := strings.TrimSpace(input.Label)
	address := strings.TrimSpace(input.FormattedAddress)
	timezone := strings.TrimSpace(input.Timezone)
	if label == "" || address == "" || timezone == "" {
		return normalizedBusinessLocationInput{}, fmt.Errorf("label, formatted_address, and timezone are required")
	}
	if (input.Latitude == nil) != (input.Longitude == nil) {
		return normalizedBusinessLocationInput{}, fmt.Errorf("latitude and longitude must be provided together")
	}
	if input.Latitude != nil && (*input.Latitude < -90 || *input.Latitude > 90) {
		return normalizedBusinessLocationInput{}, fmt.Errorf("latitude is outside its valid range")
	}
	if input.Longitude != nil && (*input.Longitude < -180 || *input.Longitude > 180) {
		return normalizedBusinessLocationInput{}, fmt.Errorf("longitude is outside its valid range")
	}

	source := strings.TrimSpace(input.AddressSource)
	if source == "" {
		source = "manual"
	}
	if source != "manual" && source != "google_place" && source != "current_location" {
		return normalizedBusinessLocationInput{}, fmt.Errorf("invalid address_source")
	}
	resolutionStatus := "text_only"
	if input.Latitude != nil {
		resolutionStatus = "coordinates_resolved"
	}

	return normalizedBusinessLocationInput{
		Label:            label,
		FormattedAddress: address,
		ProviderPlaceID:  strings.TrimSpace(input.ProviderPlaceID),
		Latitude:         input.Latitude,
		Longitude:        input.Longitude,
		AddressSource:    source,
		ResolutionStatus: resolutionStatus,
		Timezone:         timezone,
		IsPrimary:        input.IsPrimary,
	}, nil
}

type businessLocationRow interface {
	Scan(dest ...any) error
}

func queryBusinessLocation(ctx context.Context, queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, query string, args ...any) (BusinessLocationItem, error) {
	item, err := scanBusinessLocation(queryer.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return BusinessLocationItem{}, ErrNotFound
	}
	return item, err
}

func scanBusinessLocation(row businessLocationRow) (BusinessLocationItem, error) {
	var item BusinessLocationItem
	var id uuid.UUID
	if err := row.Scan(
		&id,
		&item.Label,
		&item.FormattedAddress,
		&item.ProviderPlaceID,
		&item.Latitude,
		&item.Longitude,
		&item.AddressSource,
		&item.ResolutionStatus,
		&item.CountryCode,
		&item.StateRegionID,
		&item.StateName,
		&item.LGARegionID,
		&item.LGAName,
		&item.Locality,
		&item.Timezone,
		&item.IsPrimary,
		&item.IsActive,
	); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("scan business location: %w", err)
	}
	item.ID = id.String()
	return item, nil
}
