package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManualAgreementCreateActivateAndSend(t *testing.T) {
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
	repo := NewRepository(pool)
	repo.ConfigureAgreementTokens(newTestAgreementTokenManager(t))

	var clientID, bookingID, customerID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT b.client_id, b.id, b.customer_id
		FROM bookings b
		INNER JOIN customers c ON c.id = b.customer_id AND c.email <> ''
		ORDER BY b.created_at DESC
		LIMIT 1
	`).Scan(&clientID, &bookingID, &customerID); err != nil {
		t.Fatal(err)
	}
	templates, err := agreementseed.SystemTemplates()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[0]
	familyID, versionID := uuid.New(), uuid.New()
	documentJSON, err := json.Marshal(template.Document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, current_published_version_id,
			created_by_client_id, created_at, updated_at
		) VALUES ($1,$2,'client',$3,$4,$5,$6,$7,'published',NULL,$2,NOW(),NOW())
	`, familyID, clientID, template.Title, template.Description, template.Category,
		template.Tags, template.ConfirmationMethod); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agreement_template_versions (
			id, family_id, version_number, state, document_schema, used_variable_keys,
			schema_version, renderer_version, source_kind, template_schema_hash,
			revision, published_at, created_by_client_id, created_at, updated_at
		) VALUES ($1,$2,1,'published',$3,$4,$5,$6,'system_seed',$7,1,NOW(),$8,NOW(),NOW())
	`, versionID, familyID, documentJSON, template.UsedVariableKeys,
		template.Document.SchemaVersion, agreementrender.RendererVersion,
		template.TemplateSchemaHash, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agreement_template_families SET current_published_version_id=$2 WHERE id=$1
	`, familyID, versionID); err != nil {
		t.Fatal(err)
	}

	agreementIDs := make([]uuid.UUID, 0, 2)
	t.Cleanup(func() {
		for _, agreementID := range agreementIDs {
			_, _ = pool.Exec(ctx, `DELETE FROM agreement_instances WHERE id=$1`, agreementID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id=$1`, familyID)
	})

	_, err = repo.CreateManagedAgreement(ctx, clientID, CreateManagedAgreementInput{
		CustomerID: customerID.String(), TemplateFamilyID: familyID.String(),
	})
	var missing *MissingAgreementVariablesError
	if !errors.As(err, &missing) || len(missing.Keys) == 0 {
		t.Fatalf("customer-only agreement error = %v, want missing variables", err)
	}
	provided := make(map[string]string, len(missing.Keys))
	for _, key := range missing.Keys {
		provided[key] = "Provided " + key
	}
	customerOnly, err := repo.CreateManagedAgreement(ctx, clientID, CreateManagedAgreementInput{
		CustomerID: customerID.String(), TemplateFamilyID: familyID.String(), Values: provided,
	})
	if err != nil {
		t.Fatal(err)
	}
	agreementIDs = append(agreementIDs, uuid.MustParse(customerOnly.ID))

	agreement, err := repo.CreateManagedAgreement(ctx, clientID, CreateManagedAgreementInput{
		CustomerID:       customerID.String(),
		BookingID:        bookingID.String(),
		TemplateFamilyID: familyID.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	agreementID := uuid.MustParse(agreement.ID)
	agreementIDs = append(agreementIDs, agreementID)
	if agreement.Status != "draft" || agreement.Timing != "manual" || agreement.ResolvedTermsHash == "" {
		t.Fatalf("unexpected manual agreement: %#v", agreement)
	}
	token, err := repo.ActivateManagedAgreementLink(ctx, clientID, agreementID)
	if err != nil {
		t.Fatal(err)
	}
	publicAgreement, err := repo.GetPublicAgreementByToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if publicAgreement.Status != "awaiting_customer" || publicAgreement.ResolvedTermsHash != agreement.ResolvedTermsHash {
		t.Fatalf("unexpected public agreement: %#v", publicAgreement)
	}
	if err := repo.SendManagedAgreement(ctx, clientID, agreementID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SendManagedAgreement(ctx, clientID, agreementID); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agreement_jobs
		WHERE agreement_id=$1 AND kind='send_agreement_email'
	`, agreementID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("initial email jobs = %d, want 1", jobs)
	}
}
