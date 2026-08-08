package markets

import (
	"encoding/json"
	"net/http"
	"strings"
)

type catalogResponse struct {
	Version string   `json:"version"`
	Items   []Market `json:"items"`
}

func Handler(catalog *Catalog) http.HandlerFunc {
	if catalog == nil {
		panic("market catalog is required")
	}

	response := catalogResponse{
		Version: catalog.Version(),
		Items:   catalog.All(),
	}
	etag := catalog.ETag()

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=86400")
		w.Header().Set("ETag", etag)
		w.Header().Set("X-Market-Catalog-Version", catalog.Version())
		if matchesETag(r.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(response)
	}
}

func matchesETag(header, expected string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == expected {
			return true
		}
	}
	return false
}
