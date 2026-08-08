package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/shared/ai_api"
)

func TestRenderCompletedPDFIsDeterministicAndSupportsUnicode(t *testing.T) {
	document := resolvedPDFDocument("Ọlá provided the lash service in Lagos.", domain.ConfirmationMethodConfirmation)
	summary := BookingSummary{ServiceName: "Ìtọju lashes", Date: "7 August 2026", TotalAmount: "₦100.00"}
	hash, err := ResolvedTermsHash("Àdéhọlá Service Agreement", summary, document, domain.ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("ResolvedTermsHash() error = %v", err)
	}
	input := PDFInput{
		BusinessName:      "TellBook",
		Title:             "Àdéhọlá Service Agreement",
		BookingSummary:    summary,
		ResolvedDocument:  document,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
		ResolvedTermsHash: hash,
		Acceptance: AcceptanceEvidence{
			Method:     domain.ConfirmationMethodConfirmation,
			AcceptedAt: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		},
	}

	first, err := RenderCompletedPDF(input)
	if err != nil {
		t.Fatalf("RenderCompletedPDF() error = %v", err)
	}
	second, err := RenderCompletedPDF(input)
	if err != nil {
		t.Fatalf("RenderCompletedPDF() second error = %v", err)
	}
	if len(first) < 1000 || !bytes.HasPrefix(first, []byte("%PDF")) {
		t.Fatalf("invalid PDF output of %d bytes", len(first))
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same completed agreement produced different PDF bytes")
	}
}

func TestRenderCompletedPDFRequiresMatchingTermsHash(t *testing.T) {
	document := resolvedPDFDocument("Terms", domain.ConfirmationMethodConfirmation)
	_, err := RenderCompletedPDF(PDFInput{
		Title:             "Agreement",
		ResolvedDocument:  document,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
		ResolvedTermsHash: "wrong",
		Acceptance: AcceptanceEvidence{
			Method:     domain.ConfirmationMethodConfirmation,
			AcceptedAt: time.Now(),
		},
	})
	if err == nil {
		t.Fatal("RenderCompletedPDF() expected terms hash error")
	}
}

func TestRenderCompletedPDFRequiresSignatureEvidence(t *testing.T) {
	document := resolvedPDFDocument("Terms", domain.ConfirmationMethodSignature)
	hash, err := ResolvedTermsHash("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature)
	if err != nil {
		t.Fatalf("ResolvedTermsHash() error = %v", err)
	}
	_, err = RenderCompletedPDF(PDFInput{
		Title:             "Agreement",
		ResolvedDocument:  document,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
		ResolvedTermsHash: hash,
		Acceptance: AcceptanceEvidence{
			Method:     domain.ConfirmationMethodSignature,
			SignerName: "Customer",
			AcceptedAt: time.Now(),
		},
	})
	if err == nil {
		t.Fatal("RenderCompletedPDF() expected missing signature error")
	}
}

func TestRenderCompletedPDFRejectsInvalidSignaturePNG(t *testing.T) {
	document := resolvedPDFDocument("Terms", domain.ConfirmationMethodSignature)
	hash, err := ResolvedTermsHash("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature)
	if err != nil {
		t.Fatalf("ResolvedTermsHash() error = %v", err)
	}
	_, err = RenderCompletedPDF(PDFInput{
		Title:             "Agreement",
		ResolvedDocument:  document,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
		ResolvedTermsHash: hash,
		Acceptance: AcceptanceEvidence{
			Method:       domain.ConfirmationMethodSignature,
			SignerName:   "Customer",
			SignaturePNG: []byte("not a png"),
			AcceptedAt:   time.Now(),
		},
	})
	if err == nil {
		t.Fatal("RenderCompletedPDF() accepted invalid signature PNG")
	}
}

func TestRenderCompletedPDFIncludesSignature(t *testing.T) {
	document := resolvedPDFDocument("Terms", domain.ConfirmationMethodSignature)
	hash, err := ResolvedTermsHash("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature)
	if err != nil {
		t.Fatalf("ResolvedTermsHash() error = %v", err)
	}
	canvas := image.NewNRGBA(image.Rect(0, 0, 80, 30))
	for x := 10; x < 70; x++ {
		canvas.Set(x, 15, color.Black)
	}
	var signature bytes.Buffer
	if err := png.Encode(&signature, canvas); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	result, err := RenderCompletedPDF(PDFInput{
		Title:             "Agreement",
		ResolvedDocument:  document,
		SchemaVersion:     aiapi.AgreementDocumentSchemaVersion,
		RendererVersion:   RendererVersion,
		ResolvedTermsHash: hash,
		Acceptance: AcceptanceEvidence{
			Method:       domain.ConfirmationMethodSignature,
			SignerName:   "Customer",
			SignaturePNG: signature.Bytes(),
			AcceptedAt:   time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("RenderCompletedPDF() error = %v", err)
	}
	if len(result) < 1000 {
		t.Fatalf("PDF bytes = %d", len(result))
	}
}

func resolvedPDFDocument(text string, method domain.ConfirmationMethod) aiapi.DocumentSchema {
	return aiapi.DocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.AgreementDocumentBlock{
			{
				ID:      "11111111-1111-4111-8111-111111111111",
				Type:    aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{{Type: aiapi.AgreementInlineText, Text: text}},
			},
			{
				ID:     "22222222-2222-4222-8222-222222222222",
				Type:   aiapi.AgreementBlockAcceptance,
				Method: method.AIAPIValue(),
			},
		},
	}
}
