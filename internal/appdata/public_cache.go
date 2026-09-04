package appdata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

const (
	publicProviderCacheControl      = "public, max-age=15, stale-while-revalidate=60"
	publicProviderCDNCacheControl   = "public, max-age=30, stale-while-revalidate=60"
	publicDiscoveryCacheControl     = "public, max-age=5, stale-while-revalidate=30"
	publicDiscoveryCDNCacheControl  = "public, max-age=15, stale-while-revalidate=30"
	privateNoStoreCacheControl      = "private, no-store"
	publicRepresentationETagVersion = "v2"
)

func writeRevisionedPublicJSON(
	w http.ResponseWriter,
	r *http.Request,
	payload any,
	namespace string,
	revisionParts []string,
	cacheControl string,
	resourceIsCacheable bool,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if !resourceIsCacheable || !anonymousPublicCacheRequest(r) {
		w.Header().Set("Cache-Control", privateNoStoreCacheControl)
		w.Header().Del("CDN-Cache-Control")
		w.Header().Del("ETag")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return nil
	}

	digest := sha256.New()
	_, _ = digest.Write([]byte(namespace))
	_, _ = digest.Write([]byte{'\x00'})
	_, _ = digest.Write([]byte(r.URL.RequestURI()))
	for _, part := range revisionParts {
		_, _ = digest.Write([]byte{'\x00'})
		_, _ = digest.Write([]byte(part))
	}
	bodyDigest := sha256.Sum256(body)
	_, _ = digest.Write(bodyDigest[:])
	etag := `"` + namespace + `-` + hex.EncodeToString(digest.Sum(nil)[:16]) + `"`

	setPublicCacheHeaders(w.Header(), cacheControl)
	w.Header().Set("ETag", etag)
	addPublicCacheVary(w.Header())
	if requestETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

func writeKnownRevisionPublicJSON(
	w http.ResponseWriter,
	r *http.Request,
	payload any,
	namespace string,
	revisionParts []string,
	cacheControl string,
) error {
	if !anonymousPublicCacheRequest(r) {
		return writeRevisionedPublicJSON(w, r, payload, namespace, revisionParts, cacheControl, true)
	}

	etag := knownRevisionETag(r, namespace, revisionParts)
	if requestETagMatches(r.Header.Get("If-None-Match"), etag) {
		setPublicCacheHeaders(w.Header(), cacheControl)
		w.Header().Set("ETag", etag)
		addPublicCacheVary(w.Header())
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	setPublicCacheHeaders(w.Header(), cacheControl)
	w.Header().Set("ETag", etag)
	addPublicCacheVary(w.Header())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

func writeKnownRevisionNotModified(
	w http.ResponseWriter,
	r *http.Request,
	namespace string,
	revisionParts []string,
	cacheControl string,
) bool {
	if !anonymousPublicCacheRequest(r) || strings.TrimSpace(r.Header.Get("If-None-Match")) == "" {
		return false
	}
	etag := knownRevisionETag(r, namespace, revisionParts)
	if !requestETagMatches(r.Header.Get("If-None-Match"), etag) {
		return false
	}
	setPublicCacheHeaders(w.Header(), cacheControl)
	w.Header().Set("ETag", etag)
	addPublicCacheVary(w.Header())
	w.WriteHeader(http.StatusNotModified)
	return true
}

func knownRevisionETag(r *http.Request, namespace string, revisionParts []string) string {
	digest := sha256.New()
	for _, part := range append([]string{publicRepresentationETagVersion, namespace, r.URL.RequestURI()}, revisionParts...) {
		_, _ = digest.Write([]byte{'\x00'})
		_, _ = digest.Write([]byte(part))
	}
	return `"` + namespace + `-` + hex.EncodeToString(digest.Sum(nil)[:16]) + `"`
}

func setPublicCacheHeaders(header http.Header, cacheControl string) {
	header.Set("Cache-Control", cacheControl)
	switch cacheControl {
	case publicProviderCacheControl:
		header.Set("CDN-Cache-Control", publicProviderCDNCacheControl)
	case publicDiscoveryCacheControl:
		header.Set("CDN-Cache-Control", publicDiscoveryCDNCacheControl)
	case marketplaceMetadataCacheControl:
		header.Set("CDN-Cache-Control", marketplaceMetadataCDNCacheControl)
	default:
		header.Del("CDN-Cache-Control")
	}
}

func addPublicCacheVary(header http.Header) {
	addVaryHeader(header, "Origin")
	addVaryHeader(header, "Authorization")
	addVaryHeader(header, "Cookie")
}

func anonymousPublicCacheRequest(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	return strings.TrimSpace(r.Header.Get("Authorization")) == "" &&
		strings.TrimSpace(r.Header.Get("Cookie")) == ""
}

func publicProviderRevisionPart(revision int64) []string {
	return []string{strconv.FormatInt(revision, 10)}
}

func marketplaceProviderRevisionParts(items []MarketplaceProvider) []string {
	parts := make([]string, 0, len(items)*2)
	for _, item := range items {
		parts = append(parts,
			item.ID+":"+strconv.FormatInt(item.providerDocumentRevision, 10),
			item.ServiceID+":"+strconv.FormatInt(item.serviceDocumentRevision, 10)+":"+
				strconv.FormatInt(item.availabilityDocumentRevision, 10),
		)
	}
	return parts
}
