package appdata

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/agreements/signature"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) PrepareMarketplaceInboxAIBookingProposal(
	ctx context.Context,
	marketplaceCustomerID, conversationID uuid.UUID,
	input PrepareInboxAIBookingProposalInput,
) (InboxAIBookingActionResult, error) {
	idempotencyKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	actionID, err := parseInboxAIUUID(input.ActionID, "action_id")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	serviceID, err := parseInboxAIUUID(input.ServiceID, "service_id")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	startsAt, err := normalizedInboxAIProposalTime(input.StartsAt)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	locationTokenHash := ""
	if token := strings.TrimSpace(input.CustomerLocationToken); token != "" {
		locationTokenHash = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	safeInput := map[string]any{
		"action_id": actionID.String(), "expected_revision": input.ExpectedRevision,
		"service_id": serviceID.String(), "starts_at": startsAt.UTC().Format(time.RFC3339Nano),
		"customer_location_token_hash": locationTokenHash,
	}
	clientID, providerHandle, policy, err := r.authorizeInboxAIBookingRead(
		ctx, marketplaceCustomerID, conversationID, serviceID,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	var fullName, email, phone, whatsapp string
	var customerDetailsRevision time.Time
	if err := r.db.QueryRow(ctx, `
		SELECT COALESCE(full_name,''),
			CASE WHEN email_verified_at IS NOT NULL THEN COALESCE(email,'') ELSE '' END,
			CASE WHEN phone_verified_at IS NOT NULL THEN COALESCE(phone_e164,'') ELSE '' END,
			CASE WHEN whatsapp_verified_at IS NOT NULL THEN COALESCE(whatsapp_e164,'') ELSE '' END,
			updated_at
		FROM marketplace_customers WHERE id=$1
	`, marketplaceCustomerID).Scan(
		&fullName, &email, &phone, &whatsapp, &customerDetailsRevision,
	); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("load proposal customer details: %w", err)
	}
	missing, err := loadInboxAIMissingCustomerFields(ctx, r.db, marketplaceCustomerID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if len(missing) > 0 {
		return InboxAIBookingActionResult{}, fmt.Errorf(
			"%w: %s", ErrInboxAIBookingDetailsRequired, strings.Join(missing, ","),
		)
	}
	requirements, err := r.BookingApplication().GetRequirements(ctx, providerHandle, serviceID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if requirements.CustomerLocationRequired && strings.TrimSpace(input.CustomerLocationToken) == "" {
		return InboxAIBookingActionResult{}, ErrLocationRequired
	}
	if requirements.Agreement.Required && requirements.Agreement.ConfirmationMethod == "signature" &&
		requirements.Agreement.Title == "" {
		return InboxAIBookingActionResult{}, fmt.Errorf(
			"%w: standalone_signature_not_enabled", ErrInboxAIServiceUnavailable,
		)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	defer func(active pgx.Tx) { _ = active.Rollback(ctx) }(tx)
	if replay, found, replayErr := loadInboxAIWorkflowMessageReplay(
		ctx, tx, marketplaceCustomerID, conversationID, idempotencyKey,
		"refresh_booking_proposal", safeInput,
	); replayErr != nil {
		return InboxAIBookingActionResult{}, replayErr
	} else if found {
		return r.commitInboxAIBookingMessageReplay(
			ctx, tx, marketplaceCustomerID, conversationID, replay,
		)
	}
	lockedPolicy, _, record, err := r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if lockedPolicy.revision != policy.revision || record.ClientID != clientID ||
		!inboxAIUUIDSliceContains(lockedPolicy.enabledServiceIDs, serviceID) {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	if err := validateCurrentInboxAIBookingAction(record, actionID, input.ExpectedRevision); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if !record.SelectedServiceID.Valid || record.SelectedServiceID.UUID != serviceID ||
		record.State != "offering_slots" {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	if err := beginInboxAIBookingAction(
		ctx, tx, record.AISessionID, conversationID, clientID, idempotencyKey,
		"refresh_booking_proposal", safeInput,
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("commit inbox proposal intent: %w", err)
	}
	actionPending := true
	defer func() {
		if !actionPending {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		r.failInboxAIBookingAction(cleanupCtx, conversationID, idempotencyKey, "proposal_failed")
	}()

	quote, err := r.BookingApplication().CreateQuote(ctx, CreateBookingQuoteCommand{
		ProviderHandle: providerHandle, IdempotencyKey: idempotencyKey,
		ServiceID: serviceID, StartsAt: startsAt,
		Customer: BookingCustomerDetails{
			FullName: strings.TrimSpace(fullName), Email: strings.TrimSpace(email),
			Phone: normalizeInboxAIPhone(phone, whatsapp),
		},
		CustomerLocationToken: strings.TrimSpace(input.CustomerLocationToken),
	})
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	quoteAttached := false
	defer func() {
		if quoteAttached {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		r.discardUnattachedInboxAIQuote(cleanupCtx, quote.QuoteToken)
	}()

	tx, err = r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	defer func(active pgx.Tx) { _ = active.Rollback(ctx) }(tx)
	lockedPolicy, _, record, err = r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if lockedPolicy.revision != policy.revision || record.ClientID != clientID ||
		!inboxAIUUIDSliceContains(lockedPolicy.enabledServiceIDs, serviceID) {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	if err := validateCurrentInboxAIBookingAction(record, actionID, input.ExpectedRevision); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if !record.SelectedServiceID.Valid || record.SelectedServiceID.UUID != serviceID ||
		record.State != "offering_slots" {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	var currentCustomerDetailsRevision time.Time
	if err := tx.QueryRow(ctx, `SELECT updated_at FROM marketplace_customers WHERE id=$1`, marketplaceCustomerID).Scan(
		&currentCustomerDetailsRevision,
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if !currentCustomerDetailsRevision.Equal(customerDetailsRevision) {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	quoteRecord, err := lockBookingQuote(ctx, tx, providerHandle, quote.QuoteToken)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if quoteRecord.ClientID != clientID || quoteRecord.ServiceID != serviceID ||
		!quoteRecord.StartsAt.Equal(startsAt) || quoteRecord.BookingID != nil ||
		!quoteRecord.ExpiresAt.After(time.Now().UTC()) {
		return InboxAIBookingActionResult{}, ErrInboxAIProposalStale
	}
	agreementInstanceID := uuid.NullUUID{}
	if quoteRecord.hasAgreement() && quoteRecord.AgreementTiming == "before_payment" {
		agreementID, agreementErr := r.createPrebookingAgreementFromQuote(
			ctx, tx, quoteRecord, marketplaceCustomerID,
		)
		if agreementErr != nil {
			return InboxAIBookingActionResult{}, agreementErr
		}
		agreementInstanceID = uuid.NullUUID{UUID: agreementID, Valid: true}
	}
	proposalID := uuid.New()
	proposalRevision := record.ProposalRevision + 1
	proposal := InboxAIBookingProposal{
		ID: proposalID.String(), Revision: proposalRevision,
		ExpiresAt: quoteRecord.ExpiresAt, ServiceID: quote.ServiceID,
		ServiceTitle: quote.ServiceTitle, StartsAt: quoteRecord.StartsAt,
		EndsAt: quoteRecord.EndsAt, Timezone: quote.Timezone,
		FulfillmentMode: quote.FulfillmentMode, LocationLabel: quote.LocationLabel,
		CurrencyCode: quote.CurrencyCode, TotalAmountMinor: quote.TotalAmountMinor,
		PriceLabel: "", PaymentRequirement: "No payment required",
		AgreementRequirement: "No agreement required before confirmation",
		Agreement:            InboxAIBookingAgreement{},
	}
	proposal.PriceLabel, _ = formatMarketMoney(
		int64(quote.TotalAmountMinor), quote.CountryCode, quote.CurrencyCode,
	)
	if quote.Agreement != nil && quote.Agreement.Timing == "before_payment" {
		proposal.Agreement = InboxAIBookingAgreement{
			Required: true, Title: quote.Agreement.Title,
			ConfirmationMethod: quote.Agreement.ConfirmationMethod,
			RenderedHTML:       quote.Agreement.RenderedHTML,
		}
		proposal.AgreementRequirement = "Agreement required before confirmation"
	}
	proposal.Hash, err = canonicalInboxAIProposalHash(
		proposal, quote.QuoteToken, customerDetailsRevision,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	nextState := "awaiting_confirmation"
	if proposal.Agreement.Required {
		nextState = "awaiting_before_booking_agreement"
	}
	nextActionID := uuid.New()
	nextRevision := record.Revision + 1
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_booking_sessions SET
			state=$2, selected_service_id=$3, timezone=$4,
			selected_start_at=$5, selected_end_at=$6, quote_id=$7, quote_token=$8,
			quote_expires_at=$9, currency_code=$10, total_amount_minor=$11,
			proposal_id=$12, proposal_revision=$13, proposal_hash=$14,
			proposal_customer_details_revision=$15,
			agreement_instance_id=$16, confirmation_id=NULL,
			booking_id=NULL, current_action_id=$17, revision=$18, expires_at=$9, updated_at=NOW()
		WHERE id=$1 AND revision=$19
	`, record.ID, nextState, serviceID, quote.Timezone, quoteRecord.StartsAt,
		quoteRecord.EndsAt, quoteRecord.ID, quote.QuoteToken, quoteRecord.ExpiresAt,
		quote.CurrencyCode, int64(quote.TotalAmountMinor), proposalID, proposalRevision,
		proposal.Hash, customerDetailsRevision, nullableInboxUUID(agreementInstanceID), nextActionID,
		nextRevision, record.Revision); err != nil {
		return InboxAIBookingActionResult{}, fmt.Errorf("save inbox booking proposal: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_sessions SET state=$2, selected_service_id=$3,
			revision=revision+1, expires_at=$4, updated_at=NOW()
		WHERE id=$1
	`, record.AISessionID, nextState, serviceID, quoteRecord.ExpiresAt); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	presentation, err := inboxAIProposalPresentation(proposal)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	content := "Review this exact reservation proposal. Your authenticated confirmation creates one reservation only if every canonical check still passes."
	message, summary, err := r.appendInboxAIWorkflowMessageTx(
		ctx, tx, clientID, marketplaceCustomerID, conversationID, idempotencyKey,
		content, presentation,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := auditInboxAIBookingAction(
		ctx, tx, record.AISessionID, conversationID, clientID, idempotencyKey,
		"refresh_booking_proposal", safeInput, map[string]any{
			"proposal_id": proposal.ID, "proposal_revision": proposal.Revision,
			"proposal_hash": proposal.Hash, "expires_at": proposal.ExpiresAt,
		},
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := appendInboxAISessionEvent(
		ctx, tx, conversationID, clientID, marketplaceCustomerID,
		nextState, "refresh_booking_proposal",
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	quoteAttached = true
	actionPending = false
	return InboxAIBookingActionResult{
		Workflow: workflow, Message: &message, Conversation: &summary,
	}, nil
}

func inboxAIProposalPresentation(proposal InboxAIBookingProposal) (InboxMessagePresentation, error) {
	actionLabel := "Confirm and reserve"
	if proposal.Confirmed {
		actionLabel = "Finish reservation"
	} else if proposal.Agreement.Required && !proposal.Agreement.Completed {
		actionLabel = "Review agreement and reserve"
	}
	data, err := jsonMarshal(map[string]any{
		"proposal_id": proposal.ID, "proposal_hash": proposal.Hash, "revision": proposal.Revision,
		"confirmed":  proposal.Confirmed,
		"expires_at": proposal.ExpiresAt.Format(time.RFC3339),
		"service_id": proposal.ServiceID, "service_title": proposal.ServiceTitle,
		"starts_at": proposal.StartsAt.Format(time.RFC3339),
		"ends_at":   proposal.EndsAt.Format(time.RFC3339), "timezone": proposal.Timezone,
		"fulfillment_mode": proposal.FulfillmentMode, "location_label": proposal.LocationLabel,
		"price_label": proposal.PriceLabel, "payment_requirement": proposal.PaymentRequirement,
		"agreement_requirement": proposal.AgreementRequirement, "action_label": actionLabel,
	})
	if err != nil {
		return InboxMessagePresentation{}, err
	}
	presentation := InboxMessagePresentation{Kind: "booking_proposal", Version: 1, Data: data}
	if err := validateInboxMessagePresentation(&presentation); err != nil {
		return InboxMessagePresentation{}, err
	}
	return presentation, nil
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func (r *Repository) createPrebookingAgreementFromQuote(
	ctx context.Context,
	tx pgx.Tx,
	quote bookingQuoteRecord,
	marketplaceCustomerID uuid.UUID,
) (uuid.UUID, error) {
	if r.agreementTokens == nil {
		return uuid.Nil, errors.New("agreement token encryption is not configured")
	}
	agreementID := uuid.New()
	token, err := r.agreementTokens.Generate(quote.ClientID, agreementID)
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_instances (
			id, client_id, customer_id, booking_id, template_family_id, template_version_id,
			title_snapshot, booking_summary_snapshot, resolved_document_snapshot,
			schema_version_snapshot, renderer_version_snapshot, rendered_html_snapshot,
			resolved_terms_hash, confirmation_method, timing, status,
			public_token_hash, public_token_ciphertext, public_token_nonce,
			public_token_key_version, sent_to_email, expires_at, created_at, updated_at
		) VALUES (
			$1,$2,NULL,NULL,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'awaiting_customer',
			$14,$15,$16,$17,$18,$19,NOW(),NOW()
		)
	`, agreementID, quote.ClientID, quote.AgreementTemplateFamilyID,
		quote.AgreementTemplateVersionID, quote.AgreementTitle,
		quote.AgreementBookingSummaryJSON, quote.AgreementResolvedDocumentJSON,
		quote.AgreementSchemaVersion, quote.AgreementRendererVersion,
		quote.AgreementRenderedHTML, quote.AgreementResolvedTermsHash,
		quote.AgreementConfirmationMethod, quote.AgreementTiming, token.Hash,
		token.Ciphertext.Data, token.Ciphertext.Nonce, token.Ciphertext.KeyVersion,
		quote.CustomerEmail, quote.ExpiresAt); err != nil {
		return uuid.Nil, fmt.Errorf("create prebooking agreement: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (
			id, agreement_id, event_type, actor_type, dedupe_key, metadata, occurred_at
		) VALUES (
			$1,$2,'created','system','created',
			jsonb_build_object('marketplace_customer_id',$3::uuid::text,'source','inbox_autopilot'),NOW()
		)
	`, uuid.New(), agreementID, marketplaceCustomerID); err != nil {
		return uuid.Nil, err
	}
	return agreementID, nil
}

func (r *Repository) AcceptMarketplaceInboxAIBookingAgreement(
	ctx context.Context,
	marketplaceCustomerID, conversationID, proposalID uuid.UUID,
	input AcceptInboxAIBookingAgreementInput,
) (InboxAIBookingActionResult, error) {
	idempotencyKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if !input.Accepted {
		return InboxAIBookingActionResult{}, ErrInboxAIAgreementRequired
	}
	evidenceFingerprint := sha256.Sum256([]byte(
		strings.TrimSpace(input.FullName) + "\x00" + strings.TrimSpace(input.SignatureDataURL),
	))
	safeInput := map[string]any{
		"proposal_id": proposalID.String(), "proposal_revision": input.ProposalRevision,
		"proposal_hash": input.ProposalHash, "accepted": input.Accepted,
		"evidence_fingerprint": fmt.Sprintf("%x", evidenceFingerprint),
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _, record, err := r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if replayed, replayErr := loadInboxAIBookingActionReplay(
		ctx, tx, conversationID, record.ClientID, idempotencyKey,
		"accept_booking_agreement", safeInput,
	); replayErr != nil {
		return InboxAIBookingActionResult{}, replayErr
	} else if replayed {
		workflow, workflowErr := r.loadInboxAIBookingWorkflow(
			ctx, tx, marketplaceCustomerID, conversationID,
		)
		if workflowErr != nil {
			return InboxAIBookingActionResult{}, workflowErr
		}
		if err := tx.Commit(ctx); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		return InboxAIBookingActionResult{Workflow: workflow, Replayed: true}, nil
	}
	if err := validateInboxAIProposalIdentity(record, proposalID, input.ProposalRevision, input.ProposalHash); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if !record.AgreementInstanceID.Valid {
		return InboxAIBookingActionResult{}, ErrInboxAIAgreementRequired
	}
	var method, status, termsHash string
	if err := tx.QueryRow(ctx, `
		SELECT confirmation_method, status, resolved_terms_hash
		FROM agreement_instances WHERE id=$1 FOR UPDATE
	`, record.AgreementInstanceID.UUID).Scan(&method, &status, &termsHash); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	replayed := status == "completed"
	if !replayed {
		if err := validateInboxAIProposalEvidence(
			record, proposalID, input.ProposalRevision, input.ProposalHash,
		); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if status != "awaiting_customer" {
			return InboxAIBookingActionResult{}, ErrInboxAIAgreementRequired
		}
		evidence := bookingAcceptanceEvidence{method: method, accepted: true}
		if method == "signature" {
			evidence.signer = strings.TrimSpace(input.FullName)
			if evidence.signer == "" {
				return InboxAIBookingActionResult{}, fmt.Errorf("%w: full_name is required", ErrInboxAIAgreementRequired)
			}
			normalized, normalizeErr := signature.NormalizeDataURL(input.SignatureDataURL)
			if normalizeErr != nil {
				return InboxAIBookingActionResult{}, normalizeErr
			}
			evidence.signature = &normalized
		} else if method != "confirmation" {
			return InboxAIBookingActionResult{}, ErrInboxAIAgreementRequired
		}
		if err := insertAgreementAcceptance(
			ctx, tx, record.AgreementInstanceID.UUID, termsHash, evidence,
		); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agreement_instances SET status='completed', completed_at=NOW(), updated_at=NOW()
			WHERE id=$1 AND status='awaiting_customer'
		`, record.AgreementInstanceID.UUID); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agreement_events (
				id, agreement_id, event_type, actor_type, dedupe_key, metadata, occurred_at
			) VALUES (
				$1,$2,'completed','customer','completed',
				jsonb_build_object('marketplace_customer_id',$3::uuid::text,'source','inbox_autopilot'),NOW()
			)
		`, uuid.New(), record.AgreementInstanceID.UUID, marketplaceCustomerID); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_booking_sessions SET state='awaiting_confirmation',
				revision=revision+1, updated_at=NOW() WHERE id=$1
		`, record.ID); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET state='awaiting_confirmation',
				revision=revision+1, updated_at=NOW() WHERE id=$1
		`, record.AISessionID); err != nil {
			return InboxAIBookingActionResult{}, err
		}
		if err := appendInboxAISessionEvent(
			ctx, tx, conversationID, record.ClientID, marketplaceCustomerID,
			"awaiting_confirmation", "agreement_completed",
		); err != nil {
			return InboxAIBookingActionResult{}, err
		}
	}
	if err := auditInboxAIBookingAction(
		ctx, tx, record.AISessionID, conversationID, record.ClientID, idempotencyKey,
		"accept_booking_agreement", safeInput,
		map[string]any{"agreement_instance_id": record.AgreementInstanceID.UUID.String()},
	); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingActionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingActionResult{}, err
	}
	return InboxAIBookingActionResult{Workflow: workflow, Replayed: replayed}, nil
}

func (r *Repository) recordMarketplaceInboxAIBookingConfirmationEvidence(
	ctx context.Context,
	marketplaceCustomerID, conversationID, proposalID uuid.UUID,
	input ConfirmInboxAIBookingProposalInput,
) (InboxAIBookingConfirmationResult, error) {
	idempotencyKey, err := parseInboxAIUUID(input.IdempotencyKey, "idempotency_key")
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	if !input.ContactDetailsConfirmed {
		return InboxAIBookingConfirmationResult{}, ErrInboxAIBookingDetailsRequired
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, _, record, err := r.lockInboxAIBookingAuthority(
		ctx, tx, marketplaceCustomerID, conversationID, true,
	)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	validateEvidence := validateInboxAIProposalEvidence
	if record.ConfirmationID.Valid {
		validateEvidence = validateInboxAIProposalIdentity
	}
	if err := validateEvidence(record, proposalID, input.ProposalRevision, input.ProposalHash); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	var customerDetailsRevision time.Time
	if !record.ConfirmationID.Valid {
		missing, missingErr := loadInboxAIMissingCustomerFields(ctx, tx, marketplaceCustomerID)
		if missingErr != nil {
			return InboxAIBookingConfirmationResult{}, missingErr
		}
		if len(missing) > 0 {
			return InboxAIBookingConfirmationResult{}, ErrInboxAIBookingDetailsRequired
		}
		if err := tx.QueryRow(ctx, `
			SELECT updated_at FROM marketplace_customers WHERE id=$1
		`, marketplaceCustomerID).Scan(&customerDetailsRevision); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if record.CustomerDetailsRevision == nil ||
			!record.CustomerDetailsRevision.Equal(customerDetailsRevision) {
			return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
		}
		if !record.QuoteID.Valid || record.QuoteExpiresAt == nil ||
			!record.QuoteExpiresAt.After(time.Now().UTC()) {
			return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
		}
		var quoteBookingID uuid.NullUUID
		if err := tx.QueryRow(ctx, `
			SELECT booking_id FROM booking_quotes WHERE id=$1 FOR UPDATE
		`, record.QuoteID.UUID).Scan(&quoteBookingID); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if quoteBookingID.Valid {
			return InboxAIBookingConfirmationResult{}, ErrInboxAIProposalStale
		}
		if record.AgreementInstanceID.Valid {
			var agreementStatus string
			if err := tx.QueryRow(ctx, `
				SELECT status FROM agreement_instances WHERE id=$1 FOR UPDATE
			`, record.AgreementInstanceID.UUID).Scan(&agreementStatus); err != nil {
				return InboxAIBookingConfirmationResult{}, err
			}
			if agreementStatus != "completed" {
				return InboxAIBookingConfirmationResult{}, ErrInboxAIAgreementRequired
			}
		}
	}
	var existingID uuid.UUID
	var existingProposalID uuid.UUID
	var existingRevision int64
	var existingHash string
	var existingContact, existingEmailReminder, existingWhatsApp, existingSMS bool
	var existingCustomerDetailsRevision time.Time
	var existingAgreement uuid.NullUUID
	var confirmedAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, proposal_id, proposal_revision, proposal_hash,
			contact_details_confirmed, customer_details_revision, email_reminder_consent,
			whatsapp_consent, sms_consent,
			agreement_instance_id, confirmed_at
		FROM inbox_ai_booking_confirmations
		WHERE booking_session_id=$1 AND (idempotency_key=$2 OR id=$3)
		ORDER BY (id=$3) DESC
		LIMIT 1
	`, record.ID, idempotencyKey, nullableInboxUUID(record.ConfirmationID)).Scan(
		&existingID, &existingProposalID, &existingRevision, &existingHash,
		&existingContact, &existingCustomerDetailsRevision, &existingEmailReminder,
		&existingWhatsApp, &existingSMS, &existingAgreement,
		&confirmedAt,
	)
	replayed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return InboxAIBookingConfirmationResult{}, err
	}
	if replayed {
		if existingProposalID != proposalID || existingRevision != input.ProposalRevision ||
			existingHash != input.ProposalHash || existingContact != input.ContactDetailsConfirmed ||
			existingEmailReminder != input.EmailReminderConsent ||
			existingWhatsApp != input.WhatsAppConsent || existingSMS != input.SMSConsent ||
			existingAgreement != record.AgreementInstanceID {
			return InboxAIBookingConfirmationResult{}, ErrInboxIdempotencyConflict
		}
	} else {
		existingID = uuid.New()
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_ai_booking_confirmations (
				id, booking_session_id, conversation_id, marketplace_customer_id,
				proposal_id, proposal_revision, proposal_hash, idempotency_key,
				contact_details_confirmed, customer_details_revision, email_reminder_consent,
				whatsapp_consent, sms_consent,
				agreement_instance_id, confirmed_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NOW())
			ON CONFLICT (booking_session_id,proposal_id,proposal_revision) DO UPDATE SET
				proposal_hash=inbox_ai_booking_confirmations.proposal_hash
			RETURNING id, confirmed_at
		`, existingID, record.ID, conversationID, marketplaceCustomerID,
			proposalID, input.ProposalRevision, input.ProposalHash, idempotencyKey,
			input.ContactDetailsConfirmed, customerDetailsRevision,
			input.EmailReminderConsent, input.WhatsAppConsent, input.SMSConsent,
			nullableInboxUUID(record.AgreementInstanceID)).Scan(&existingID, &confirmedAt); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_booking_sessions SET confirmation_id=$2,
				state='creating_reservation', revision=revision+1, updated_at=NOW() WHERE id=$1
		`, record.ID, existingID); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inbox_ai_sessions SET state='creating_reservation',
				revision=revision+1, updated_at=NOW() WHERE id=$1
		`, record.AISessionID); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
		if err := appendInboxAISessionEvent(
			ctx, tx, conversationID, record.ClientID, marketplaceCustomerID,
			"creating_reservation", "proposal_confirmed",
		); err != nil {
			return InboxAIBookingConfirmationResult{}, err
		}
	}
	workflow, err := r.loadInboxAIBookingWorkflow(ctx, tx, marketplaceCustomerID, conversationID)
	if err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	var message *InboxMessage
	var conversation *InboxConversationSummary
	if !replayed && workflow.Proposal != nil {
		presentation, presentationErr := inboxAIProposalPresentation(*workflow.Proposal)
		if presentationErr != nil {
			return InboxAIBookingConfirmationResult{}, presentationErr
		}
		committedMessage, summary, messageErr := r.appendInboxCustomerWorkflowMessageTx(
			ctx, tx, record.ClientID, marketplaceCustomerID, conversationID,
			existingID,
			"I confirmed this exact proposal and asked Tellbook to create the reservation.",
			presentation,
		)
		if messageErr != nil {
			return InboxAIBookingConfirmationResult{}, messageErr
		}
		message = &committedMessage
		conversation = &summary
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIBookingConfirmationResult{}, err
	}
	return InboxAIBookingConfirmationResult{
		Workflow: workflow, Messages: inboxAIMessages(message), Conversation: conversation,
		ConfirmationID: existingID.String(), ConfirmedAt: confirmedAt,
		ReservationCreated: false, Replayed: replayed,
	}, nil
}

func inboxAIMessages(message *InboxMessage) []InboxMessage {
	if message == nil {
		return []InboxMessage{}
	}
	return []InboxMessage{*message}
}

func validateInboxAIProposalEvidence(
	record inboxAIBookingSessionRecord,
	proposalID uuid.UUID,
	proposalRevision int64,
	proposalHash string,
) error {
	if err := validateInboxAIProposalIdentity(
		record, proposalID, proposalRevision, proposalHash,
	); err != nil {
		return err
	}
	if record.QuoteExpiresAt == nil || !record.QuoteExpiresAt.After(time.Now().UTC()) {
		return ErrInboxAIProposalStale
	}
	return nil
}

func validateInboxAIProposalIdentity(
	record inboxAIBookingSessionRecord,
	proposalID uuid.UUID,
	proposalRevision int64,
	proposalHash string,
) error {
	if !record.ProposalID.Valid || record.ProposalID.UUID != proposalID ||
		record.ProposalRevision != proposalRevision ||
		record.ProposalHash != strings.TrimSpace(proposalHash) {
		return ErrInboxAIProposalStale
	}
	return nil
}
