package appdata

import (
	"net/http"
	"strings"
)

const (
	marketplaceMetadataCacheControl    = "public, max-age=60, stale-while-revalidate=600"
	marketplaceMetadataCDNCacheControl = "public, max-age=300, stale-while-revalidate=600"
)

func writePublicMarketplaceMetadata(w http.ResponseWriter, r *http.Request, payload any) error {
	return writeRevisionedPublicJSON(
		w,
		r,
		payload,
		"marketplace-metadata",
		nil,
		marketplaceMetadataCacheControl,
		true,
	)
}

func addVaryHeader(header http.Header, value string) {
	for _, entry := range header.Values("Vary") {
		for _, existing := range strings.Split(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(existing), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}

func requestETagMatches(header, expected string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == expected || strings.TrimPrefix(candidate, "W/") == expected {
			return true
		}
	}
	return false
}
