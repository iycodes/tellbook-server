package appdata

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNormalizeUploadCategoryRejectsUnknownPublicPaths(t *testing.T) {
	for _, input := range []string{"services", " sections ", "PROFILES", "portfolio"} {
		if category, ok := normalizeUploadCategory(input); !ok || category == "" {
			t.Fatalf("normalizeUploadCategory(%q) = %q, %t", input, category, ok)
		}
	}
	if category, ok := normalizeUploadCategory("documents"); ok || category != "" {
		t.Fatalf("unknown category = %q, %t", category, ok)
	}
}

func TestDecodeImageDataURLRejectsDetectedTypeOutsideAllowlist(t *testing.T) {
	bmp := append([]byte{'B', 'M'}, make([]byte, 510)...)
	_, err := decodeImageDataURL(base64.StdEncoding.EncodeToString(bmp), "image/png")
	if err == nil || !strings.Contains(err.Error(), "jpeg, png, webp, or gif") {
		t.Fatalf("decodeImageDataURL error = %v, want detected-type rejection", err)
	}
}
