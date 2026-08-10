package repository

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"booking/go-server/internal/agreements/domain"
	aiapi "booking/go-server/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGenerationDraftLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	created, err := repository.CreateGenerationDraft(ctx, CreateGenerationDraftParams{
		ClientID:           clientID,
		Title:              "Integration lash agreement",
		Description:        "Generated during repository verification.",
		Category:           "Beauty",
		Tags:               []string{"Beauty", "Lashes"},
		ConfirmationMethod: domain.ConfirmationMethodConfirmation,
		InputKind:          domain.GenerationInputFields,
		Input: domain.FieldsGenerationInput{
			BusinessCategory:          "Beauty",
			ServiceName:               "Lash extensions",
			IncludeCancellationPolicy: true,
			IncludeLatenessPolicy:     true,
			IncludePaymentTerms:       true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id = $1`, created.FamilyID)
	})

	jobs, err := repository.ClaimGenerationJobs(ctx, "integration-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != created.JobID {
		t.Fatalf("claimed jobs = %#v, want job %s", jobs, created.JobID)
	}

	document, err := domain.FinalizeGeneratedDocument(aiapi.GeneratedDocumentSchema{
		SchemaVersion: aiapi.AgreementDocumentSchemaVersion,
		Blocks: []aiapi.GeneratedAgreementDocumentBlock{
			{
				Type:  aiapi.AgreementBlockHeading,
				Level: 1,
				Content: []aiapi.AgreementInlineNode{
					{Type: aiapi.AgreementInlineText, Text: "Service Agreement"},
				},
			},
			{
				Type: aiapi.AgreementBlockParagraph,
				Content: []aiapi.AgreementInlineNode{
					{Type: aiapi.AgreementInlineText, Text: "The provider will perform "},
					{Type: aiapi.AgreementInlineVariable, Key: "SERVICE_NAME"},
					{Type: aiapi.AgreementInlineText, Text: "."},
				},
			},
			{Type: aiapi.AgreementBlockAcceptance, Method: aiapi.AgreementConfirmation},
		},
	}, domain.ConfirmationMethodConfirmation, domain.AgreementVariableKeySet())
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteGenerationJob(ctx, CompleteGenerationJobParams{
		JobID: created.JobID, WorkerID: "integration-worker",
		SuggestedTitle: "Lash Service Agreement", SuggestedDescription: "Terms for lash services.",
		Document: document,
	}); err != nil {
		t.Fatal(err)
	}

	job, err := repository.GetGenerationJob(ctx, clientID, created.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.JobStatusCompleted || job.CompletedAt == nil {
		t.Fatalf("completed job = %#v", job)
	}
	details, err := repository.GetClientTemplateFamily(ctx, clientID, created.FamilyID)
	if err != nil {
		t.Fatal(err)
	}
	if details.Draft == nil || details.Draft.Document == nil || details.Family.Title != "Lash Service Agreement" {
		t.Fatalf("generated family details = %#v", details)
	}
	if _, err := repository.GetClientTemplateFamily(ctx, uuid.New(), created.FamilyID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant lookup error = %v, want ErrNotFound", err)
	}

	published, err := repository.PublishClientTemplateDraft(ctx, clientID, created.FamilyID)
	if err != nil {
		t.Fatal(err)
	}
	if published.State != domain.TemplateVersionPublished || published.PublishedAt == nil {
		t.Fatalf("published version = %#v", published)
	}
}

func TestDeleteUploadDraftReturnsSourceForCleanup(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	repository := New(pool)
	const sourceKey = "clients/integration/templates/source.pdf"
	created, err := repository.CreateGenerationDraft(ctx, CreateGenerationDraftParams{
		ClientID: clientID, Title: "Upload deletion test", Category: "Services",
		ConfirmationMethod: domain.ConfirmationMethodConfirmation,
		InputKind:          domain.GenerationInputUpload,
		Input: domain.UploadGenerationInput{
			SourcePDFR2Key: sourceKey, SourcePDFFileName: "source.pdf",
			BusinessCategory: "Services", ServiceName: "Consultation",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id=$1`, created.FamilyID)
	})
	items, err := repository.ListClientTemplateFamilies(ctx, clientID, TemplateFamilyListFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range items {
		if item.ID == created.FamilyID {
			found = true
			if item.ServiceUsage != 0 || item.AgreementUsage != 0 {
				t.Fatalf("new draft usage = %d/%d, want 0/0", item.ServiceUsage, item.AgreementUsage)
			}
		}
	}
	if !found {
		t.Fatal("new upload draft was not listed")
	}
	keys, err := repository.DeleteClientTemplateDraftFamily(ctx, clientID, created.FamilyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != sourceKey {
		t.Fatalf("source keys = %#v, want %q", keys, sourceKey)
	}
}
