package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	agreementrender "booking/go-server/internal/agreements/render"
	agreementseed "booking/go-server/internal/agreements/seed"
	"booking/go-server/internal/payments"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type successfulReservationPaymentReconciler struct {
	calls int
}

type uncertainReservationPaymentReconciler struct{}

func (uncertainReservationPaymentReconciler) ReconcileByPublicToken(
	context.Context,
	string,
) (payments.FinancialPayment, error) {
	return payments.FinancialPayment{}, errors.New("provider status unavailable")
}

func (reconciler *successfulReservationPaymentReconciler) ReconcileByPublicToken(
	context.Context,
	string,
) (payments.FinancialPayment, error) {
	reconciler.calls++
	return payments.FinancialPayment{}, nil
}

func TestInboxAIAutopilotConfirmationCreatesOneExactReservationConcurrently(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// A single connection catches accidental nested repository transactions:
	// proposal preparation must release its authority transaction before the
	// canonical quote service starts its own transaction.
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := NewRepository(pool)
	repo.ConfigureAgreementTokens(newTestAgreementTokenManager(t))

	var clientID uuid.UUID
	var providerHandle string
	if err := pool.QueryRow(ctx, `
		SELECT profile.client_id, profile.handle_slug
		FROM client_profiles profile
		WHERE profile.marketplace_enabled AND NOT profile.platform_restricted AND profile.market_configured_at IS NOT NULL
		  AND btrim(profile.handle_slug) <> ''
		ORDER BY profile.client_id
		LIMIT 1
	`).Scan(&clientID, &providerHandle); errors.Is(err, pgx.ErrNoRows) {
		t.Skip("no marketplace-enabled provider is available")
	} else if err != nil {
		t.Fatal(err)
	}

	type policySnapshot struct {
		defaultMode       string
		enabledServiceIDs []uuid.UUID
		paused            bool
		maxTurns          int
		inactivity        int
		revision          int64
		updatedBy         uuid.UUID
		createdAt         time.Time
		updatedAt         time.Time
	}
	var previousPolicy policySnapshot
	previousPolicyExists := true
	if err := pool.QueryRow(ctx, `
		SELECT default_mode, enabled_service_ids, paused, max_turns,
			inactivity_timeout_minutes, revision, updated_by, created_at, updated_at
		FROM inbox_ai_policies WHERE client_id=$1
	`, clientID).Scan(
		&previousPolicy.defaultMode, &previousPolicy.enabledServiceIDs,
		&previousPolicy.paused, &previousPolicy.maxTurns, &previousPolicy.inactivity,
		&previousPolicy.revision, &previousPolicy.updatedBy,
		&previousPolicy.createdAt, &previousPolicy.updatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		previousPolicyExists = false
	} else if err != nil {
		t.Fatal(err)
	}

	serviceID := uuid.New()
	serviceSlug := "autopilot-evidence-" + serviceID.String()
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, client_id, title, slug, duration_minutes, price_amount_minor,
			availability_mode, fulfillment_mode, agreement_timing,
			currency_code, cancellation_policy, lateness_policy,
			status, is_active, is_hidden
		) VALUES (
			$1,$2,'Autopilot evidence test',$3,30,0,
			'custom','virtual',NULL,'NGN',
			'24 hours notice','Please arrive on time','published',TRUE,FALSE
		)
	`, serviceID, clientID, serviceSlug); err != nil {
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
		UPDATE agreement_template_families SET current_published_version_id=$2 WHERE id=$1
	`, familyID, versionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE services SET agreement_template_family_id=$2, agreement_timing='before_payment'
		WHERE id=$1
	`, serviceID, familyID); err != nil {
		t.Fatal(err)
	}
	for day := 0; day < 7; day++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO service_availability_windows (
				id, service_id, day_of_week, start_time, end_time, slot_interval_minutes
			) VALUES ($1,$2,$3,'08:00','20:00',30)
		`, uuid.New(), serviceID, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inbox_ai_policies (
			client_id, default_mode, enabled_service_ids, paused, max_turns,
			inactivity_timeout_minutes, revision, updated_by
		) VALUES ($1,'autopilot',ARRAY[$2]::uuid[],FALSE,12,60,1,$1)
		ON CONFLICT (client_id) DO UPDATE SET
			default_mode='autopilot', enabled_service_ids=ARRAY[$2]::uuid[],
			paused=FALSE, max_turns=12, inactivity_timeout_minutes=60,
			revision=inbox_ai_policies.revision+1, updated_by=$1, updated_at=NOW()
	`, clientID, serviceID); err != nil {
		t.Fatal(err)
	}
	repo.ConfigureInboxAIAutomation(true, []string{clientID.String()})

	marketplaceCustomerID := uuid.New()
	email := fmt.Sprintf("autopilot-evidence-%s@example.com", marketplaceCustomerID)
	phone := "+23480" + fmt.Sprintf("%08d", time.Now().UnixNano()%100000000)
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (
			id, full_name, email, email_verified_at, phone_e164, phone_verified_at
		) VALUES ($1,'Autopilot Evidence Customer',$2,NOW(),$3,NOW())
	`, marketplaceCustomerID, email, phone); err != nil {
		t.Fatal(err)
	}
	conversationResult, err := repo.GetOrCreateMarketplaceProviderConversation(
		ctx, marketplaceCustomerID, clientID,
	)
	if err != nil {
		t.Fatal(err)
	}
	conversationID := uuid.MustParse(conversationResult.Detail.Conversation.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inbox_conversations WHERE id=$1`, conversationID)
		_, _ = pool.Exec(ctx, `UPDATE booking_quotes SET booking_id=NULL,consumed_at=NULL WHERE booking_id IN (SELECT id FROM bookings WHERE marketplace_customer_id=$1)`, marketplaceCustomerID)
		_, _ = pool.Exec(ctx, `DELETE FROM payments WHERE booking_id IN (SELECT id FROM bookings WHERE marketplace_customer_id=$1)`, marketplaceCustomerID)
		_, _ = pool.Exec(ctx, `DELETE FROM bookings WHERE marketplace_customer_id=$1`, marketplaceCustomerID)
		_, _ = pool.Exec(ctx, `DELETE FROM booking_quotes WHERE service_id=$1 AND booking_id IS NULL`, serviceID)
		_, _ = pool.Exec(ctx, `DELETE FROM service_availability_windows WHERE service_id=$1`, serviceID)
		_, _ = pool.Exec(ctx, `DELETE FROM services WHERE id=$1`, serviceID)
		_, _ = pool.Exec(ctx, `DELETE FROM agreement_template_families WHERE id=$1`, familyID)
		_, _ = pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID)
		if previousPolicyExists {
			_, _ = pool.Exec(ctx, `
				INSERT INTO inbox_ai_policies (
					client_id, default_mode, enabled_service_ids, paused, max_turns,
					inactivity_timeout_minutes, revision, updated_by, created_at, updated_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
				ON CONFLICT (client_id) DO UPDATE SET
					default_mode=EXCLUDED.default_mode,
					enabled_service_ids=EXCLUDED.enabled_service_ids,
					paused=EXCLUDED.paused, max_turns=EXCLUDED.max_turns,
					inactivity_timeout_minutes=EXCLUDED.inactivity_timeout_minutes,
					revision=EXCLUDED.revision, updated_by=EXCLUDED.updated_by,
					created_at=EXCLUDED.created_at, updated_at=EXCLUDED.updated_at
			`, clientID, previousPolicy.defaultMode, previousPolicy.enabledServiceIDs,
				previousPolicy.paused, previousPolicy.maxTurns, previousPolicy.inactivity,
				previousPolicy.revision, previousPolicy.updatedBy,
				previousPolicy.createdAt, previousPolicy.updatedAt)
		} else {
			_, _ = pool.Exec(ctx, `DELETE FROM inbox_ai_policies WHERE client_id=$1`, clientID)
		}
	})

	start, err := repo.StartMarketplaceInboxAIBooking(
		ctx, marketplaceCustomerID, conversationID,
		StartInboxAIBookingInput{IdempotencyKey: uuid.NewString()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if start.Message == nil || start.Message.Presentation == nil ||
		start.Message.Presentation.Kind != "service_choices" {
		t.Fatalf("start message = %#v, want canonical service choices", start.Message)
	}

	from := time.Now().In(time.FixedZone("WAT", 60*60)).Add(24 * time.Hour).Format("2006-01-02")
	offerKey := uuid.NewString()
	offerInput := OfferInboxAIAvailabilityInput{
		IdempotencyKey: offerKey, ActionID: start.Workflow.ActionID,
		ExpectedRevision: start.Workflow.Revision, ServiceID: serviceID.String(),
		From: from, Days: 7,
	}
	offer, err := repo.OfferMarketplaceInboxAIAvailability(
		ctx, marketplaceCustomerID, conversationID,
		offerInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	var availabilityCard struct {
		Choices []struct {
			StartsAt string `json:"starts_at"`
		} `json:"choices"`
	}
	if offer.Message == nil || offer.Message.Presentation == nil ||
		offer.Message.Presentation.Kind != "availability_choices" {
		t.Fatalf("availability message = %#v", offer.Message)
	}
	if err := json.Unmarshal(offer.Message.Presentation.Data, &availabilityCard); err != nil {
		t.Fatal(err)
	}
	if len(availabilityCard.Choices) == 0 {
		t.Fatal("canonical availability card has no choices")
	}
	if replay, replayErr := repo.OfferMarketplaceInboxAIAvailability(
		ctx, marketplaceCustomerID, conversationID, offerInput,
	); replayErr != nil || !replay.Replayed {
		t.Fatalf("availability replay=%v error=%v", replay.Replayed, replayErr)
	}
	conflictingOffer := offerInput
	conflictingOffer.Days = 6
	if _, conflictErr := repo.OfferMarketplaceInboxAIAvailability(
		ctx, marketplaceCustomerID, conversationID, conflictingOffer,
	); !errors.Is(conflictErr, ErrInboxIdempotencyConflict) {
		t.Fatalf("availability key reuse error=%v, want idempotency conflict", conflictErr)
	}

	proposalKey := uuid.NewString()
	proposalInput := PrepareInboxAIBookingProposalInput{
		IdempotencyKey: proposalKey, ActionID: offer.Workflow.ActionID,
		ExpectedRevision: offer.Workflow.Revision, ServiceID: serviceID.String(),
		StartsAt: availabilityCard.Choices[0].StartsAt,
	}
	proposalResult, err := repo.PrepareMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID,
		proposalInput,
	)
	if err != nil {
		t.Fatal(err)
	}
	proposal := proposalResult.Workflow.Proposal
	if proposal == nil || proposal.Hash == "" || proposal.TotalAmountMinor != 0 ||
		proposalResult.Message == nil || proposalResult.Message.Presentation == nil ||
		proposalResult.Message.Presentation.Kind != "booking_proposal" {
		t.Fatalf("proposal result = %#v", proposalResult)
	}
	if replay, replayErr := repo.PrepareMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, proposalInput,
	); replayErr != nil || !replay.Replayed {
		t.Fatalf("proposal replay=%v error=%v", replay.Replayed, replayErr)
	}
	conflictingProposal := proposalInput
	conflictingProposal.ExpectedRevision++
	if _, conflictErr := repo.PrepareMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, conflictingProposal,
	); !errors.Is(conflictErr, ErrInboxIdempotencyConflict) {
		t.Fatalf("proposal key reuse error=%v, want idempotency conflict", conflictErr)
	}
	if !proposal.Agreement.Required || proposal.Agreement.Completed {
		t.Fatalf("proposal agreement = %#v, want a real pending before-booking agreement", proposal.Agreement)
	}
	var agreementID uuid.UUID
	var tokenCiphertext, tokenNonce []byte
	if err := pool.QueryRow(ctx, `
		SELECT agreement.id, agreement.public_token_ciphertext, agreement.public_token_nonce
		FROM inbox_ai_booking_sessions session
		INNER JOIN agreement_instances agreement ON agreement.id=session.agreement_instance_id
		WHERE session.conversation_id=$1
	`, conversationID).Scan(&agreementID, &tokenCiphertext, &tokenNonce); err != nil {
		t.Fatal(err)
	}
	if len(tokenCiphertext) == 0 || len(tokenNonce) == 0 {
		t.Fatal("real proposal agreement did not persist encrypted public-token material")
	}
	if _, err := repo.ConfirmMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID),
		ConfirmInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ProposalRevision: proposal.Revision,
			ProposalHash: proposal.Hash, ContactDetailsConfirmed: true,
		},
	); !errors.Is(err, ErrInboxAIAgreementRequired) {
		t.Fatalf("confirmation without agreement error = %v, want agreement required", err)
	}
	agreementKey := uuid.NewString()
	agreementInput := AcceptInboxAIBookingAgreementInput{
		IdempotencyKey: agreementKey, ProposalRevision: proposal.Revision,
		ProposalHash: proposal.Hash, Accepted: true,
	}
	agreementResult, err := repo.AcceptMarketplaceInboxAIBookingAgreement(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID),
		agreementInput,
	)
	if err != nil || agreementResult.Workflow.Proposal == nil ||
		!agreementResult.Workflow.Proposal.Agreement.Completed {
		t.Fatalf("customer agreement acceptance = %#v, error=%v", agreementResult, err)
	}
	if replay, replayErr := repo.AcceptMarketplaceInboxAIBookingAgreement(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID), agreementInput,
	); replayErr != nil || !replay.Replayed {
		t.Fatalf("agreement replay=%v error=%v", replay.Replayed, replayErr)
	}
	conflictingAgreement := agreementInput
	conflictingAgreement.FullName = "Different evidence"
	if _, conflictErr := repo.AcceptMarketplaceInboxAIBookingAgreement(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID), conflictingAgreement,
	); !errors.Is(conflictErr, ErrInboxIdempotencyConflict) {
		t.Fatalf("agreement key reuse error=%v, want idempotency conflict", conflictErr)
	}

	var bookingsBefore int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM bookings WHERE marketplace_customer_id=$1
	`, marketplaceCustomerID).Scan(&bookingsBefore); err != nil {
		t.Fatal(err)
	}
	type confirmationOutcome struct {
		result InboxAIBookingConfirmationResult
		err    error
	}
	outcomes := make(chan confirmationOutcome, 2)
	startConfirm := make(chan struct{})
	var confirmWait sync.WaitGroup
	confirmWait.Add(2)
	for range 2 {
		go func() {
			defer confirmWait.Done()
			<-startConfirm
			result, confirmErr := repo.ConfirmMarketplaceInboxAIBookingProposal(
				ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID),
				ConfirmInboxAIBookingProposalInput{
					IdempotencyKey: uuid.NewString(), ProposalRevision: proposal.Revision,
					ProposalHash: proposal.Hash, ContactDetailsConfirmed: true,
					WhatsAppConsent: true,
				},
			)
			outcomes <- confirmationOutcome{result: result, err: confirmErr}
		}()
	}
	close(startConfirm)
	confirmWait.Wait()
	close(outcomes)
	confirmationIDs := map[string]struct{}{}
	bookingIDs := map[string]struct{}{}
	replayCount := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent confirmation error: %v", outcome.err)
		}
		if !outcome.result.ReservationCreated || outcome.result.BookingID == "" ||
			outcome.result.NextStep != BookingNextStepProviderConfirmation {
			t.Fatalf("reservation result = %#v", outcome.result)
		}
		confirmedProposal := outcome.result.Workflow.Proposal
		if confirmedProposal == nil || !confirmedProposal.ContactDetailsConfirmed ||
			!confirmedProposal.WhatsAppConsent || confirmedProposal.SMSConsent {
			t.Fatalf("restorable confirmation evidence = %#v", confirmedProposal)
		}
		confirmationIDs[outcome.result.ConfirmationID] = struct{}{}
		bookingIDs[outcome.result.BookingID] = struct{}{}
		if outcome.result.Replayed {
			replayCount++
		}
	}
	if len(confirmationIDs) != 1 || len(bookingIDs) != 1 || replayCount != 1 {
		t.Fatalf("confirmations=%v bookings=%v replay_count=%d, want one reservation and one replay", confirmationIDs, bookingIDs, replayCount)
	}

	var confirmationCount, bookingsAfter int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_ai_booking_confirmations WHERE conversation_id=$1),
			(SELECT COUNT(*) FROM bookings WHERE marketplace_customer_id=$2)
	`, conversationID, marketplaceCustomerID).Scan(&confirmationCount, &bookingsAfter); err != nil {
		t.Fatal(err)
	}
	if confirmationCount != 1 || bookingsAfter != bookingsBefore+1 {
		t.Fatalf("confirmation_count=%d bookings=%d->%d", confirmationCount, bookingsBefore, bookingsAfter)
	}
	var bookingID string
	for id := range bookingIDs {
		bookingID = id
	}
	var linkedAgreementID, confirmationBookingID, sessionBookingID uuid.UUID
	var bookingSessionState string
	var reservationMessages, bookingEvents, conversationLinks, bookingAgreements int
	if err := pool.QueryRow(ctx, `
		SELECT agreement.id, confirmation.booking_id, session.booking_id, session.state,
			(SELECT COUNT(*) FROM inbox_messages message
			 WHERE message.conversation_id=$1 AND message.booking_id=$2),
			(SELECT COUNT(*) FROM booking_domain_events event
			 WHERE event.booking_id=$2 AND event.event_type='booking_created'),
			(SELECT COUNT(*) FROM inbox_conversation_bookings link
			 WHERE link.conversation_id=$1 AND link.booking_id=$2),
			(SELECT COUNT(*) FROM agreement_instances instance WHERE instance.booking_id=$2)
		FROM inbox_ai_booking_sessions session
		INNER JOIN inbox_ai_booking_confirmations confirmation ON confirmation.id=session.confirmation_id
		INNER JOIN agreement_instances agreement ON agreement.id=session.agreement_instance_id
		WHERE session.conversation_id=$1
	`, conversationID, uuid.MustParse(bookingID)).Scan(
		&linkedAgreementID, &confirmationBookingID, &sessionBookingID, &bookingSessionState,
		&reservationMessages, &bookingEvents, &conversationLinks, &bookingAgreements,
	); err != nil {
		t.Fatal(err)
	}
	if linkedAgreementID != agreementID || confirmationBookingID.String() != bookingID ||
		sessionBookingID.String() != bookingID || bookingSessionState != "awaiting_provider_confirmation" ||
		reservationMessages != 2 || bookingEvents != 1 || conversationLinks != 1 || bookingAgreements != 1 {
		t.Fatalf("reservation lineage agreement=%s confirmation=%s session=%s state=%s messages=%d events=%d links=%d agreements=%d",
			linkedAgreementID, confirmationBookingID, sessionBookingID, bookingSessionState,
			reservationMessages, bookingEvents, conversationLinks, bookingAgreements)
	}
	providerDetail, err := repo.GetProviderConversationDetail(ctx, clientID, conversationID, 20)
	if err != nil {
		t.Fatal(err)
	}
	lastProviderMessage := providerDetail.Messages[len(providerDetail.Messages)-1]
	if providerDetail.Conversation.UnreadCount != 3 ||
		lastProviderMessage.SenderType != "system" ||
		lastProviderMessage.Presentation == nil ||
		lastProviderMessage.Presentation.Kind != "booking_next_step" {
		t.Fatalf("provider confirmation transcript = %#v", providerDetail)
	}
	var confirmationID string
	for id := range confirmationIDs {
		confirmationID = id
	}
	if _, err := pool.Exec(ctx, `
		UPDATE marketplace_customers
		SET full_name='Updated Evidence Customer', updated_at=NOW()+INTERVAL '1 second'
		WHERE id=$1
	`, marketplaceCustomerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE inbox_ai_booking_sessions SET expires_at=NOW()-INTERVAL '1 second'
		WHERE conversation_id=$1
	`, conversationID); err != nil {
		t.Fatal(err)
	}
	expiredReplay, err := repo.ConfirmMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID),
		ConfirmInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ProposalRevision: proposal.Revision,
			ProposalHash: proposal.Hash, ContactDetailsConfirmed: true,
			WhatsAppConsent: true,
		},
	)
	if err != nil || !expiredReplay.Replayed || expiredReplay.ConfirmationID != confirmationID {
		t.Fatalf("expired/profile-changed evidence replay = %#v, error=%v", expiredReplay, err)
	}

	if _, err := repo.ConfirmMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(proposal.ID),
		ConfirmInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ProposalRevision: proposal.Revision,
			ProposalHash:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ContactDetailsConfirmed: true,
		},
	); !errors.Is(err, ErrInboxAIProposalStale) {
		t.Fatalf("altered proposal hash error = %v, want stale proposal", err)
	}

	if _, err := repo.SendMarketplaceMessage(
		ctx, marketplaceCustomerID, conversationID, uuid.New(),
		"Yes, I agree. Book it for me.", uuid.NullUUID{},
	); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM inbox_ai_booking_confirmations WHERE conversation_id=$1),
			(SELECT COUNT(*) FROM bookings WHERE marketplace_customer_id=$2)
	`, conversationID, marketplaceCustomerID).Scan(&confirmationCount, &bookingsAfter); err != nil {
		t.Fatal(err)
	}
	if confirmationCount != 1 || bookingsAfter != bookingsBefore+1 {
		t.Fatalf("plain chat text changed durable booking evidence: confirmations=%d bookings=%d", confirmationCount, bookingsAfter)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE services SET agreement_timing='after_payment' WHERE id=$1
	`, serviceID); err != nil {
		t.Fatal(err)
	}
	secondStart, err := repo.StartMarketplaceInboxAIBooking(
		ctx, marketplaceCustomerID, conversationID,
		StartInboxAIBookingInput{IdempotencyKey: uuid.NewString()},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondOffer, err := repo.OfferMarketplaceInboxAIAvailability(
		ctx, marketplaceCustomerID, conversationID,
		OfferInboxAIAvailabilityInput{
			IdempotencyKey: uuid.NewString(), ActionID: secondStart.Workflow.ActionID,
			ExpectedRevision: secondStart.Workflow.Revision, ServiceID: serviceID.String(),
			From: from, Days: 7,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var secondAvailabilityCard struct {
		Choices []struct {
			StartsAt string `json:"starts_at"`
		} `json:"choices"`
	}
	if secondOffer.Message == nil || secondOffer.Message.Presentation == nil {
		t.Fatal("second availability card is missing")
	}
	if err := json.Unmarshal(secondOffer.Message.Presentation.Data, &secondAvailabilityCard); err != nil {
		t.Fatal(err)
	}
	if len(secondAvailabilityCard.Choices) == 0 {
		t.Fatal("second availability card has no choices")
	}
	secondProposalResult, err := repo.PrepareMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID,
		PrepareInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ActionID: secondOffer.Workflow.ActionID,
			ExpectedRevision: secondOffer.Workflow.Revision, ServiceID: serviceID.String(),
			StartsAt: secondAvailabilityCard.Choices[0].StartsAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	secondProposal := secondProposalResult.Workflow.Proposal
	if secondProposal == nil || secondProposal.Agreement.Required {
		t.Fatalf("after-payment proposal incorrectly required prebooking agreement: %#v", secondProposal)
	}
	secondReservation, err := repo.ConfirmMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(secondProposal.ID),
		ConfirmInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ProposalRevision: secondProposal.Revision,
			ProposalHash: secondProposal.Hash, ContactDetailsConfirmed: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var secondAgreementStatus, secondSessionState string
	if err := pool.QueryRow(ctx, `
		SELECT agreement.status, session.state
		FROM inbox_ai_booking_sessions session
		INNER JOIN agreement_instances agreement ON agreement.booking_id=session.booking_id
		WHERE session.conversation_id=$1 AND session.booking_id=$2
	`, conversationID, uuid.MustParse(secondReservation.BookingID)).Scan(
		&secondAgreementStatus, &secondSessionState,
	); err != nil {
		t.Fatal(err)
	}
	if secondReservation.NextStep != BookingNextStepAgreement ||
		secondAgreementStatus != "awaiting_customer" ||
		secondSessionState != "awaiting_after_payment_agreement" {
		t.Fatalf("after-payment result=%#v agreement=%s state=%s",
			secondReservation, secondAgreementStatus, secondSessionState)
	}

	repo.ConfigureInboxAIAutopilotPaymentWindow(30 * time.Minute)
	if _, err := pool.Exec(ctx, `
		UPDATE services
		SET price_amount_minor=250000, agreement_timing=NULL,
			agreement_template_family_id=NULL
		WHERE id=$1
	`, serviceID); err != nil {
		t.Fatal(err)
	}
	paidStart, err := repo.StartMarketplaceInboxAIBooking(
		ctx, marketplaceCustomerID, conversationID,
		StartInboxAIBookingInput{IdempotencyKey: uuid.NewString()},
	)
	if err != nil {
		t.Fatal(err)
	}
	paidOffer, err := repo.OfferMarketplaceInboxAIAvailability(
		ctx, marketplaceCustomerID, conversationID,
		OfferInboxAIAvailabilityInput{
			IdempotencyKey: uuid.NewString(), ActionID: paidStart.Workflow.ActionID,
			ExpectedRevision: paidStart.Workflow.Revision, ServiceID: serviceID.String(),
			From: from, Days: 7,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var paidAvailabilityCard struct {
		Choices []struct {
			StartsAt string `json:"starts_at"`
		} `json:"choices"`
	}
	if paidOffer.Message == nil || paidOffer.Message.Presentation == nil {
		t.Fatal("paid availability card is missing")
	}
	if err := json.Unmarshal(paidOffer.Message.Presentation.Data, &paidAvailabilityCard); err != nil {
		t.Fatal(err)
	}
	if len(paidAvailabilityCard.Choices) == 0 {
		t.Fatal("paid availability card has no choices")
	}
	paidStartsAt := paidAvailabilityCard.Choices[0].StartsAt
	paidProposalResult, err := repo.PrepareMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID,
		PrepareInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ActionID: paidOffer.Workflow.ActionID,
			ExpectedRevision: paidOffer.Workflow.Revision, ServiceID: serviceID.String(),
			StartsAt: paidStartsAt,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	paidProposal := paidProposalResult.Workflow.Proposal
	if paidProposal == nil || paidProposal.TotalAmountMinor != 250000 || paidProposal.Agreement.Required {
		t.Fatalf("paid proposal = %#v", paidProposal)
	}
	// A proposal/quote issued before restriction must not reserve a booking.
	if _, err = pool.Exec(ctx, `UPDATE client_profiles SET platform_restricted=true WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE client_profiles SET platform_restricted=false WHERE client_id=$1`, clientID)
	})
	_, restrictedErr := repo.ConfirmMarketplaceInboxAIBookingProposal(ctx, marketplaceCustomerID, conversationID, uuid.MustParse(paidProposal.ID), ConfirmInboxAIBookingProposalInput{
		IdempotencyKey: uuid.NewString(), ProposalRevision: paidProposal.Revision, ProposalHash: paidProposal.Hash, ContactDetailsConfirmed: true,
	})
	if !errors.Is(restrictedErr, ErrBusinessRestricted) {
		t.Fatalf("restricted autopilot reservation: %v", restrictedErr)
	}
	if _, err = pool.Exec(ctx, `UPDATE client_profiles SET platform_restricted=false WHERE client_id=$1`, clientID); err != nil {
		t.Fatal(err)
	}
	paidReservation, err := repo.ConfirmMarketplaceInboxAIBookingProposal(
		ctx, marketplaceCustomerID, conversationID, uuid.MustParse(paidProposal.ID),
		ConfirmInboxAIBookingProposalInput{
			IdempotencyKey: uuid.NewString(), ProposalRevision: paidProposal.Revision,
			ProposalHash: paidProposal.Hash, ContactDetailsConfirmed: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if paidReservation.NextStep != BookingNextStepPayment ||
		paidReservation.Workflow.Reservation == nil ||
		paidReservation.Workflow.Reservation.ExpiresAt == nil {
		t.Fatalf("paid reservation = %#v, want a canonical payment deadline", paidReservation)
	}
	paidBookingID := uuid.MustParse(paidReservation.BookingID)
	var persistedDeadline time.Time
	if err := pool.QueryRow(ctx, `
		SELECT reservation_expires_at FROM bookings WHERE id=$1
	`, paidBookingID).Scan(&persistedDeadline); err != nil {
		t.Fatal(err)
	}
	if !persistedDeadline.Equal(paidReservation.Workflow.Reservation.ExpiresAt.UTC()) {
		t.Fatalf("persisted deadline=%s workflow deadline=%s",
			persistedDeadline, paidReservation.Workflow.Reservation.ExpiresAt)
	}

	availabilityContains := func(target string) bool {
		targetTime, parseErr := time.Parse(time.RFC3339, target)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		startDate, parseErr := time.Parse("2006-01-02", from)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		availability, availabilityErr := repo.GetPublicAvailabilityRange(
			ctx, providerHandle, serviceID, &startDate, 7,
		)
		if availabilityErr != nil {
			t.Fatal(availabilityErr)
		}
		for _, day := range availability.Dates {
			for _, slot := range day.Slots {
				slotTime, slotParseErr := time.Parse(time.RFC3339, slot.StartAt)
				if slotParseErr != nil {
					t.Fatal(slotParseErr)
				}
				if slotTime.Equal(targetTime) {
					return true
				}
			}
		}
		return false
	}
	if availabilityContains(paidStartsAt) {
		t.Fatal("paid reservation did not consume its exact capacity")
	}
	var paymentCustomerID uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT customer_id FROM bookings WHERE id=$1
	`, paidBookingID).Scan(&paymentCustomerID); err != nil {
		t.Fatal(err)
	}
	ledger, err := payments.NewLedgerService(payments.NewLedgerRepository(pool), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	payment, _, err := ledger.CreatePaymentAttempt(ctx, payments.CreatePaymentAttemptInput{
		BookingID: paidBookingID, ClientID: clientID, CustomerID: paymentCustomerID,
		Purpose: payments.PaymentPurposeFull, Provider: "paystack", Method: "card",
		CountryCode: "NG", CurrencyCode: "NGN", AmountMinor: 250000,
		PriceSnapshot:  map[string]string{"total_amount_minor": "250000"},
		IdempotencyKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var paymentDeadline time.Time
	if err := pool.QueryRow(ctx, `
		UPDATE payments SET status='pending', expires_at=NOW()+INTERVAL '1 hour', updated_at=NOW()
		WHERE id=$1 RETURNING expires_at
	`, payment.ID).Scan(&paymentDeadline); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE bookings
		SET created_at=NOW()-INTERVAL '1 hour',
			reservation_expires_at=NOW()-INTERVAL '1 second'
		WHERE id=$1
	`, paidBookingID); err != nil {
		t.Fatal(err)
	}
	uncertainWorker := NewInboxAIReservationExpiryWorker(
		pool, repo, uncertainReservationPaymentReconciler{}, NewInboxMetrics(), nil,
	)
	if expired, deferred, expireErr := uncertainWorker.processBooking(ctx, paidBookingID); expireErr == nil || expired || !deferred {
		t.Fatalf("provider-uncertainty outcome expired=%v deferred=%v error=%v",
			expired, deferred, expireErr)
	}
	var statusAfterUncertainty string
	if err := pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id=$1`, paidBookingID).Scan(
		&statusAfterUncertainty,
	); err != nil || statusAfterUncertainty == "expired" {
		t.Fatalf("provider uncertainty status=%s error=%v", statusAfterUncertainty, err)
	}
	reconciler := &successfulReservationPaymentReconciler{}
	worker := NewInboxAIReservationExpiryWorker(
		pool, repo, reconciler, NewInboxMetrics(), nil,
	)
	if expired, deferred, expireErr := worker.processBooking(ctx, paidBookingID); expireErr != nil ||
		expired || !deferred {
		t.Fatalf("active-payment expiry outcome expired=%v deferred=%v error=%v",
			expired, deferred, expireErr)
	}
	var extendedDeadline time.Time
	if err := pool.QueryRow(ctx, `
		SELECT reservation_expires_at FROM bookings WHERE id=$1
	`, paidBookingID).Scan(&extendedDeadline); err != nil {
		t.Fatal(err)
	}
	if reconciler.calls != 1 || !extendedDeadline.Equal(paymentDeadline) {
		t.Fatalf("provider reconciliation calls=%d deadline=%s want=%s",
			reconciler.calls, extendedDeadline, paymentDeadline)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE payments SET status='expired', updated_at=NOW() WHERE id=$1
	`, payment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE bookings SET reservation_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1
	`, paidBookingID); err != nil {
		t.Fatal(err)
	}
	type expiryOutcome struct {
		expired  bool
		deferred bool
		err      error
	}
	expiryOutcomes := make(chan expiryOutcome, 2)
	var expiryWait sync.WaitGroup
	expiryWait.Add(2)
	for range 2 {
		go func() {
			defer expiryWait.Done()
			expired, deferred, expireErr := worker.processBooking(ctx, paidBookingID)
			expiryOutcomes <- expiryOutcome{expired: expired, deferred: deferred, err: expireErr}
		}()
	}
	expiryWait.Wait()
	close(expiryOutcomes)
	expiredCount := 0
	for outcome := range expiryOutcomes {
		if outcome.err != nil || outcome.deferred {
			t.Fatalf("expiry outcome = %#v", outcome)
		}
		if outcome.expired {
			expiredCount++
		}
	}
	if expiredCount != 1 {
		t.Fatalf("expired_count=%d, want exactly one state transition", expiredCount)
	}
	var paidBookingStatus, paidSessionState, expiryReason string
	var expiredAt time.Time
	var expiryMessages, expiryEvents int
	if err := pool.QueryRow(ctx, `
		SELECT booking.status, session.state, booking.reservation_expiry_reason,
			booking.reservation_expired_at,
			(SELECT COUNT(*) FROM inbox_messages message
			 WHERE message.booking_id=$1 AND message.presentation->>'kind'='reservation_expired'),
			(SELECT COUNT(*) FROM booking_domain_events event
			 WHERE event.booking_id=$1 AND event.event_type='booking_updated'
 AND event.payload->>'status'='expired' AND event.payload->>'previous_status'<>'expired')
		FROM bookings booking
		INNER JOIN inbox_ai_booking_sessions session ON session.booking_id=booking.id
		WHERE booking.id=$1
	`, paidBookingID).Scan(
		&paidBookingStatus, &paidSessionState, &expiryReason, &expiredAt,
		&expiryMessages, &expiryEvents,
	); err != nil {
		t.Fatal(err)
	}
	if paidBookingStatus != "expired" || paidSessionState != "expired" ||
		expiryReason != "payment_deadline_elapsed" || expiredAt.IsZero() ||
		expiryMessages != 1 || expiryEvents != 1 {
		t.Fatalf("expired booking status=%s session=%s reason=%s at=%s messages=%d events=%d",
			paidBookingStatus, paidSessionState, expiryReason, expiredAt,
			expiryMessages, expiryEvents)
	}
	if !availabilityContains(paidStartsAt) {
		t.Fatal("expired reservation did not release its exact capacity")
	}
	if _, err := payments.NewLedgerRepository(pool).GetOutstandingBookingPaymentObligation(
		ctx, paidBookingID,
	); !errors.Is(
		err, payments.ErrBookingPaymentClosed,
	) {
		t.Fatalf("expired booking payment error=%v, want booking payment closed", err)
	}
}
