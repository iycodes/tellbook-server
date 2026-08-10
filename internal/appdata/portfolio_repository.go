package appdata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) ListPortfolioItems(ctx context.Context, clientID uuid.UUID) ([]ManagedPortfolioItem, error) {
	const query = `
		SELECT id, title, image_url, sort_order, created_at
		FROM provider_portfolio_items
		WHERE client_id = $1
		ORDER BY sort_order ASC, created_at ASC
	`

	rows, err := r.db.Query(ctx, query, clientID)
	if err != nil {
		return nil, fmt.Errorf("list portfolio items: %w", err)
	}
	defer rows.Close()

	items := make([]ManagedPortfolioItem, 0)
	for rows.Next() {
		var item ManagedPortfolioItem
		var itemID uuid.UUID
		if err := rows.Scan(&itemID, &item.Title, &item.ImageURL, &item.SortOrder, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan portfolio item: %w", err)
		}
		item.ID = itemID.String()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate portfolio items: %w", err)
	}
	return items, nil
}

func (r *Repository) CreatePortfolioItem(ctx context.Context, clientID uuid.UUID, input CreatePortfolioItemInput) (ManagedPortfolioItem, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.ImageURL = strings.TrimSpace(input.ImageURL)
	if input.ImageURL == "" {
		return ManagedPortfolioItem{}, errors.New("image URL is required")
	}
	if len(input.Title) > 120 {
		return ManagedPortfolioItem{}, errors.New("title must be 120 characters or fewer")
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return ManagedPortfolioItem{}, fmt.Errorf("begin create portfolio item: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := tx.QueryRow(ctx, `SELECT id FROM clients WHERE id = $1 FOR UPDATE`, clientID).Scan(&clientID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManagedPortfolioItem{}, ErrNotFound
		}
		return ManagedPortfolioItem{}, fmt.Errorf("lock portfolio owner: %w", err)
	}

	var item ManagedPortfolioItem
	var itemID uuid.UUID
	const query = `
		INSERT INTO provider_portfolio_items (id, client_id, title, image_url, sort_order)
		VALUES (
			$1,
			$2,
			$3,
			$4,
			COALESCE((SELECT MAX(sort_order) + 1 FROM provider_portfolio_items WHERE client_id = $2), 0)
		)
		RETURNING id, title, image_url, sort_order, created_at
	`
	if err := tx.QueryRow(ctx, query, uuid.New(), clientID, input.Title, input.ImageURL).Scan(
		&itemID,
		&item.Title,
		&item.ImageURL,
		&item.SortOrder,
		&item.CreatedAt,
	); err != nil {
		return ManagedPortfolioItem{}, fmt.Errorf("create portfolio item: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ManagedPortfolioItem{}, fmt.Errorf("commit create portfolio item: %w", err)
	}
	item.ID = itemID.String()
	return item, nil
}

func (r *Repository) DeletePortfolioItem(ctx context.Context, clientID, itemID uuid.UUID) (string, error) {
	var imageURL string
	err := r.db.QueryRow(ctx, `
		DELETE FROM provider_portfolio_items
		WHERE client_id = $1 AND id = $2
		RETURNING image_url
	`, clientID, itemID).Scan(&imageURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("delete portfolio item: %w", err)
	}
	return imageURL, nil
}

func (r *Repository) ReorderPortfolioItems(ctx context.Context, clientID uuid.UUID, orderedIDs []uuid.UUID) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder portfolio items: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id
		FROM provider_portfolio_items
		WHERE client_id = $1
		FOR UPDATE
	`, clientID)
	if err != nil {
		return fmt.Errorf("lock portfolio items: %w", err)
	}

	existing := make(map[uuid.UUID]struct{})
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan locked portfolio item: %w", err)
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate locked portfolio items: %w", err)
	}
	rows.Close()

	if !isExactPortfolioOrder(existing, orderedIDs) {
		return ErrInvalidPortfolioOrder
	}

	for index, itemID := range orderedIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE provider_portfolio_items
			SET sort_order = $3
			WHERE client_id = $1 AND id = $2
		`, clientID, itemID, index); err != nil {
			return fmt.Errorf("reorder portfolio item: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder portfolio items: %w", err)
	}
	return nil
}

func isExactPortfolioOrder(existing map[uuid.UUID]struct{}, orderedIDs []uuid.UUID) bool {
	if len(existing) != len(orderedIDs) {
		return false
	}
	seen := make(map[uuid.UUID]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if _, exists := existing[id]; !exists {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
