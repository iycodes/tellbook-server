package render

import (
	"errors"
	"strings"
	"testing"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
)

func TestBuildSnapshotResolvesEscapesAndHashesVisibleTerms(t *testing.T) {
	document := renderTestDocument()
	snapshot, err := BuildSnapshot(
		"Lash Service Agreement",
		BookingSummary{ServiceName: "Classic lashes", Date: "18 August 2026"},
		document,
		domain.ConfirmationMethodSignature,
		map[string]string{"CUSTOMER_NAME": "Ada <Okafor>"},
	)
	if err != nil {
		t.Fatalf("BuildSnapshot returned error: %v", err)
	}
	if strings.Contains(snapshot.RenderedHTML, "<Okafor>") || !strings.Contains(snapshot.RenderedHTML, "Ada &lt;Okafor&gt;") {
		t.Fatalf("HTML was not escaped: %s", snapshot.RenderedHTML)
	}
	if !strings.Contains(snapshot.RenderedHTML, `data-method="signature"`) {
		t.Fatalf("acceptance marker missing: %s", snapshot.RenderedHTML)
	}
	if !strings.Contains(snapshot.RenderedHTML, "Classic lashes") || !strings.Contains(snapshot.RenderedHTML, "18 August 2026") {
		t.Fatalf("booking summary missing from customer HTML: %s", snapshot.RenderedHTML)
	}
	if len(snapshot.ResolvedTermsHash) != 64 || snapshot.RendererVersion != RendererVersion {
		t.Fatalf("invalid snapshot metadata: %#v", snapshot)
	}
	if len(snapshot.ResolvedDocument.VariableKeys()) != 0 {
		t.Fatalf("resolved document still contains variables: %#v", snapshot.ResolvedDocument.VariableKeys())
	}
}

func TestResolvedTermsHashIgnoresBlockIDs(t *testing.T) {
	document := renderTestDocument()
	first, err := BuildSnapshot("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature, map[string]string{"CUSTOMER_NAME": "Ada"})
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	for index := range document.Blocks {
		document.Blocks[index].ID = uuid.NewString()
	}
	second, err := BuildSnapshot("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature, map[string]string{"CUSTOMER_NAME": "Ada"})
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if first.ResolvedTermsHash != second.ResolvedTermsHash {
		t.Fatalf("block IDs changed semantic hash: %s != %s", first.ResolvedTermsHash, second.ResolvedTermsHash)
	}
}

func TestResolvedTermsHashChangesWithVisibleTerms(t *testing.T) {
	document := renderTestDocument()
	first, err := BuildSnapshot("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature, map[string]string{"CUSTOMER_NAME": "Ada"})
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	second, err := BuildSnapshot("Agreement", BookingSummary{}, document, domain.ConfirmationMethodSignature, map[string]string{"CUSTOMER_NAME": "Bola"})
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if first.ResolvedTermsHash == second.ResolvedTermsHash {
		t.Fatal("different resolved terms produced the same hash")
	}
}

func TestArtifactSHA256HashesFinalBytesIndependently(t *testing.T) {
	first := ArtifactSHA256([]byte("pdf-one"))
	second := ArtifactSHA256([]byte("pdf-two"))
	if len(first) != 64 || first == second {
		t.Fatalf("artifact hashes = %q, %q", first, second)
	}
}

func TestResolveDocumentReturnsSortedMissingVariables(t *testing.T) {
	document := renderTestDocument()
	document.Blocks[0].Content = append(document.Blocks[0].Content,
		aiapi.AgreementInlineNode{Type: aiapi.AgreementInlineVariable, Key: "BUSINESS_NAME"},
	)
	_, err := ResolveDocument(document, domain.ConfirmationMethodSignature, map[string]string{})
	var missing *MissingVariablesError
	if !errors.As(err, &missing) {
		t.Fatalf("expected MissingVariablesError, got %v", err)
	}
	if len(missing.Keys) != 2 || missing.Keys[0] != "BUSINESS_NAME" || missing.Keys[1] != "CUSTOMER_NAME" {
		t.Fatalf("missing keys = %#v", missing.Keys)
	}
}

func renderTestDocument() aiapi.DocumentSchema {
	return aiapi.DocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.AgreementDocumentBlock{
			{
				ID:   uuid.NewString(),
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{
					{Type: aiapi.AgreementInlineText, Text: "Customer: "},
					{Type: aiapi.AgreementInlineVariable, Key: "CUSTOMER_NAME", Bold: true},
				},
			},
			{ID: uuid.NewString(), Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementSignature},
		},
	}
}
