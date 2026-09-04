package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type inboxAIPolicyRecord struct {
	defaultMode              string
	enabledServiceIDs        []uuid.UUID
	paused                   bool
	maxTurns                 int
	inactivityTimeoutMinutes int
	revision                 int64
	updatedAt                *time.Time
}

func defaultInboxAIPolicyRecord() inboxAIPolicyRecord {
	return inboxAIPolicyRecord{
		defaultMode: InboxAIModeManual, maxTurns: 12, inactivityTimeoutMinutes: 60,
	}
}

func (r *Repository) GetInboxAIPolicy(ctx context.Context, clientID uuid.UUID) (InboxAIPolicy, error) {
	record, err := loadInboxAIPolicyRecord(ctx, r.db, clientID)
	if err != nil {
		return InboxAIPolicy{}, err
	}
	options, err := loadInboxAIServiceOptions(ctx, r.db, clientID, record.enabledServiceIDs)
	if err != nil {
		return InboxAIPolicy{}, err
	}
	return inboxAIPolicyResponse(record, options, r.inboxAIAutomationAvailable(clientID)), nil
}

func (r *Repository) UpdateInboxAIPolicy(
	ctx context.Context,
	clientID uuid.UUID,
	input UpdateInboxAIPolicyInput,
) (InboxAIPolicy, error) {
	input.DefaultMode = strings.TrimSpace(input.DefaultMode)
	if input.DefaultMode != InboxAIModeManual && input.DefaultMode != InboxAIModeSemiPilot &&
		input.DefaultMode != InboxAIModeAutopilot {
		return InboxAIPolicy{}, fmt.Errorf("%w: default_mode is invalid", ErrInboxAIInvalidState)
	}
	if input.ExpectedRevision < 0 || input.MaxTurns < 1 || input.MaxTurns > 30 ||
		input.InactivityTimeoutMinutes < 5 || input.InactivityTimeoutMinutes > 1440 {
		return InboxAIPolicy{}, fmt.Errorf("%w: AI policy limits are invalid", ErrInboxAIInvalidState)
	}
	if input.DefaultMode != InboxAIModeManual && !r.inboxAIAutomationAvailable(clientID) {
		return InboxAIPolicy{}, ErrInboxAIAutomationUnavailable
	}
	serviceIDs, err := parseUniqueUUIDs(input.EnabledServiceIDs, 100)
	if err != nil {
		return InboxAIPolicy{}, fmt.Errorf("%w: enabled_service_ids: %v", ErrInboxAIInvalidState, err)
	}
	if input.DefaultMode != InboxAIModeManual && len(serviceIDs) == 0 {
		return InboxAIPolicy{}, fmt.Errorf("%w: at least one service must be enabled for an automated mode", ErrInboxAIInvalidState)
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return InboxAIPolicy{}, fmt.Errorf("begin AI policy update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var lockedClientID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM clients WHERE id=$1 FOR UPDATE
	`, clientID).Scan(&lockedClientID); errors.Is(err, pgx.ErrNoRows) {
		return InboxAIPolicy{}, ErrNotFound
	} else if err != nil {
		return InboxAIPolicy{}, fmt.Errorf("lock AI policy owner: %w", err)
	}

	var currentRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT revision FROM inbox_ai_policies WHERE client_id=$1 FOR UPDATE
	`, clientID).Scan(&currentRevision); errors.Is(err, pgx.ErrNoRows) {
		currentRevision = 0
	} else if err != nil {
		return InboxAIPolicy{}, fmt.Errorf("lock AI policy: %w", err)
	}
	if currentRevision != input.ExpectedRevision {
		return InboxAIPolicy{}, ErrInboxAIRevisionConflict
	}
	if err := validateInboxAIServiceSelection(ctx, tx, clientID, serviceIDs, input.DefaultMode); err != nil {
		return InboxAIPolicy{}, err
	}
	nextRevision := currentRevision + 1
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_ai_policies (
			client_id, default_mode, enabled_service_ids, paused, max_turns,
			inactivity_timeout_minutes, revision, updated_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$1,NOW(),NOW())
		ON CONFLICT (client_id) DO UPDATE SET
			default_mode=EXCLUDED.default_mode,
			enabled_service_ids=EXCLUDED.enabled_service_ids,
			paused=EXCLUDED.paused,
			max_turns=EXCLUDED.max_turns,
			inactivity_timeout_minutes=EXCLUDED.inactivity_timeout_minutes,
			revision=EXCLUDED.revision,
			updated_by=EXCLUDED.updated_by,
			updated_at=NOW()
	`, clientID, input.DefaultMode, serviceIDs, input.Paused, input.MaxTurns,
		input.InactivityTimeoutMinutes, nextRevision); err != nil {
		return InboxAIPolicy{}, fmt.Errorf("save AI policy: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_ai_turn_jobs
		SET status='cancelled', error_code='policy_revised', lease_owner='',
			lease_expires_at=NULL, completed_at=NOW(), updated_at=NOW()
		WHERE client_id=$1 AND status IN ('queued','processing')
	`, clientID); err != nil {
		return InboxAIPolicy{}, fmt.Errorf("cancel AI turns after policy revision: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InboxAIPolicy{}, fmt.Errorf("commit AI policy: %w", err)
	}
	return r.GetInboxAIPolicy(ctx, clientID)
}

func loadInboxAIPolicyRecord(
	ctx context.Context,
	q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	clientID uuid.UUID,
) (inboxAIPolicyRecord, error) {
	record := defaultInboxAIPolicyRecord()
	var updatedAt time.Time
	err := q.QueryRow(ctx, `
		SELECT default_mode, enabled_service_ids, paused, max_turns,
			inactivity_timeout_minutes, revision, updated_at
		FROM inbox_ai_policies WHERE client_id=$1
	`, clientID).Scan(&record.defaultMode, &record.enabledServiceIDs, &record.paused,
		&record.maxTurns, &record.inactivityTimeoutMinutes, &record.revision, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, nil
	}
	if err != nil {
		return inboxAIPolicyRecord{}, fmt.Errorf("load AI policy: %w", err)
	}
	record.updatedAt = &updatedAt
	return record, nil
}

func loadInboxAIServiceOptions(
	ctx context.Context,
	q interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	},
	clientID uuid.UUID,
	enabled []uuid.UUID,
) ([]InboxAIServiceOption, error) {
	rows, err := q.Query(ctx, `
		SELECT service.id, service.title, service.fulfillment_mode, service.currency_code,
			service.price_amount_minor, service.standalone_signature_required
		FROM services service
		WHERE service.client_id=$1 AND service.status='published'
		  AND service.is_active AND NOT service.is_hidden
		ORDER BY service.sort_order, service.title, service.id
	`, clientID)
	if err != nil {
		return nil, fmt.Errorf("list AI policy services: %w", err)
	}
	defer rows.Close()
	enabledSet := make(map[uuid.UUID]struct{}, len(enabled))
	for _, id := range enabled {
		enabledSet[id] = struct{}{}
	}
	items := make([]InboxAIServiceOption, 0)
	for rows.Next() {
		var id uuid.UUID
		var item InboxAIServiceOption
		var standaloneSignatureRequired bool
		if err := rows.Scan(&id, &item.Title, &item.FulfillmentMode, &item.CurrencyCode,
			&item.BaseAmountMinor, &standaloneSignatureRequired); err != nil {
			return nil, fmt.Errorf("scan AI policy service: %w", err)
		}
		item.ID = id.String()
		_, item.Enabled = enabledSet[id]
		item.EligibleModes = []string{InboxAIModeSemiPilot}
		if standaloneSignatureRequired {
			item.AutopilotBlockReason = "standalone_signature_not_enabled"
		} else {
			item.EligibleModes = append(item.EligibleModes, InboxAIModeAutopilot)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AI policy services: %w", err)
	}
	return items, nil
}

func validateInboxAIServiceSelection(
	ctx context.Context,
	tx pgx.Tx,
	clientID uuid.UUID,
	serviceIDs []uuid.UUID,
	mode string,
) error {
	if len(serviceIDs) == 0 {
		return nil
	}
	var found int
	var allAutopilotEligible bool
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int,
			COALESCE(BOOL_AND(NOT service.standalone_signature_required), TRUE)
		FROM services service
		WHERE service.client_id=$1 AND service.id=ANY($2::uuid[])
		  AND service.status='published' AND service.is_active AND NOT service.is_hidden
	`, clientID, serviceIDs).Scan(&found, &allAutopilotEligible); err != nil {
		return fmt.Errorf("validate AI policy services: %w", err)
	}
	if found != len(serviceIDs) {
		return ErrInboxAIServiceUnavailable
	}
	if mode == InboxAIModeAutopilot && !allAutopilotEligible {
		return ErrInboxAIServiceUnavailable
	}
	return nil
}

func inboxAIPolicyResponse(
	record inboxAIPolicyRecord,
	services []InboxAIServiceOption,
	automationAvailable bool,
) InboxAIPolicy {
	if services == nil {
		services = []InboxAIServiceOption{}
	}
	enabled := make([]string, 0, len(record.enabledServiceIDs))
	for _, service := range services {
		if service.Enabled {
			enabled = append(enabled, service.ID)
		}
	}
	return InboxAIPolicy{
		DefaultMode: record.defaultMode, EnabledServiceIDs: enabled, Paused: record.paused,
		MaxTurns: record.maxTurns, InactivityTimeoutMinutes: record.inactivityTimeoutMinutes,
		Revision: record.revision, AutomationAvailable: automationAvailable,
		Services: services, UpdatedAt: record.updatedAt,
	}
}

func parseUniqueUUIDs(values []string, maximum int) ([]uuid.UUID, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("at most %d values are allowed", maximum)
	}
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, raw := range values {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("invalid UUID %q", raw)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}
