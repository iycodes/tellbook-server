package appdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	agreementservice "booking/go-server/internal/agreements/service"
	"booking/go-server/internal/secure"

	"github.com/go-chi/chi/v5"
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
	bookingApplication := repo.BookingApplication()

	var serviceID uuid.UUID
	var clientID uuid.UUID
	var slug string
	if err := pool.QueryRow(ctx, `
		SELECT s.id, s.client_id, cp.handle_slug
		FROM services s
		INNER JOIN client_profiles cp ON cp.client_id = s.client_id
		WHERE s.status = 'published' AND s.fulfillment_mode = 'provider_location'
		  AND s.agreement_timing IS NULL AND NOT s.standalone_signature_required
		  AND cp.marketplace_enabled AND cp.market_configured_at IS NOT NULL
		ORDER BY s.created_at ASC
		LIMIT 1
	`).Scan(&serviceID, &clientID, &slug); err != nil {
		t.Fatal(err)
	}
	historicalHandle := "old-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profile_handles (handle_slug, client_id, created_at, updated_at)
		VALUES ($1,$2,NOW(),NOW())
	`, historicalHandle, clientID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM client_profile_handles WHERE handle_slug=$1`, historicalHandle)
	})

	rangeAvailability, err := bookingApplication.SearchAvailability(ctx, SearchBookingAvailabilityCommand{
		ProviderHandle: slug, ServiceID: serviceID, Days: 14,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeAvailability.Dates) != 14 || rangeAvailability.Days != 14 || rangeAvailability.From == "" {
		t.Fatalf("unexpected availability range: %#v", rangeAvailability)
	}
	var selectedStart string
	for _, day := range rangeAvailability.Dates {
		if len(day.Slots) > 0 {
			selectedStart = day.Slots[0].StartAt
			selectedDate, parseErr := time.Parse("2006-01-02", day.Date)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			singleDay, singleErr := repo.GetPublicAvailability(ctx, slug, serviceID, selectedDate)
			if singleErr != nil {
				t.Fatal(singleErr)
			}
			if len(singleDay.Slots) != len(day.Slots) || singleDay.Slots[0].StartAt != day.Slots[0].StartAt {
				t.Fatalf("range day does not match single-day availability: %#v %#v", day, singleDay)
			}
			break
		}
	}
	if selectedStart == "" {
		t.Fatal("expected a seeded future availability slot")
	}
	selectedStartAt, err := time.Parse(time.RFC3339, selectedStart)
	if err != nil {
		t.Fatal(err)
	}

	email := fmt.Sprintf("quote-%s@example.com", uuid.NewString())
	name := "Quote Integration Customer"
	phone := "+15555550199"
	marketplaceCustomerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id,full_name,email,email_verified_at)
		VALUES ($1,$2,$3,NOW())
	`, marketplaceCustomerID, name, email); err != nil {
		t.Fatal(err)
	}
	preBookingConversation, err := repo.GetOrCreateMarketplaceProviderConversation(
		ctx, marketplaceCustomerID, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := uuid.MustParse(preBookingConversation.Detail.Conversation.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE id=$1`, conversationID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
	})
	requirements, err := bookingApplication.GetRequirements(ctx, slug, serviceID)
	if err != nil {
		t.Fatal(err)
	}
	if requirements.ProviderID != clientID || requirements.ServiceID != serviceID ||
		!requirements.PaymentRequired || !requirements.Autopilot.Eligible ||
		requirements.Autopilot.BlockReason != "" {
		t.Fatalf("unexpected shared booking requirements: %#v", requirements)
	}
	canonicalLink, err := bookingApplication.BuildCanonicalBookingLink(ctx, BuildCanonicalBookingLinkCommand{
		MarketplaceCustomerID: marketplaceCustomerID,
		ConversationID:        conversationID,
		ServiceID:             &serviceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if canonicalLink.ProviderID != clientID || canonicalLink.ConversationAttributionID != conversationID ||
		canonicalLink.Href != "/booking/checkout?provider="+url.QueryEscape(slug)+"&service="+serviceID.String() ||
		strings.Contains(canonicalLink.Href, conversationID.String()) || strings.Contains(canonicalLink.Href, email) {
		t.Fatalf("unsafe or incorrect canonical booking link: %#v", canonicalLink)
	}
	providerLink, err := bookingApplication.BuildCanonicalBookingLink(ctx, BuildCanonicalBookingLinkCommand{
		MarketplaceCustomerID: marketplaceCustomerID,
		ConversationID:        conversationID,
	})
	if err != nil || providerLink.Href != "/providers/"+url.PathEscape(slug) {
		t.Fatalf("provider booking link = %#v, error=%v", providerLink, err)
	}
	if _, err := bookingApplication.BuildCanonicalBookingLink(ctx, BuildCanonicalBookingLinkCommand{
		MarketplaceCustomerID: uuid.New(), ConversationID: conversationID, ServiceID: &serviceID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-customer booking link error = %v, want ErrNotFound", err)
	}
	quoteInput := CreatePublicBookingQuoteInput{
		IdempotencyKey: uuid.NewString(),
		ServiceID:      serviceID.String(), StartsAt: selectedStart,
		CustomerName: name, CustomerEmail: email, CustomerPhone: phone,
		BookingNotes: "Integration booking",
	}
	type quoteResult struct {
		quote PublicBookingQuoteResponse
		err   error
	}
	results := make(chan quoteResult, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			created, createErr := bookingApplication.CreateQuote(ctx, CreateBookingQuoteCommand{
				ProviderHandle: slug, IdempotencyKey: uuid.MustParse(quoteInput.IdempotencyKey),
				ServiceID: serviceID, StartsAt: selectedStartAt,
				Customer: BookingCustomerDetails{
					FullName: name, Email: email, Phone: phone, Notes: "Integration booking",
				},
			})
			results <- quoteResult{quote: created, err: createErr}
		}()
	}
	close(start)
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent idempotent quote errors = %v, %v", firstResult.err, secondResult.err)
	}
	quote := firstResult.quote
	if quote.QuoteToken != secondResult.quote.QuoteToken {
		t.Fatalf("concurrent idempotent quote tokens = %q, %q", quote.QuoteToken, secondResult.quote.QuoteToken)
	}
	if quote.QuoteToken == "" || quote.TotalAmountMinor <= 0 || quote.DepositAmountMinor != quote.TotalAmountMinor {
		t.Fatalf("unexpected quote: %#v", quote)
	}
	if quote.SlotHeld || !quote.AvailabilityRevalidated {
		t.Fatalf("quote hold semantics are ambiguous: %#v", quote)
	}
	replayedQuote, err := repo.CreatePublicBookingQuote(ctx, slug, quoteInput)
	if err != nil {
		t.Fatal(err)
	}
	if !replayedQuote.IdempotentReplay || replayedQuote.QuoteToken != quote.QuoteToken {
		t.Fatalf("idempotent quote replay = %#v, want original quote token %q", replayedQuote, quote.QuoteToken)
	}
	conflictingInput := quoteInput
	conflictingInput.BookingNotes = "Different request with the same key"
	if _, err := repo.CreatePublicBookingQuote(ctx, slug, conflictingInput); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same idempotency key with different input error = %v, want ErrIdempotencyConflict", err)
	}

	expiredEmail := fmt.Sprintf("expired-quote-%s@example.com", uuid.NewString())
	expiredQuote, err := repo.CreatePublicBookingQuote(ctx, slug, CreatePublicBookingQuoteInput{
		IdempotencyKey: uuid.NewString(),
		ServiceID:      serviceID.String(), StartsAt: selectedStart,
		CustomerName: "Expired Quote Customer", CustomerEmail: expiredEmail,
		CustomerPhone: "+15555550200", BookingNotes: "Expired integration booking",
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
		QuoteToken:            quote.QuoteToken,
		Source:                "marketplace",
		FullName:              name,
		Email:                 email,
		Phone:                 phone,
		Notes:                 "Integration booking",
		EmailReminderConsent:  true,
		WhatsAppConsent:       true,
		MarketplaceCustomerID: &marketplaceCustomerID,
	}
	reservation, err := bookingApplication.CreateReservation(ctx, CreateBookingReservationCommand{
		ProviderHandle: slug, QuoteToken: input.QuoteToken,
		Authority: BookingReservationAuthorityAutopilot, Source: input.Source,
		Customer: BookingCustomerDetails{
			FullName: input.FullName, Email: input.Email, Phone: input.Phone, Notes: input.Notes,
		},
		NotificationConsent:   BookingNotificationConsent{EmailReminder: true, WhatsApp: true},
		MarketplaceCustomerID: &marketplaceCustomerID,
	})
	if !errors.Is(err, ErrAutopilotReservationAuthorityRequired) {
		t.Fatalf("unauthorized paid autopilot reservation error = %v, want missing authority", err)
	}
	var rejectedQuoteBookingID *uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT booking_id FROM booking_quotes WHERE public_token=$1
	`, quote.QuoteToken).Scan(&rejectedQuoteBookingID); err != nil {
		t.Fatal(err)
	}
	if rejectedQuoteBookingID != nil {
		t.Fatalf("rejected autopilot consumed quote with booking %s", rejectedQuoteBookingID.String())
	}

	reservation, err = bookingApplication.CreateReservation(ctx, CreateBookingReservationCommand{
		ProviderHandle: slug, QuoteToken: input.QuoteToken,
		Authority: BookingReservationAuthorityPublic, Source: input.Source,
		Customer: BookingCustomerDetails{
			FullName: input.FullName, Email: input.Email, Phone: input.Phone, Notes: input.Notes,
		},
		NotificationConsent:   BookingNotificationConsent{EmailReminder: true, WhatsApp: true},
		MarketplaceCustomerID: &marketplaceCustomerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	booking := reservation.Booking
	if reservation.Lifecycle.NextStep != BookingNextStepPayment {
		t.Fatalf("reservation next step = %q, want payment", reservation.Lifecycle.NextStep)
	}
	if booking.TotalAmountMinor != quote.TotalAmountMinor || booking.FulfillmentMode != "provider_location" {
		t.Fatalf("booking does not match quote: %#v %#v", booking, quote)
	}
	if booking.Source != "marketplace" || !booking.WhatsAppConsent ||
		!booking.DeliveryStatus.CustomerWhatsApp.Requested ||
		booking.DeliveryStatus.CustomerWhatsApp.Eligible ||
		booking.DeliveryStatus.CustomerWhatsApp.Status != "not_requested" {
		t.Fatalf("booking origin or consent was not preserved: %#v", booking)
	}
	if !booking.EmailReminderConsent {
		t.Fatalf("email reminder consent was not preserved: %#v", booking)
	}
	var snapshotEmail, snapshotWhatsApp, emailSource, whatsappSource string
	var emailConsentAt, whatsappConsentAt *time.Time
	var consentPolicyRevision int
	if err := pool.QueryRow(ctx, `
		SELECT customer_email_snapshot, customer_whatsapp_e164_snapshot,
			email_reminder_consent_at, whatsapp_consent_at,
			email_reminder_consent_source, whatsapp_consent_source,
			notification_consent_policy_revision
		FROM bookings WHERE id=$1
	`, booking.BookingID).Scan(
		&snapshotEmail, &snapshotWhatsApp, &emailConsentAt, &whatsappConsentAt,
		&emailSource, &whatsappSource, &consentPolicyRevision,
	); err != nil {
		t.Fatal(err)
	}
	if snapshotEmail != strings.ToLower(email) || snapshotWhatsApp != phone ||
		emailConsentAt == nil || whatsappConsentAt == nil ||
		emailSource != "marketplace_checkout" || whatsappSource != "marketplace_checkout" ||
		consentPolicyRevision != 1 {
		t.Fatalf("invalid immutable notification snapshot: email=%q whatsapp=%q email_at=%v whatsapp_at=%v sources=%q/%q revision=%d",
			snapshotEmail, snapshotWhatsApp, emailConsentAt, whatsappConsentAt,
			emailSource, whatsappSource, consentPolicyRevision)
	}
	replayedBooking, err := repo.CreatePublicBooking(ctx, slug, input)
	if err != nil || replayedBooking.BookingToken != booking.BookingToken {
		t.Fatalf("identical booking replay = token %q err=%v, want %q", replayedBooking.BookingToken, err, booking.BookingToken)
	}
	conflictingReplay := input
	conflictingReplay.WhatsAppConsent = false
	if _, err := repo.CreatePublicBooking(ctx, slug, conflictingReplay); !errors.Is(err, ErrBookingReplayConflict) {
		t.Fatalf("changed-consent replay error = %v, want ErrBookingReplayConflict", err)
	}
	var linkedCustomerID uuid.UUID
	var linkedBookingCount int
	if err := pool.QueryRow(ctx, `
		SELECT conversation.customer_id,
			(SELECT COUNT(*) FROM inbox_conversation_bookings link
			 WHERE link.conversation_id=conversation.id AND link.booking_id=$2)
		FROM inbox_conversations conversation
		WHERE conversation.id=$1
	`, conversationID, booking.BookingID).Scan(&linkedCustomerID, &linkedBookingCount); err != nil {
		t.Fatal(err)
	}
	if linkedCustomerID == uuid.Nil || linkedBookingCount != 1 {
		t.Fatalf("pre-booking conversation link = customer %s/bookings %d, want customer/1", linkedCustomerID, linkedBookingCount)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE customers SET email='changed@example.com', phone='+15555550999' WHERE id=$1
	`, linkedCustomerID); err != nil {
		t.Fatal(err)
	}
	var snapshotEmailAfterCRMChange, snapshotWhatsAppAfterCRMChange string
	if err := pool.QueryRow(ctx, `
		SELECT customer_email_snapshot, customer_whatsapp_e164_snapshot
		FROM bookings WHERE id=$1
	`, booking.BookingID).Scan(&snapshotEmailAfterCRMChange, &snapshotWhatsAppAfterCRMChange); err != nil {
		t.Fatal(err)
	}
	if snapshotEmailAfterCRMChange != snapshotEmail || snapshotWhatsAppAfterCRMChange != snapshotWhatsApp {
		t.Fatalf("mutable CRM contact redirected booking snapshot: %q/%q -> %q/%q",
			snapshotEmail, snapshotWhatsApp, snapshotEmailAfterCRMChange, snapshotWhatsAppAfterCRMChange)
	}
	if _, err := pool.Exec(ctx, `UPDATE customers SET email=$2, phone=$3 WHERE id=$1`, linkedCustomerID, email, phone); err != nil {
		t.Fatal(err)
	}
	providerBookingPage, err := repo.ListBookingsWindow(ctx, clientID, BookingListInput{
		WindowStart: selectedStartAt.Add(-24 * time.Hour),
		WindowEnd:   selectedStartAt.Add(24 * time.Hour),
		Limit:       100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var providerBookingSource string
	for _, providerBooking := range providerBookingPage.Items {
		if providerBooking.ID == booking.BookingID {
			providerBookingSource = providerBooking.Source
			break
		}
	}
	if providerBookingSource != "marketplace" {
		t.Fatalf("provider booking source = %q, want marketplace", providerBookingSource)
	}
	providerDetails, err := repo.GetBookingDetails(ctx, clientID, uuid.MustParse(booking.BookingID))
	if err != nil {
		t.Fatal(err)
	}
	if providerDetails.Source != "marketplace" {
		t.Fatalf("provider booking details source = %q, want marketplace", providerDetails.Source)
	}
	var bookingEventCount, providerNotificationCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM booking_domain_events
		WHERE booking_id = $1 AND event_type = 'booking_created'
	`, booking.BookingID).Scan(&bookingEventCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM notifications
		WHERE booking_id = $1 AND type = 'booking_created'
	`, booking.BookingID).Scan(&providerNotificationCount); err != nil {
		t.Fatal(err)
	}
	if bookingEventCount != 1 || providerNotificationCount != 1 {
		t.Fatalf("booking event/notification counts = %d/%d, want 1/1", bookingEventCount, providerNotificationCount)
	}
	cursorBeforeUpdate, err := repo.LatestBookingEventCursor(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE bookings SET status = 'confirmed', updated_at = NOW() WHERE id = $1`, booking.BookingID); err != nil {
		t.Fatal(err)
	}
	cursorAfterUpdate, err := repo.LatestBookingEventCursor(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	if cursorAfterUpdate <= cursorBeforeUpdate {
		t.Fatalf("booking update cursor did not advance: %d <= %d", cursorAfterUpdate, cursorBeforeUpdate)
	}
	repeated, err := repo.CreatePublicBooking(ctx, slug, input)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.BookingToken != booking.BookingToken {
		t.Fatalf("idempotent quote consumption created another booking: %q != %q", repeated.BookingToken, booking.BookingToken)
	}

	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(
		repo, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		"http://localhost:5173", "http://localhost:5174",
	)
	router := chi.NewRouter()
	handler.Routes(router)
	request := httptest.NewRequest(
		http.MethodPost,
		"/public/clients/"+url.PathEscape(slug)+"/bookings",
		bytes.NewReader(payload),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("HTTP reservation replay status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var httpBooking PublicBookingSummaryResponse
	if err := json.NewDecoder(recorder.Body).Decode(&httpBooking); err != nil {
		t.Fatal(err)
	}
	if httpBooking.BookingToken != booking.BookingToken {
		t.Fatalf("HTTP/internal reservation tokens = %q/%q", httpBooking.BookingToken, booking.BookingToken)
	}

	t.Cleanup(func() {
		bookingID := uuid.MustParse(booking.BookingID)
		_, _ = pool.Exec(ctx, `DELETE FROM notifications WHERE booking_id = $1`, bookingID)
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
		IdempotencyKey: uuid.NewString(),
		ServiceID:      serviceID.String(), StartsAt: selectedStart,
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
