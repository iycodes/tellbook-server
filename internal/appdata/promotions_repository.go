package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/bookingdomain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type promotionExec interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type promotionScanner interface {
	Scan(dest ...any) error
}

type promotionCandidate struct {
	ID                        uuid.UUID
	Name                      string
	PromotionType             string
	Code                      string
	DiscountType              string
	DiscountPercentageBPS     int64
	DiscountValueMinor        int64
	ScopeType                 string
	IsActive                  bool
	StartsAt                  time.Time
	EndsAt                    *time.Time
	MaxRedemptions            int
	MaxRedemptionsPerCustomer int
	MinimumSpendMinor         int64
	CurrencyCode              string
	FirstTimeCustomersOnly    bool
	AppliesToDeposit          bool
	StackWithAutomatic        bool
	SectionTargetIDs          []uuid.UUID
	ServiceTargetIDs          []uuid.UUID
}

type appliedDiscountSnapshot struct {
	PromotionID           uuid.UUID
	DiscountName          string
	DiscountSource        string
	DiscountCode          string
	DiscountType          string
	DiscountPercentageBPS int64
	DiscountValueMinor    int64
	OriginalAmountMinor   int64
	DiscountAmountMinor   int64
	FinalAmountMinor      int64
	DepositAmountMinor    int64
}

type promotionResolution struct {
	Snapshot             appliedDiscountSnapshot
	AutomaticPromotion   *promotionCandidate
	AutomaticAmountMinor int64
	CodePromotion        *promotionCandidate
	CodeAmountMinor      int64
	CodeError            string
}

func (r *Repository) ListPromotions(ctx context.Context, clientID uuid.UUID, promotionType string) ([]PromotionListItem, error) {
	const query = `
		SELECT
			p.id,
			p.name,
			p.promotion_type,
			p.code,
			p.discount_type,
			p.discount_percentage_bps,
			p.discount_value_minor,
			p.scope_type,
			p.is_active,
			p.starts_at,
			p.ends_at,
			p.max_redemptions,
			p.max_redemptions_per_customer,
			p.minimum_spend_minor,
			p.currency_code,
			p.first_time_customers_only,
			p.applies_to_deposit,
			p.stack_with_automatic_discounts,
			COALESCE(redemption_stats.count, 0),
			COALESCE(service_targets.items, '[]'::jsonb),
			COALESCE(section_targets.items, '[]'::jsonb),
			p.created_at,
			p.updated_at
		FROM promotions p
		LEFT JOIN LATERAL (
			SELECT COUNT(*)::int AS count
			FROM promotion_redemptions pr
			WHERE pr.promotion_id = p.id
		) redemption_stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(
				jsonb_build_object('id', s.id::text, 'name', s.title)
				ORDER BY s.title ASC
			) AS items
			FROM promotion_services ps
			INNER JOIN services s ON s.id = ps.service_id
			WHERE ps.promotion_id = p.id
		) service_targets ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(
				jsonb_build_object('id', ss.id::text, 'name', ss.name)
				ORDER BY ss.name ASC
			) AS items
			FROM promotion_sections ps
			INNER JOIN service_sections ss ON ss.id = ps.section_id
			WHERE ps.promotion_id = p.id
		) section_targets ON TRUE
		WHERE p.client_id = $1
		  AND ($2 = '' OR p.promotion_type = $2)
		ORDER BY p.updated_at DESC, p.created_at DESC
	`

	rows, err := r.db.Query(ctx, query, clientID, normalizePromotionTypeFilter(promotionType))
	if err != nil {
		return nil, fmt.Errorf("list promotions: %w", err)
	}
	defer rows.Close()

	items := make([]PromotionListItem, 0)
	for rows.Next() {
		item, err := scanPromotionListItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate promotions: %w", err)
	}

	return items, nil
}

func (r *Repository) GetPromotionDetails(ctx context.Context, clientID, promotionID uuid.UUID) (PromotionListItem, error) {
	const query = `
		SELECT
			p.id,
			p.name,
			p.promotion_type,
			p.code,
			p.discount_type,
			p.discount_percentage_bps,
			p.discount_value_minor,
			p.scope_type,
			p.is_active,
			p.starts_at,
			p.ends_at,
			p.max_redemptions,
			p.max_redemptions_per_customer,
			p.minimum_spend_minor,
			p.currency_code,
			p.first_time_customers_only,
			p.applies_to_deposit,
			p.stack_with_automatic_discounts,
			COALESCE(redemption_stats.count, 0),
			COALESCE(service_targets.items, '[]'::jsonb),
			COALESCE(section_targets.items, '[]'::jsonb),
			p.created_at,
			p.updated_at
		FROM promotions p
		LEFT JOIN LATERAL (
			SELECT COUNT(*)::int AS count
			FROM promotion_redemptions pr
			WHERE pr.promotion_id = p.id
		) redemption_stats ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(
				jsonb_build_object('id', s.id::text, 'name', s.title)
				ORDER BY s.title ASC
			) AS items
			FROM promotion_services ps
			INNER JOIN services s ON s.id = ps.service_id
			WHERE ps.promotion_id = p.id
		) service_targets ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(
				jsonb_build_object('id', ss.id::text, 'name', ss.name)
				ORDER BY ss.name ASC
			) AS items
			FROM promotion_sections ps
			INNER JOIN service_sections ss ON ss.id = ps.section_id
			WHERE ps.promotion_id = p.id
		) section_targets ON TRUE
		WHERE p.client_id = $1
		  AND p.id = $2
	`

	row := r.db.QueryRow(ctx, query, clientID, promotionID)
	item, err := scanPromotionListItem(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PromotionListItem{}, ErrNotFound
		}
		return PromotionListItem{}, err
	}
	return item, nil
}

func (r *Repository) CreatePromotion(ctx context.Context, clientID uuid.UUID, input CreatePromotionInput) (PromotionListItem, error) {
	normalized, serviceIDs, sectionIDs, err := normalizePromotionInput(input)
	if err != nil {
		return PromotionListItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PromotionListItem{}, fmt.Errorf("begin create promotion: %w", err)
	}
	defer tx.Rollback(ctx)

	currencyCode, err := loadConfiguredCurrencyCodeTx(ctx, tx, clientID)
	if err != nil {
		return PromotionListItem{}, err
	}

	promotionID := uuid.New()
	const query = `
		INSERT INTO promotions (
			id, client_id, name, promotion_type, code, discount_type,
			discount_percentage_bps, discount_value_minor, scope_type,
			starts_at, ends_at, is_active, max_redemptions, max_redemptions_per_customer,
			minimum_spend_minor, currency_code, first_time_customers_only, applies_to_deposit,
			stack_with_automatic_discounts, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,NOW(),NOW())
	`
	if _, err := tx.Exec(
		ctx,
		query,
		promotionID,
		clientID,
		normalized.Name,
		normalized.PromotionType,
		normalized.Code,
		normalized.DiscountType,
		normalized.DiscountPercentageBPS,
		normalized.DiscountValueMinor,
		normalized.ScopeType,
		normalized.StartsAt,
		normalized.EndsAt,
		normalized.IsActive,
		normalized.MaxRedemptions,
		normalized.MaxRedemptionsPerCustomer,
		normalized.MinimumSpendMinor,
		currencyCode,
		normalized.FirstTimeCustomersOnly,
		normalized.AppliesToDeposit,
		normalized.StackWithAutomatic,
	); err != nil {
		if isPromotionCodeConflict(err) {
			return PromotionListItem{}, fmt.Errorf("a discount code with that name already exists")
		}
		return PromotionListItem{}, fmt.Errorf("create promotion: %w", err)
	}

	if err := syncPromotionTargets(ctx, tx, clientID, promotionID, serviceIDs, sectionIDs); err != nil {
		return PromotionListItem{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return PromotionListItem{}, fmt.Errorf("commit create promotion: %w", err)
	}

	return r.GetPromotionDetails(ctx, clientID, promotionID)
}

func (r *Repository) UpdatePromotion(ctx context.Context, clientID, promotionID uuid.UUID, input CreatePromotionInput) (PromotionListItem, error) {
	normalized, serviceIDs, sectionIDs, err := normalizePromotionInput(input)
	if err != nil {
		return PromotionListItem{}, err
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return PromotionListItem{}, fmt.Errorf("begin update promotion: %w", err)
	}
	defer tx.Rollback(ctx)

	const query = `
		UPDATE promotions
		SET
			name = $3,
			promotion_type = $4,
			code = $5,
			discount_type = $6,
			discount_percentage_bps = $7,
			discount_value_minor = $8,
			scope_type = $9,
			starts_at = $10,
			ends_at = $11,
			is_active = $12,
			max_redemptions = $13,
			max_redemptions_per_customer = $14,
			minimum_spend_minor = $15,
			first_time_customers_only = $16,
			applies_to_deposit = $17,
			stack_with_automatic_discounts = $18,
			updated_at = NOW()
		WHERE client_id = $1
		  AND id = $2
	`
	tag, err := tx.Exec(
		ctx,
		query,
		clientID,
		promotionID,
		normalized.Name,
		normalized.PromotionType,
		normalized.Code,
		normalized.DiscountType,
		normalized.DiscountPercentageBPS,
		normalized.DiscountValueMinor,
		normalized.ScopeType,
		normalized.StartsAt,
		normalized.EndsAt,
		normalized.IsActive,
		normalized.MaxRedemptions,
		normalized.MaxRedemptionsPerCustomer,
		normalized.MinimumSpendMinor,
		normalized.FirstTimeCustomersOnly,
		normalized.AppliesToDeposit,
		normalized.StackWithAutomatic,
	)
	if err != nil {
		if isPromotionCodeConflict(err) {
			return PromotionListItem{}, fmt.Errorf("a discount code with that name already exists")
		}
		return PromotionListItem{}, fmt.Errorf("update promotion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return PromotionListItem{}, ErrNotFound
	}

	if err := syncPromotionTargets(ctx, tx, clientID, promotionID, serviceIDs, sectionIDs); err != nil {
		return PromotionListItem{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return PromotionListItem{}, fmt.Errorf("commit update promotion: %w", err)
	}

	return r.GetPromotionDetails(ctx, clientID, promotionID)
}

func (r *Repository) UpdatePromotionStatus(ctx context.Context, clientID, promotionID uuid.UUID, isActive bool) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE promotions
		SET is_active = $3, updated_at = NOW()
		WHERE client_id = $1 AND id = $2
	`, clientID, promotionID, isActive)
	if err != nil {
		return fmt.Errorf("update promotion status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) DeletePromotion(ctx context.Context, clientID, promotionID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM promotions WHERE client_id = $1 AND id = $2`, clientID, promotionID)
	if err != nil {
		return fmt.Errorf("delete promotion: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) ListPromotionRedemptions(ctx context.Context, clientID, promotionID uuid.UUID) ([]PromotionRedemptionItem, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, COALESCE(booking_id::text, ''), COALESCE(customer_id::text, ''), customer_email, code_used, discount_amount_minor, currency_code, created_at
		FROM promotion_redemptions
		WHERE client_id = $1 AND promotion_id = $2
		ORDER BY created_at DESC
	`, clientID, promotionID)
	if err != nil {
		return nil, fmt.Errorf("list promotion redemptions: %w", err)
	}
	defer rows.Close()

	items := make([]PromotionRedemptionItem, 0)
	for rows.Next() {
		var item PromotionRedemptionItem
		var id uuid.UUID
		if err := rows.Scan(&id, &item.BookingID, &item.CustomerID, &item.CustomerEmail, &item.CodeUsed, &item.DiscountAmountMinor, &item.CurrencyCode, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan promotion redemption: %w", err)
		}
		item.ID = id.String()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate promotion redemptions: %w", err)
	}
	return items, nil
}

func findPromotionDiscountAmount(candidate promotionCandidate, subtotal int64) int64 {
	if subtotal <= 0 {
		return 0
	}

	switch candidate.DiscountType {
	case "percentage":
		if candidate.DiscountPercentageBPS <= 0 {
			return 0
		}
		const basisPointScale int64 = 10000
		amount := (subtotal/basisPointScale)*candidate.DiscountPercentageBPS +
			((subtotal%basisPointScale)*candidate.DiscountPercentageBPS)/basisPointScale
		if amount > subtotal {
			return subtotal
		}
		return amount
	case "fixed_amount":
		if candidate.DiscountValueMinor <= 0 {
			return 0
		}
		if candidate.DiscountValueMinor > subtotal {
			return subtotal
		}
		return candidate.DiscountValueMinor
	case "set_price":
		if candidate.DiscountValueMinor >= subtotal {
			return 0
		}
		return subtotal - candidate.DiscountValueMinor
	default:
		return 0
	}
}

func (r *Repository) findBestAutomaticPromotion(ctx context.Context, service publicBookingServiceInfo, subtotal int64, customerEmail string) (*promotionCandidate, error) {
	candidates, err := r.listPromotionCandidates(ctx, service.ClientID, "automatic", "")
	if err != nil {
		return nil, err
	}

	var best *promotionCandidate
	bestAmount := int64(0)
	for _, candidate := range candidates {
		applies, err := r.promotionApplies(ctx, candidate, service, subtotal, customerEmail)
		if err != nil {
			return nil, err
		}
		if !applies {
			continue
		}
		amount := findPromotionDiscountAmount(candidate, subtotal)
		if amount <= 0 || amount <= bestAmount {
			continue
		}
		copyCandidate := candidate
		best = &copyCandidate
		bestAmount = amount
	}

	return best, nil
}

func (r *Repository) findCodePromotion(ctx context.Context, service publicBookingServiceInfo, code string, subtotal int64, customerEmail string) (*promotionCandidate, string, error) {
	candidates, err := r.listPromotionCandidates(ctx, service.ClientID, "code", code)
	if err != nil {
		return nil, "", err
	}
	if len(candidates) == 0 {
		return nil, "That code is invalid.", nil
	}

	candidate := candidates[0]
	applies, err := r.promotionApplies(ctx, candidate, service, subtotal, customerEmail)
	if err != nil {
		return nil, "", err
	}
	if !applies {
		return nil, "That code is not available for this service.", nil
	}

	if findPromotionDiscountAmount(candidate, subtotal) <= 0 {
		return nil, "That code does not reduce this price.", nil
	}

	return &candidate, "", nil
}

func (r *Repository) resolvePublicDiscount(
	ctx context.Context,
	service publicBookingServiceInfo,
	originalAmountMinor int64,
	customerEmail string,
	code string,
) (promotionResolution, error) {
	resolution := promotionResolution{
		Snapshot: appliedDiscountSnapshot{
			OriginalAmountMinor: originalAmountMinor,
			FinalAmountMinor:    originalAmountMinor,
		},
	}

	currentDepositAmount, err := bookingdomain.CalculateDepositAmount(
		originalAmountMinor,
		service.DepositRequired,
		bookingdomain.DepositType(service.DepositType),
		service.DepositAmountMinor,
		service.DepositPercentageBPS,
	)
	if err != nil {
		return promotionResolution{}, fmt.Errorf("calculate promotion deposit basis: %w", err)
	}
	automatic, err := r.findBestAutomaticPromotion(ctx, service, originalAmountMinor, customerEmail)
	if err != nil {
		return promotionResolution{}, err
	}

	currentAmount := originalAmountMinor
	if automatic != nil {
		autoAmount := findPromotionDiscountAmount(*automatic, originalAmountMinor)
		if autoAmount > 0 {
			currentAmount -= autoAmount
			resolution.AutomaticPromotion = automatic
			resolution.AutomaticAmountMinor = autoAmount
			if automatic.AppliesToDeposit && currentDepositAmount > 0 {
				currentDepositAmount -= findPromotionDiscountAmount(*automatic, currentDepositAmount)
			}
		}
	}

	trimmedCode := strings.ToUpper(strings.TrimSpace(code))
	if trimmedCode != "" {
		codePromotion, codeErr, err := r.findCodePromotion(ctx, service, trimmedCode, currentAmount, customerEmail)
		if err != nil {
			return promotionResolution{}, err
		}
		if codePromotion != nil && automatic != nil && !codePromotion.StackWithAutomatic {
			codePromotion = nil
			codeErr = "This code can't be used on a service that already has a discount."
		}
		if codePromotion != nil {
			codeAmount := findPromotionDiscountAmount(*codePromotion, currentAmount)
			if codeAmount > 0 {
				currentAmount -= codeAmount
				resolution.CodePromotion = codePromotion
				resolution.CodeAmountMinor = codeAmount
				if codePromotion.AppliesToDeposit && currentDepositAmount > 0 {
					currentDepositAmount -= findPromotionDiscountAmount(*codePromotion, currentDepositAmount)
				}
			}
		}
		resolution.CodeError = codeErr
	}

	switch {
	case resolution.CodePromotion != nil && resolution.AutomaticPromotion != nil:
		resolution.Snapshot.PromotionID = resolution.CodePromotion.ID
		resolution.Snapshot.DiscountName = resolution.CodePromotion.Name
		resolution.Snapshot.DiscountSource = "stacked"
		resolution.Snapshot.DiscountCode = resolution.CodePromotion.Code
		resolution.Snapshot.DiscountType = resolution.CodePromotion.DiscountType
		resolution.Snapshot.DiscountPercentageBPS = resolution.CodePromotion.DiscountPercentageBPS
		resolution.Snapshot.DiscountValueMinor = resolution.CodePromotion.DiscountValueMinor
		resolution.Snapshot.DiscountAmountMinor = resolution.AutomaticAmountMinor + resolution.CodeAmountMinor
		resolution.Snapshot.FinalAmountMinor = currentAmount
	case resolution.CodePromotion != nil:
		resolution.Snapshot.PromotionID = resolution.CodePromotion.ID
		resolution.Snapshot.DiscountName = resolution.CodePromotion.Name
		resolution.Snapshot.DiscountSource = "code"
		resolution.Snapshot.DiscountCode = resolution.CodePromotion.Code
		resolution.Snapshot.DiscountType = resolution.CodePromotion.DiscountType
		resolution.Snapshot.DiscountPercentageBPS = resolution.CodePromotion.DiscountPercentageBPS
		resolution.Snapshot.DiscountValueMinor = resolution.CodePromotion.DiscountValueMinor
		resolution.Snapshot.DiscountAmountMinor = resolution.CodeAmountMinor
		resolution.Snapshot.FinalAmountMinor = currentAmount
	case resolution.AutomaticPromotion != nil:
		resolution.Snapshot.PromotionID = resolution.AutomaticPromotion.ID
		resolution.Snapshot.DiscountName = resolution.AutomaticPromotion.Name
		resolution.Snapshot.DiscountSource = "automatic"
		resolution.Snapshot.DiscountCode = ""
		resolution.Snapshot.DiscountType = resolution.AutomaticPromotion.DiscountType
		resolution.Snapshot.DiscountPercentageBPS = resolution.AutomaticPromotion.DiscountPercentageBPS
		resolution.Snapshot.DiscountValueMinor = resolution.AutomaticPromotion.DiscountValueMinor
		resolution.Snapshot.DiscountAmountMinor = resolution.AutomaticAmountMinor
		resolution.Snapshot.FinalAmountMinor = currentAmount
	default:
		resolution.Snapshot.DiscountAmountMinor = 0
		resolution.Snapshot.FinalAmountMinor = originalAmountMinor
	}
	if currentDepositAmount < 0 {
		currentDepositAmount = 0
	}
	if currentDepositAmount > resolution.Snapshot.FinalAmountMinor {
		currentDepositAmount = resolution.Snapshot.FinalAmountMinor
	}
	resolution.Snapshot.DepositAmountMinor = currentDepositAmount

	return resolution, nil
}

func (r *Repository) promotionApplies(ctx context.Context, candidate promotionCandidate, service publicBookingServiceInfo, subtotal int64, customerEmail string) (bool, error) {
	now := time.Now().UTC()
	if !candidate.IsActive {
		return false, nil
	}
	if candidate.StartsAt.After(now) {
		return false, nil
	}
	if candidate.EndsAt != nil && candidate.EndsAt.Before(now) {
		return false, nil
	}
	if candidate.MinimumSpendMinor > 0 && subtotal < candidate.MinimumSpendMinor {
		return false, nil
	}
	if candidate.CurrencyCode != service.CurrencyCode {
		return false, nil
	}

	switch candidate.ScopeType {
	case "all_services":
	case "selected_services":
		if !uuidSliceContains(candidate.ServiceTargetIDs, service.ID) {
			return false, nil
		}
	case "selected_sections":
		if service.SectionID == uuid.Nil || !uuidSliceContains(candidate.SectionTargetIDs, service.SectionID) {
			return false, nil
		}
	default:
		return false, nil
	}

	if candidate.FirstTimeCustomersOnly {
		if customerEmail == "" {
			return false, nil
		}
		hasPrior, err := r.customerHasPriorBooking(ctx, service.ClientID, customerEmail)
		if err != nil {
			return false, err
		}
		if hasPrior {
			return false, nil
		}
	}

	totalRedemptions, err := r.countPromotionRedemptions(ctx, candidate.ID)
	if err != nil {
		return false, err
	}
	if candidate.MaxRedemptions > 0 && totalRedemptions >= candidate.MaxRedemptions {
		return false, nil
	}

	if candidate.MaxRedemptionsPerCustomer > 0 && customerEmail != "" {
		customerRedemptions, err := r.countPromotionCustomerRedemptions(ctx, candidate.ID, customerEmail)
		if err != nil {
			return false, err
		}
		if customerRedemptions >= candidate.MaxRedemptionsPerCustomer {
			return false, nil
		}
	}

	return true, nil
}

func (r *Repository) listPromotionCandidates(ctx context.Context, clientID uuid.UUID, promotionType, code string) ([]promotionCandidate, error) {
	const query = `
		SELECT
			p.id,
			p.name,
			p.promotion_type,
			p.code,
			p.discount_type,
			p.discount_percentage_bps,
			p.discount_value_minor,
			p.scope_type,
			p.is_active,
			p.starts_at,
			p.ends_at,
			p.max_redemptions,
			p.max_redemptions_per_customer,
			p.minimum_spend_minor,
			p.currency_code,
			p.first_time_customers_only,
			p.applies_to_deposit,
			p.stack_with_automatic_discounts,
			COALESCE(service_target_ids.items, '[]'::jsonb),
			COALESCE(section_target_ids.items, '[]'::jsonb)
		FROM promotions p
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(ps.service_id::text ORDER BY ps.service_id::text) AS items
			FROM promotion_services ps
			WHERE ps.promotion_id = p.id
		) service_target_ids ON TRUE
		LEFT JOIN LATERAL (
			SELECT jsonb_agg(ps.section_id::text ORDER BY ps.section_id::text) AS items
			FROM promotion_sections ps
			WHERE ps.promotion_id = p.id
		) section_target_ids ON TRUE
		WHERE p.client_id = $1
		  AND p.promotion_type = $2
		  AND ($3 = '' OR LOWER(p.code) = LOWER($3))
		ORDER BY p.updated_at DESC, p.created_at DESC
	`

	rows, err := r.db.Query(ctx, query, clientID, promotionType, code)
	if err != nil {
		return nil, fmt.Errorf("list promotion candidates: %w", err)
	}
	defer rows.Close()

	items := make([]promotionCandidate, 0)
	for rows.Next() {
		item, err := scanPromotionCandidate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate promotion candidates: %w", err)
	}
	return items, nil
}

func scanPromotionListItem(scanner promotionScanner) (PromotionListItem, error) {
	var item PromotionListItem
	var id uuid.UUID
	var endsAt pgtype.Timestamptz
	var serviceTargetsJSON []byte
	var sectionTargetsJSON []byte
	if err := scanner.Scan(
		&id,
		&item.Name,
		&item.PromotionType,
		&item.Code,
		&item.DiscountType,
		&item.DiscountPercentageBPS,
		&item.DiscountValueMinor,
		&item.ScopeType,
		&item.IsActive,
		&item.StartsAt,
		&endsAt,
		&item.MaxRedemptions,
		&item.MaxRedemptionsPerCustomer,
		&item.MinimumSpendMinor,
		&item.CurrencyCode,
		&item.FirstTimeCustomersOnly,
		&item.AppliesToDeposit,
		&item.StackWithAutomatic,
		&item.RedemptionCount,
		&serviceTargetsJSON,
		&sectionTargetsJSON,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return PromotionListItem{}, err
	}
	item.ID = id.String()
	if endsAt.Valid {
		item.EndsAt = &endsAt.Time
	}
	if len(serviceTargetsJSON) > 0 {
		if err := json.Unmarshal(serviceTargetsJSON, &item.ServiceTargets); err != nil {
			return PromotionListItem{}, fmt.Errorf("decode promotion service targets: %w", err)
		}
	}
	if len(sectionTargetsJSON) > 0 {
		if err := json.Unmarshal(sectionTargetsJSON, &item.SectionTargets); err != nil {
			return PromotionListItem{}, fmt.Errorf("decode promotion section targets: %w", err)
		}
	}
	return item, nil
}

func scanPromotionCandidate(scanner promotionScanner) (promotionCandidate, error) {
	var item promotionCandidate
	var endsAt pgtype.Timestamptz
	var serviceIDsJSON []byte
	var sectionIDsJSON []byte
	if err := scanner.Scan(
		&item.ID,
		&item.Name,
		&item.PromotionType,
		&item.Code,
		&item.DiscountType,
		&item.DiscountPercentageBPS,
		&item.DiscountValueMinor,
		&item.ScopeType,
		&item.IsActive,
		&item.StartsAt,
		&endsAt,
		&item.MaxRedemptions,
		&item.MaxRedemptionsPerCustomer,
		&item.MinimumSpendMinor,
		&item.CurrencyCode,
		&item.FirstTimeCustomersOnly,
		&item.AppliesToDeposit,
		&item.StackWithAutomatic,
		&serviceIDsJSON,
		&sectionIDsJSON,
	); err != nil {
		return promotionCandidate{}, err
	}
	if endsAt.Valid {
		item.EndsAt = &endsAt.Time
	}
	item.ServiceTargetIDs = decodeUUIDJSONArray(serviceIDsJSON)
	item.SectionTargetIDs = decodeUUIDJSONArray(sectionIDsJSON)
	return item, nil
}

func decodeUUIDJSONArray(value []byte) []uuid.UUID {
	if len(value) == 0 {
		return nil
	}
	var items []string
	if err := json.Unmarshal(value, &items); err != nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		parsed, err := uuid.Parse(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		ids = append(ids, parsed)
	}
	return ids
}

func normalizePromotionInput(input CreatePromotionInput) (CreatePromotionInput, []uuid.UUID, []uuid.UUID, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("promotion name is required")
	}

	input.PromotionType = normalizePromotionTypeFilter(input.PromotionType)
	if input.PromotionType != "automatic" && input.PromotionType != "code" {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("promotion_type must be automatic or code")
	}

	input.DiscountType = strings.TrimSpace(strings.ToLower(input.DiscountType))
	if input.DiscountType != "percentage" && input.DiscountType != "fixed_amount" && input.DiscountType != "set_price" {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("discount_type is invalid")
	}

	switch input.DiscountType {
	case "percentage":
		if input.DiscountPercentageBPS <= 0 || input.DiscountPercentageBPS > 10000 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("discount_percentage_bps must be between 1 and 10000")
		}
		if input.DiscountValueMinor != 0 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("discount_value_minor must be zero for percentage discounts")
		}
	default:
		if input.DiscountPercentageBPS != 0 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("discount_percentage_bps must be zero for monetary discounts")
		}
		if input.DiscountValueMinor <= 0 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("discount_value_minor must be greater than zero")
		}
	}

	input.ScopeType = strings.TrimSpace(strings.ToLower(input.ScopeType))
	if input.ScopeType == "" {
		input.ScopeType = "all_services"
	}
	if input.ScopeType != "all_services" && input.ScopeType != "selected_services" && input.ScopeType != "selected_sections" {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("scope_type is invalid")
	}

	if input.StartsAt.IsZero() {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("starts_at is required")
	}
	if input.EndsAt != nil && input.EndsAt.Before(input.StartsAt) {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("ends_at must be after starts_at")
	}
	if input.MaxRedemptions < 0 || input.MaxRedemptionsPerCustomer < 0 {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("redemption limits cannot be negative")
	}
	if input.MinimumSpendMinor < 0 {
		return CreatePromotionInput{}, nil, nil, fmt.Errorf("minimum_spend_minor cannot be negative")
	}

	if input.PromotionType == "code" {
		input.Code = strings.ToUpper(strings.TrimSpace(input.Code))
		if input.Code == "" {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("code is required for code promotions")
		}
	} else {
		input.Code = ""
		input.StackWithAutomatic = false
	}

	serviceIDs, err := parsePromotionUUIDList(input.ServiceIDs)
	if err != nil {
		return CreatePromotionInput{}, nil, nil, err
	}
	sectionIDs, err := parsePromotionUUIDList(input.SectionIDs)
	if err != nil {
		return CreatePromotionInput{}, nil, nil, err
	}

	switch input.ScopeType {
	case "selected_services":
		if len(serviceIDs) == 0 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("select at least one service")
		}
		sectionIDs = nil
	case "selected_sections":
		if len(sectionIDs) == 0 {
			return CreatePromotionInput{}, nil, nil, fmt.Errorf("select at least one section")
		}
		serviceIDs = nil
	default:
		serviceIDs = nil
		sectionIDs = nil
	}

	return input, serviceIDs, sectionIDs, nil
}

func parsePromotionUUIDList(values []string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0, len(values))
	seen := make(map[uuid.UUID]struct{})
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		parsed, err := uuid.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid target id")
		}
		if _, exists := seen[parsed]; exists {
			continue
		}
		seen[parsed] = struct{}{}
		ids = append(ids, parsed)
	}
	return ids, nil
}

func syncPromotionTargets(ctx context.Context, exec promotionExec, clientID, promotionID uuid.UUID, serviceIDs, sectionIDs []uuid.UUID) error {
	if _, err := exec.Exec(ctx, `DELETE FROM promotion_services WHERE promotion_id = $1`, promotionID); err != nil {
		return fmt.Errorf("clear promotion services: %w", err)
	}
	if _, err := exec.Exec(ctx, `DELETE FROM promotion_sections WHERE promotion_id = $1`, promotionID); err != nil {
		return fmt.Errorf("clear promotion sections: %w", err)
	}

	for _, serviceID := range serviceIDs {
		var exists bool
		if err := exec.QueryRow(
			ctx,
			`
				SELECT EXISTS(
					SELECT 1
					FROM services AS service
					INNER JOIN promotions AS promotion
						ON promotion.id = $3
					   AND promotion.client_id = service.client_id
					   AND promotion.currency_code = service.currency_code
					WHERE service.client_id = $1
					  AND service.id = $2
				)
			`,
			clientID,
			serviceID,
			promotionID,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check promotion service: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := exec.Exec(ctx, `INSERT INTO promotion_services (promotion_id, service_id) VALUES ($1, $2)`, promotionID, serviceID); err != nil {
			return fmt.Errorf("insert promotion service: %w", err)
		}
	}

	for _, sectionID := range sectionIDs {
		var exists bool
		if err := exec.QueryRow(
			ctx,
			`
				SELECT EXISTS(
					SELECT 1
					FROM service_sections AS section
					INNER JOIN promotions AS promotion
						ON promotion.id = $3
					   AND promotion.client_id = section.client_id
					WHERE section.client_id = $1
					  AND section.id = $2
					  AND NOT EXISTS (
						SELECT 1
						FROM services AS service
						WHERE service.section_id = section.id
						  AND service.currency_code <> promotion.currency_code
					  )
				)
			`,
			clientID,
			sectionID,
			promotionID,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check promotion section: %w", err)
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := exec.Exec(ctx, `INSERT INTO promotion_sections (promotion_id, section_id) VALUES ($1, $2)`, promotionID, sectionID); err != nil {
			return fmt.Errorf("insert promotion section: %w", err)
		}
	}

	return nil
}

func normalizePromotionTypeFilter(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "automatic" || value == "code" {
		return value
	}
	return ""
}

func isPromotionCodeConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == "promotions_client_id_code_unique_idx"
}

func uuidSliceContains(values []uuid.UUID, target uuid.UUID) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (r *Repository) countPromotionRedemptions(ctx context.Context, promotionID uuid.UUID) (int, error) {
	var count int
	if err := r.db.QueryRow(ctx, `
		SELECT (
			(SELECT COUNT(*) FROM promotion_redemptions WHERE promotion_id = $1) +
			(SELECT COUNT(*) FROM booking_quote_promotions bqp
			 INNER JOIN booking_quotes bq ON bq.id = bqp.booking_quote_id
			 WHERE bqp.promotion_id = $1
			   AND bq.consumed_at IS NULL AND bq.expires_at > NOW())
		)::int
	`, promotionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count promotion redemptions: %w", err)
	}
	return count, nil
}

func (r *Repository) countPromotionCustomerRedemptions(ctx context.Context, promotionID uuid.UUID, customerEmail string) (int, error) {
	var count int
	if err := r.db.QueryRow(ctx, `
		SELECT (
			(SELECT COUNT(*) FROM promotion_redemptions
			 WHERE promotion_id = $1 AND LOWER(customer_email) = LOWER($2)) +
			(SELECT COUNT(*) FROM booking_quote_promotions bqp
			 INNER JOIN booking_quotes bq ON bq.id = bqp.booking_quote_id
			 WHERE bqp.promotion_id = $1
			   AND bqp.customer_email_normalized = LOWER($2)
			   AND bq.consumed_at IS NULL AND bq.expires_at > NOW())
		)::int
	`, promotionID, customerEmail).Scan(&count); err != nil {
		return 0, fmt.Errorf("count promotion customer redemptions: %w", err)
	}
	return count, nil
}

func (r *Repository) customerHasPriorBooking(ctx context.Context, clientID uuid.UUID, customerEmail string) (bool, error) {
	var exists bool
	if err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM bookings b
			INNER JOIN customers c ON c.id = b.customer_id
			WHERE b.client_id = $1
			  AND LOWER(c.email) = LOWER($2)
		)
	`, clientID, customerEmail).Scan(&exists); err != nil {
		return false, fmt.Errorf("check prior bookings: %w", err)
	}
	return exists, nil
}
