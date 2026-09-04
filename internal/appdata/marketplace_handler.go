package appdata

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/marketplaceauth"
	"booking/go-server/internal/money"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (h *Handler) listMarketplaceCustomerBookings(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	status := MarketplaceBookingStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status == "" {
		status = MarketplaceBookingUpcoming
	}
	if status != MarketplaceBookingUpcoming && status != MarketplaceBookingPast && status != MarketplaceBookingCancelled {
		writeError(w, http.StatusBadRequest, "invalid_booking_status", "status must be upcoming, past, or cancelled.")
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	limit, ok := marketplaceIntQuery(w, r.URL.Query().Get("limit"), "limit", 20, 1, 50)
	if !ok {
		return
	}
	includeCounts, err := listIncludeCounts(r, cursor == "")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_include_counts", err.Error())
		return
	}
	response, err := h.repo.ListMarketplaceCustomerBookings(r.Context(), customer.ID, status, cursor, limit, includeCounts)
	if errors.Is(err, ErrInvalidKeysetCursor) {
		writeError(w, http.StatusBadRequest, "invalid_booking_cursor", "Booking cursor does not match this status.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_bookings_failed", "Could not load your bookings.")
		return
	}
	for index := range response.Items {
		response.Items[index].ServiceImageURL = h.signedMediaURL(r.Context(), response.Items[index].ServiceImageURL)
		response.Items[index].ProviderAvatarURL = h.signedMediaURL(r.Context(), response.Items[index].ProviderAvatarURL)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getMarketplaceCustomerBooking(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	detail, err := h.repo.GetMarketplaceCustomerBooking(r.Context(), customer.ID, bookingID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_booking_failed", "Could not load the booking.")
		return
	}
	detail.ServiceImageURL = h.signedMediaURL(r.Context(), detail.ServiceImageURL)
	detail.ProviderAvatarURL = h.signedMediaURL(r.Context(), detail.ProviderAvatarURL)
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) claimMarketplaceCustomerBooking(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	input, err := decodeJSON[struct {
		BookingToken string `json:"booking_token"`
	}](r)
	if err != nil || strings.TrimSpace(input.BookingToken) == "" {
		writeError(w, http.StatusBadRequest, "invalid_booking_claim", "Enter a valid booking reference link.")
		return
	}
	booking, err := h.repo.GetPublicBookingSummary(r.Context(), input.BookingToken)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_claim_failed", "Could not verify the booking.")
		return
	}
	if !marketplaceauth.CustomerMatchesBookingContact(customer, booking.CustomerEmail, booking.CustomerPhone) {
		writeError(w, http.StatusForbidden, "booking_contact_mismatch", "Verify the email or phone used for this booking before adding it to your account.")
		return
	}
	bookingID, err := h.repo.ClaimMarketplaceBooking(r.Context(), customer.ID, input.BookingToken)
	if errors.Is(err, ErrMarketplaceBookingOwned) {
		writeError(w, http.StatusConflict, "booking_already_claimed", "This booking is already linked to another account.")
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_claim_failed", "Could not add the booking to your account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"booking_id": bookingID.String()})
}

func (h *Handler) getMarketplaceCustomerBookingCalendar(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	detail, err := h.repo.GetMarketplaceCustomerBooking(r.Context(), customer.ID, bookingID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_calendar_failed", "Could not create the calendar event.")
		return
	}
	if !detail.AllowedActions.AddToCalendar {
		writeError(w, http.StatusConflict, "booking_not_confirmed", "The booking must be confirmed before it can be added to a calendar.")
		return
	}
	booking, err := h.repo.GetPublicBookingSummary(r.Context(), detail.BookingToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_calendar_failed", "Could not create the calendar event.")
		return
	}
	calendar, err := buildPublicBookingCalendar(booking, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_calendar_failed", "Could not create the calendar event.")
		return
	}
	setPublicFinancialHeaders(w)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", publicBookingCalendarDisposition(booking.ServiceTitle))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, calendar)
}

func (h *Handler) getMarketplaceCustomerBookingReceipt(w http.ResponseWriter, r *http.Request) {
	customer, _ := marketplaceauth.CustomerFromContext(r.Context())
	bookingID, ok := marketplaceBookingIDParam(w, r)
	if !ok {
		return
	}
	receipt, err := h.repo.GetMarketplaceBookingReceipt(r.Context(), customer.ID, bookingID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "receipt_not_found", "A receipt is not available for this booking yet.")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "receipt_failed", "Could not create the receipt.")
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, receipt)
		return
	}
	disposition := "inline"
	if r.URL.Query().Get("download") == "1" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="tellbook-receipt-%s.html"`, disposition, receipt.ReceiptNumber))
	if err := marketplaceReceiptTemplate.Execute(w, receipt); err != nil {
		return
	}
}

func marketplaceBookingIDParam(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	bookingID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "bookingID")))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_booking_id", "Booking ID is invalid.")
		return uuid.Nil, false
	}
	return bookingID, true
}

var marketplaceReceiptTemplate = template.Must(template.New("marketplace-receipt").Funcs(template.FuncMap{
	"amount": func(value money.Minor, currency string) string {
		symbol := currency + " "
		if currency == "NGN" {
			symbol = "₦"
		}
		minor := int64(value)
		sign := ""
		if minor < 0 {
			sign = "-"
			minor = -minor
		}
		return fmt.Sprintf("%s%s%d.%02d", sign, symbol, minor/100, minor%100)
	},
	"datetime": func(value time.Time, timezone string) string {
		location, err := time.LoadLocation(timezone)
		if err != nil {
			location = time.UTC
		}
		return value.In(location).Format("02 Jan 2006, 03:04 PM")
	},
}).Parse(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Tellbook receipt {{.ReceiptNumber}}</title><style>body{font-family:Inter,system-ui,sans-serif;background:#f8f5f5;color:#2d2426;margin:0;padding:32px}.receipt{max-width:680px;margin:auto;background:white;border:1px solid #eadfe1;border-radius:24px;padding:32px}.brand{color:#e64d73;font-weight:800}.muted{color:#806d71}.row{display:flex;justify-content:space-between;gap:24px;padding:12px 0;border-bottom:1px solid #f0e7e8}.total{font-size:1.25rem;font-weight:800}.paid{color:#16805b}h1{margin:.4rem 0 0}@media(max-width:520px){body{padding:12px}.receipt{padding:22px}}</style></head><body><main class="receipt"><div class="brand">Tellbook</div><h1>Payment receipt</h1><p class="muted">Receipt {{.ReceiptNumber}}</p><div class="row"><span>Service</span><strong>{{.ServiceTitle}}</strong></div><div class="row"><span>Provider</span><strong>{{.ProviderName}}</strong></div><div class="row"><span>Customer</span><strong>{{.CustomerName}}</strong></div><div class="row"><span>Appointment</span><strong>{{datetime .StartsAt .Timezone}}</strong></div><div class="row"><span>Booking total</span><strong>{{amount .TotalAmountMinor .CurrencyCode}}</strong></div><div class="row total"><span>Net paid</span><strong class="paid">{{amount .NetPaidAmountMinor .CurrencyCode}}</strong></div>{{if .RefundedAmountMinor}}<div class="row"><span>Refunded</span><strong>{{amount .RefundedAmountMinor .CurrencyCode}}</strong></div>{{end}}<h2>Payments</h2>{{range .Payments}}<div class="row"><span>{{.Purpose}} · {{.Method}}<br><small class="muted">{{.Reference}}</small></span><strong>{{amount .AmountMinor $.CurrencyCode}}</strong></div>{{end}}<p class="muted">Generated from Tellbook's authoritative payment record.</p></main></body></html>`))

func (h *Handler) searchMarketplaceProviders(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if country := strings.ToUpper(strings.TrimSpace(query.Get("country_code"))); country != "" && country != "NG" {
		writeError(w, http.StatusBadRequest, "unsupported_country", "Marketplace search currently supports country_code=NG.")
		return
	}
	stateID, ok := optionalMarketplaceUUID(w, query.Get("state_id"), "state_id")
	if !ok {
		return
	}
	lgaID, ok := optionalMarketplaceUUID(w, query.Get("lga_id"), "lga_id")
	if !ok {
		return
	}
	categoryID, ok := optionalMarketplaceUUID(w, query.Get("category_id"), "category_id")
	if !ok {
		return
	}
	locationToken := strings.TrimSpace(query.Get("location_token"))
	if locationToken != "" && (stateID != nil || lgaID != nil) {
		writeError(w, http.StatusBadRequest, "location_mode_conflict", "Use either current location or a selected region.")
		return
	}
	searchQuery := strings.TrimSpace(query.Get("q"))
	if len(searchQuery) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_query", "q must be 100 characters or fewer.")
		return
	}

	limit, ok := marketplaceIntQuery(w, query.Get("limit"), "limit", 20, 1, 50)
	if !ok {
		return
	}
	radius := 0
	if locationToken != "" {
		radius = 15000
	}
	if query.Has("radius_meters") {
		radius, ok = marketplaceIntQuery(w, query.Get("radius_meters"), "radius_meters", radius, 1000, 100000)
		if !ok {
			return
		}
		if locationToken == "" {
			writeError(w, http.StatusBadRequest, "radius_requires_location", "radius_meters requires location_token.")
			return
		}
	}
	minimumRating, ok := marketplaceFloatQuery(w, query.Get("minimum_rating"), "minimum_rating", 0, 0, 5)
	if !ok {
		return
	}
	minimumPrice, ok := optionalMarketplaceMinor(w, query.Get("minimum_price_minor"), "minimum_price_minor")
	if !ok {
		return
	}
	maximumPrice, ok := optionalMarketplaceMinor(w, query.Get("maximum_price_minor"), "maximum_price_minor")
	if !ok {
		return
	}
	if minimumPrice != nil && maximumPrice != nil && *minimumPrice > *maximumPrice {
		writeError(w, http.StatusBadRequest, "invalid_price_range", "minimum_price_minor cannot exceed maximum_price_minor.")
		return
	}
	fulfillmentMode := strings.TrimSpace(query.Get("fulfillment_mode"))
	if fulfillmentMode != "" && fulfillmentMode != "provider_location" && fulfillmentMode != "customer_location" && fulfillmentMode != "virtual" {
		writeError(w, http.StatusBadRequest, "invalid_fulfillment_mode", "fulfillment_mode is invalid.")
		return
	}
	var availableOn *time.Time
	if raw := strings.TrimSpace(query.Get("available_on")); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_available_on", "available_on must use YYYY-MM-DD.")
			return
		}
		availableOn = &parsed
	}
	sortMode := strings.TrimSpace(query.Get("sort"))
	if sortMode == "" {
		sortMode = "recommended"
	}
	if sortMode != "recommended" && sortMode != "rating" && sortMode != "distance" && sortMode != "price" {
		writeError(w, http.StatusBadRequest, "invalid_sort", "sort must be recommended, rating, distance, or price.")
		return
	}

	searchInput := MarketplaceProviderSearchInput{
		Query: searchQuery, LocationToken: locationToken, StateID: stateID, LGAID: lgaID,
		CategoryID: categoryID, RadiusMeters: radius, MinimumRating: minimumRating,
		MinimumPriceMinor: minimumPrice, MaximumPriceMinor: maximumPrice,
		FulfillmentMode: fulfillmentMode, AvailableOn: availableOn, Sort: sortMode,
		Limit: limit,
	}
	cursorFingerprint := marketplaceSearchFingerprint(searchInput)
	cursor, ok := marketplaceSearchCursor(w, query.Get("cursor"), cursorFingerprint)
	if !ok {
		return
	}
	searchInput.Cursor = cursor
	response, err := h.repo.SearchMarketplaceProviders(r.Context(), searchInput)
	if err != nil {
		switch {
		case errors.Is(err, ErrMarketplaceCategory):
			writeError(w, http.StatusNotFound, "marketplace_category_not_found", "The selected marketplace category is unavailable.")
		case errors.Is(err, ErrMarketplaceRegion):
			writeError(w, http.StatusBadRequest, "invalid_region_selection", "Choose a valid Nigerian State and LGA combination.")
		case errors.Is(err, ErrMarketplaceCountry):
			writeError(w, http.StatusBadRequest, "unsupported_country", "Marketplace search currently supports locations in Nigeria.")
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "location_not_found", "The location is unavailable or expired.")
		case errors.Is(err, ErrInvalidKeysetCursor):
			writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid for this sort order.")
		case errors.Is(err, ErrMarketplaceAvailabilityRange):
			writeError(w, http.StatusBadRequest, "available_on_out_of_range", "available_on must be within the next 31 days.")
		default:
			writeError(w, http.StatusInternalServerError, "marketplace_search_failed", "Could not search marketplace providers.")
		}
		return
	}
	for index := range response.Items {
		response.Items[index].PublicBookingURL = h.publicBaseURL + "/p/" + url.PathEscape(response.Items[index].HandleSlug)
	}
	if response.nextCursor != nil {
		encoded, encodeErr := encodeKeysetCursor(cursorFingerprint, response.nextCursor)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "marketplace_search_failed", "Could not paginate marketplace providers.")
			return
		}
		response.NextCursor = encoded
	}
	cacheable := locationToken == "" && availableOn == nil
	if err := writeRevisionedPublicJSON(w, r, response, "marketplace-providers", marketplaceProviderRevisionParts(response.Items), publicDiscoveryCacheControl, cacheable); err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_search_failed", "Could not search marketplace providers.")
	}
}

func (h *Handler) listMarketplaceCategories(w http.ResponseWriter, r *http.Request) {
	items, err := h.repo.ListMarketplaceCategories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_categories_failed", "Could not load marketplace categories.")
		return
	}
	if err := writePublicMarketplaceMetadata(w, r, map[string]any{"items": items}); err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_categories_failed", "Could not load marketplace categories.")
	}
}

func (h *Handler) listMarketplaceRegions(w http.ResponseWriter, r *http.Request) {
	if country := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country_code"))); country != "" && country != "NG" {
		writeError(w, http.StatusBadRequest, "unsupported_country", "Marketplace regions currently support country_code=NG.")
		return
	}
	level := strings.TrimSpace(r.URL.Query().Get("level"))
	if level != "state" && level != "lga" {
		writeError(w, http.StatusBadRequest, "invalid_level", "level must be state or lga.")
		return
	}
	parentID, ok := optionalMarketplaceUUID(w, r.URL.Query().Get("parent_id"), "parent_id")
	if !ok {
		return
	}
	if level == "lga" && parentID == nil {
		writeError(w, http.StatusBadRequest, "parent_required", "parent_id is required for LGAs.")
		return
	}
	items, err := h.repo.ListMarketplaceRegions(r.Context(), level, parentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_regions_failed", "Could not load marketplace regions.")
		return
	}
	if err := writePublicMarketplaceMetadata(w, r, map[string]any{"items": items}); err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_regions_failed", "Could not load marketplace regions.")
	}
}

func (h *Handler) getMarketplaceHome(w http.ResponseWriter, r *http.Request) {
	if country := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country_code"))); country != "" && country != "NG" {
		writeError(w, http.StatusBadRequest, "unsupported_country", "Marketplace discovery currently supports country_code=NG.")
		return
	}
	stateID, ok := optionalMarketplaceUUID(w, r.URL.Query().Get("state_id"), "state_id")
	if !ok {
		return
	}
	lgaID, ok := optionalMarketplaceUUID(w, r.URL.Query().Get("lga_id"), "lga_id")
	if !ok {
		return
	}
	categoryID, ok := optionalMarketplaceUUID(w, r.URL.Query().Get("category_id"), "category_id")
	if !ok {
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("location_token")) != "" && (stateID != nil || lgaID != nil) {
		writeError(w, http.StatusBadRequest, "location_mode_conflict", "Use either current location or a selected region.")
		return
	}
	limit := 12
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 50.")
			return
		}
		limit = parsed
	}
	response, err := h.repo.GetMarketplaceHome(r.Context(), r.URL.Query().Get("location_token"), stateID, lgaID, categoryID, limit)
	if err != nil {
		if errors.Is(err, ErrMarketplaceCategory) {
			writeError(w, http.StatusNotFound, "marketplace_category_not_found", "The selected marketplace category is unavailable.")
			return
		}
		if errors.Is(err, ErrMarketplaceRegion) {
			writeError(w, http.StatusBadRequest, "invalid_region_selection", "Choose a valid Nigerian State and LGA combination.")
			return
		}
		if errors.Is(err, ErrMarketplaceCountry) {
			writeError(w, http.StatusBadRequest, "unsupported_country", "Marketplace discovery currently supports locations in Nigeria.")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "location_not_found", "The location is unavailable or expired.")
			return
		}
		writeError(w, http.StatusInternalServerError, "marketplace_home_failed", "Could not load marketplace providers.")
		return
	}
	for index := range response.Providers {
		response.Providers[index].PublicBookingURL = h.publicBaseURL + "/p/" + url.PathEscape(response.Providers[index].HandleSlug)
	}
	cacheable := strings.TrimSpace(r.URL.Query().Get("location_token")) == ""
	if err := writeRevisionedPublicJSON(w, r, response, "marketplace-home", marketplaceProviderRevisionParts(response.Providers), publicDiscoveryCacheControl, cacheable); err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_home_failed", "Could not load marketplace providers.")
	}
}

func (h *Handler) getMarketplaceProfileSettings(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	settings, err := h.repo.GetMarketplaceProfileSettings(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marketplace_settings_failed", "Could not load marketplace settings.")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h *Handler) updateMarketplaceProfileSettings(w http.ResponseWriter, r *http.Request) {
	client, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}
	input, err := decodeJSON[UpdateMarketplaceProfileSettingsInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	settings, err := h.repo.UpdateMarketplaceProfileSettings(r.Context(), client.ID, input)
	if err != nil {
		if errors.Is(err, ErrMarketplaceNotReady) {
			writeError(w, http.StatusConflict, "marketplace_not_ready", "Complete the listed marketplace requirements before publishing.")
			return
		}
		writeError(w, http.StatusBadRequest, "marketplace_settings_invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func optionalMarketplaceUUID(w http.ResponseWriter, raw, field string) (*uuid.UUID, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_"+field, field+" must be a UUID.")
		return nil, false
	}
	return &parsed, true
}

func marketplaceIntQuery(w http.ResponseWriter, raw, field string, fallback, minimum, maximum int) (int, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minimum || parsed > maximum {
		writeError(w, http.StatusBadRequest, "invalid_"+field, field+" is outside the supported range.")
		return 0, false
	}
	return parsed, true
}

func marketplaceFloatQuery(w http.ResponseWriter, raw, field string, fallback, minimum, maximum float64) (float64, bool) {
	if strings.TrimSpace(raw) == "" {
		return fallback, true
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		writeError(w, http.StatusBadRequest, "invalid_"+field, field+" is outside the supported range.")
		return 0, false
	}
	return parsed, true
}

func optionalMarketplaceMinor(w http.ResponseWriter, raw, field string) (*int64, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		writeError(w, http.StatusBadRequest, "invalid_"+field, field+" must be a non-negative integer in minor units.")
		return nil, false
	}
	return &parsed, true
}

func marketplaceSearchCursor(w http.ResponseWriter, raw, fingerprint string) (*MarketplaceProviderSearchCursor, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	var cursor MarketplaceProviderSearchCursor
	if err := decodeKeysetCursor(raw, fingerprint, &cursor); err != nil || cursor.ProviderID == "" {
		writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is invalid.")
		return nil, false
	}
	return &cursor, true
}
