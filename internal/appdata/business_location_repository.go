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
	rows, err := r.db.Query(ctx, `
		SELECT
			id,
			label,
			formatted_address,
			COALESCE(provider_place_id, ''),
			latitude::double precision,
			longitude::double precision,
			address_source,
			resolution_status,
			timezone,
			is_primary,
			is_active
		FROM business_locations
		WHERE client_id = $1
		ORDER BY is_active DESC, is_primary DESC, created_at ASC
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

func (r *Repository) CreateBusinessLocation(ctx context.Context, clientID uuid.UUID, input UpsertBusinessLocationInput) (BusinessLocationItem, error) {
	normalized, err := normalizeBusinessLocationInput(input)
	if err != nil {
		return BusinessLocationItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return BusinessLocationItem{}, fmt.Errorf("begin create business location: %w", err)
	}
	defer tx.Rollback(ctx)

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
	item, err := queryBusinessLocation(ctx, tx, `
		INSERT INTO business_locations (
			id, client_id, label, formatted_address, provider_place_id,
			latitude, longitude, address_source, resolution_status, timezone,
			is_primary, is_active, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,TRUE,NOW(),NOW())
		RETURNING
			id, label, formatted_address, COALESCE(provider_place_id, ''),
			latitude::double precision, longitude::double precision,
			address_source, resolution_status, timezone, is_primary, is_active
	`, id, clientID, normalized.Label, normalized.FormattedAddress,
		nullIfBlank(normalized.ProviderPlaceID), normalized.Latitude, normalized.Longitude,
		normalized.AddressSource, normalized.ResolutionStatus, normalized.Timezone,
		normalized.IsPrimary)
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

	item, err := queryBusinessLocation(ctx, tx, `
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
			updated_at = NOW()
		WHERE client_id = $1 AND id = $2 AND is_active
		RETURNING
			id, label, formatted_address, COALESCE(provider_place_id, ''),
			latitude::double precision, longitude::double precision,
			address_source, resolution_status, timezone, is_primary, is_active
	`, clientID, locationID, normalized.Label, normalized.FormattedAddress,
		nullIfBlank(normalized.ProviderPlaceID), normalized.Latitude, normalized.Longitude,
		normalized.AddressSource, normalized.ResolutionStatus, normalized.Timezone,
		normalized.IsPrimary)
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
	Timezone         string
	IsPrimary        bool
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
		&item.Timezone,
		&item.IsPrimary,
		&item.IsActive,
	); err != nil {
		return BusinessLocationItem{}, fmt.Errorf("scan business location: %w", err)
	}
	item.ID = id.String()
	return item, nil
}
