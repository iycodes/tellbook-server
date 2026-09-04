package appdata

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	MaxProviderServices          = 100
	MaxProviderServiceSections   = 25
	MaxProviderBusinessLocations = 20
	MaxProviderPortfolioItems    = 50
)

var (
	ErrServiceLimitReached          = errors.New("service limit reached")
	ErrServiceSectionLimitReached   = errors.New("service section limit reached")
	ErrBusinessLocationLimitReached = errors.New("business location limit reached")
	ErrPortfolioItemLimitReached    = errors.New("portfolio item limit reached")
)

func enforceProviderCollectionLimit(
	ctx context.Context,
	tx pgx.Tx,
	clientID uuid.UUID,
	countQuery string,
	limit int,
	limitErr error,
) error {
	var ownerID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM clients WHERE id = $1 FOR UPDATE`, clientID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock provider collection owner: %w", err)
	}

	var count int
	if err := tx.QueryRow(ctx, countQuery, clientID).Scan(&count); err != nil {
		return fmt.Errorf("count provider collection: %w", err)
	}
	if count >= limit {
		return limitErr
	}
	return nil
}
