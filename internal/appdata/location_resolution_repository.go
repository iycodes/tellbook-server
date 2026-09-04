package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	CountryCode      string
	StateRegionID    *uuid.UUID
	StateName        string
	LGARegionID      *uuid.UUID
	LGAName          string
	Locality         string
	ExpiresAt        time.Time
}

type resolvedAddress struct {
	FormattedAddress string
	Latitude         float64
	Longitude        float64
	CountryCode      string
	Locality         string
}

type googleAddressComponent struct {
	LongName  string   `json:"long_name"`
	ShortName string   `json:"short_name"`
	LongText  string   `json:"longText"`
	ShortText string   `json:"shortText"`
	Types     []string `json:"types"`
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
			if resolved, err := r.googleGeocodeAddress(ctx, record.FormattedAddress); err == nil {
				applyResolvedAddress(&record, resolved)
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
		resolved, err := r.googlePlaceDetails(ctx, record.ProviderPlaceID)
		if err != nil {
			return ResolvedPublicLocationResponse{}, err
		}
		applyResolvedAddress(&record, resolved)
		provider = "google"
	case "current_location":
		if strings.TrimSpace(input.Address) != "" || strings.TrimSpace(input.ProviderPlaceID) != "" {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("current_location accepts only coordinates")
		}
		if input.Latitude == nil || input.Longitude == nil {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("latitude and longitude are required")
		}
		if *input.Latitude < -90 || *input.Latitude > 90 || *input.Longitude < -180 || *input.Longitude > 180 {
			return ResolvedPublicLocationResponse{}, fmt.Errorf("coordinates are outside their valid range")
		}
		record.FormattedAddress = "Current location"
		record.Latitude = input.Latitude
		record.Longitude = input.Longitude
		if r.googleMapsAPIKey != "" {
			if resolved, err := r.googleReverseGeocode(ctx, *input.Latitude, *input.Longitude); err == nil {
				applyResolvedAddress(&record, resolved)
				provider = "google"
			}
		}
	}

	record.ResolutionStatus = "text_only"
	if record.Latitude != nil && record.Longitude != nil {
		record.ResolutionStatus = "coordinates_resolved"
		assignment, err := r.assignAdministrativeRegions(ctx, *record.Latitude, *record.Longitude, record.Locality)
		if err != nil {
			return ResolvedPublicLocationResponse{}, err
		}
		if assignment.CountryCode != "" {
			record.CountryCode = assignment.CountryCode
		}
		record.StateRegionID = assignment.StateRegionID
		record.StateName = assignment.StateName
		record.LGARegionID = assignment.LGARegionID
		record.LGAName = assignment.LGAName
		record.Locality = assignment.Locality
		if record.FormattedAddress == "Current location" {
			record.FormattedAddress = firstNonBlank(record.Locality, record.LGAName, record.StateName, record.FormattedAddress)
		}
	}
	record.ExpiresAt = time.Now().UTC().Add(30 * time.Minute)
	token, err := publictoken.New()
	if err != nil {
		return ResolvedPublicLocationResponse{}, fmt.Errorf("create location token: %w", err)
	}
	if _, err := r.db.Exec(ctx, `
		INSERT INTO resolved_locations (
			id, public_token, provider, provider_place_id, formatted_address,
			latitude, longitude, address_source, resolution_status,
			country_code, state_region_id, lga_region_id, locality, expires_at, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NOW())
	`, record.ID, token, provider, nullIfBlank(record.ProviderPlaceID), record.FormattedAddress,
		record.Latitude, record.Longitude, record.AddressSource, record.ResolutionStatus,
		nullIfBlank(record.CountryCode), record.StateRegionID, record.LGARegionID, record.Locality,
		record.ExpiresAt); err != nil {
		return ResolvedPublicLocationResponse{}, fmt.Errorf("store resolved location: %w", err)
	}

	return ResolvedPublicLocationResponse{
		LocationToken:    token,
		FormattedAddress: record.FormattedAddress,
		ResolutionStatus: record.ResolutionStatus,
		CountryCode:      record.CountryCode,
		StateRegionID:    uuidString(record.StateRegionID),
		StateName:        record.StateName,
		LGARegionID:      uuidString(record.LGARegionID),
		LGAName:          record.LGAName,
		Locality:         record.Locality,
		ExpiresAt:        record.ExpiresAt.Format(time.RFC3339),
	}, nil
}

func (r *Repository) loadResolvedLocation(ctx context.Context, token string) (resolvedLocationRecord, error) {
	var record resolvedLocationRecord
	if err := r.db.QueryRow(ctx, `
		SELECT resolved.id, resolved.formatted_address, COALESCE(resolved.provider_place_id, ''),
			resolved.latitude::double precision, resolved.longitude::double precision,
			resolved.address_source, resolved.resolution_status,
			COALESCE(resolved.country_code, ''), resolved.state_region_id,
			COALESCE(state.name, ''), resolved.lga_region_id, COALESCE(lga.name, ''),
			resolved.locality, resolved.expires_at
		FROM resolved_locations resolved
		LEFT JOIN administrative_regions state ON state.id = resolved.state_region_id
		LEFT JOIN administrative_regions lga ON lga.id = resolved.lga_region_id
		WHERE resolved.public_token = $1 AND resolved.expires_at > NOW()
	`, strings.TrimSpace(token)).Scan(
		&record.ID, &record.FormattedAddress, &record.ProviderPlaceID,
		&record.Latitude, &record.Longitude, &record.AddressSource,
		&record.ResolutionStatus, &record.CountryCode, &record.StateRegionID,
		&record.StateName, &record.LGARegionID, &record.LGAName, &record.Locality,
		&record.ExpiresAt,
	); err != nil {
		return resolvedLocationRecord{}, fmt.Errorf("load location token: %w", err)
	}
	return record, nil
}

func (r *Repository) googlePlaceDetails(ctx context.Context, placeID string) (resolvedAddress, error) {
	placeID = strings.TrimSpace(placeID)
	return r.coalesceGoogleAddress(ctx, "place", placeID, func(loadCtx context.Context) (resolvedAddress, error) {
		return r.fetchGooglePlaceDetails(loadCtx, placeID)
	})
}

func (r *Repository) fetchGooglePlaceDetails(ctx context.Context, placeID string) (resolvedAddress, error) {
	requestURL := "https://places.googleapis.com/v1/places/" + url.PathEscape(placeID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return resolvedAddress{}, fmt.Errorf("create place-details request: %w", err)
	}
	req.Header.Set("X-Goog-Api-Key", r.googleMapsAPIKey)
	req.Header.Set("X-Goog-FieldMask", "formattedAddress,location,addressComponents")

	var payload struct {
		FormattedAddress string `json:"formattedAddress"`
		Location         struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"location"`
		AddressComponents []googleAddressComponent `json:"addressComponents"`
	}
	if err := r.doGoogleJSON(req, &payload); err != nil {
		return resolvedAddress{}, err
	}
	if strings.TrimSpace(payload.FormattedAddress) == "" {
		return resolvedAddress{}, fmt.Errorf("map provider returned no address")
	}
	return resolvedAddress{
		FormattedAddress: payload.FormattedAddress,
		Latitude:         payload.Location.Latitude, Longitude: payload.Location.Longitude,
		CountryCode: componentValue(payload.AddressComponents, "country", true),
		Locality:    preferredLocality(payload.AddressComponents),
	}, nil
}

func (r *Repository) googleGeocodeAddress(ctx context.Context, address string) (resolvedAddress, error) {
	address = strings.TrimSpace(address)
	identity := strings.ToLower(strings.Join(strings.Fields(address), " "))
	return r.coalesceGoogleAddress(ctx, "address", identity, func(loadCtx context.Context) (resolvedAddress, error) {
		values := url.Values{"address": {address}, "key": {r.googleMapsAPIKey}}
		return r.googleGeocode(loadCtx, values)
	})
}

func (r *Repository) googleReverseGeocode(ctx context.Context, latitude, longitude float64) (resolvedAddress, error) {
	coordinates := strconv.FormatFloat(latitude, 'f', 6, 64) + "," + strconv.FormatFloat(longitude, 'f', 6, 64)
	return r.coalesceGoogleAddress(ctx, "coordinates", coordinates, func(loadCtx context.Context) (resolvedAddress, error) {
		values := url.Values{"latlng": {coordinates}, "key": {r.googleMapsAPIKey}}
		return r.googleGeocode(loadCtx, values)
	})
}

func (r *Repository) coalesceGoogleAddress(
	ctx context.Context,
	kind, identity string,
	load func(context.Context) (resolvedAddress, error),
) (resolvedAddress, error) {
	digest := sha256.Sum256([]byte(identity))
	key := kind + ":" + hex.EncodeToString(digest[:16])
	result := r.locationResolutionFlight.DoChan(key, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 9*time.Second)
		defer cancel()
		return load(loadCtx)
	})
	select {
	case <-ctx.Done():
		return resolvedAddress{}, ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			return resolvedAddress{}, outcome.Err
		}
		address, ok := outcome.Val.(resolvedAddress)
		if !ok {
			return resolvedAddress{}, fmt.Errorf("invalid coalesced map provider response")
		}
		return address, nil
	}
}

func (r *Repository) googleGeocode(ctx context.Context, values url.Values) (resolvedAddress, error) {
	requestURL := "https://maps.googleapis.com/maps/api/geocode/json?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return resolvedAddress{}, fmt.Errorf("create geocoding request: %w", err)
	}
	var payload struct {
		Status  string `json:"status"`
		Results []struct {
			FormattedAddress  string                   `json:"formatted_address"`
			AddressComponents []googleAddressComponent `json:"address_components"`
			Geometry          struct {
				Location struct {
					Latitude  float64 `json:"lat"`
					Longitude float64 `json:"lng"`
				} `json:"location"`
			} `json:"geometry"`
		} `json:"results"`
	}
	if err := r.doGoogleJSON(req, &payload); err != nil {
		return resolvedAddress{}, err
	}
	if payload.Status != "OK" || len(payload.Results) == 0 {
		return resolvedAddress{}, fmt.Errorf("map provider could not resolve the location")
	}
	result := payload.Results[0]
	return resolvedAddress{
		FormattedAddress: result.FormattedAddress,
		Latitude:         result.Geometry.Location.Latitude,
		Longitude:        result.Geometry.Location.Longitude,
		CountryCode:      componentValue(result.AddressComponents, "country", true),
		Locality:         preferredLocality(result.AddressComponents),
	}, nil
}

func applyResolvedAddress(record *resolvedLocationRecord, address resolvedAddress) {
	record.FormattedAddress = address.FormattedAddress
	record.Latitude = &address.Latitude
	record.Longitude = &address.Longitude
	record.CountryCode = strings.ToUpper(address.CountryCode)
	record.Locality = address.Locality
}

func componentValue(components []googleAddressComponent, wantedType string, short bool) string {
	for _, component := range components {
		for _, componentType := range component.Types {
			if componentType != wantedType {
				continue
			}
			if short {
				if value := strings.TrimSpace(firstNonBlank(component.ShortName, component.ShortText)); value != "" {
					return value
				}
			}
			return strings.TrimSpace(firstNonBlank(component.LongName, component.LongText))
		}
	}
	return ""
}

func preferredLocality(components []googleAddressComponent) string {
	for _, componentType := range []string{"locality", "postal_town", "administrative_area_level_3", "sublocality_level_1"} {
		if value := componentValue(components, componentType, false); value != "" {
			return value
		}
	}
	return ""
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uuidString(value *uuid.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
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
