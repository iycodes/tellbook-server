package appdata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrServiceWizardDraftConflict = errors.New("service wizard draft revision conflict")

const maxServiceWizardDraftPayloadBytes = 256 << 10

var validServiceWizardSteps = map[string]struct{}{
	"preset":             {},
	"choose-section":     {},
	"info":               {},
	"pricing":            {},
	"duration":           {},
	"availability":       {},
	"location":           {},
	"policy":             {},
	"agreement-settings": {},
	"preview":            {},
}

type ServiceWizardDraft struct {
	ID          string          `json:"id"`
	ServiceID   string          `json:"service_id,omitempty"`
	Payload     json.RawMessage `json:"payload"`
	CurrentStep string          `json:"current_step"`
	Revision    int64           `json:"revision"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type CreateServiceWizardDraftInput struct {
	ServiceID   string          `json:"service_id"`
	Payload     json.RawMessage `json:"payload"`
	CurrentStep string          `json:"current_step"`
}

type UpdateServiceWizardDraftInput struct {
	Revision    int64           `json:"revision"`
	Payload     json.RawMessage `json:"payload"`
	CurrentStep string          `json:"current_step"`
}

func (r *Repository) CreateServiceWizardDraft(ctx context.Context, clientID uuid.UUID, input CreateServiceWizardDraftInput) (ServiceWizardDraft, error) {
	payload, step, err := validateServiceWizardDraftState(input.Payload, input.CurrentStep)
	if err != nil {
		return ServiceWizardDraft{}, err
	}

	var serviceID *uuid.UUID
	if strings.TrimSpace(input.ServiceID) != "" {
		parsed, parseErr := uuid.Parse(strings.TrimSpace(input.ServiceID))
		if parseErr != nil {
			return ServiceWizardDraft{}, fmt.Errorf("service_id is invalid")
		}
		var exists bool
		if queryErr := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE client_id = $1 AND id = $2)`, clientID, parsed).Scan(&exists); queryErr != nil {
			return ServiceWizardDraft{}, fmt.Errorf("validate service wizard draft service: %w", queryErr)
		}
		if !exists {
			return ServiceWizardDraft{}, ErrNotFound
		}
		serviceID = &parsed
	}

	id := uuid.New()
	if serviceID == nil {
		_, err = r.db.Exec(ctx, `
			INSERT INTO service_wizard_drafts (id, client_id, payload, current_step)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (client_id) WHERE service_id IS NULL DO NOTHING
		`, id, clientID, payload, step)
	} else {
		_, err = r.db.Exec(ctx, `
			INSERT INTO service_wizard_drafts (id, client_id, service_id, payload, current_step)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (client_id, service_id) WHERE service_id IS NOT NULL DO NOTHING
		`, id, clientID, *serviceID, payload, step)
	}
	if err != nil {
		return ServiceWizardDraft{}, fmt.Errorf("create service wizard draft: %w", err)
	}

	if serviceID == nil {
		return r.getServiceWizardDraftByScope(ctx, clientID, nil)
	}
	return r.getServiceWizardDraftByScope(ctx, clientID, serviceID)
}

func (r *Repository) GetServiceWizardDraft(ctx context.Context, clientID, draftID uuid.UUID) (ServiceWizardDraft, error) {
	return scanServiceWizardDraft(r.db.QueryRow(ctx, `
		SELECT id, service_id, payload, current_step, revision, updated_at
		FROM service_wizard_drafts
		WHERE client_id = $1 AND id = $2
	`, clientID, draftID))
}

func (r *Repository) UpdateServiceWizardDraft(ctx context.Context, clientID, draftID uuid.UUID, input UpdateServiceWizardDraftInput) (ServiceWizardDraft, error) {
	if input.Revision <= 0 {
		return ServiceWizardDraft{}, fmt.Errorf("revision must be positive")
	}
	payload, step, err := validateServiceWizardDraftState(input.Payload, input.CurrentStep)
	if err != nil {
		return ServiceWizardDraft{}, err
	}

	draft, err := scanServiceWizardDraft(r.db.QueryRow(ctx, `
		UPDATE service_wizard_drafts
		SET payload = $4, current_step = $5, revision = revision + 1, updated_at = NOW()
		WHERE client_id = $1 AND id = $2 AND revision = $3
		RETURNING id, service_id, payload, current_step, revision, updated_at
	`, clientID, draftID, input.Revision, payload, step))
	if errors.Is(err, ErrNotFound) {
		var exists bool
		if queryErr := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_wizard_drafts WHERE client_id = $1 AND id = $2)`, clientID, draftID).Scan(&exists); queryErr != nil {
			return ServiceWizardDraft{}, fmt.Errorf("check service wizard draft revision: %w", queryErr)
		}
		if exists {
			return ServiceWizardDraft{}, ErrServiceWizardDraftConflict
		}
	}
	return draft, err
}

func (r *Repository) DeleteServiceWizardDraft(ctx context.Context, clientID, draftID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM service_wizard_drafts WHERE client_id = $1 AND id = $2`, clientID, draftID)
	if err != nil {
		return fmt.Errorf("delete service wizard draft: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) getServiceWizardDraftByScope(ctx context.Context, clientID uuid.UUID, serviceID *uuid.UUID) (ServiceWizardDraft, error) {
	if serviceID == nil {
		return scanServiceWizardDraft(r.db.QueryRow(ctx, `
			SELECT id, service_id, payload, current_step, revision, updated_at
			FROM service_wizard_drafts
			WHERE client_id = $1 AND service_id IS NULL
		`, clientID))
	}
	return scanServiceWizardDraft(r.db.QueryRow(ctx, `
		SELECT id, service_id, payload, current_step, revision, updated_at
		FROM service_wizard_drafts
		WHERE client_id = $1 AND service_id = $2
	`, clientID, *serviceID))
}

func scanServiceWizardDraft(row pgx.Row) (ServiceWizardDraft, error) {
	var draft ServiceWizardDraft
	var id uuid.UUID
	var serviceID *uuid.UUID
	if err := row.Scan(&id, &serviceID, &draft.Payload, &draft.CurrentStep, &draft.Revision, &draft.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceWizardDraft{}, ErrNotFound
		}
		return ServiceWizardDraft{}, fmt.Errorf("scan service wizard draft: %w", err)
	}
	draft.ID = id.String()
	if serviceID != nil {
		draft.ServiceID = serviceID.String()
	}
	return draft, nil
}

func validateServiceWizardDraftState(raw json.RawMessage, currentStep string) (json.RawMessage, string, error) {
	if len(raw) == 0 || len(raw) > maxServiceWizardDraftPayloadBytes {
		return nil, "", fmt.Errorf("payload must be a JSON object no larger than 256 KB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, "", fmt.Errorf("payload must be a JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, "", fmt.Errorf("payload must contain one JSON object")
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, "", fmt.Errorf("normalize service wizard draft payload: %w", err)
	}
	step := strings.TrimSpace(currentStep)
	if _, ok := validServiceWizardSteps[step]; !ok {
		return nil, "", fmt.Errorf("current_step is invalid")
	}
	return normalized, step, nil
}
