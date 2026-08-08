package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"booking/go-server/internal/agreements/domain"
	agreementrender "booking/go-server/internal/agreements/render"
	aiapi "booking/shared/ai_api"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CreateManagedAgreementInput struct {
	CustomerID       string            `json:"customer_id"`
	BookingID        string            `json:"booking_id"`
	TemplateFamilyID string            `json:"template_family_id"`
	Values           map[string]string `json:"values"`
	PersonalMessage  string            `json:"personal_message"`
	ExpiresAt        *time.Time        `json:"expires_at"`
}

type MissingAgreementVariablesError struct {
	Keys []string
}

func (e *MissingAgreementVariablesError) Error() string {
	return "missing agreement details: " + strings.Join(e.Keys, ", ")
}

func (r *Repository) CreateManagedAgreement(ctx context.Context, clientID uuid.UUID, input CreateManagedAgreementInput) (ManagedAgreementDetailsResponse, error) {
	if r.agreementTokens == nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("agreement token encryption is not configured")
	}
	customerID, err := uuid.Parse(strings.TrimSpace(input.CustomerID))
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("customer_id is invalid")
	}
	familyID, err := uuid.Parse(strings.TrimSpace(input.TemplateFamilyID))
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("template_family_id is invalid")
	}
	var bookingID *uuid.UUID
	if value := strings.TrimSpace(input.BookingID); value != "" {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return ManagedAgreementDetailsResponse{}, fmt.Errorf("booking_id is invalid")
		}
		bookingID = &parsed
	}
	if input.ExpiresAt != nil {
		now := time.Now().UTC()
		if !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.AddDate(1, 0, 0)) {
			return ManagedAgreementDetailsResponse{}, fmt.Errorf("expires_at must be within the next year")
		}
	}
	personalMessage := strings.TrimSpace(input.PersonalMessage)
	if len(personalMessage) > 2000 {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("personal_message must be 2000 characters or fewer")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("begin manual agreement creation: %w", err)
	}
	defer tx.Rollback(ctx)

	var customerName, customerEmail, customerPhone string
	if err := tx.QueryRow(ctx, `
		SELECT full_name, email, phone FROM customers WHERE client_id = $1 AND id = $2
	`, clientID, customerID).Scan(&customerName, &customerEmail, &customerPhone); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedAgreementDetailsResponse{}, ErrNotFound
		}
		return ManagedAgreementDetailsResponse{}, err
	}

	var title, method string
	var versionID uuid.UUID
	var documentJSON []byte
	if err := tx.QueryRow(ctx, `
		SELECT f.title, f.confirmation_method, v.id, v.document_schema
		FROM agreement_template_families f
		INNER JOIN agreement_template_versions v ON v.id = f.current_published_version_id
		WHERE f.client_id = $1 AND f.id = $2 AND f.owner_type = 'client'
		  AND f.status = 'published' AND v.state = 'published'
	`, clientID, familyID).Scan(&title, &method, &versionID, &documentJSON); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedAgreementDetailsResponse{}, ErrNotFound
		}
		return ManagedAgreementDetailsResponse{}, err
	}
	confirmationMethod, err := domain.ParseConfirmationMethod(method)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, err
	}
	var document aiapi.DocumentSchema
	if err := json.Unmarshal(documentJSON, &document); err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("decode agreement template: %w", err)
	}

	var businessName, businessLocation string
	if err := tx.QueryRow(ctx, `
		SELECT business_name, public_location_label FROM client_profiles WHERE client_id = $1
	`, clientID).Scan(&businessName, &businessLocation); err != nil {
		return ManagedAgreementDetailsResponse{}, err
	}
	values := map[string]string{
		"BUSINESS_NAME": businessName, "BUSINESS_LOCATION": businessLocation,
		"CUSTOMER_NAME": customerName, "CUSTOMER_EMAIL": customerEmail,
		"CUSTOMER_PHONE": customerPhone,
	}
	summary := agreementrender.BookingSummary{}
	if bookingID != nil {
		if err := r.resolveManualAgreementBooking(ctx, tx, clientID, customerID, *bookingID, values, &summary); err != nil {
			return ManagedAgreementDetailsResponse{}, err
		}
	}
	for key, value := range input.Values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) > 5000 {
			return ManagedAgreementDetailsResponse{}, fmt.Errorf("agreement value %s is too long", key)
		}
		if _, known := domain.AgreementVariable(key); known && strings.TrimSpace(values[key]) == "" {
			values[key] = value
		}
	}
	missing := make([]string, 0)
	for _, key := range document.VariableKeys() {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return ManagedAgreementDetailsResponse{}, &MissingAgreementVariablesError{Keys: missing}
	}
	snapshot, err := agreementrender.BuildSnapshot(title, summary, document, confirmationMethod, values)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("render manual agreement: %w", err)
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("encode agreement summary: %w", err)
	}
	resolvedJSON, err := json.Marshal(snapshot.ResolvedDocument)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("encode resolved agreement: %w", err)
	}
	agreementID := uuid.New()
	token, err := r.agreementTokens.Generate(clientID, agreementID)
	if err != nil {
		return ManagedAgreementDetailsResponse{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_instances (
			id, client_id, customer_id, booking_id, template_family_id, template_version_id,
			title_snapshot, booking_summary_snapshot, resolved_document_snapshot,
			schema_version_snapshot, renderer_version_snapshot, rendered_html_snapshot,
			resolved_terms_hash, confirmation_method, timing, status,
			public_token_hash, public_token_ciphertext, public_token_nonce,
			public_token_key_version, sent_to_email, personal_message_snapshot,
			expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'manual','draft',
		          $15,$16,$17,$18,$19,$20,$21,NOW(),NOW())
	`, agreementID, clientID, customerID, bookingID, familyID, versionID, title,
		summaryJSON, resolvedJSON, snapshot.SchemaVersion, snapshot.RendererVersion,
		snapshot.RenderedHTML, snapshot.ResolvedTermsHash, confirmationMethod,
		token.Hash, token.Ciphertext.Data, token.Ciphertext.Nonce, token.Ciphertext.KeyVersion,
		customerEmail, personalMessage, input.ExpiresAt); err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("create manual agreement: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_events (id, agreement_id, event_type, actor_type, dedupe_key, occurred_at)
		VALUES ($1,$2,'created','business','created',NOW())
	`, uuid.New(), agreementID); err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("record manual agreement creation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ManagedAgreementDetailsResponse{}, fmt.Errorf("commit manual agreement creation: %w", err)
	}
	return r.GetManagedAgreement(ctx, clientID, agreementID)
}

func (r *Repository) resolveManualAgreementBooking(
	ctx context.Context,
	q agreementQueryer,
	clientID, customerID, bookingID uuid.UUID,
	values map[string]string,
	summary *agreementrender.BookingSummary,
) error {
	var bookingCustomerID uuid.UUID
	var serviceName, locationLabel, countryCode, currencyCode, timezone, cancellation, lateness, notes string
	var startAt, endAt time.Time
	var totalMinor, depositMinor int64
	var durationMinutes int
	if err := q.QueryRow(ctx, `
		SELECT customer_id, title, start_at, end_at, location_label, country_code,
		       currency_code, timezone, total_amount_minor, deposit_amount_minor,
		       duration_minutes, cancellation_policy_snapshot, lateness_policy_snapshot, notes
		FROM bookings WHERE client_id = $1 AND id = $2
	`, clientID, bookingID).Scan(
		&bookingCustomerID, &serviceName, &startAt, &endAt, &locationLabel,
		&countryCode, &currencyCode, &timezone, &totalMinor, &depositMinor,
		&durationMinutes, &cancellation, &lateness, &notes,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if bookingCustomerID != customerID {
		return fmt.Errorf("booking does not belong to the selected customer")
	}
	if depositMinor < 0 {
		depositMinor = 0
	}
	if depositMinor > totalMinor {
		depositMinor = totalMinor
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("load booking timezone: %w", err)
	}
	startAt, endAt = startAt.In(location), endAt.In(location)
	total, err := formatMarketMoney(totalMinor, countryCode, currencyCode)
	if err != nil {
		return err
	}
	deposit, err := formatMarketMoney(depositMinor, countryCode, currencyCode)
	if err != nil {
		return err
	}
	remaining, err := formatMarketMoney(totalMinor-depositMinor, countryCode, currencyCode)
	if err != nil {
		return err
	}
	date := startAt.Format("Monday, Jan 2, 2006")
	startTime, endTime := startAt.Format("03:04 PM"), endAt.Format("03:04 PM")
	timeRange := startTime + " - " + endTime
	values["SERVICE_NAME"] = serviceName
	values["BOOKING_DATE"] = date
	values["BOOKING_START_TIME"], values["BOOKING_END_TIME"] = startTime, endTime
	values["BOOKING_TIME_RANGE"] = timeRange
	values["BOOKING_LOCATION"] = locationLabel
	values["TOTAL_AMOUNT"] = total
	values["DEPOSIT_AMOUNT"], values["REMAINING_AMOUNT"] = deposit, remaining
	values["DURATION_MINUTES"] = fmt.Sprintf("%d", durationMinutes)
	values["SERVICE_DURATION"] = formatDurationLabel(durationMinutes)
	values["BOOKING_NOTES"] = strings.TrimSpace(notes)
	values["CANCELLATION_POLICY"] = strings.TrimSpace(cancellation)
	values["LATENESS_POLICY"] = strings.TrimSpace(lateness)
	*summary = agreementrender.BookingSummary{
		ServiceName: serviceName, Date: date, Time: timeRange,
		Location: locationLabel, TotalAmount: total,
	}
	return nil
}

func (r *Repository) SendManagedAgreement(ctx context.Context, clientID, agreementID uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status, email string
	var revision int
	if err := tx.QueryRow(ctx, `
		SELECT status, sent_to_email, delivery_revision
		FROM agreement_instances
		WHERE client_id = $1 AND id = $2 AND timing = 'manual'
		FOR UPDATE
	`, clientID, agreementID).Scan(&status, &email, &revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != "draft" && status != "awaiting_customer" {
		return ErrNotFound
	}
	if strings.TrimSpace(email) == "" {
		return fmt.Errorf("agreement customer email is unavailable")
	}
	if status == "draft" {
		if _, err := tx.Exec(ctx, `UPDATE agreement_instances SET status='awaiting_customer', updated_at=NOW() WHERE id=$1`, agreementID); err != nil {
			return err
		}
	}
	key := fmt.Sprintf("initial-email:%s:%d", agreementID, revision)
	if _, err := tx.Exec(ctx, `
		INSERT INTO agreement_jobs (id, agreement_id, kind, dedupe_key, status, run_at, created_at, updated_at)
		VALUES ($1,$2,'send_agreement_email',$3,'queued',NOW(),NOW(),NOW())
		ON CONFLICT (dedupe_key) DO NOTHING
	`, uuid.New(), agreementID, key); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) ActivateManagedAgreementLink(ctx context.Context, clientID, agreementID uuid.UUID) (string, error) {
	command, err := r.db.Exec(ctx, `
		UPDATE agreement_instances
		SET status = CASE
		        WHEN timing = 'manual' AND status = 'draft' THEN 'awaiting_customer'
		        ELSE status
		    END,
		    updated_at=NOW()
		WHERE client_id=$1 AND id=$2
		  AND (
		    (timing = 'manual' AND status IN ('draft', 'awaiting_customer', 'completed'))
		    OR (timing IN ('before_payment', 'after_payment') AND status IN ('awaiting_customer', 'completed'))
		  )
	`, clientID, agreementID)
	if err != nil {
		return "", err
	}
	if command.RowsAffected() != 1 {
		return "", ErrNotFound
	}
	return r.GetManagedAgreementDeliveryToken(ctx, clientID, agreementID)
}
