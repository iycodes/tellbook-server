package signature

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
)

const (
	MaxInputBytes      = 2 << 20
	MaxNormalizedBytes = 256 << 10
	MaxWidth           = 1600
	MaxHeight          = 800
	maxDecodedPixels   = 16_000_000
	cropPadding        = 8
)

type ErrorCode string

const (
	ErrorInvalidFormat ErrorCode = "invalid_signature_format"
	ErrorTooLarge      ErrorCode = "signature_too_large"
	ErrorBlank         ErrorCode = "blank_signature"
)

type ValidationError struct {
	Code ErrorCode
	err  error
}

func (e *ValidationError) Error() string {
	return e.err.Error()
}

func (e *ValidationError) Unwrap() error {
	return e.err
}

type Normalized struct {
	PNG    []byte
	SHA256 string
	Width  int
	Height int
}

func NormalizeDataURL(value string) (Normalized, error) {
	const prefix = "data:image/png;base64,"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) {
		return Normalized{}, validationError(ErrorInvalidFormat, "signature must be a PNG data URL")
	}
	encoded := strings.TrimPrefix(value, prefix)
	if encoded == "" || base64.StdEncoding.DecodedLen(len(encoded)) > MaxInputBytes {
		return Normalized{}, validationError(ErrorTooLarge, "signature image exceeds the input limit")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Normalized{}, validationError(ErrorInvalidFormat, "signature contains invalid base64 data")
	}
	return NormalizePNG(decoded)
}

func NormalizePNG(content []byte) (Normalized, error) {
	if len(content) == 0 {
		return Normalized{}, validationError(ErrorInvalidFormat, "signature image is empty")
	}
	if len(content) > MaxInputBytes {
		return Normalized{}, validationError(ErrorTooLarge, "signature image exceeds the input limit")
	}

	config, err := png.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return Normalized{}, validationError(ErrorInvalidFormat, "signature is not a valid PNG")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxDecodedPixels/config.Height {
		return Normalized{}, validationError(ErrorTooLarge, "signature image dimensions are too large")
	}
	if config.Width*config.Height > maxDecodedPixels {
		return Normalized{}, validationError(ErrorTooLarge, "signature image dimensions are too large")
	}

	decoded, err := png.Decode(bytes.NewReader(content))
	if err != nil {
		return Normalized{}, validationError(ErrorInvalidFormat, "signature is not a valid PNG")
	}
	inkBounds, ok := visibleInkBounds(decoded)
	if !ok {
		return Normalized{}, validationError(ErrorBlank, "signature image does not contain a visible signature")
	}

	croppedBounds := addPadding(inkBounds, decoded.Bounds(), cropPadding)
	cropped := image.NewNRGBA(image.Rect(0, 0, croppedBounds.Dx(), croppedBounds.Dy()))
	draw.Draw(cropped, cropped.Bounds(), decoded, croppedBounds.Min, draw.Src)
	normalizedImage := fitWithin(cropped, MaxWidth, MaxHeight)

	encoded, err := encodePNG(normalizedImage, png.BestSpeed)
	if err != nil {
		return Normalized{}, fmt.Errorf("encode normalized signature: %w", err)
	}
	if len(encoded) > MaxNormalizedBytes {
		encoded, err = encodePNG(normalizedImage, png.BestCompression)
		if err != nil {
			return Normalized{}, fmt.Errorf("compress normalized signature: %w", err)
		}
	}
	if len(encoded) > MaxNormalizedBytes {
		return Normalized{}, validationError(ErrorTooLarge, "normalized signature exceeds the storage limit")
	}

	digest := sha256.Sum256(encoded)
	return Normalized{
		PNG:    encoded,
		SHA256: hex.EncodeToString(digest[:]),
		Width:  normalizedImage.Bounds().Dx(),
		Height: normalizedImage.Bounds().Dy(),
	}, nil
}

func visibleInkBounds(source image.Image) (image.Rectangle, bool) {
	bounds := source.Bounds()
	minX, minY := bounds.Max.X, bounds.Max.Y
	maxX, maxY := bounds.Min.X-1, bounds.Min.Y-1
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !isVisibleInk(source.At(x, y)) {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x > maxX {
				maxX = x
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxX < minX || maxY < minY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX+1, maxY+1), true
}

func isVisibleInk(value color.Color) bool {
	r, g, b, a := value.RGBA()
	if a < 0x1000 {
		return false
	}
	// Signature canvases are commonly transparent or white. Treat near-white pixels as background.
	return r < 0xf000 || g < 0xf000 || b < 0xf000
}

func addPadding(bounds, limit image.Rectangle, padding int) image.Rectangle {
	return image.Rect(
		max(bounds.Min.X-padding, limit.Min.X),
		max(bounds.Min.Y-padding, limit.Min.Y),
		min(bounds.Max.X+padding, limit.Max.X),
		min(bounds.Max.Y+padding, limit.Max.Y),
	)
}

func fitWithin(source *image.NRGBA, maxWidth, maxHeight int) *image.NRGBA {
	width := source.Bounds().Dx()
	height := source.Bounds().Dy()
	if width <= maxWidth && height <= maxHeight {
		return source
	}

	scaleWidth := float64(maxWidth) / float64(width)
	scaleHeight := float64(maxHeight) / float64(height)
	scale := min(scaleWidth, scaleHeight)
	targetWidth := max(1, int(float64(width)*scale))
	targetHeight := max(1, int(float64(height)*scale))
	target := image.NewNRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sourceY := min(height-1, int(float64(y)/scale))
		for x := 0; x < targetWidth; x++ {
			sourceX := min(width-1, int(float64(x)/scale))
			target.SetNRGBA(x, y, source.NRGBAAt(sourceX, sourceY))
		}
	}
	return target
}

func encodePNG(source image.Image, level png.CompressionLevel) ([]byte, error) {
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: level}
	if err := encoder.Encode(&output, source); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func validationError(code ErrorCode, message string) error {
	return &ValidationError{Code: code, err: fmt.Errorf("%s", message)}
}
