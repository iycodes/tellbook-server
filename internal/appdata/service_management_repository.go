package appdata

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *Repository) ListServiceSections(ctx context.Context, clientID uuid.UUID) ([]ServiceSectionItem, error) {
	const query = `
		SELECT
			ss.id,
			ss.name,
			ss.description,
			COALESCE(ss.cover_image_url, ''),
			COUNT(s.id),
			ss.updated_at
		FROM service_sections ss
		LEFT JOIN services s
			ON s.section_id = ss.id
			AND s.client_id = ss.client_id
		WHERE ss.client_id = $1
		GROUP BY ss.id
		ORDER BY ss.sort_order ASC, ss.created_at ASC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list service sections: %w", err)
	}
	defer rows.Close()

	items := make([]ServiceSectionItem, 0)
	for rows.Next() {
		var item ServiceSectionItem
		var id uuid.UUID
		var updatedAt time.Time
		if err := rows.Scan(&id, &item.Name, &item.Description, &item.CoverImageURL, &item.ServiceCount, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan service section: %w", err)
		}
		item.ID = id.String()
		item.UpdatedLabel = formatUpdatedLabel(updatedAt)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service sections: %w", err)
	}

	return items, nil
}

func (r *Repository) CreateServiceSection(ctx context.Context, clientID uuid.UUID, input CreateServiceSectionInput) (ServiceSectionItem, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ServiceSectionItem{}, fmt.Errorf("section name is required")
	}

	slug := slugify(name)
	id := uuid.New()

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ServiceSectionItem{}, fmt.Errorf("begin create service section: %w", err)
	}
	defer tx.Rollback(ctx)

	var nextSortOrder int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM service_sections WHERE client_id = $1`, clientID).Scan(&nextSortOrder); err != nil {
		return ServiceSectionItem{}, fmt.Errorf("get next section sort order: %w", err)
	}

	if err := ensureUniqueSectionSlug(ctx, tx, clientID, &slug); err != nil {
		return ServiceSectionItem{}, err
	}

	const query = `
		INSERT INTO service_sections (
			id, client_id, name, slug, description, cover_image_url, sort_order, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NOW(),NOW())
	`
	if _, err := tx.Exec(ctx, query, id, clientID, name, slug, strings.TrimSpace(input.Description), nullIfBlank(input.CoverImageURL), nextSortOrder); err != nil {
		return ServiceSectionItem{}, fmt.Errorf("insert service section: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ServiceSectionItem{}, fmt.Errorf("commit create service section: %w", err)
	}

	return ServiceSectionItem{
		ID:            id.String(),
		Name:          name,
		Description:   strings.TrimSpace(input.Description),
		CoverImageURL: strings.TrimSpace(input.CoverImageURL),
		ServiceCount:  0,
		UpdatedLabel:  "Updated just now",
	}, nil
}

func (r *Repository) UpdateServiceSection(ctx context.Context, clientID, sectionID uuid.UUID, input UpdateServiceSectionInput) (ServiceSectionItem, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ServiceSectionItem{}, fmt.Errorf("section name is required")
	}

	var coverImageURL string
	var updatedAt time.Time
	if err := r.db.QueryRow(
		ctx,
		`UPDATE service_sections
		 SET name = $3, description = $4, cover_image_url = $5, updated_at = NOW()
		 WHERE client_id = $1 AND id = $2
		 RETURNING COALESCE(cover_image_url, ''), updated_at`,
		clientID,
		sectionID,
		name,
		strings.TrimSpace(input.Description),
		nullIfBlank(input.CoverImageURL),
	).Scan(&coverImageURL, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceSectionItem{}, ErrNotFound
		}
		return ServiceSectionItem{}, fmt.Errorf("update service section: %w", err)
	}

	if _, err := r.db.Exec(ctx, `UPDATE services SET category = $3, updated_at = NOW() WHERE client_id = $1 AND section_id = $2`, clientID, sectionID, name); err != nil {
		return ServiceSectionItem{}, fmt.Errorf("sync service section category: %w", err)
	}

	var serviceCount int
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM services WHERE client_id = $1 AND section_id = $2`, clientID, sectionID).Scan(&serviceCount); err != nil {
		return ServiceSectionItem{}, fmt.Errorf("count service section items: %w", err)
	}

	return ServiceSectionItem{
		ID:            sectionID.String(),
		Name:          name,
		Description:   strings.TrimSpace(input.Description),
		CoverImageURL: coverImageURL,
		ServiceCount:  serviceCount,
		UpdatedLabel:  formatUpdatedLabel(updatedAt),
	}, nil
}

func (r *Repository) DeleteServiceSection(ctx context.Context, clientID, sectionID uuid.UUID, input DeleteServiceSectionInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete service section: %w", err)
	}
	defer tx.Rollback(ctx)

	switch input.Mode {
	case "", "uncategorized":
		if _, err := tx.Exec(ctx, `UPDATE services SET section_id = NULL, category = '' WHERE client_id = $1 AND section_id = $2`, clientID, sectionID); err != nil {
			return fmt.Errorf("uncategorize services: %w", err)
		}
	case "move":
		targetID, err := uuid.Parse(strings.TrimSpace(input.TargetSectionID))
		if err != nil {
			return fmt.Errorf("target section is required when move mode is used")
		}
		if _, err := tx.Exec(ctx, `UPDATE services SET section_id = $3, category = COALESCE((SELECT name FROM service_sections WHERE id = $3 AND client_id = $1), category) WHERE client_id = $1 AND section_id = $2`, clientID, sectionID, targetID); err != nil {
			return fmt.Errorf("move services to section: %w", err)
		}
	default:
		return fmt.Errorf("unsupported delete mode")
	}

	commandTag, err := tx.Exec(ctx, `DELETE FROM service_sections WHERE client_id = $1 AND id = $2`, clientID, sectionID)
	if err != nil {
		return fmt.Errorf("delete service section: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete service section: %w", err)
	}

	return nil
}

func (r *Repository) GetServiceSectionDetails(ctx context.Context, clientID, sectionID uuid.UUID) (ServiceSectionDetailsResponse, error) {
	const sectionQuery = `
		SELECT
			ss.id,
			ss.name,
			ss.description,
			COALESCE(ss.cover_image_url, ''),
			COUNT(s.id),
			ss.updated_at
		FROM service_sections ss
		LEFT JOIN services s
			ON s.section_id = ss.id
			AND s.client_id = ss.client_id
		WHERE ss.client_id = $1 AND ss.id = $2
		GROUP BY ss.id
	`

	var section ServiceSectionItem
	var sectionUUID uuid.UUID
	var updatedAt time.Time
	if err := r.db.QueryRow(ctx, sectionQuery, clientID, sectionID).Scan(
		&sectionUUID,
		&section.Name,
		&section.Description,
		&section.CoverImageURL,
		&section.ServiceCount,
		&updatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceSectionDetailsResponse{}, ErrNotFound
		}
		return ServiceSectionDetailsResponse{}, fmt.Errorf("get service section details: %w", err)
	}
	section.ID = sectionUUID.String()
	section.UpdatedLabel = formatUpdatedLabel(updatedAt)

	services, err := r.listManagedServicesWithWhere(ctx, clientID, `s.section_id = $2`, sectionID)
	if err != nil {
		return ServiceSectionDetailsResponse{}, err
	}

	return ServiceSectionDetailsResponse{
		Section:  section,
		Services: services,
	}, nil
}

func (r *Repository) ReorderServiceSections(ctx context.Context, clientID uuid.UUID, orderedIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder service sections: %w", err)
	}
	defer tx.Rollback(ctx)

	for index, sectionID := range orderedIDs {
		commandTag, err := tx.Exec(ctx, `UPDATE service_sections SET sort_order = $3, updated_at = NOW() WHERE client_id = $1 AND id = $2`, clientID, sectionID, index+1)
		if err != nil {
			return fmt.Errorf("reorder service section: %w", err)
		}
		if commandTag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder service sections: %w", err)
	}
	return nil
}

func (r *Repository) ReorderSectionServices(ctx context.Context, clientID, sectionID uuid.UUID, orderedIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder services: %w", err)
	}
	defer tx.Rollback(ctx)

	for index, serviceID := range orderedIDs {
		commandTag, err := tx.Exec(ctx, `UPDATE services SET sort_order = $4, updated_at = NOW() WHERE client_id = $1 AND id = $2 AND section_id = $3`, clientID, serviceID, sectionID, index+1)
		if err != nil {
			return fmt.Errorf("reorder section services: %w", err)
		}
		if commandTag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder section services: %w", err)
	}
	return nil
}

func (r *Repository) ReorderUncategorizedServices(ctx context.Context, clientID uuid.UUID, orderedIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder uncategorized services: %w", err)
	}
	defer tx.Rollback(ctx)

	for index, serviceID := range orderedIDs {
		commandTag, err := tx.Exec(ctx, `UPDATE services SET sort_order = $3, updated_at = NOW() WHERE client_id = $1 AND id = $2 AND section_id IS NULL`, clientID, serviceID, index+1)
		if err != nil {
			return fmt.Errorf("reorder uncategorized services: %w", err)
		}
		if commandTag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder uncategorized services: %w", err)
	}
	return nil
}

func (r *Repository) ListManagedServices(ctx context.Context, clientID uuid.UUID) ([]ManagedServiceItem, error) {
	return r.listManagedServicesWithWhere(ctx, clientID, "", nil)
}

func (r *Repository) listManagedServicesWithWhere(ctx context.Context, clientID uuid.UUID, whereClause string, arg any) ([]ManagedServiceItem, error) {
	const query = `
		SELECT
			s.id, s.title, s.description, s.currency_code, s.duration_minutes,
			s.status, COALESCE(s.is_hidden, FALSE), COALESCE(s.image_url, ''),
			COALESCE(ss.id::text, ''), COALESCE(ss.name, ''), s.badge,
			s.price_amount_minor, s.compare_price_amount_minor, s.deposit_required,
			s.deposit_type, s.deposit_amount_minor, s.deposit_percentage_bps,
			s.fulfillment_mode, COALESCE(s.provider_location_id::text, ''),
			COALESCE(bl.label, ''), s.travel_fee_minor, s.max_travel_distance_meters,
			s.availability_mode, s.minimum_notice_minutes, s.max_bookings_per_day,
			s.prep_time_minutes, s.buffer_time_minutes,
			s.virtual_delivery_label, COALESCE(s.virtual_join_url, ''),
			COALESCE(s.virtual_instructions, ''), s.cancellation_policy,
			s.lateness_policy, COALESCE(s.agreement_template_family_id::text, ''),
			COALESCE(atf.title, ''), COALESCE(atf.confirmation_method, ''),
			COALESCE(s.agreement_timing, ''), s.standalone_signature_required,
			s.prep_aftercare_instructions
		FROM services s
		LEFT JOIN service_sections ss ON ss.id = s.section_id AND ss.client_id = s.client_id
		LEFT JOIN business_locations bl
			ON bl.id = s.provider_location_id AND bl.client_id = s.client_id
		LEFT JOIN agreement_template_families atf
			ON atf.id = s.agreement_template_family_id AND atf.client_id = s.client_id
		WHERE s.client_id = $1 %s
		ORDER BY s.sort_order ASC, s.created_at DESC
	`
	renderedQuery := fmt.Sprintf(query, func() string {
		if whereClause == "" {
			return ""
		}
		return " AND " + whereClause
	}())

	var rows pgx.Rows
	var err error
	if whereClause == "" {
		rows, err = r.db.Query(ctx, renderedQuery, clientID)
	} else {
		rows, err = r.db.Query(ctx, renderedQuery, clientID, arg)
	}
	if err != nil {
		return nil, fmt.Errorf("list managed services: %w", err)
	}
	defer rows.Close()

	items := make([]ManagedServiceItem, 0)
	for rows.Next() {
		item, err := scanManagedService(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed services: %w", err)
	}

	return items, nil
}

func (r *Repository) GetManagedServiceDetails(ctx context.Context, clientID, serviceID uuid.UUID) (ManagedServiceItem, error) {
	const query = `
		SELECT
			s.id, s.title, s.description, s.currency_code, s.duration_minutes,
			s.status, COALESCE(s.is_hidden, FALSE), COALESCE(s.image_url, ''),
			COALESCE(ss.id::text, ''), COALESCE(ss.name, ''), s.badge,
			s.price_amount_minor, s.compare_price_amount_minor, s.deposit_required,
			s.deposit_type, s.deposit_amount_minor, s.deposit_percentage_bps,
			s.fulfillment_mode, COALESCE(s.provider_location_id::text, ''),
			COALESCE(bl.label, ''), s.travel_fee_minor, s.max_travel_distance_meters,
			s.availability_mode, s.minimum_notice_minutes, s.max_bookings_per_day,
			s.prep_time_minutes, s.buffer_time_minutes,
			s.virtual_delivery_label, COALESCE(s.virtual_join_url, ''),
			COALESCE(s.virtual_instructions, ''), s.cancellation_policy,
			s.lateness_policy, COALESCE(s.agreement_template_family_id::text, ''),
			COALESCE(atf.title, ''), COALESCE(atf.confirmation_method, ''),
			COALESCE(s.agreement_timing, ''), s.standalone_signature_required,
			s.prep_aftercare_instructions
		FROM services s
		LEFT JOIN service_sections ss ON ss.id = s.section_id AND ss.client_id = s.client_id
		LEFT JOIN business_locations bl
			ON bl.id = s.provider_location_id AND bl.client_id = s.client_id
		LEFT JOIN agreement_template_families atf
			ON atf.id = s.agreement_template_family_id AND atf.client_id = s.client_id
		WHERE s.client_id = $1 AND s.id = $2
	`
	item, err := scanManagedService(r.db.QueryRow(ctx, query, clientID, serviceID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedServiceItem{}, ErrNotFound
		}
		return ManagedServiceItem{}, err
	}
	if err := r.loadManagedServiceChildren(ctx, serviceID, &item); err != nil {
		return ManagedServiceItem{}, err
	}
	return item, nil
}

func (r *Repository) UpdateManagedService(ctx context.Context, clientID, serviceID uuid.UUID, input CreateManagedServiceInput) (ManagedServiceItem, error) {
	return r.saveManagedService(ctx, clientID, serviceID, input, false)
}

func (r *Repository) DuplicateManagedService(ctx context.Context, clientID, serviceID uuid.UUID) (ManagedServiceItem, error) {
	var newServiceID uuid.UUID
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ManagedServiceItem{}, fmt.Errorf("begin duplicate managed service: %w", err)
	}
	defer tx.Rollback(ctx)

	var sourceName string
	var sourceSlug string
	if err := tx.QueryRow(ctx, `SELECT title, slug FROM services WHERE client_id = $1 AND id = $2`, clientID, serviceID).Scan(&sourceName, &sourceSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedServiceItem{}, ErrNotFound
		}
		return ManagedServiceItem{}, fmt.Errorf("load source service: %w", err)
	}

	newServiceID = uuid.New()
	newName := strings.TrimSpace(sourceName) + " (Copy)"
	newSlug := slugify(newName)
	if err := ensureUniqueServiceSlug(ctx, tx, clientID, &newSlug); err != nil {
		return ManagedServiceItem{}, err
	}

	var nextSortOrder int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM services WHERE client_id = $1`, clientID).Scan(&nextSortOrder); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("get next service sort order: %w", err)
	}

	commandTag, err := tx.Exec(ctx, `
		INSERT INTO services (
			id, client_id, section_id, title, slug, description, category, icon_name, image_url,
			duration_minutes, price_amount_minor, is_active, sort_order, status, is_hidden,
			compare_price_amount_minor, deposit_required, deposit_type, deposit_amount_minor, deposit_percentage_bps,
			prep_time_minutes, buffer_time_minutes, availability_mode, minimum_notice_minutes,
			max_bookings_per_day, fulfillment_mode, provider_location_id, travel_fee_minor,
			max_travel_distance_meters, virtual_delivery_label, virtual_join_url, virtual_instructions,
			cancellation_policy, lateness_policy, agreement_template_family_id, agreement_timing,
			standalone_signature_required, prep_aftercare_instructions, badge, currency_code,
			created_at, updated_at
		)
		SELECT
			$3, client_id, section_id, $4, $5, description, category, icon_name, image_url,
			duration_minutes, price_amount_minor, false, $6, 'draft', is_hidden,
			compare_price_amount_minor, deposit_required, deposit_type, deposit_amount_minor, deposit_percentage_bps,
			prep_time_minutes, buffer_time_minutes, availability_mode, minimum_notice_minutes,
			max_bookings_per_day, fulfillment_mode, provider_location_id, travel_fee_minor,
			max_travel_distance_meters, virtual_delivery_label, virtual_join_url, virtual_instructions,
			cancellation_policy, lateness_policy, agreement_template_family_id, agreement_timing,
			standalone_signature_required, prep_aftercare_instructions, badge, currency_code,
			NOW(), NOW()
		FROM services
		WHERE client_id = $1 AND id = $2
	`, clientID, serviceID, newServiceID, newName, newSlug, nextSortOrder)
	if err != nil {
		return ManagedServiceItem{}, fmt.Errorf("duplicate managed service: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ManagedServiceItem{}, ErrNotFound
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO service_availability_windows (
			id, service_id, day_of_week, start_time, end_time,
			slot_interval_minutes, created_at, updated_at
		)
		SELECT gen_random_uuid(), $2, day_of_week, start_time, end_time,
			slot_interval_minutes, NOW(), NOW()
		FROM service_availability_windows
		WHERE service_id = $1
	`, serviceID, newServiceID); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("duplicate service availability: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO service_short_notice_rules (
			id, service_id, threshold_minutes, surcharge_type,
			surcharge_amount_minor, surcharge_percentage_bps, created_at, updated_at
		)
		SELECT gen_random_uuid(), $2, threshold_minutes, surcharge_type,
			surcharge_amount_minor, surcharge_percentage_bps, NOW(), NOW()
		FROM service_short_notice_rules
		WHERE service_id = $1
	`, serviceID, newServiceID); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("duplicate short-notice rules: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("commit duplicate managed service: %w", err)
	}

	return r.GetManagedServiceDetails(ctx, clientID, newServiceID)
}

func (r *Repository) CreateManagedService(ctx context.Context, clientID uuid.UUID, input CreateManagedServiceInput) (ManagedServiceItem, error) {
	return r.saveManagedService(ctx, clientID, uuid.New(), input, true)
}

type managedServiceScanner interface {
	Scan(dest ...any) error
}

func scanManagedService(row managedServiceScanner) (ManagedServiceItem, error) {
	var item ManagedServiceItem
	var id uuid.UUID
	var maxTravelDistance *int
	if err := row.Scan(
		&id, &item.Name, &item.Description, &item.CurrencyCode, &item.DurationMinutes,
		&item.Status, &item.IsHidden, &item.ImageURL, &item.SectionID, &item.SectionName,
		&item.Badge, &item.Pricing.PriceAmountMinor, &item.Pricing.ComparePriceAmountMinor,
		&item.Pricing.DepositRequired, &item.Pricing.DepositType,
		&item.Pricing.DepositAmountMinor, &item.Pricing.DepositPercentageBPS,
		&item.Fulfillment.Mode, &item.Fulfillment.ProviderLocationID,
		&item.Fulfillment.ProviderLocationLabel, &item.Fulfillment.TravelFeeMinor,
		&maxTravelDistance, &item.Availability.Mode, &item.Availability.MinimumNoticeMinutes,
		&item.Availability.MaxBookingsPerDay, &item.Availability.PrepTimeMinutes,
		&item.Availability.BufferTimeMinutes, &item.VirtualDelivery.Label,
		&item.VirtualDelivery.JoinURL, &item.VirtualDelivery.Instructions,
		&item.CancellationPolicy, &item.LatenessPolicy,
		&item.AgreementTemplateFamilyID, &item.AgreementTemplateTitle,
		&item.AgreementConfirmationMethod, &item.AgreementTiming,
		&item.StandaloneSignatureRequired, &item.Instructions,
	); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("scan managed service: %w", err)
	}
	item.ID = id.String()
	item.DurationLabel = formatDurationLabel(item.DurationMinutes)
	item.Fulfillment.MaxTravelDistanceMeters = maxTravelDistance
	item.Availability.CustomWindows = []ServiceAvailabilityWindow{}
	item.ShortNoticeRules = []ServiceShortNoticeRule{}
	return item, nil
}

func (r *Repository) loadManagedServiceChildren(ctx context.Context, serviceID uuid.UUID, item *ManagedServiceItem) error {
	windowRows, err := r.db.Query(ctx, `
		SELECT id, day_of_week, TO_CHAR(start_time, 'HH24:MI'),
			TO_CHAR(end_time, 'HH24:MI'), slot_interval_minutes
		FROM service_availability_windows
		WHERE service_id = $1
		ORDER BY day_of_week, start_time
	`, serviceID)
	if err != nil {
		return fmt.Errorf("load service availability windows: %w", err)
	}
	defer windowRows.Close()
	for windowRows.Next() {
		var window ServiceAvailabilityWindow
		var id uuid.UUID
		if err := windowRows.Scan(&id, &window.DayOfWeek, &window.StartTime, &window.EndTime, &window.SlotIntervalMinutes); err != nil {
			return fmt.Errorf("scan service availability window: %w", err)
		}
		window.ID = id.String()
		item.Availability.CustomWindows = append(item.Availability.CustomWindows, window)
	}
	if err := windowRows.Err(); err != nil {
		return fmt.Errorf("iterate service availability windows: %w", err)
	}

	ruleRows, err := r.db.Query(ctx, `
		SELECT id, threshold_minutes, surcharge_type,
			surcharge_amount_minor, surcharge_percentage_bps
		FROM service_short_notice_rules
		WHERE service_id = $1
		ORDER BY threshold_minutes
	`, serviceID)
	if err != nil {
		return fmt.Errorf("load service short-notice rules: %w", err)
	}
	defer ruleRows.Close()
	for ruleRows.Next() {
		var rule ServiceShortNoticeRule
		var id uuid.UUID
		if err := ruleRows.Scan(&id, &rule.ThresholdMinutes, &rule.SurchargeType, &rule.SurchargeAmountMinor, &rule.SurchargePercentageBPS); err != nil {
			return fmt.Errorf("scan service short-notice rule: %w", err)
		}
		rule.ID = id.String()
		item.ShortNoticeRules = append(item.ShortNoticeRules, rule)
	}
	if err := ruleRows.Err(); err != nil {
		return fmt.Errorf("iterate service short-notice rules: %w", err)
	}
	return nil
}

func (r *Repository) saveManagedService(ctx context.Context, clientID, serviceID uuid.UUID, input CreateManagedServiceInput, create bool) (ManagedServiceItem, error) {
	normalized, err := validateManagedServiceInput(input)
	if err != nil {
		return ManagedServiceItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ManagedServiceItem{}, fmt.Errorf("begin save managed service: %w", err)
	}
	defer tx.Rollback(ctx)

	currencyCode, err := loadConfiguredCurrencyCodeTx(ctx, tx, clientID)
	if err != nil {
		return ManagedServiceItem{}, err
	}
	sectionID, sectionName, err := resolveServiceSection(ctx, tx, clientID, input.SectionID)
	if err != nil {
		return ManagedServiceItem{}, err
	}
	agreementTemplateFamilyID, err := resolveAgreementTemplateFamilyID(ctx, tx, clientID, input.AgreementTemplateFamilyID)
	if err != nil {
		return ManagedServiceItem{}, err
	}
	providerLocationID, err := resolveServiceProviderLocation(ctx, tx, clientID, normalized)
	if err != nil {
		return ManagedServiceItem{}, err
	}

	name := strings.TrimSpace(input.ServiceName)
	slug := slugify(name)
	status := normalizeManagedServiceStatus(input.PublishStatus)
	if create {
		if err := ensureUniqueServiceSlug(ctx, tx, clientID, &slug); err != nil {
			return ManagedServiceItem{}, err
		}
		var sortOrder int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order), 0) + 1 FROM services WHERE client_id = $1`, clientID).Scan(&sortOrder); err != nil {
			return ManagedServiceItem{}, fmt.Errorf("get next service sort order: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO services (
				id, client_id, section_id, title, slug, description, category, icon_name,
				image_url, duration_minutes, price_amount_minor, is_active, sort_order,
				status, is_hidden, compare_price_amount_minor, deposit_required,
				deposit_type, deposit_amount_minor, deposit_percentage_bps,
				prep_time_minutes, buffer_time_minutes, availability_mode,
				minimum_notice_minutes, max_bookings_per_day, fulfillment_mode,
				provider_location_id, travel_fee_minor, max_travel_distance_meters,
				virtual_delivery_label, virtual_join_url, virtual_instructions,
				cancellation_policy, lateness_policy, agreement_template_family_id,
				agreement_timing, standalone_signature_required,
				prep_aftercare_instructions, badge, currency_code, created_at, updated_at
			)
			VALUES (
				$1,$2,$3,$4,$5,$6,$7,'inventory_2',$8,$9,$10,$11,$12,$13,FALSE,
				$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,
					$30,$31,$32,$33,$34,$35,$36,$37,$38,NOW(),NOW()
			)
		`, serviceID, clientID, sectionID, name, slug, strings.TrimSpace(input.Description),
			sectionName, nullIfBlank(input.ImageURL), input.DurationMinutes,
			input.Pricing.PriceAmountMinor, status == "published", sortOrder, status,
			input.Pricing.ComparePriceAmountMinor, input.Pricing.DepositRequired,
			normalized.depositType, input.Pricing.DepositAmountMinor,
			input.Pricing.DepositPercentageBPS, input.Availability.PrepTimeMinutes,
			input.Availability.BufferTimeMinutes, normalized.availabilityMode,
			input.Availability.MinimumNoticeMinutes, input.Availability.MaxBookingsPerDay,
			normalized.fulfillmentMode, providerLocationID, input.Fulfillment.TravelFeeMinor,
			input.Fulfillment.MaxTravelDistanceMeters, normalized.virtualLabel,
			nullIfBlank(normalized.virtualJoinURL), nullIfBlank(normalized.virtualInstructions),
			strings.TrimSpace(input.CancellationPolicy), strings.TrimSpace(input.LatenessPolicy),
			agreementTemplateFamilyID, agreementTimingValue(agreementTemplateFamilyID, input.AgreementTiming),
			input.StandaloneSignatureRequired,
			strings.TrimSpace(input.Instructions), strings.TrimSpace(input.Badge), currencyCode)
	} else {
		if err := ensureUniqueServiceSlugExcluding(ctx, tx, clientID, serviceID, &slug); err != nil {
			return ManagedServiceItem{}, err
		}
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `
			UPDATE services
			SET section_id=$3, title=$4, slug=$5, description=$6, category=$7,
				image_url=$8, duration_minutes=$9, price_amount_minor=$10,
				is_active=$11, status=$12, compare_price_amount_minor=$13,
				deposit_required=$14, deposit_type=$15, deposit_amount_minor=$16,
				deposit_percentage_bps=$17, prep_time_minutes=$18,
				buffer_time_minutes=$19, availability_mode=$20,
				minimum_notice_minutes=$21, max_bookings_per_day=$22,
				fulfillment_mode=$23, provider_location_id=$24, travel_fee_minor=$25,
				max_travel_distance_meters=$26, virtual_delivery_label=$27,
				virtual_join_url=$28, virtual_instructions=$29,
				cancellation_policy=$30, lateness_policy=$31,
				agreement_template_family_id=$32, agreement_timing=$33,
				standalone_signature_required=$34, prep_aftercare_instructions=$35,
				badge=$36, updated_at=NOW()
			WHERE client_id=$1 AND id=$2
		`, clientID, serviceID, sectionID, name, slug, strings.TrimSpace(input.Description),
			sectionName, nullIfBlank(input.ImageURL), input.DurationMinutes,
			input.Pricing.PriceAmountMinor, status == "published", status,
			input.Pricing.ComparePriceAmountMinor, input.Pricing.DepositRequired,
			normalized.depositType, input.Pricing.DepositAmountMinor,
			input.Pricing.DepositPercentageBPS, input.Availability.PrepTimeMinutes,
			input.Availability.BufferTimeMinutes, normalized.availabilityMode,
			input.Availability.MinimumNoticeMinutes, input.Availability.MaxBookingsPerDay,
			normalized.fulfillmentMode, providerLocationID, input.Fulfillment.TravelFeeMinor,
			input.Fulfillment.MaxTravelDistanceMeters, normalized.virtualLabel,
			nullIfBlank(normalized.virtualJoinURL), nullIfBlank(normalized.virtualInstructions),
			strings.TrimSpace(input.CancellationPolicy), strings.TrimSpace(input.LatenessPolicy),
			agreementTemplateFamilyID, agreementTimingValue(agreementTemplateFamilyID, input.AgreementTiming),
			input.StandaloneSignatureRequired,
			strings.TrimSpace(input.Instructions), strings.TrimSpace(input.Badge))
		if err == nil && tag.RowsAffected() == 0 {
			return ManagedServiceItem{}, ErrNotFound
		}
	}
	if err != nil {
		return ManagedServiceItem{}, fmt.Errorf("save managed service: %w", err)
	}

	if err := replaceServiceChildren(ctx, tx, serviceID, input.Availability.CustomWindows, input.ShortNoticeRules); err != nil {
		return ManagedServiceItem{}, err
	}
	if err := consumeServiceWizardDraft(ctx, tx, clientID, serviceID, input.WizardDraftID, create); err != nil {
		return ManagedServiceItem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ManagedServiceItem{}, fmt.Errorf("commit managed service: %w", err)
	}
	return r.GetManagedServiceDetails(ctx, clientID, serviceID)
}

func consumeServiceWizardDraft(ctx context.Context, tx pgx.Tx, clientID, serviceID uuid.UUID, rawDraftID string, create bool) error {
	if strings.TrimSpace(rawDraftID) == "" {
		return nil
	}
	draftID, err := uuid.Parse(strings.TrimSpace(rawDraftID))
	if err != nil {
		return fmt.Errorf("wizard_draft_id is invalid")
	}
	query := `DELETE FROM service_wizard_drafts WHERE client_id = $1 AND id = $2 AND service_id = $3`
	args := []any{clientID, draftID, serviceID}
	if create {
		query = `DELETE FROM service_wizard_drafts WHERE client_id = $1 AND id = $2 AND service_id IS NULL`
		args = []any{clientID, draftID}
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("consume service wizard draft: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type normalizedManagedServiceInput struct {
	fulfillmentMode     bookingdomain.FulfillmentMode
	availabilityMode    bookingdomain.AvailabilityMode
	depositType         string
	providerLocationID  string
	virtualLabel        string
	virtualJoinURL      string
	virtualInstructions string
}

func validateManagedServiceInput(input CreateManagedServiceInput) (normalizedManagedServiceInput, error) {
	if strings.TrimSpace(input.ServiceName) == "" {
		return normalizedManagedServiceInput{}, fmt.Errorf("service_name is required")
	}
	if input.DurationMinutes <= 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("duration_minutes must be positive")
	}
	hasAgreementTemplate := strings.TrimSpace(input.AgreementTemplateFamilyID) != ""
	agreementTiming := strings.TrimSpace(input.AgreementTiming)
	if hasAgreementTemplate {
		if agreementTiming != "before_payment" && agreementTiming != "after_payment" {
			return normalizedManagedServiceInput{}, fmt.Errorf("agreement_timing must be before_payment or after_payment")
		}
		if input.StandaloneSignatureRequired {
			return normalizedManagedServiceInput{}, fmt.Errorf("standalone signature cannot be required when an agreement template is assigned")
		}
	} else if agreementTiming != "" {
		return normalizedManagedServiceInput{}, fmt.Errorf("agreement_timing requires an agreement template family")
	}
	if input.Pricing.PriceAmountMinor < 0 || input.Pricing.ComparePriceAmountMinor < 0 ||
		input.Pricing.DepositAmountMinor < 0 || input.Fulfillment.TravelFeeMinor < 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("service money values cannot be negative")
	}
	if input.Pricing.ComparePriceAmountMinor > 0 && input.Pricing.ComparePriceAmountMinor < input.Pricing.PriceAmountMinor {
		return normalizedManagedServiceInput{}, fmt.Errorf("compare price cannot be lower than price")
	}

	depositType := strings.TrimSpace(input.Pricing.DepositType)
	if depositType == "" {
		depositType = "fixed"
	}
	if depositType != "fixed" && depositType != "percentage" {
		return normalizedManagedServiceInput{}, fmt.Errorf("invalid deposit_type")
	}
	if !input.Pricing.DepositRequired {
		if input.Pricing.DepositAmountMinor != 0 || input.Pricing.DepositPercentageBPS != 0 {
			return normalizedManagedServiceInput{}, fmt.Errorf("deposit values must be zero when no deposit is required")
		}
	} else if depositType == "fixed" {
		if input.Pricing.DepositAmountMinor <= 0 || input.Pricing.DepositPercentageBPS != 0 {
			return normalizedManagedServiceInput{}, fmt.Errorf("fixed deposit requires a positive amount and zero percentage")
		}
	} else if input.Pricing.DepositAmountMinor != 0 || input.Pricing.DepositPercentageBPS < 1 || input.Pricing.DepositPercentageBPS > 10000 {
		return normalizedManagedServiceInput{}, fmt.Errorf("percentage deposit requires 1 to 10000 basis points and zero fixed amount")
	}

	fulfillmentMode, err := bookingdomain.ParseFulfillmentMode(input.Fulfillment.Mode)
	if err != nil {
		return normalizedManagedServiceInput{}, err
	}
	if fulfillmentMode != bookingdomain.FulfillmentCustomerLocation {
		if input.Fulfillment.TravelFeeMinor != 0 || input.Fulfillment.MaxTravelDistanceMeters != nil {
			return normalizedManagedServiceInput{}, fmt.Errorf("travel settings are valid only for customer-location services")
		}
	}
	if input.Fulfillment.MaxTravelDistanceMeters != nil && *input.Fulfillment.MaxTravelDistanceMeters <= 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("max_travel_distance_meters must be positive")
	}
	if fulfillmentMode.IsPhysical() && normalizeManagedServiceStatus(input.PublishStatus) == "published" && strings.TrimSpace(input.Fulfillment.ProviderLocationID) == "" {
		return normalizedManagedServiceInput{}, fmt.Errorf("published physical services require provider_location_id")
	}
	if fulfillmentMode == bookingdomain.FulfillmentVirtual && strings.TrimSpace(input.Fulfillment.ProviderLocationID) != "" {
		return normalizedManagedServiceInput{}, fmt.Errorf("virtual services cannot reference a physical location")
	}

	availabilityMode, err := bookingdomain.ParseAvailabilityMode(input.Availability.Mode)
	if err != nil {
		return normalizedManagedServiceInput{}, err
	}
	if input.Availability.MinimumNoticeMinutes < 0 || input.Availability.MaxBookingsPerDay < 0 ||
		input.Availability.PrepTimeMinutes < 0 || input.Availability.BufferTimeMinutes < 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("availability values cannot be negative")
	}
	if availabilityMode == bookingdomain.AvailabilityCustom && len(input.Availability.CustomWindows) == 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("custom availability requires at least one window")
	}
	if availabilityMode == bookingdomain.AvailabilityInheritBusinessHours && len(input.Availability.CustomWindows) > 0 {
		return normalizedManagedServiceInput{}, fmt.Errorf("inherited availability cannot include custom windows")
	}
	if err := validateServiceAvailabilityWindows(input.Availability.CustomWindows); err != nil {
		return normalizedManagedServiceInput{}, err
	}
	if len(input.ShortNoticeRules) > 3 {
		return normalizedManagedServiceInput{}, fmt.Errorf("a service can have at most three short-notice rules")
	}
	thresholds := make(map[int]struct{}, len(input.ShortNoticeRules))
	for _, rule := range input.ShortNoticeRules {
		if _, exists := thresholds[rule.ThresholdMinutes]; exists {
			return normalizedManagedServiceInput{}, fmt.Errorf("short-notice thresholds must be unique")
		}
		thresholds[rule.ThresholdMinutes] = struct{}{}
		domainRule := bookingdomain.ShortNoticeRule{
			ThresholdMinutes:      rule.ThresholdMinutes,
			Type:                  bookingdomain.SurchargeType(rule.SurchargeType),
			AmountMinor:           int64(rule.SurchargeAmountMinor),
			PercentageBasisPoints: rule.SurchargePercentageBPS,
		}
		if err := domainRule.Validate(input.Availability.MinimumNoticeMinutes); err != nil {
			return normalizedManagedServiceInput{}, err
		}
	}

	virtualLabel := ""
	virtualJoinURL := ""
	virtualInstructions := ""
	if fulfillmentMode == bookingdomain.FulfillmentVirtual {
		virtualLabel = defaultString(input.VirtualDelivery.Label, "Provider will contact you")
		virtualJoinURL = strings.TrimSpace(input.VirtualDelivery.JoinURL)
		virtualInstructions = strings.TrimSpace(input.VirtualDelivery.Instructions)
	} else if strings.TrimSpace(input.VirtualDelivery.Label) != "" || strings.TrimSpace(input.VirtualDelivery.JoinURL) != "" || strings.TrimSpace(input.VirtualDelivery.Instructions) != "" {
		return normalizedManagedServiceInput{}, fmt.Errorf("virtual delivery settings are valid only for virtual services")
	}

	return normalizedManagedServiceInput{
		fulfillmentMode:     fulfillmentMode,
		availabilityMode:    availabilityMode,
		depositType:         depositType,
		providerLocationID:  strings.TrimSpace(input.Fulfillment.ProviderLocationID),
		virtualLabel:        virtualLabel,
		virtualJoinURL:      virtualJoinURL,
		virtualInstructions: virtualInstructions,
	}, nil
}

func validateServiceAvailabilityWindows(windows []ServiceAvailabilityWindow) error {
	type parsedWindow struct {
		day   int
		start int
		end   int
	}
	parsed := make([]parsedWindow, 0, len(windows))
	for _, window := range windows {
		if window.DayOfWeek < 0 || window.DayOfWeek > 6 {
			return fmt.Errorf("availability day_of_week must be between 0 and 6")
		}
		if window.SlotIntervalMinutes <= 0 {
			return fmt.Errorf("slot_interval_minutes must be positive")
		}
		start, err := time.Parse("15:04", strings.TrimSpace(window.StartTime))
		if err != nil {
			return fmt.Errorf("invalid availability start_time")
		}
		end, err := time.Parse("15:04", strings.TrimSpace(window.EndTime))
		if err != nil {
			return fmt.Errorf("invalid availability end_time")
		}
		startMinutes := start.Hour()*60 + start.Minute()
		endMinutes := end.Hour()*60 + end.Minute()
		if endMinutes <= startMinutes {
			return fmt.Errorf("availability end_time must be after start_time")
		}
		for _, existing := range parsed {
			if existing.day == window.DayOfWeek && startMinutes < existing.end && endMinutes > existing.start {
				return fmt.Errorf("availability windows cannot overlap")
			}
		}
		parsed = append(parsed, parsedWindow{day: window.DayOfWeek, start: startMinutes, end: endMinutes})
	}
	return nil
}

func resolveServiceSection(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, value string) (*uuid.UUID, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "__uncategorized" {
		return nil, "", nil
	}
	sectionID, err := uuid.Parse(value)
	if err != nil {
		return nil, "", fmt.Errorf("invalid section_id")
	}
	var name string
	if err := tx.QueryRow(ctx, `SELECT name FROM service_sections WHERE client_id = $1 AND id = $2`, clientID, sectionID).Scan(&name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("load service section: %w", err)
	}
	return &sectionID, name, nil
}

func resolveServiceProviderLocation(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, input normalizedManagedServiceInput) (*uuid.UUID, error) {
	if !input.fulfillmentMode.IsPhysical() || input.providerLocationID == "" {
		return nil, nil
	}
	locationID, err := uuid.Parse(input.providerLocationID)
	if err != nil {
		return nil, fmt.Errorf("invalid provider_location_id")
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM business_locations
			WHERE client_id = $1 AND id = $2 AND is_active
		)
	`, clientID, locationID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("validate provider location: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("provider_location_id must reference an active business location")
	}
	return &locationID, nil
}

func replaceServiceChildren(ctx context.Context, tx pgx.Tx, serviceID uuid.UUID, windows []ServiceAvailabilityWindow, rules []ServiceShortNoticeRule) error {
	if _, err := tx.Exec(ctx, `DELETE FROM service_availability_windows WHERE service_id = $1`, serviceID); err != nil {
		return fmt.Errorf("replace service availability windows: %w", err)
	}
	for _, window := range windows {
		if _, err := tx.Exec(ctx, `
			INSERT INTO service_availability_windows (
				id, service_id, day_of_week, start_time, end_time,
				slot_interval_minutes, created_at, updated_at
			) VALUES ($1,$2,$3,$4::time,$5::time,$6,NOW(),NOW())
		`, uuid.New(), serviceID, window.DayOfWeek, window.StartTime, window.EndTime, window.SlotIntervalMinutes); err != nil {
			return fmt.Errorf("insert service availability window: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `DELETE FROM service_short_notice_rules WHERE service_id = $1`, serviceID); err != nil {
		return fmt.Errorf("replace service short-notice rules: %w", err)
	}
	for _, rule := range rules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO service_short_notice_rules (
				id, service_id, threshold_minutes, surcharge_type,
				surcharge_amount_minor, surcharge_percentage_bps, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,NOW(),NOW())
		`, uuid.New(), serviceID, rule.ThresholdMinutes, rule.SurchargeType,
			rule.SurchargeAmountMinor, rule.SurchargePercentageBPS); err != nil {
			return fmt.Errorf("insert service short-notice rule: %w", err)
		}
	}
	return nil
}

func (r *Repository) DeleteManagedService(ctx context.Context, clientID, serviceID uuid.UUID) error {
	commandTag, err := r.db.Exec(ctx, `DELETE FROM services WHERE client_id = $1 AND id = $2`, clientID, serviceID)
	if err != nil {
		return fmt.Errorf("delete managed service: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) UpdateManagedServiceVisibility(ctx context.Context, clientID, serviceID uuid.UUID, isHidden bool) (ManagedServiceItem, error) {
	commandTag, err := r.db.Exec(
		ctx,
		`UPDATE services SET is_hidden = $3, updated_at = NOW() WHERE client_id = $1 AND id = $2`,
		clientID,
		serviceID,
		isHidden,
	)
	if err != nil {
		return ManagedServiceItem{}, fmt.Errorf("update managed service visibility: %w", err)
	}
	if commandTag.RowsAffected() == 0 {
		return ManagedServiceItem{}, ErrNotFound
	}

	return r.GetManagedServiceDetails(ctx, clientID, serviceID)
}

func ensureUniqueSectionSlug(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, slug *string) error {
	base := *slug
	if base == "" {
		base = "section"
	}
	current := base
	for suffix := 1; ; suffix++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_sections WHERE client_id = $1 AND slug = $2)`, clientID, current).Scan(&exists); err != nil {
			return fmt.Errorf("check section slug: %w", err)
		}
		if !exists {
			*slug = current
			return nil
		}
		current = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func ensureUniqueServiceSlug(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, slug *string) error {
	base := *slug
	if base == "" {
		base = "service"
	}
	current := base
	for suffix := 1; ; suffix++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE client_id = $1 AND slug = $2)`, clientID, current).Scan(&exists); err != nil {
			return fmt.Errorf("check service slug: %w", err)
		}
		if !exists {
			*slug = current
			return nil
		}
		current = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func ensureUniqueServiceSlugExcluding(ctx context.Context, tx pgx.Tx, clientID, serviceID uuid.UUID, slug *string) error {
	base := *slug
	if base == "" {
		base = "service"
	}
	current := base
	for suffix := 1; ; suffix++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE client_id = $1 AND slug = $2 AND id <> $3)`, clientID, current, serviceID).Scan(&exists); err != nil {
			return fmt.Errorf("check service slug: %w", err)
		}
		if !exists {
			*slug = current
			return nil
		}
		current = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func formatDurationLabel(minutes int) string {
	if minutes <= 0 {
		return "0 min"
	}
	return fmt.Sprintf("%d min", minutes)
}

func formatUpdatedLabel(updatedAt time.Time) string {
	elapsed := time.Since(updatedAt)
	switch {
	case elapsed < time.Minute:
		return "Updated just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("Updated %d minutes ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("Updated %d hours ago", int(elapsed.Hours()))
	default:
		days := int(elapsed.Hours() / 24)
		if days == 1 {
			return "Updated yesterday"
		}
		return fmt.Sprintf("Updated %d days ago", days)
	}
}

func parseMoneyToMinor(value string, exponent uint8) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("money value is required")
	}
	return money.ParseDecimal(trimmed, exponent)
}

func parseOptionalMoneyToMinor(value string, exponent uint8) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	return parseMoneyToMinor(trimmed, exponent)
}

func parseDeposit(depositType string, depositValue string, exponent uint8) (int64, int, error) {
	if strings.TrimSpace(depositValue) == "" {
		return 0, 0, nil
	}
	if depositType == "percentage" {
		percentage, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(depositValue, "%")))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid deposit percentage")
		}
		return 0, percentage, nil
	}
	amount, err := parseMoneyToMinor(depositValue, exponent)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid deposit amount")
	}
	return amount, 0, nil
}

func parseDurationMinutes(value string) int {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return 0
	}
	re := regexp.MustCompile(`\d+`)
	match := re.FindString(trimmed)
	if match == "" {
		return 0
	}
	number, _ := strconv.Atoi(match)
	if strings.Contains(trimmed, "hour") {
		return number * 60
	}
	return number
}

func parseIntDefault(value string, fallback int) int {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return number
}

func parseOptionalClockTime(value string) (*time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse("3:04 PM", trimmed)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func defaultAvailableDays(days []string) []string {
	if len(days) == 0 {
		return []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	}
	return days
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func valueOrEmptyUUID(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func resolveAgreementTemplateFamilyID(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, rawFamilyID string) (*uuid.UUID, error) {
	trimmed := strings.TrimSpace(rawFamilyID)
	if trimmed == "" {
		return nil, nil
	}

	familyID, err := uuid.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid agreement template family id")
	}

	var exists bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS(
			SELECT 1
			FROM agreement_template_families f
			JOIN agreement_template_versions v
			  ON v.id = f.current_published_version_id
			 AND v.family_id = f.id
			 AND v.state = 'published'
			WHERE f.id = $2
			  AND f.client_id = $1
			  AND f.owner_type = 'client'
			  AND f.status = 'published'
		)`,
		clientID,
		familyID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("validate agreement template family: %w", err)
	}
	if !exists {
		return nil, ErrNotFound
	}

	return &familyID, nil
}

func agreementTimingValue(familyID *uuid.UUID, value string) any {
	if familyID == nil {
		return nil
	}
	return strings.TrimSpace(value)
}

func loadConfiguredCurrencyCodeTx(ctx context.Context, tx pgx.Tx, clientID uuid.UUID) (string, error) {
	var currencyCode string
	if err := tx.QueryRow(
		ctx,
		`
			SELECT currency_code
			FROM client_profiles
			WHERE client_id = $1
			  AND market_configured_at IS NOT NULL
		`,
		clientID,
	).Scan(&currencyCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrMarketNotConfigured
		}
		return "", fmt.Errorf("load configured business currency: %w", err)
	}
	return currencyCode, nil
}

type currencyConfigQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadConfiguredCurrencySpec(ctx context.Context, querier currencyConfigQuerier, clientID uuid.UUID) (string, uint8, error) {
	var countryCode string
	var currencyCode string
	if err := querier.QueryRow(
		ctx,
		`
			SELECT country_code, currency_code
			FROM client_profiles
			WHERE client_id = $1
			  AND market_configured_at IS NOT NULL
		`,
		clientID,
	).Scan(&countryCode, &currencyCode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", 0, ErrMarketNotConfigured
		}
		return "", 0, fmt.Errorf("load configured business currency: %w", err)
	}

	market, ok := markets.DefaultCatalog().Lookup(countryCode)
	if !ok {
		return "", 0, fmt.Errorf("configured business country is unsupported")
	}
	for _, currency := range market.Currencies {
		if currency.Code == currencyCode {
			return currencyCode, currency.MinorUnitExponent, nil
		}
	}
	return "", 0, fmt.Errorf("configured business currency is unsupported")
}

func normalizeManagedServiceStatus(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "publish_now", "published", "live":
		return "published"
	case "paused", "pause":
		return "paused"
	default:
		return "draft"
	}
}
