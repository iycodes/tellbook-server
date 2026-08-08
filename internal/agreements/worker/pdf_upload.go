package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"

	"github.com/ledongthuc/pdf"
)

const (
	MaxAgreementPDFBytes       = 10 << 20
	MaxAgreementPDFPages       = 50
	MaxExtractedDocumentRunes  = 100_000
	agreementPDFExtractTimeout = 60 * time.Second
)

type PrivateObjectStore interface {
	PrivateBucketName() string
	DownloadLimited(context.Context, string, int64, ...string) ([]byte, error)
}

type PDFUploadPreparer struct {
	storage PrivateObjectStore
}

func NewPDFUploadPreparer(storage PrivateObjectStore) (*PDFUploadPreparer, error) {
	if storage == nil || strings.TrimSpace(storage.PrivateBucketName()) == "" {
		return nil, fmt.Errorf("private agreement storage is required")
	}
	return &PDFUploadPreparer{storage: storage}, nil
}

func (p *PDFUploadPreparer) PrepareAgreementUpload(
	ctx context.Context,
	job domain.TemplateGenerationJob,
	input domain.UploadGenerationInput,
) (UploadPreparation, error) {
	if p == nil || p.storage == nil {
		return UploadPreparation{}, permanentGenerationError("upload_storage_unavailable", "private agreement storage is not configured")
	}
	expectedPrefix := "clients/" + job.ClientID.String() + "/templates/"
	objectKey := strings.TrimSpace(input.SourcePDFR2Key)
	if job.ClientID.String() == "00000000-0000-0000-0000-000000000000" ||
		strings.Contains(objectKey, "\\") || path.Clean(objectKey) != objectKey ||
		!strings.HasPrefix(objectKey, expectedPrefix) {
		return UploadPreparation{}, permanentGenerationError("source_pdf_not_owned", "uploaded agreement PDF does not belong to this business")
	}
	content, err := p.storage.DownloadLimited(ctx, objectKey, MaxAgreementPDFBytes, p.storage.PrivateBucketName())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "exceeds") {
			return UploadPreparation{}, permanentGenerationError("pdf_too_large", "uploaded agreement PDF exceeds 10 MiB")
		}
		return UploadPreparation{}, transientGenerationError("source_pdf_download_failed", "could not load the uploaded agreement PDF")
	}

	extractCtx, cancel := context.WithTimeout(ctx, agreementPDFExtractTimeout)
	defer cancel()
	type extractionResult struct {
		text string
		err  error
	}
	resultChannel := make(chan extractionResult, 1)
	go func() {
		text, err := extractBoundedPDFText(content)
		resultChannel <- extractionResult{text: text, err: err}
	}()
	var extracted string
	select {
	case <-extractCtx.Done():
		if ctx.Err() != nil {
			return UploadPreparation{}, transientGenerationError("pdf_processing_cancelled", "agreement PDF processing was cancelled")
		}
		return UploadPreparation{}, permanentGenerationError("pdf_extraction_timeout", "agreement PDF text extraction exceeded 60 seconds")
	case result := <-resultChannel:
		if result.err != nil {
			return UploadPreparation{}, result.err
		}
		extracted = result.text
	}

	redacted, literals := redactAgreementSource(extracted, input.Context)
	if len(strings.Fields(redacted)) < 20 {
		return UploadPreparation{}, permanentGenerationError("image_only_pdf", "the uploaded PDF has too little readable text; create the agreement with AI fields instead")
	}
	return UploadPreparation{RedactedDocumentText: redacted, ProhibitedLiterals: literals}, nil
}

func extractBoundedPDFText(content []byte) (string, error) {
	if len(content) == 0 || len(content) > MaxAgreementPDFBytes {
		return "", permanentGenerationError("pdf_too_large", "uploaded agreement PDF is empty or exceeds 10 MiB")
	}
	header := content
	if len(header) > 1024 {
		header = header[:1024]
	}
	if !bytes.Contains(header, []byte("%PDF-")) || http.DetectContentType(header) != "application/pdf" {
		return "", permanentGenerationError("invalid_pdf", "uploaded file is not a valid PDF")
	}
	reader, err := pdf.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "encrypt") || strings.Contains(message, "password") {
			return "", permanentGenerationError("encrypted_pdf", "encrypted PDFs are not supported")
		}
		return "", permanentGenerationError("malformed_pdf", "uploaded PDF could not be read")
	}
	if !reader.Trailer().Key("Encrypt").IsNull() {
		return "", permanentGenerationError("encrypted_pdf", "encrypted PDFs are not supported")
	}
	pageCount := reader.NumPage()
	if pageCount <= 0 {
		return "", permanentGenerationError("malformed_pdf", "uploaded PDF contains no pages")
	}
	if pageCount > MaxAgreementPDFPages {
		return "", permanentGenerationError("pdf_page_limit", "uploaded PDF exceeds the 50-page limit")
	}
	plainText, err := reader.GetPlainText()
	if err != nil {
		return "", permanentGenerationError("pdf_text_unreadable", "text could not be extracted from the uploaded PDF")
	}
	contentLimit := int64(MaxExtractedDocumentRunes*utf8.UTFMax + 1)
	textBytes, err := io.ReadAll(io.LimitReader(plainText, contentLimit))
	if err != nil {
		return "", permanentGenerationError("pdf_text_unreadable", "text could not be extracted from the uploaded PDF")
	}
	if !utf8.Valid(textBytes) {
		return "", permanentGenerationError("pdf_text_unreadable", "uploaded PDF text is not valid Unicode")
	}
	runes := []rune(normalizeExtractedText(string(textBytes)))
	if len(runes) > MaxExtractedDocumentRunes {
		return "", permanentGenerationError("pdf_text_limit", "uploaded PDF contains more than 100,000 characters")
	}
	return string(runes), nil
}

var (
	redactEmailPattern   = regexp.MustCompile(`(?i)\b[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`)
	redactPhonePattern   = regexp.MustCompile(`(?:\+?\d[\d ()-]{8,}\d)`)
	redactAccountPattern = regexp.MustCompile(`\b\d{10,16}\b`)
	redactAmountPattern  = regexp.MustCompile(`(?i)(?:₦|NGN|N|\$|USD|GHS|KES|ZAR)\s*[0-9][0-9,.]*`)
	redactDatePattern    = regexp.MustCompile(`(?i)\b(?:\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\s+\d{1,2},?\s+\d{4})\b`)
	redactPartyLine      = regexp.MustCompile(`(?im)^\s*(?:customer|client|provider|business|full name|address|signature)\s*[:\-]\s*(.+)$`)
	redactAddressLine    = regexp.MustCompile(`(?im)^.*\b(?:street|road|avenue|close|crescent|estate|district|postcode|postal code)\b.*$`)
)

func redactAgreementSource(value string, contextValues []aiapi.NamedValue) (string, []string) {
	redacted := value
	literals := make([]string, 0)
	for _, contextValue := range contextValues {
		literal := strings.TrimSpace(contextValue.Value)
		if len([]rune(literal)) < 4 {
			continue
		}
		pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(literal))
		if pattern.MatchString(redacted) {
			literals = append(literals, literal)
			redacted = pattern.ReplaceAllString(redacted, "[REDACTED]")
		}
	}
	patterns := []*regexp.Regexp{
		redactEmailPattern,
		redactPhonePattern,
		redactAccountPattern,
		redactAmountPattern,
		redactDatePattern,
		redactPartyLine,
		redactAddressLine,
	}
	for _, pattern := range patterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			match = strings.TrimSpace(match)
			if match != "" {
				literals = append(literals, match)
			}
			return "[REDACTED]"
		})
	}
	return normalizeExtractedText(redacted), uniqueLiterals(literals)
}

func normalizeExtractedText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(result) > 0 {
				result = append(result, "")
			}
			blank = true
			continue
		}
		blank = false
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func uniqueLiterals(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key := strings.ToLower(strings.TrimSpace(value))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, strings.TrimSpace(value))
	}
	return result
}
