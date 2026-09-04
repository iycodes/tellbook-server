package regionimport

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Abuja Federal Capital Territory": "abuja-federal-capital-territory",
		"Eti-Osa":                         "eti-osa",
		"  Isiala Ngwa North  ":           "isiala-ngwa-north",
	}
	for input, expected := range tests {
		if actual := slugify(input); actual != expected {
			t.Fatalf("slugify(%q) = %q, expected %q", input, actual, expected)
		}
	}
}

func TestDecodeCollectionRejectsUnexpectedCount(t *testing.T) {
	t.Parallel()

	_, err := decodeCollection(strings.NewReader(`{"type":"FeatureCollection","features":[]}`), "ADM1", 37)
	if err == nil || !strings.Contains(err.Error(), "feature count") {
		t.Fatalf("expected feature-count error, got %v", err)
	}
}
