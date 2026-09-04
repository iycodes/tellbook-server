package appdata

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMarketplaceSortKeyValidation(t *testing.T) {
	for _, value := range []string{"0", "-4.5", "1000000000000", "0.000001"} {
		if !validMarketplaceSortKey(value) {
			t.Errorf("validMarketplaceSortKey(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "-", ".", "-.", "1.", ".1", "1..2", "1e3", "+1"} {
		if validMarketplaceSortKey(value) {
			t.Errorf("validMarketplaceSortKey(%q) = true, want false", value)
		}
	}
}

func TestMarketplaceSearchCursorIsBoundToFilters(t *testing.T) {
	firstInput := MarketplaceProviderSearchInput{Query: "hair", Sort: "price", Limit: 20}
	secondInput := MarketplaceProviderSearchInput{Query: "makeup", Sort: "price", Limit: 20}
	cursor := MarketplaceProviderSearchCursor{
		Primary: "1000", Secondary: "-4.5", Tertiary: "2000",
		ProviderID: "11111111-1111-1111-1111-111111111111",
	}
	encoded, err := encodeKeysetCursor(marketplaceSearchFingerprint(firstInput), cursor)
	if err != nil {
		t.Fatal(err)
	}
	validResponse := httptest.NewRecorder()
	decoded, ok := marketplaceSearchCursor(validResponse, encoded, marketplaceSearchFingerprint(firstInput))
	if !ok || decoded == nil || decoded.ProviderID != cursor.ProviderID {
		t.Fatalf("same-filter cursor decoded=%+v ok=%t status=%d", decoded, ok, validResponse.Code)
	}
	mismatchedResponse := httptest.NewRecorder()
	if decoded, ok := marketplaceSearchCursor(mismatchedResponse, encoded, marketplaceSearchFingerprint(secondInput)); ok || decoded != nil {
		t.Fatalf("cross-filter cursor decoded=%+v ok=%t, want rejection", decoded, ok)
	}
	if mismatchedResponse.Code != http.StatusBadRequest {
		t.Fatalf("cross-filter cursor status=%d, want %d", mismatchedResponse.Code, http.StatusBadRequest)
	}
}

func TestMarketplaceSearchFingerprintPreservesCaseSensitiveLocationTokens(t *testing.T) {
	upper := marketplaceSearchFingerprint(MarketplaceProviderSearchInput{LocationToken: "AbC", Sort: "distance"})
	lower := marketplaceSearchFingerprint(MarketplaceProviderSearchInput{LocationToken: "abc", Sort: "distance"})
	if upper == lower {
		t.Fatal("case-sensitive location tokens produced the same search fingerprint")
	}
}
