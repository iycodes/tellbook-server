package appdata

import (
	"booking/go-server/internal/markets"
	"context"
	"fmt"
	"github.com/google/uuid"
)

func (h *Handler) CatalogProfile(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	profile, err := h.repo.GetClientProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	exponent := uint8(0)
	foundCurrency := false
	if market, ok := markets.DefaultCatalog().Lookup(profile.CountryCode); ok {
		for _, currency := range market.Currencies {
			if currency.Code == profile.CurrencyCode {
				exponent = currency.MinorUnitExponent
				foundCurrency = true
			}
		}
	}
	if profile.MarketConfigured && !foundCurrency {
		return nil, catalogError("invalid_currency", "Select a supported business currency in Tellbook.")
	}
	return map[string]any{"id": id.String(), "business_name": profile.BusinessName, "handle_slug": profile.HandleSlug, "timezone": profile.Timezone, "country_code": profile.CountryCode, "currency_code": profile.CurrencyCode, "minor_unit_exponent": exponent, "market_configured": profile.MarketConfigured}, nil
}
func (h *Handler) CatalogSetup(ctx context.Context, id uuid.UUID) (map[string]any, error) {
	profile, err := h.CatalogProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	sections, err := h.repo.ListServiceSections(ctx, id)
	if err != nil {
		return nil, err
	}
	locations, err := h.repo.ListBusinessLocations(ctx, id)
	if err != nil {
		return nil, err
	}
	hours, err := h.repo.GetBusinessHours(ctx, id)
	if err != nil {
		return nil, err
	}
	templates := []map[string]string{}
	rows, err := h.repo.db.Query(ctx, `SELECT f.id,f.title FROM agreement_template_families f JOIN agreement_template_versions v ON v.id=f.current_published_version_id AND v.family_id=f.id AND v.state='published' WHERE f.client_id=$1 AND f.owner_type='client' AND f.status='published' ORDER BY f.id LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var family, title string
		if err = rows.Scan(&family, &title); err != nil {
			return nil, err
		}
		templates = append(templates, map[string]string{"id": family, "title": title})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"profile": profile, "sections": sections, "locations": locations, "business_hours": hours, "agreement_templates": templates, "fulfillment_modes": []string{"provider_location", "customer_location", "virtual"}, "availability_modes": []string{"inherit_business_hours", "custom"}}, nil
}
func (h *Handler) CatalogExponent(ctx context.Context, id uuid.UUID) (uint8, error) {
	profile, err := h.CatalogProfile(ctx, id)
	if err != nil {
		return 0, err
	}
	if profile["market_configured"] != true {
		return 0, fmt.Errorf("complete your business country and currency in Tellbook first")
	}
	return profile["minor_unit_exponent"].(uint8), nil
}
