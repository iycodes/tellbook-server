package appdata

import (
	"context"
	"strings"
	"testing"
)

func TestResolvePublicLocationRejectsMixedSources(t *testing.T) {
	latitude := 6.5244
	longitude := 3.3792
	tests := []struct {
		name  string
		input ResolvePublicLocationInput
		want  string
	}{
		{
			name: "manual with coordinates",
			input: ResolvePublicLocationInput{
				Source: "manual", Address: "Lagos", Latitude: &latitude, Longitude: &longitude,
			},
			want: "manual location accepts only address",
		},
		{
			name: "place with address",
			input: ResolvePublicLocationInput{
				Source: "google_place", Address: "Lagos", ProviderPlaceID: "place-id",
			},
			want: "google_place location accepts only provider_place_id",
		},
		{
			name: "coordinates with place",
			input: ResolvePublicLocationInput{
				Source: "current_location", ProviderPlaceID: "place-id", Latitude: &latitude, Longitude: &longitude,
			},
			want: "current_location accepts only coordinates",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := (&Repository{}).ResolvePublicLocation(context.Background(), test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolvePublicLocation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
