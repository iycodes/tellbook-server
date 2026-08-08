package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	agreementservice "booking/go-server/internal/agreements/service"
	"booking/go-server/internal/secure"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicQuoteBookingRoundTrip(t *testing.T) {
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

	var serviceID uuid.UUID
	var slug string
	if err := pool.QueryRow(ctx, `
		SELECT s.id, cph.handle_slug
		FROM services s
		INNER JOIN client_profile_handles cph ON cph.client_id = s.client_id
		WHERE s.status = 'published' AND s.fulfillment_mode = 'provider_location'
		ORDER BY s.created_at ASC
		LIMIT 1
	`).Scan(&serviceID, &slug); err != nil {
		t.Fatal(err)
	}

	var selectedStart string
	for offset := 1; offset <= 14 && selectedStart == ""; offset++ {
		date := time.Now().AddDate(0, 0, offset)
		availability, err := repo.GetPublicAvailability(ctx, slug, serviceID, date)
		if err != nil {
			t.Fatal(err)
		}
		if len(availability.Slots) > 0 {
			selectedStart = availability.Slots[0].StartAt
		}
	}
	if selectedStart == "" {
		t.Fatal("expected a seeded future availability slot")
	}

	email := fmt.Sprintf("quote-%s@example.com", uuid.NewString())
	name := "Quote Integration Customer"
	phone := "+15555550199"
	quote, err := repo.CreatePublicBookingQuote(ctx, slug, CreatePublicBookingQuoteInput{
		ServiceID: serviceID.String(), StartsAt: selectedStart,
		CustomerName: name, CustomerEmail: email, CustomerPhone: phone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if quote.QuoteToken == "" || quote.TotalAmountMinor <= 0 || quote.DepositAmountMinor != quote.TotalAmountMinor {
		t.Fatalf("unexpected quote: %#v", quote)
	}

	expiredEmail := fmt.Sprintf("expired-quote-%s@example.com", uuid.NewString())
	expiredQuote, err := repo.CreatePublicBookingQuote(ctx, slug, CreatePublicBookingQuoteInput{
		ServiceID: serviceID.String(), StartsAt: selectedStart,
		CustomerName: "Expired Quote Customer", CustomerEmail: expiredEmail,
		CustomerPhone: "+15555550200",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE booking_quotes
		SET created_at = NOW() - INTERVAL '2 minutes',
			expires_at = NOW() - INTERVAL '1 minute'
		WHERE public_token = $1
	`, expiredQuote.QuoteToken); err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreatePublicBooking(ctx, slug, CreatePublicBookingInput{
		QuoteToken: expiredQuote.QuoteToken,
		FullName:   "Expired Quote Customer",
		Email:      expiredEmail,
		Phone:      "+15555550200",
	})
	if !errors.Is(err, ErrQuoteExpired) {
		t.Fatalf("expected expired quote error, got %v", err)
	}

	input := CreatePublicBookingInput{
		QuoteToken: quote.QuoteToken,
		FullName:   name,
		Email:      email,
		Phone:      phone,
	}
	booking, err := repo.CreatePublicBooking(ctx, slug, input)
	if err != nil {
		t.Fatal(err)
	}
	if booking.TotalAmountMinor != quote.TotalAmountMinor || booking.FulfillmentMode != "provider_location" {
		t.Fatalf("booking does not match quote: %#v %#v", booking, quote)
	}
	repeated, err := repo.CreatePublicBooking(ctx, slug, input)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.BookingToken != booking.BookingToken {
		t.Fatalf("idempotent quote consumption created another booking: %q != %q", repeated.BookingToken, booking.BookingToken)
	}

	t.Cleanup(func() {
		bookingID := uuid.MustParse(booking.BookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM promotion_redemptions WHERE booking_id = $1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_instances WHERE booking_id = $1`, bookingID)
		_, _ = pool.Exec(ctx, `UPDATE booking_quotes SET booking_id = NULL, consumed_at = NULL WHERE public_token = $1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id = $1`, bookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE public_token = $1`, quote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE public_token = $1`, expiredQuote.QuoteToken)
		_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE email = $1`, email)
	})
}

func TestAfterPaymentAgreementLifecycle(t *testing.T) {
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

	var serviceID, clientID uuid.UUID
	var slug string
	var previousFamilyID *uuid.UUID
	var previousTiming *string
	var previousStandalone bool
	if err := pool.QueryRow(ctx, `
		SELECT s.id, s.client_id, cph.handle_slug, s.agreement_template_family_id,
		       s.agreement_timing, s.standalone_signature_required
		FROM services s
		INNER JOIN client_profile_handles cph ON cph.client_id = s.client_id
		WHERE s.status = 'published' AND s.fulfillment_mode = 'provider_location'
		ORDER BY s.created_at ASC
		LIMIT 1
	`).Scan(
		&serviceID, &clientID, &slug, &previousFamilyID, &previousTiming, &previousStandalone,
	); err != nil {
		t.Fatal(err)
	}

	templates, err := agreementseed.SystemTemplates()
	if err != nil {
		t.Fatal(err)
	}
	template := templates[0]
	familyID := uuid.New()
	versionID := uuid.New()
	documentJSON, err := json.Marshal(template.Document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agreement_template_families (
			id, client_id, owner_type, title, description, category, tags,
			confirmation_method, status, created_by_client_id, created_at, updated_at
		) VALUES ($1,$2,'client',$3,$4,$5,$6,$7,'published',$2,NOW(),NOW())
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
		UPDATE agreement_template_families SET current_published_version_id = $2 WHERE id = $1
	`, familyID, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE services
		SET agreement_template_family_id = $2, agreement_timing = 'after_payment',
		    standalone_signature_required = FALSE, updated_at = NOW()
		WHERE id = $1
	`, serviceID, familyID); err != nil {
		t.Fatal(err)
	}

	var bookingID uuid.UUID
	var quoteToken string
	t.Cleanup(func() {
		if bookingID != uuid.Nil {
			_, _ = pool.Exec(ctx, `DELETE FROM agreement_instances WHERE booking_id = $1`, bookingID)
			_, _ = pool.Exec(ctx, `UPDATE booking_quotes SET booking_id = NULL, consumed_at = NULL WHERE booking_id = $1`, bookingID)
			_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE id = $1`, bookingID)
		}
		if quoteToken != "" {
			_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE public_token = $1`, quoteToken)
		}
		_, _ = pool.Exec(ctx, `
			UPDATE services
			SET agreement_template_family_id = $2, agreement_timing = $3,
			    standalone_signature_required = $4, updated_at = NOW()
			WHERE id = $1
		`, serviceID, previousFamilyID, previousTiming, previousStandalone)
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id = $1`, familyID)
	})

	selectedStart := firstFuturePublicSlot(t, ctx, repo, slug, serviceID)
	email := fmt.Sprintf("agreement-%s@example.com", uuid.NewString())
	name := "After Payment Customer"
	phone := "+15555550300"
	quote, err := repo.CreatePublicBookingQuote(ctx, slug, CreatePublicBookingQuoteInput{
		ServiceID: serviceID.String(), StartsAt: selectedStart,
		CustomerName: name, CustomerEmail: email, CustomerPhone: phone,
	})
	if err != nil {
		t.Fatal(err)
	}
	quoteToken = quote.QuoteToken
	if quote.Agreement == nil || quote.Agreement.Timing != "after_payment" || quote.Agreement.ResolvedTermsHash == "" {
		t.Fatalf("quote did not snapshot the after-payment agreement: %#v", quote.Agreement)
	}
	booking, err := repo.CreatePublicBooking(ctx, slug, CreatePublicBookingInput{
		QuoteToken: quote.QuoteToken, FullName: name, Email: email, Phone: phone,
	})
	if err != nil {
		t.Fatal(err)
	}
	bookingID = uuid.MustParse(booking.BookingID)
	if _, err := pool.Exec(ctx, `UPDATE bookings SET payment_status = 'paid_in_full' WHERE id = $1`, bookingID); err != nil {
		t.Fatal(err)
	}

	prepared, err := repo.PreparePublicBookingAgreement(ctx, booking.BookingToken)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.PublicToken == "" || prepared.Status != "awaiting_customer" {
		t.Fatalf("unexpected prepared agreement: %#v", prepared)
	}
	publicAgreement, err := repo.GetPublicAgreementByToken(ctx, prepared.PublicToken)
	if err != nil {
		t.Fatal(err)
	}
	if publicAgreement.ResolvedTermsHash != quote.Agreement.ResolvedTermsHash {
		t.Fatalf("agreement hash changed after booking: %q != %q", publicAgreement.ResolvedTermsHash, quote.Agreement.ResolvedTermsHash)
	}
	completed, err := repo.AcceptPublicAgreementByToken(ctx, prepared.PublicToken, PublicAgreementAcceptInput{Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" || completed.AcceptedAt == nil {
		t.Fatalf("agreement was not completed: %#v", completed)
	}
	repeated, err := repo.AcceptPublicAgreementByToken(ctx, prepared.PublicToken, PublicAgreementAcceptInput{Accepted: true})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID != completed.ID || repeated.Status != "completed" {
		t.Fatalf("repeated acceptance was not idempotent: %#v", repeated)
	}

	var acceptanceCount, jobCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agreement_acceptances WHERE agreement_id = $1`, completed.ID).Scan(&acceptanceCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agreement_jobs WHERE agreement_id = $1`, completed.ID).Scan(&jobCount); err != nil {
		t.Fatal(err)
	}
	if acceptanceCount != 1 || jobCount != 3 {
		t.Fatalf("unexpected lifecycle records: acceptances=%d jobs=%d", acceptanceCount, jobCount)
	}
	if completed.PDFStatus != "queued" {
		t.Fatalf("completed PDF status = %q, want queued", completed.PDFStatus)
	}
	artifactStatus, _, err := repo.GetPublicAgreementPDFArtifact(ctx, prepared.PublicToken)
	if err != nil || artifactStatus != "queued" {
		t.Fatalf("public PDF artifact = %q, %v; want queued", artifactStatus, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agreement_jobs SET status='failed'
		WHERE agreement_id=$1 AND kind IN ('render_completed_pdf','send_completed_email')
	`, completed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agreement_instances SET pdf_status='failed' WHERE id=$1`, completed.ID); err != nil {
		t.Fatal(err)
	}
	details, err := repo.GetManagedAgreement(ctx, clientID, uuid.MustParse(completed.ID))
	if err != nil || !details.ProcessingFailed {
		t.Fatalf("processing failure not exposed: %#v, %v", details, err)
	}
	if err := repo.RetryManagedAgreementProcessing(ctx, clientID, uuid.MustParse(completed.ID)); err != nil {
		t.Fatal(err)
	}
	var failedJobs int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM agreement_jobs
		WHERE agreement_id=$1 AND status='failed'
	`, completed.ID).Scan(&failedJobs); err != nil {
		t.Fatal(err)
	}
	if failedJobs != 0 {
		t.Fatalf("failed processing jobs after retry = %d", failedJobs)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM customers WHERE client_id = $1 AND email = $2`, clientID, email)
}

func newTestAgreementTokenManager(t *testing.T) *agreementservice.PublicTokenManager {
	t.Helper()
	keyring, err := secure.ParseKeyring(`{"test":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`, "test")
	if err != nil {
		t.Fatal(err)
	}
	manager, err := agreementservice.NewPublicTokenManager(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func firstFuturePublicSlot(t *testing.T, ctx context.Context, repo *Repository, slug string, serviceID uuid.UUID) string {
	t.Helper()
	for offset := 1; offset <= 14; offset++ {
		availability, err := repo.GetPublicAvailability(ctx, slug, serviceID, time.Now().AddDate(0, 0, offset))
		if err != nil {
			t.Fatal(err)
		}
		if len(availability.Slots) > 0 {
			return availability.Slots[0].StartAt
		}
	}
	t.Fatal("expected a seeded future availability slot")
	return ""
}

func TestHaversineDistanceMeters(t *testing.T) {
	distance := haversineDistanceMeters(6.5244, 3.3792, 6.6018, 3.3515)
	if distance < 9000 || distance > 10000 {
		t.Fatalf("unexpected Lagos distance: %d", distance)
	}
}
