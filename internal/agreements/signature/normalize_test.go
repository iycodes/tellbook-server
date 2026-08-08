package signature

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"testing"
)

func TestNormalizePNGRejectsBlankCanvas(t *testing.T) {
	canvas := image.NewNRGBA(image.Rect(0, 0, 320, 120))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)

	_, err := NormalizePNG(mustEncodePNG(t, canvas))
	assertValidationCode(t, err, ErrorBlank)
}

func TestNormalizePNGCropsScalesAndProducesStableChecksum(t *testing.T) {
	canvas := image.NewNRGBA(image.Rect(0, 0, 2400, 1200))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(100, 100, 2300, 1100), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)

	first, err := NormalizePNG(mustEncodePNG(t, canvas))
	if err != nil {
		t.Fatalf("NormalizePNG() error = %v", err)
	}
	second, err := NormalizePNG(mustEncodePNG(t, canvas))
	if err != nil {
		t.Fatalf("NormalizePNG() second error = %v", err)
	}
	if first.Width > MaxWidth || first.Height > MaxHeight {
		t.Fatalf("normalized dimensions = %dx%d", first.Width, first.Height)
	}
	if len(first.PNG) > MaxNormalizedBytes {
		t.Fatalf("normalized bytes = %d", len(first.PNG))
	}
	if first.SHA256 == "" || first.SHA256 != second.SHA256 {
		t.Fatalf("checksums = %q and %q", first.SHA256, second.SHA256)
	}
}

func TestNormalizeDataURLAcceptsOnlyPNG(t *testing.T) {
	canvas := image.NewNRGBA(image.Rect(0, 0, 100, 50))
	draw.Draw(canvas, image.Rect(10, 10, 90, 40), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	encoded := base64.StdEncoding.EncodeToString(mustEncodePNG(t, canvas))

	result, err := NormalizeDataURL("data:image/png;base64," + encoded)
	if err != nil {
		t.Fatalf("NormalizeDataURL() error = %v", err)
	}
	if result.Width == 0 || result.Height == 0 {
		t.Fatalf("invalid normalized dimensions: %+v", result)
	}

	_, err = NormalizeDataURL("data:image/jpeg;base64," + encoded)
	assertValidationCode(t, err, ErrorInvalidFormat)
}

func TestNormalizePNGRejectsInvalidContent(t *testing.T) {
	_, err := NormalizePNG([]byte("not a png"))
	assertValidationCode(t, err, ErrorInvalidFormat)
}

func mustEncodePNG(t *testing.T, source image.Image) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, source); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return output.Bytes()
}

func assertValidationCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want ValidationError", err)
	}
	if validationErr.Code != want {
		t.Fatalf("code = %q, want %q", validationErr.Code, want)
	}
}
