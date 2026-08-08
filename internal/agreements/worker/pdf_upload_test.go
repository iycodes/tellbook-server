package worker

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

type privateObjectStoreFake struct {
	content []byte
	key     string
	bucket  string
}

func (s *privateObjectStoreFake) PrivateBucketName() string { return "private" }

func (s *privateObjectStoreFake) DownloadLimited(_ context.Context, key string, _ int64, bucket ...string) ([]byte, error) {
	s.key = key
	if len(bucket) > 0 {
		s.bucket = bucket[0]
	}
	return append([]byte(nil), s.content...), nil
}

func TestExtractBoundedPDFTextEnforcesPageAndFileTypeLimits(t *testing.T) {
	content := generatedTextPDF(t, 2)
	text, err := extractBoundedPDFText(content)
	if err != nil {
		t.Fatalf("extractBoundedPDFText() error = %v", err)
	}
	if !strings.Contains(text, "Service agreement terms") {
		t.Fatalf("text = %q", text)
	}

	if _, err := extractBoundedPDFText([]byte("not a PDF")); err == nil {
		t.Fatal("extractBoundedPDFText() accepted a non-PDF")
	}
	if _, err := extractBoundedPDFText(generatedTextPDF(t, MaxAgreementPDFPages+1)); err == nil {
		t.Fatal("extractBoundedPDFText() accepted too many pages")
	}
}

func TestPDFUploadPreparerEnforcesOwnershipAndRedactsSource(t *testing.T) {
	clientID := uuid.New()
	store := &privateObjectStoreFake{content: generatedTextPDF(t, 1)}
	preparer, err := NewPDFUploadPreparer(store)
	if err != nil {
		t.Fatalf("NewPDFUploadPreparer() error = %v", err)
	}
	objectKey := "clients/" + clientID.String() + "/templates/source.pdf"
	prepared, err := preparer.PrepareAgreementUpload(context.Background(), domain.TemplateGenerationJob{ClientID: clientID}, domain.UploadGenerationInput{
		SourcePDFR2Key: objectKey,
		Context:        []aiapi.NamedValue{{Key: "customer_name", Value: "Ada Okafor"}},
	})
	if err != nil {
		t.Fatalf("PrepareAgreementUpload() error = %v", err)
	}
	if store.key != objectKey || store.bucket != "private" {
		t.Fatalf("storage read = %q from %q", store.key, store.bucket)
	}
	if strings.Contains(prepared.RedactedDocumentText, "Ada Okafor") || len(prepared.ProhibitedLiterals) == 0 {
		t.Fatalf("redaction = %+v", prepared)
	}

	_, err = preparer.PrepareAgreementUpload(context.Background(), domain.TemplateGenerationJob{ClientID: uuid.New()}, domain.UploadGenerationInput{SourcePDFR2Key: objectKey})
	if err == nil {
		t.Fatal("PrepareAgreementUpload() accepted a cross-tenant object")
	}
}

func TestRedactAgreementSourceRemovesContactAmountsDatesAndPartyLines(t *testing.T) {
	text := `SERVICE AGREEMENT
Customer: Ada Okafor
Email ada@example.com and phone +234 801 234 5678.
Account 3003859147. Total NGN 25,000.
Date August 7, 2026.
The remaining reusable service obligations continue here for enough words to form a useful agreement source document.`
	redacted, literals := redactAgreementSource(text, nil)
	for _, prohibited := range []string{"Ada Okafor", "ada@example.com", "3003859147", "25,000", "August 7, 2026"} {
		if strings.Contains(strings.ToLower(redacted), strings.ToLower(prohibited)) {
			t.Fatalf("redacted text retained %q: %s", prohibited, redacted)
		}
	}
	if len(literals) < 4 {
		t.Fatalf("literals = %#v", literals)
	}
}

func generatedTextPDF(t *testing.T, pages int) []byte {
	t.Helper()
	document := gofpdf.New("P", "mm", "A4", "")
	for page := 0; page < pages; page++ {
		document.AddPage()
		document.SetFont("Helvetica", "", 11)
		document.MultiCell(0, 6, "Service agreement terms between Ada Okafor and the Provider. The parties agree to service scope payment cancellation timing preparation aftercare liability communication and reasonable remedies.", "", "L", false)
	}
	var output bytes.Buffer
	if err := document.Output(&output); err != nil {
		t.Fatalf("generate PDF: %v", err)
	}
	return output.Bytes()
}
