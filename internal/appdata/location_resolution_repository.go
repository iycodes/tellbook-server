package appdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/publictoken"

	"github.com/google/uuid"
)

type resolvedLocationRecord struct {
	ID               uuid.UUID
	FormattedAddress string
	ProviderPlaceID  string
	Latitude         *float64
	Longitude        *float64
	AddressSource    string
	ResolutionStatus string
	ExpiresAt        time.Time
}

func (r *Repository) ResolvePublicLocation(ctx context.Context, input ResolvePublicLocationInput) (ResolvedPublicLocationResponse, error) {
	source := strings.TrimSpace(input.Source)
	if source != "manual" && source != "google_place" && source != "current_location" {
		return ResolvedPublicLocationResponse{}, fmt.Errorf("source must be manual, google_place, or current_location")
	}

	record := resolvedLocationRecord{ID: uuid.New(), AddressSource: source}
	provider := "manual"
	switch source {
	case "manual":
		if strings.TrimSpace(input.ProviderPlaceID) != "" || input.Latitude != nil || input.Longitude != nil {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("manual location accepts only address")
		}
		record.FormattedAddress = strings.TrimSpace(input.Address)
		if record.FormattedAddress == "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("address is required")
		}
		if r.googleMapsAPIKey != "" {
			if address, latitude, longitude, err := r.googleGeocodeAddress(ctx, record.FormattedAddress); err == nil {
				record.FormattedAddress = address
				record.Latitude = &latitude
				record.Longitude = &longitude
				provider = "google"
			}
		}
	case "google_place":
		if strings.TrimSpace(input.Address) != "" || input.Latitude != nil || input.Longitude != nil {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("google_place location accepts only provider_place_id")
		}
		if r.googleMapsAPIKey == "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("map location resolution is unavailable")
		}
		record.ProviderPlaceID = strings.TrimSpace(input.ProviderPlaceID)
		if record.ProviderPlaceID == "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("provider_place_id is required")
		}
		address, latitude, longitude, err := r.googlePlaceDetails(ctx, record.ProviderPlaceID)
		if err != nil {
			return ResolvedPublicLocationResponse{}, err
		}
		record.FormattedAddress = address
		record.Latitude = &latitude
		record.Longitude = &longitude
		provider = "google"
	case "current_location":
		if strings.TrimSpace(input.Address) != "" || strings.TrimSpace(input.ProviderPlaceID) != "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("current_location accepts only coordinates")
		}
		if r.googleMapsAPIKey == "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("current location resolution is unavailable")
		}
		if input.Latitude == nil || input.Longitude == nil {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("latitude and longitude are required")
		}
		if *input.Latitude < -90 || *input.Latitude > 90 || *input.Longitude < -180 || *input.Longitude > 180 {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("coordinates are outside their valid range")
		}
		address, latitude, longitude, err := r.googleReverseGeocode(ctx, *input.Latitude, *input.Longitude)
		if err != nil {
			return ResolvedPublicLocationResponse{}, err
		}
		record.FormattedAddress = address
		record.Latitude = &latitude
		record.Longitude = &longitude
		provider = "google"
	}

	record.ResolutionStatus = "text_only"
	if record.Latitude != nil && record.Longitude != nil {
		record.ResolutionStatus = "coordinates_resolved"
	}
	record.ExpiresAt = time.Now().UTC().Add(30 * time.Minute)
	token, err := publictoken.New()
	if err != nil {
		return ResolvedPublicLocationResponse{}, fmt.Errorf("create location token: %w", err)
	}
	if _, err := r.db.Exec(ctx, `
		INSERT INTO resolved_locations (
			id, public_token, provider, provider_place_id, formatted_address,
			latitude, longitude, address_source, resolution_status, expires_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
	`, record.ID, token, provider, nullIfBlank(record.ProviderPlaceID), record.FormattedAddress,
		record.Latitude, record.Longitude, record.AddressSource, record.ResolutionStatus,
		record.ExpiresAt); err != nil {
		return ResolvedPublicLocationResponse{}, fmt.Errorf("store resolved location: %w", err)
	}

	return ResolvedPublicLocationResponse{
		LocationToken:    token,
		FormattedAddress: record.FormattedAddress,
		ResolutionStatus: record.ResolutionStatus,
		ExpiresAt:        record.ExpiresAt.Format(time.RFC3339),
	}, nil
}

func (r *Repository) loadResolvedLocation(ctx context.Context, token string) (resolvedLocationRecord, error) {
	var record resolvedLocationRecord
	if err := r.db.QueryRow(ctx, `
		SELECT id, formatted_address, COALESCE(provider_place_id, ''),
			latitude::double precision, longitude::double precision,
			address_source, resolution_status, expires_at
		FROM resolved_locations
		WHERE public_token = $1 AND expires_at > NOW()
	`, strings.TrimSpace(token)).Scan(
		&record.ID, &record.FormattedAddress, &record.ProviderPlaceID,
		&record.Latitude, &record.Longitude, &record.AddressSource,
		&record.ResolutionStatus, &record.ExpiresAt,
	); err != nil {
		return resolvedLocationRecord{}, fmt.Errorf("load location token: %w", err)
	}
	return record, nil
}

func (r *Repository) googlePlaceDetails(ctx context.Context, placeID string) (string, float64, float64, error) {
	requestURL := "https://places.googleapis.com/v1/places/" + url.PathEscape(placeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", 0, 0, fmt.Errorf("create place-details request: %w", err)
	}
	req.Header.Set("X-Goog-Api-Key", r.googleMapsAPIKey)
	req.Header.Set("X-Goog-FieldMask", "formattedAddress,location")

	var payload struct {
		FormattedAddress string `json:"formattedAddress"`
		Location         struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
	}
	if err := r.doGoogleJSON(req, &payload); err != nil {
		return "", 0, 0, err
	}
	if strings.TrimSpace(payload.FormattedAddress) == "" {
		return "", 0, 0, fmt.Errorf("map provider returned no address")
	}
	return payload.FormattedAddress, payload.Location.Latitude, payload.Location.Longitude, nil
}

func (r *Repository) googleGeocodeAddress(ctx context.Context, address string) (string, float64, float64, error) {
	values := url.Values{"address": {address}, "key": {r.googleMapsAPIKey}}
	return r.googleGeocode(ctx, values)
}

func (r *Repository) googleReverseGeocode(ctx context.Context, latitude, longitude float64) (string, float64, float64, error) {
	values := url.Values{
		"latlng": {strconv.FormatFloat(latitude, 'f', 6, 64) + "," + strconv.FormatFloat(longitude, 'f', 6, 64)},
		"key":    {r.googleMapsAPIKey},
	}
	return r.googleGeocode(ctx, values)
}

func (r *Repository) googleGeocode(ctx context.Context, values url.Values) (string, float64, float64, error) {
	requestURL := "https://maps.googleapis.com/maps/api/geocode/json?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", 0, 0, fmt.Errorf("create geocoding request: %w", err)
	}
	var payload struct {
		Status  string `json:"status"`
		Results []struct {
			FormattedAddress string `json:"formatted_address"`
			Geometry         struct {
				Location struct {
					Latitude  float64 `json:"lat"`
					Longitude float64 `json:"lng"`
				} `json:"location"`
			} `json:"geometry"`
		} `json:"results"`
	}
	if err := r.doGoogleJSON(req, &payload); err != nil {
		return "", 0, 0, err
	}
	if payload.Status != "OK" || len(payload.Results) == 0 {
		return "", 0, 0, fmt.Errorf("map provider could not resolve the location")
	}
	result := payload.Results[0]
	return result.FormattedAddress, result.Geometry.Location.Latitude, result.Geometry.Location.Longitude, nil
}

func (r *Repository) doGoogleJSON(req *http.Request, target any) error {
	response, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call map provider: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("map provider returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode map provider response: %w", err)
	}
	return nil
}
