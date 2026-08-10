package domain

import (
	"testing"
	"time"

	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
)

func TestTemplateVersionAllowsOnlyIncompleteDraftWithoutDocument(t *testing.T) {
	version := TemplateVersion{
		ID: uuid.New(), FamilyID: uuid.New(), VersionNumber: 1, Revision: 1,
		State: TemplateVersionDraft, SourceKind: TemplateSourceAI,
	}
	if err := version.Validate(ConfirmationMethodConfirmation); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	version.State = TemplateVersionPublished
	if err := version.Validate(ConfirmationMethodConfirmation); err == nil {
		t.Fatal("published version omitted document")
	}
}

func TestTemplateVersionValidatesHashAndVariables(t *testing.T) {
	document := hashTestDocument()
	hash, err := TemplateSchemaHash(document, ConfirmationMethodConfirmation)
	if err != nil {
		t.Fatalf("TemplateSchemaHash() error = %v", err)
	}
	now := time.Now()
	version := TemplateVersion{
		ID: uuid.New(), FamilyID: uuid.New(), VersionNumber: 1, Revision: 2,
		State: TemplateVersionPublished, SourceKind: TemplateSourceSystemSeed,
		Document: &document, UsedVariableKeys: document.VariableKeys(),
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion, RendererVersion: 1,
		TemplateSchemaHash: hash, PublishedAt: &now,
	}
	if err := version.Validate(ConfirmationMethodConfirmation); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	version.TemplateSchemaHash = "wrong"
	if err := version.Validate(ConfirmationMethodConfirmation); err == nil {
		t.Fatal("version accepted wrong schema hash")
	}
}
