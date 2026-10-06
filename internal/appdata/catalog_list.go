package appdata

import (
	"context"
	"encoding/json"
	"fmt"

	"booking/go-server/internal/money"
	"github.com/google/uuid"
)

type CatalogServiceSummary struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Revision         int64       `json:"revision"`
	Status           string      `json:"status"`
	IsHidden         bool        `json:"is_hidden"`
	SectionID        string      `json:"section_id"`
	CurrencyCode     string      `json:"currency_code"`
	PriceAmountMinor money.Minor `json:"price_amount_minor"`
	DurationMinutes  int         `json:"duration_minutes"`
}

type CatalogServicePage struct {
	Items      []CatalogServiceSummary `json:"items"`
	NextCursor string                  `json:"next_cursor"`
	Total      int                     `json:"total"`
}

// ListCatalogServices filters, orders and limits summaries in one database
// snapshot. It does not load full service settings or join unrelated collections.
func (h *Handler) ListCatalogServices(ctx context.Context, provider uuid.UUID, limit int, cursor, search, status, section string) (CatalogServicePage, error) {
	if limit < 1 || limit > 100 {
		return CatalogServicePage{}, catalogError("invalid_request", "Limit must be between 1 and 100.")
	}
	const query = `
		WITH filtered AS MATERIALIZED (
			SELECT id,title,revision,status,is_hidden,section_id,currency_code,
			       price_amount_minor,duration_minutes,
			       row_number() OVER (ORDER BY sort_order,created_at DESC,id) AS position
			FROM services
			WHERE client_id=$1
			  AND ($3='' OR strpos(lower(title),lower($3))>0)
			  AND ($4='' OR status=$4)
			  AND ($5='' OR section_id::text=$5)
		), page AS (
			SELECT * FROM filtered
			WHERE position>COALESCE((SELECT position FROM filtered WHERE id::text=$6),0)
			ORDER BY position LIMIT $2+1
		)
		SELECT (SELECT count(*) FROM filtered),
		       $6='' OR EXISTS(SELECT 1 FROM filtered WHERE id::text=$6),
		       COALESCE(jsonb_agg(jsonb_build_object(
			 'id',id::text,'name',title,'revision',revision,'status',status,
			 'is_hidden',COALESCE(is_hidden,false),'section_id',COALESCE(section_id::text,''),
			 'currency_code',currency_code,'price_amount_minor',price_amount_minor::text,
			 'duration_minutes',duration_minutes) ORDER BY position),'[]'::jsonb)
		FROM page`
	page := CatalogServicePage{}
	var validCursor bool
	var raw []byte
	if err := h.repo.catalogDB().QueryRow(ctx, query, provider, limit, search, status, section, cursor).Scan(&page.Total, &validCursor, &raw); err != nil {
		return page, fmt.Errorf("list catalog summaries: %w", err)
	}
	if !validCursor {
		return page, catalogError("invalid_cursor", "The service list changed. Start the list again.")
	}
	if err := json.Unmarshal(raw, &page.Items); err != nil {
		return page, fmt.Errorf("decode catalog summaries: %w", err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	return page, nil
}
