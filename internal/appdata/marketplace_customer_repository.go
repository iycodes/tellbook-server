package appdata

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrMarketplaceCustomerCursor = errors.New("invalid marketplace customer cursor")

type marketplaceCustomerCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func encodeMarketplaceCustomerCursor(createdAt time.Time, id uuid.UUID) string {
	value := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeMarketplaceCustomerCursor(raw string) (*marketplaceCustomerCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrMarketplaceCustomerCursor
	}
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 {
		return nil, ErrMarketplaceCustomerCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, ErrMarketplaceCustomerCursor
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return nil, ErrMarketplaceCustomerCursor
	}
	return &marketplaceCustomerCursor{CreatedAt: createdAt, ID: id}, nil
}

func (r *Repository) SaveMarketplaceProvider(ctx context.Context, customerID, providerID uuid.UUID) error {
	result, err := r.db.Exec(ctx, `
		INSERT INTO marketplace_saved_providers (marketplace_customer_id, provider_id)
		SELECT $1, provider.client_id
		FROM marketplace_provider_documents provider
 JOIN client_profiles eligibility ON eligibility.client_id=provider.client_id AND NOT eligibility.platform_restricted
		WHERE provider.client_id=$2
		ON CONFLICT (marketplace_customer_id, provider_id) DO NOTHING
	`, customerID, providerID)
	if err != nil {
		return fmt.Errorf("save marketplace provider: %w", err)
	}
	if result.RowsAffected() == 0 {
		var alreadySaved bool
		if err := r.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM marketplace_saved_providers
				WHERE marketplace_customer_id=$1 AND provider_id=$2
			)
		`, customerID, providerID).Scan(&alreadySaved); err != nil {
			return fmt.Errorf("check saved marketplace provider: %w", err)
		}
		if !alreadySaved {
			return ErrNotFound
		}
	}
	return nil
}

func (r *Repository) RemoveMarketplaceSavedProvider(ctx context.Context, customerID, providerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		DELETE FROM marketplace_saved_providers
		WHERE marketplace_customer_id=$1 AND provider_id=$2
	`, customerID, providerID)
	if err != nil {
		return fmt.Errorf("remove saved marketplace provider: %w", err)
	}
	return nil
}

func (r *Repository) IsMarketplaceProviderSaved(ctx context.Context, customerID, providerID uuid.UUID) (bool, error) {
	var saved bool
	if err := r.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM marketplace_saved_providers
			WHERE marketplace_customer_id=$1 AND provider_id=$2
		)
	`, customerID, providerID).Scan(&saved); err != nil {
		return false, fmt.Errorf("check saved marketplace provider: %w", err)
	}
	return saved, nil
}

func (r *Repository) ListMarketplaceSavedProviders(
	ctx context.Context,
	customerID uuid.UUID,
	limit int,
	cursorRaw string,
) (MarketplaceSavedProviderList, error) {
	cursor, err := decodeMarketplaceCustomerCursor(cursorRaw)
	if err != nil {
		return MarketplaceSavedProviderList{}, err
	}
	var cursorTime *time.Time
	var cursorID *uuid.UUID
	if cursor != nil {
		cursorTime = &cursor.CreatedAt
		cursorID = &cursor.ID
	}
	rows, err := r.db.Query(ctx, `
		SELECT provider.client_id, provider.handle_slug, provider.business_name, provider.headline,
			provider.category_id, provider.category_name, provider.avatar_url, provider.hero_image_url,
			provider.verified, provider.review_rating, provider.review_count,
			service.location_label,
			service.service_id, service.slug, service.title, service.duration_minutes,
			service.price_amount_minor, service.currency_code, service.fulfillment_mode,
			provider.completed_bookings,
			ARRAY_REMOVE(ARRAY[
				CASE WHEN provider.verified THEN 'Verified' END,
				CASE service.fulfillment_mode WHEN 'virtual' THEN 'Online'
					WHEN 'customer_location' THEN 'Comes to you' END
			], NULL), saved.created_at
		FROM marketplace_saved_providers saved
		JOIN marketplace_provider_documents provider ON provider.client_id=saved.provider_id
 JOIN client_profiles eligibility ON eligibility.client_id=provider.client_id AND NOT eligibility.platform_restricted
		CROSS JOIN LATERAL (
			SELECT candidate.*
			FROM marketplace_service_documents candidate
			WHERE candidate.provider_id=provider.client_id
			ORDER BY candidate.price_amount_minor, candidate.sort_order, candidate.service_id
			LIMIT 1
		) service
		WHERE saved.marketplace_customer_id=$1
		  AND ($2::timestamptz IS NULL OR (saved.created_at, saved.provider_id) < ($2, $3::uuid))
		ORDER BY saved.created_at DESC, saved.provider_id DESC
		LIMIT $4
	`, customerID, cursorTime, cursorID, limit+1)
	if err != nil {
		return MarketplaceSavedProviderList{}, fmt.Errorf("list saved marketplace providers: %w", err)
	}
	defer rows.Close()
	items := make([]MarketplaceSavedProvider, 0, limit+1)
	for rows.Next() {
		var item MarketplaceSavedProvider
		if err := rows.Scan(
			&item.ID, &item.HandleSlug, &item.BusinessName, &item.Headline,
			&item.CategoryID, &item.CategoryName, &item.AvatarURL, &item.HeroImageURL,
			&item.Verified, &item.ReviewRating, &item.ReviewCount, &item.LocationLabel,
			&item.ServiceID, &item.ServiceSlug, &item.ServiceTitle, &item.DurationMinutes,
			&item.PriceAmountMinor, &item.CurrencyCode, &item.FulfillmentMode,
			&item.CompletedBookings, &item.Badges, &item.SavedAt,
		); err != nil {
			return MarketplaceSavedProviderList{}, fmt.Errorf("scan saved marketplace provider: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MarketplaceSavedProviderList{}, fmt.Errorf("iterate saved marketplace providers: %w", err)
	}
	rows.Close()
	providers := make([]MarketplaceProvider, len(items))
	for index := range items {
		providers[index] = items[index].MarketplaceProvider
	}
	if len(providers) > 0 {
		if err := r.loadMarketplaceProjectedNextAvailability(ctx, providers); err != nil {
			return MarketplaceSavedProviderList{}, err
		}
		for index := range items {
			items[index].MarketplaceProvider = providers[index]
		}
	}
	response := MarketplaceSavedProviderList{Items: items}
	if len(items) > limit {
		last := items[limit-1]
		providerID, parseErr := uuid.Parse(last.ID)
		if parseErr != nil {
			return MarketplaceSavedProviderList{}, fmt.Errorf("parse saved provider ID: %w", parseErr)
		}
		response.Items = items[:limit]
		response.NextCursor = encodeMarketplaceCustomerCursor(last.SavedAt, providerID)
	}
	return response, nil
}

func (r *Repository) ListMarketplaceNotifications(
	ctx context.Context,
	customerID uuid.UUID,
	kind string,
	unreadOnly bool,
	limit int,
	cursorRaw string,
) (MarketplaceNotificationList, error) {
	cursor, err := decodeMarketplaceCustomerCursor(cursorRaw)
	if err != nil {
		return MarketplaceNotificationList{}, err
	}
	var cursorTime *time.Time
	var cursorID *uuid.UUID
	if cursor != nil {
		cursorTime = &cursor.CreatedAt
		cursorID = &cursor.ID
	}
	var unreadCount int
	if err := r.db.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM marketplace_notifications
		WHERE marketplace_customer_id=$1 AND read_at IS NULL
	`, customerID).Scan(&unreadCount); err != nil {
		return MarketplaceNotificationList{}, fmt.Errorf("count unread marketplace notifications: %w", err)
	}
	rows, err := r.db.Query(ctx, `
		SELECT notification.id, notification.kind, notification.event_type,
			notification.title, notification.body,
			COALESCE(notification.provider_id::text, ''), COALESCE(notification.booking_id::text, ''),
			COALESCE(notification.conversation_id::text, ''), notification.read_at, notification.created_at
		FROM marketplace_notifications notification
		WHERE notification.marketplace_customer_id=$1
		  AND ($2='' OR notification.kind=$2)
		  AND (NOT $3 OR notification.read_at IS NULL)
		  AND ($4::timestamptz IS NULL OR (notification.created_at, notification.id) < ($4, $5::uuid))
		ORDER BY notification.created_at DESC, notification.id DESC
		LIMIT $6
	`, customerID, kind, unreadOnly, cursorTime, cursorID, limit+1)
	if err != nil {
		return MarketplaceNotificationList{}, fmt.Errorf("list marketplace notifications: %w", err)
	}
	defer rows.Close()
	items := make([]MarketplaceNotification, 0, limit+1)
	for rows.Next() {
		var item MarketplaceNotification
		if err := rows.Scan(
			&item.ID, &item.Kind, &item.EventType, &item.Title, &item.Body,
			&item.ProviderID, &item.BookingID, &item.ConversationID,
			&item.ReadAt, &item.CreatedAt,
		); err != nil {
			return MarketplaceNotificationList{}, fmt.Errorf("scan marketplace notification: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return MarketplaceNotificationList{}, fmt.Errorf("iterate marketplace notifications: %w", err)
	}
	response := MarketplaceNotificationList{Items: items, UnreadCount: unreadCount}
	if len(items) > limit {
		last := items[limit-1]
		id, parseErr := uuid.Parse(last.ID)
		if parseErr != nil {
			return MarketplaceNotificationList{}, fmt.Errorf("parse marketplace notification ID: %w", parseErr)
		}
		response.Items = items[:limit]
		response.NextCursor = encodeMarketplaceCustomerCursor(last.CreatedAt, id)
	}
	return response, nil
}

func (r *Repository) MarkMarketplaceNotificationRead(ctx context.Context, customerID, notificationID uuid.UUID) error {
	result, err := r.db.Exec(ctx, `
		UPDATE marketplace_notifications SET read_at=COALESCE(read_at, NOW())
		WHERE id=$1 AND marketplace_customer_id=$2
	`, notificationID, customerID)
	if err != nil {
		return fmt.Errorf("mark marketplace notification read: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) MarkAllMarketplaceNotificationsRead(ctx context.Context, customerID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `
		UPDATE marketplace_notifications SET read_at=NOW()
		WHERE marketplace_customer_id=$1 AND read_at IS NULL
	`, customerID)
	if err != nil {
		return fmt.Errorf("mark all marketplace notifications read: %w", err)
	}
	return nil
}
