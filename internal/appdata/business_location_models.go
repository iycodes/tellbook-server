package appdata

type BusinessLocationItem struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	FormattedAddress string   `json:"formatted_address"`
	ProviderPlaceID  string   `json:"provider_place_id,omitempty"`
	Latitude         *float64 `json:"latitude,omitempty"`
	Longitude        *float64 `json:"longitude,omitempty"`
	AddressSource    string   `json:"address_source"`
	ResolutionStatus string   `json:"resolution_status"`
	CountryCode      string   `json:"country_code,omitempty"`
	StateRegionID    string   `json:"state_region_id,omitempty"`
	StateName        string   `json:"state_name,omitempty"`
	LGARegionID      string   `json:"lga_region_id,omitempty"`
	LGAName          string   `json:"lga_name,omitempty"`
	Locality         string   `json:"locality,omitempty"`
	Timezone         string   `json:"timezone"`
	IsPrimary        bool     `json:"is_primary"`
	IsActive         bool     `json:"is_active"`
}

type UpsertBusinessLocationInput struct {
	Label            string   `json:"label"`
	FormattedAddress string   `json:"formatted_address"`
	ProviderPlaceID  string   `json:"provider_place_id"`
	Latitude         *float64 `json:"latitude"`
	Longitude        *float64 `json:"longitude"`
	AddressSource    string   `json:"address_source"`
	Timezone         string   `json:"timezone"`
	IsPrimary        bool     `json:"is_primary"`
}
