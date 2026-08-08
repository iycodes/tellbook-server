package appdata

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	agreementrepo "booking/go-server/internal/agreements/repository"
	aisvc "booking/go-server/internal/ai"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/bookingdomain"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/payments"
	"booking/go-server/internal/storage"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var (
	ErrNotFound             = errors.New("resource not found")
	ErrHandleSlugTaken      = errors.New("public handle is already taken")
	ErrInvalidHandleSlug    = errors.New("public handle is invalid")
	ErrMarketLocked         = errors.New("profile market is locked by existing business data")
	ErrMarketNotConfigured  = errors.New("profile market is not configured")
	ErrLocationInUse        = errors.New("business location is used by a published service")
	ErrQuoteExpired         = errors.New("booking quote has expired")
	ErrSlotUnavailable      = errors.New("selected slot is no longer available")
	ErrLocationNotAllowed   = errors.New("service is unavailable at this location")
	ErrPromotionUnavailable = errors.New("promotion is no longer available")
	ErrLocationRequired     = errors.New("customer location is required")
	ErrDiscountInvalid      = errors.New("discount code is invalid")
	ErrOutsideServiceArea   = errors.New("location is outside the service area")
)

type Handler struct {
	repo           *Repository
	agreements     *agreementrepo.Repository
	auth           *auth.Handler
	destinations   *payments.DestinationService
	storage        *storage.R2Service
	mailer         mailer.Sender
	ai             *aisvc.Client
	checkout       *payments.CheckoutService
	payoutService  *payments.PayoutService
	paymentEvents  *payments.PaymentEventBroker
	activePayments *payments.ActivePaymentReconciler
	publicBaseURL  string
}

func NewHandler(repo *Repository, authHandler *auth.Handler, destinationService *payments.DestinationService, storageService *storage.R2Service, mailerSender mailer.Sender, aiClient *aisvc.Client, checkoutService *payments.CheckoutService, payoutService *payments.PayoutService, paymentEvents *payments.PaymentEventBroker, activePayments *payments.ActivePaymentReconciler, publicBaseURL string) *Handler {
	var agreements *agreementrepo.Repository
	if repo != nil {
		agreements = agreementrepo.New(repo.db)
	}
	return &Handler{
		repo:           repo,
		agreements:     agreements,
		auth:           authHandler,
		destinations:   destinationService,
		storage:        storageService,
		mailer:         mailerSender,
		ai:             aiClient,
		checkout:       checkoutService,
		payoutService:  payoutService,
		paymentEvents:  paymentEvents,
		activePayments: activePayments,
		publicBaseURL:  strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
	}
}

func (h *Handler) Routes(r chi.Router) {
	r.Route("/app", func(r chi.Router) {
		if h.auth != nil {
			r.Use(h.auth.AuthMiddleware())
		}

		r.Get("/dashboard", h.getDashboard)
		r.Get("/stats", h.getStatsOverview)
		r.Get("/revenue", h.getRevenueOverview)
		r.Get("/bookings", h.listBookings)
		r.Get("/bookings/optimization-insight", h.getBookingOptimizationInsight)
		r.Get("/bookings/{bookingID}", h.getBookingDetails)
		r.Get("/inbox/ws", h.streamInboxListWebSocket)
		r.Get("/inbox/conversations", h.listInboxConversations)
		r.Get("/inbox/conversations/{conversationID}", h.getInboxConversationDetails)
		r.Get("/inbox/conversations/{conversationID}/ws", h.streamInboxConversationWebSocket)
		r.Patch("/inbox/conversations/{conversationID}/compose-state", h.updateInboxConversationComposeState)
		r.Post("/inbox/conversations/{conversationID}/suggest-reply", h.suggestInboxConversationReply)
		r.Patch("/inbox/conversations/{conversationID}/controls", h.updateInboxConversationControls)
		r.Post("/inbox/conversations/{conversationID}/messages", h.sendInboxConversationMessage)
		r.Get("/customers", h.listCustomers)
		r.Get("/customers/{customerID}", h.getCustomerDetails)
		r.Get("/notifications", h.getNotifications)
		r.Get("/automation-settings", h.listAutomationSettings)
		r.Patch("/automation-settings/{key}", h.updateAutomationSetting)
		r.Get("/agreement-templates", h.listAgreementTemplateFamilies)
		r.Get("/agreement-template-variables", h.listAgreementTemplateVariables)
		r.Get("/agreement-template-library", h.listAgreementTemplateLibrary)
		r.Post("/agreement-template-library/{familyID}/copy", h.copyAgreementTemplateLibraryFamily)
		r.Post("/agreement-templates/generation-jobs", h.startAgreementTemplateGeneration)
		r.Get("/agreement-templates/generation-jobs/{jobID}", h.getAgreementTemplateGeneration)
		r.Post("/agreement-templates/generation-jobs/{jobID}/retry", h.retryAgreementTemplateGeneration)
		r.Get("/agreement-templates/{familyID}", h.getAgreementTemplateFamily)
		r.Patch("/agreement-templates/{familyID}/draft", h.updateAgreementTemplateDraft)
		r.Post("/agreement-templates/{familyID}/publish", h.publishAgreementTemplateDraft)
		r.Post("/agreement-templates/{familyID}/duplicate", h.duplicateAgreementTemplateFamily)
		r.Post("/agreement-templates/{familyID}/archive", h.archiveAgreementTemplateFamily)
		r.Post("/agreement-templates/{familyID}/restore", h.restoreAgreementTemplateFamily)
		r.Delete("/agreement-templates/{familyID}", h.deleteAgreementTemplateFamily)
		r.Get("/agreement-templates/{familyID}/usage", h.getAgreementTemplateFamilyUsage)
		r.Get("/agreements", h.listManagedAgreements)
		r.Post("/agreements", h.createManagedAgreement)
		r.Get("/agreements/{agreementID}", h.getManagedAgreement)
		r.Post("/agreements/{agreementID}/send", h.sendManagedAgreement)
		r.Post("/agreements/{agreementID}/delivery-link", h.getManagedAgreementDeliveryLink)
		r.Post("/agreements/{agreementID}/resend", h.resendManagedAgreement)
		r.Post("/agreements/{agreementID}/cancel", h.cancelManagedAgreement)
		r.Post("/agreements/{agreementID}/expire", h.expireManagedAgreement)
		r.Post("/agreements/{agreementID}/retry-processing", h.retryManagedAgreementProcessing)
		r.Get("/agreements/{agreementID}/pdf", h.getManagedAgreementPDF)
		r.Get("/agreements/{agreementID}/signature", h.getManagedAgreementSignature)
		r.Get("/promotions", h.listPromotions)
		r.Post("/promotions", h.createPromotion)
		r.Get("/promotions/{promotionID}", h.getPromotionDetails)
		r.Put("/promotions/{promotionID}", h.updatePromotion)
		r.Patch("/promotions/{promotionID}/status", h.updatePromotionStatus)
		r.Delete("/promotions/{promotionID}", h.deletePromotion)
		r.Get("/promotions/{promotionID}/redemptions", h.listPromotionRedemptions)
		r.Get("/service-sections", h.listServiceSections)
		r.Post("/service-sections", h.createServiceSection)
		r.Put("/service-sections/reorder", h.reorderServiceSections)
		r.Get("/service-sections/{sectionID}", h.getServiceSectionDetails)
		r.Put("/service-sections/{sectionID}", h.updateServiceSection)
		r.Delete("/service-sections/{sectionID}", h.deleteServiceSection)
		r.Get("/services", h.listManagedServices)
		r.Post("/services", h.createManagedService)
		r.Post("/service-wizard-drafts", h.createServiceWizardDraft)
		r.Get("/service-wizard-drafts/{draftID}", h.getServiceWizardDraft)
		r.Patch("/service-wizard-drafts/{draftID}", h.updateServiceWizardDraft)
		r.Delete("/service-wizard-drafts/{draftID}", h.deleteServiceWizardDraft)
		r.Get("/services/{serviceID}", h.getManagedServiceDetails)
		r.Put("/services/{serviceID}", h.updateManagedService)
		r.Patch("/services/{serviceID}/visibility", h.updateManagedServiceVisibility)
		r.Post("/services/{serviceID}/duplicate", h.duplicateManagedService)
		r.Delete("/services/{serviceID}", h.deleteManagedService)
		r.Get("/business-locations", h.listBusinessLocations)
		r.Post("/business-locations", h.createBusinessLocation)
		r.Put("/business-locations/{locationID}", h.updateBusinessLocation)
		r.Delete("/business-locations/{locationID}", h.archiveBusinessLocation)
		r.Put("/service-sections/{sectionID}/services/reorder", h.reorderSectionServices)
		r.Put("/services/uncategorized/reorder", h.reorderUncategorizedServices)
		r.Get("/profile/handle-availability", h.checkHandleSlugAvailability)
		r.Get("/profile", h.getClientProfile)
		r.Put("/profile", h.updateClientProfile)
		r.Patch("/profile/market", h.updateClientMarket)
		r.Post("/ai/services/generate-description", h.generateServiceDescription)
		r.Post("/ai/services/generate-instructions", h.generatePrepAftercareInstructions)
		r.Post("/ai/sections/generate-description", h.generateSectionDescription)
		r.Post("/ai/profiles/generate-public-page-content", h.generatePublicPageContent)
		r.Get("/payout-setup", h.getPayoutSetup)
		r.Get("/payout-destination-options", h.getPayoutDestinationOptions)
		r.Get("/payout-destinations", h.listPayoutDestinations)
		r.Post("/payout-destinations/resolve", h.resolvePayoutDestination)
		r.Post("/payout-destinations", h.savePayoutDestination)
		r.Delete("/payout-destinations/{destinationID}", h.revokePayoutDestination)
		r.Get("/payouts", h.getPayoutOverview)
		r.Post("/payouts", h.createPayout)
		r.Get("/payouts/{payoutID}", h.getPayout)
		r.Post("/uploads/image", h.uploadImage)
		r.Post("/uploads/document", h.uploadDocument)
	})

	r.Route("/public", func(r chi.Router) {
		r.Post("/locations/resolve", h.resolvePublicLocation)
		r.Get("/clients/{slug}", h.getPublicProfile)
		r.Get("/clients/{slug}/services", h.listPublicServices)
		r.Get("/clients/{slug}/availability", h.getPublicAvailability)
		r.Post("/clients/{slug}/booking-quotes", h.createPublicBookingQuote)
		r.Post("/clients/{slug}/bookings", h.createPublicBooking)
		r.Get("/agreements/{token}", h.getPublicAgreement)
		r.Get("/agreements/{token}/pdf", h.getPublicAgreementPDF)
		r.Post("/agreements/{token}/accept", h.acceptPublicAgreement)
		r.Get("/bookings/{bookingToken}", h.getPublicBookingSummary)
		r.Get("/bookings/{bookingToken}/calendar", h.getPublicBookingCalendar)
		r.Get("/bookings/{bookingToken}/checkout-state", h.getPublicBookingCheckoutState)
		r.Get("/payments/{paymentToken}", h.getPublicPaymentStatus)
		r.Get("/payments/{paymentToken}/bank-transfer", h.getPublicBankTransfer)
		r.Get("/payments/{paymentToken}/events", h.streamPublicPaymentStatus)
		r.Post("/payments/{paymentToken}/verification", h.requestPublicPaymentVerification)
		r.Post("/bookings/{bookingToken}/agreement", h.preparePublicBookingAgreement)
		r.Post("/bookings/{bookingToken}/checkout", h.createPublicBookingCheckout)
	})
}

func (h *Handler) getDashboard(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	response, err := h.repo.GetDashboard(r.Context(), authedClient.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "dashboard_not_found", "Client dashboard was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "dashboard_failed", "Could not load dashboard.")
		return
	}

	response = h.signDashboardResponse(r.Context(), response)
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) listBookings(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListBookings(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "bookings_failed", "Could not load bookings.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getBookingDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	bookingID, err := uuidFromURLParam("bookingID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_booking_id", "Booking ID is invalid.")
		return
	}

	details, err := h.repo.GetBookingDetails(r.Context(), authedClient.ID, bookingID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "booking_details_failed", "Could not load booking details.")
		return
	}

	details = h.signBookingDetailsResponse(r.Context(), details)
	writeJSON(w, http.StatusOK, details)
}

func (h *Handler) getBookingOptimizationInsight(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	insight, err := h.repo.GetBookingOptimizationInsight(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_optimization_failed", "Could not load booking optimization insight.")
		return
	}

	writeJSON(w, http.StatusOK, insight)
}

func (h *Handler) listCustomers(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListCustomers(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "customers_failed", "Could not load customers.")
		return
	}

	items = h.signCustomerItems(r.Context(), items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getCustomerDetails(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	customerID, err := uuidFromURLParam("customerID", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_customer_id", "Customer ID is invalid.")
		return
	}

	details, err := h.repo.GetCustomerDetails(r.Context(), authedClient.ID, customerID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "customer_not_found", "Customer was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "customer_details_failed", "Could not load customer details.")
		return
	}

	details = h.signCustomerDetailsResponse(r.Context(), details)
	writeJSON(w, http.StatusOK, details)
}

func (h *Handler) getNotifications(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	response, err := h.repo.GetNotifications(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "notifications_failed", "Could not load notifications.")
		return
	}

	response = h.signNotificationsResponse(r.Context(), response)
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) listAutomationSettings(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	items, err := h.repo.ListAutomationSettings(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "automation_settings_failed", "Could not load automation settings.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) updateAutomationSetting(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	key := chi.URLParam(r, "key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "Automation setting key is required.")
		return
	}

	input, err := decodeJSON[UpdateAutomationSettingInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.repo.UpdateAutomationSetting(r.Context(), authedClient.ID, key, input.Enabled); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "automation_setting_not_found", "Automation setting was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "automation_setting_failed", "Could not update automation setting.")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getClientProfile(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	profile, err := h.repo.GetClientProfile(r.Context(), authedClient.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "profile_not_found", "Client profile was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "profile_failed", "Could not load client profile.")
		return
	}

	profile = h.signClientProfileResponse(r.Context(), profile)
	writeJSON(w, http.StatusOK, profile)
}

func (h *Handler) updateClientProfile(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[UpdateClientProfileInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := h.repo.UpdateClientProfile(r.Context(), authedClient.ID, input); err != nil {
		writeProfileUpdateError(w, err)
		return
	}

	profile, err := h.repo.GetClientProfile(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile_failed", "Could not load updated client profile.")
		return
	}

	profile = h.signClientProfileResponse(r.Context(), profile)
	writeJSON(w, http.StatusOK, profile)
}

func (h *Handler) updateClientMarket(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	input, err := decodeJSON[UpdateClientMarketInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := markets.DefaultCatalog().ValidateConfiguration(
		input.CountryCode,
		input.CurrencyCode,
		input.Timezone,
		input.Locale,
	); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_market", err.Error())
		return
	}

	if err := h.repo.UpdateClientMarket(r.Context(), authedClient.ID, input); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "profile_not_found", "Client profile was not found.")
		case errors.Is(err, ErrMarketLocked):
			writeError(w, http.StatusConflict, "market_locked", "Country or currency cannot be changed after financial setup or booking activity has started.")
		default:
			writeError(w, http.StatusInternalServerError, "market_update_failed", "Could not update the business market.")
		}
		return
	}

	profile, err := h.repo.GetClientProfile(r.Context(), authedClient.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "profile_failed", "Could not load updated client profile.")
		return
	}

	profile = h.signClientProfileResponse(r.Context(), profile)
	writeJSON(w, http.StatusOK, profile)
}

func writeProfileUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "profile_not_found", "Client profile was not found.")
	case errors.Is(err, ErrInvalidHandleSlug):
		writeError(w, http.StatusBadRequest, "invalid_handle_slug", "Public handle must contain letters, numbers, or hyphens and be 64 characters or fewer.")
	case errors.Is(err, ErrHandleSlugTaken):
		writeError(w, http.StatusConflict, "handle_slug_taken", "That public handle is already taken.")
	default:
		writeError(w, http.StatusInternalServerError, "profile_update_failed", "Could not update client profile.")
	}
}

func (h *Handler) checkHandleSlugAvailability(w http.ResponseWriter, r *http.Request) {
	authedClient, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "You must be signed in.")
		return
	}

	handleSlug, available, err := h.repo.CheckHandleSlugAvailability(
		r.Context(),
		authedClient.ID,
		r.URL.Query().Get("handle_slug"),
	)
	if err != nil {
		if errors.Is(err, ErrInvalidHandleSlug) {
			writeError(w, http.StatusBadRequest, "invalid_handle_slug", "Public handle must contain letters, numbers, or hyphens and be 64 characters or fewer.")
			return
		}
		writeError(w, http.StatusInternalServerError, "handle_availability_failed", "Could not check public handle availability.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"handle_slug": handleSlug,
		"available":   available,
	})
}

func (h *Handler) getPublicProfile(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_slug", "Client slug is required.")
		return
	}

	response, err := h.repo.GetPublicProfileBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "public_profile_not_found", "Client profile was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "public_profile_failed", "Could not load client profile.")
		return
	}

	response = h.signPublicProfileResponse(r.Context(), response)
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) listPublicServices(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_slug", "Client slug is required.")
		return
	}

	items, err := h.repo.ListPublicServicesBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "services_not_found", "Client services were not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "services_failed", "Could not load services.")
		return
	}

	items = h.signPublicServices(r.Context(), items)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getPublicAvailability(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_slug", "Client slug is required.")
		return
	}

	serviceID, err := uuidFromQueryParam("service_id", r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service_id", "Service ID is invalid.")
		return
	}

	dateValue := r.URL.Query().Get("date")
	if dateValue == "" {
		writeError(w, http.StatusBadRequest, "missing_date", "Date is required.")
		return
	}

	selectedDate, err := time.Parse("2006-01-02", dateValue)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_date", "Date must use YYYY-MM-DD.")
		return
	}

	response, err := h.repo.GetPublicAvailability(r.Context(), slug, serviceID, selectedDate)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "availability_not_found", "Availability was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "availability_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) createPublicBooking(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	if slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_slug", "Client slug is required.")
		return
	}

	input, err := decodeJSON[CreatePublicBookingInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	response, err := h.repo.CreatePublicBooking(r.Context(), slug, input)
	if err != nil {
		if errors.Is(err, ErrMarketNotConfigured) {
			writeError(w, http.StatusConflict, "market_not_configured", "This business is not ready to accept bookings.")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_target_not_found", "Booking target was not found.")
			return
		}
		if errors.Is(err, ErrQuoteExpired) {
			writeError(w, http.StatusConflict, "quote_expired", "Refresh the booking price before continuing.")
			return
		}
		if errors.Is(err, ErrSlotUnavailable) {
			writeError(w, http.StatusConflict, "slot_unavailable", "This time is no longer available.")
			return
		}
		writeError(w, http.StatusBadRequest, "booking_create_failed", err.Error())
		return
	}

	response = h.signPublicBookingSummaryResponse(r.Context(), response)
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) createPublicBookingQuote(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if slug == "" {
		writeError(w, http.StatusBadRequest, "invalid_slug", "Client slug is required.")
		return
	}
	input, err := decodeJSON[CreatePublicBookingQuoteInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	response, err := h.repo.CreatePublicBookingQuote(r.Context(), slug, input)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			writeError(w, http.StatusNotFound, "service_not_found", "Service was not found.")
		case errors.Is(err, ErrMarketNotConfigured):
			writeError(w, http.StatusConflict, "market_not_configured", "This business is not ready to accept bookings.")
		case errors.Is(err, ErrSlotUnavailable):
			writeError(w, http.StatusConflict, "slot_unavailable", "This time is no longer available.")
		case errors.Is(err, bookingdomain.ErrMinimumNoticeNotMet):
			writeError(w, http.StatusConflict, "minimum_notice_not_met", "This appointment is too soon to book.")
		case errors.Is(err, ErrLocationRequired):
			writeError(w, http.StatusBadRequest, "location_required", "Enter the service location before continuing.")
		case errors.Is(err, ErrOutsideServiceArea):
			writeError(w, http.StatusUnprocessableEntity, "outside_service_area", "This location is outside the service area.")
		case errors.Is(err, ErrLocationNotAllowed):
			writeError(w, http.StatusUnprocessableEntity, "location_not_allowed", "This location could not be validated for the service.")
		case errors.Is(err, ErrDiscountInvalid):
			writeError(w, http.StatusBadRequest, "discount_invalid", err.Error())
		case errors.Is(err, ErrPromotionUnavailable):
			writeError(w, http.StatusConflict, "quote_unavailable", "The selected discount was just claimed. Refresh the price and try again.")
		default:
			writeError(w, http.StatusBadRequest, "quote_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) requireClientMarket(w http.ResponseWriter, r *http.Request, clientID uuid.UUID) bool {
	err := h.repo.EnsureClientMarketConfigured(r.Context(), clientID)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrMarketNotConfigured) {
		writeError(w, http.StatusConflict, "market_not_configured", "Complete business country and currency setup first.")
		return false
	}

	writeError(w, http.StatusInternalServerError, "market_check_failed", "Could not verify business market setup.")
	return false
}

func (h *Handler) getPublicBookingSummary(w http.ResponseWriter, r *http.Request) {
	response, err := h.repo.GetPublicBookingSummary(r.Context(), chi.URLParam(r, "bookingToken"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "booking_summary_failed", "Could not load booking summary.")
		return
	}

	response = h.signPublicBookingSummaryResponse(r.Context(), response)
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPublicBookingCalendar(w http.ResponseWriter, r *http.Request) {
	response, err := h.repo.GetPublicBookingSummary(r.Context(), chi.URLParam(r, "bookingToken"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "booking_calendar_failed", "Could not create the calendar event.")
		return
	}
	if !publicBookingCalendarAvailable(response) {
		writeError(w, http.StatusConflict, "booking_not_confirmed", "The booking must be confirmed before it can be added to a calendar.")
		return
	}

	calendar, err := buildPublicBookingCalendar(response, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "booking_calendar_failed", "Could not create the calendar event.")
		return
	}

	setPublicFinancialHeaders(w)
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", publicBookingCalendarDisposition(response.ServiceTitle))
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, calendar)
}

func (h *Handler) createPublicBookingCheckout(w http.ResponseWriter, r *http.Request) {
	setPublicFinancialHeaders(w)
	bookingToken := chi.URLParam(r, "bookingToken")
	input, err := decodeJSON[CreatePublicBookingCheckoutInput](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if h.checkout == nil {
		writeError(w, http.StatusServiceUnavailable, "booking_checkout_unavailable", "Payments are currently unavailable.")
		return
	}
	paymentContext, err := h.repo.getPublicBookingPaymentContext(r.Context(), bookingToken)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "booking_checkout_failed", err.Error())
		return
	}
	returnURLTemplate := h.publicBaseURL + "/p/" + url.PathEscape(paymentContext.HandleSlug) + "/booking/payment/return?payment={payment_token}"
	attempt, err := h.checkout.Initialize(r.Context(), payments.BookingCheckoutInput{
		BookingID: paymentContext.BookingID, ClientID: paymentContext.ClientID,
		CustomerID: paymentContext.CustomerID, BookingToken: paymentContext.BookingToken,
		CountryCode: paymentContext.CountryCode, CurrencyCode: paymentContext.CurrencyCode,
		CustomerName: paymentContext.CustomerName, CustomerEmail: paymentContext.CustomerEmail,
		CustomerPhone: paymentContext.CustomerPhone, ServiceTitle: paymentContext.ServiceTitle,
		ReturnURLTemplate: returnURLTemplate, IdempotencyKey: input.IdempotencyKey, Method: input.Method,
	})
	if err != nil {
		var initializationErr *payments.CheckoutInitializationError
		if errors.As(err, &initializationErr) {
			response := buildPublicCheckoutResponse(paymentContext.BookingToken, attempt)
			h.trackActiveCheckout(attempt.Payment)
			status := http.StatusOK
			if initializationErr.Ambiguous {
				status = http.StatusAccepted
			}
			setPublicFinancialHeaders(w)
			writeJSON(w, status, response)
			return
		}
		if errors.Is(err, payments.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", "This checkout request key was already used with different details.")
			return
		}
		var methodConflict *payments.PaymentMethodConflictError
		if errors.As(err, &methodConflict) {
			response := buildPublicCheckoutResponse(paymentContext.BookingToken, attempt)
			setPublicFinancialHeaders(w)
			writeJSON(w, http.StatusConflict, response)
			return
		}
		if errors.Is(err, payments.ErrPaymentObligationSatisfied) {
			writeError(w, http.StatusConflict, "payment_already_complete", "This booking has no outstanding payment balance.")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusBadRequest, "booking_checkout_failed", err.Error())
		return
	}

	setPublicFinancialHeaders(w)
	status := http.StatusCreated
	if attempt.Resumed {
		status = http.StatusOK
	}
	h.trackActiveCheckout(attempt.Payment)
	writeJSON(w, status, buildPublicCheckoutResponse(paymentContext.BookingToken, attempt))
}

func (h *Handler) trackActiveCheckout(payment payments.FinancialPayment) {
	if h.activePayments != nil && !isTerminalPaymentStatus(payment.Status) && strings.TrimSpace(payment.PublicToken) != "" {
		h.activePayments.TrackCheckout(payment.PublicToken)
	}
}

func buildPublicCheckoutResponse(bookingToken string, attempt payments.CheckoutAttempt) PublicBookingCheckoutResponse {
	response := PublicBookingCheckoutResponse{
		BookingToken: bookingToken, Provider: attempt.Payment.Provider, Method: attempt.Payment.Method,
		ProviderChannel:  attempt.Payment.ProviderChannel,
		RecoveryAction:   publicPaymentRecoveryAction(attempt.Payment),
		PaymentReference: attempt.Payment.Reference, PaymentToken: attempt.Payment.PublicToken,
		Status: string(attempt.Payment.Status), AmountMinor: attempt.Payment.AmountMinor,
		CurrencyCode: attempt.Payment.CurrencyCode, RedirectURL: attempt.Session.CheckoutURL,
		Flow: attempt.Session.Flow, PublicKey: attempt.Session.PublicKey, Instructions: attempt.Session.Instructions,
	}
	if attempt.Initializing {
		response.RecoveryAction = "wait_for_initialization"
	}
	if attempt.Session.BankTransfer != nil {
		response.BankTransfer = &PublicBankTransferInstructions{
			AccountName: attempt.Session.BankTransfer.AccountName, AccountNumber: attempt.Session.BankTransfer.AccountNumber,
			BankName: attempt.Session.BankTransfer.BankName, TransferReference: attempt.Session.BankTransfer.TransferReference,
		}
	}
	if attempt.Session.ExpiresAt != nil {
		response.ExpiresAt = attempt.Session.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return response
}

func (h *Handler) getPublicBookingCheckoutState(w http.ResponseWriter, r *http.Request) {
	setPublicFinancialHeaders(w)
	if h.checkout == nil {
		writeError(w, http.StatusServiceUnavailable, "booking_checkout_unavailable", "Payments are currently unavailable.")
		return
	}
	bookingToken := chi.URLParam(r, "bookingToken")
	paymentContext, err := h.repo.getPublicBookingPaymentContext(r.Context(), bookingToken)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "booking_not_found", "Booking was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "checkout_state_failed", "Could not load payment options.")
		return
	}
	state, err := h.checkout.GetCheckoutState(
		r.Context(), paymentContext.BookingID, paymentContext.CountryCode, paymentContext.CurrencyCode,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "checkout_state_failed", "Could not load payment options.")
		return
	}
	response := PublicBookingCheckoutStateResponse{
		BookingToken: bookingToken, ObligationSatisfied: state.ObligationSatisfied,
		AmountMinor: state.AmountMinor, CurrencyCode: state.CurrencyCode,
		AvailableMethods: state.AvailableMethods,
	}
	if state.ActivePayment != nil {
		active := buildPublicPaymentStatusResponse(*state.ActivePayment)
		response.ActivePayment = &active
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPublicBankTransfer(w http.ResponseWriter, r *http.Request) {
	setPublicFinancialHeaders(w)
	if h.checkout == nil {
		writeError(w, http.StatusServiceUnavailable, "payment_status_unavailable", "Payment status is currently unavailable.")
		return
	}
	payment, err := h.checkout.GetPaymentByPublicToken(r.Context(), chi.URLParam(r, "paymentToken"))
	if err != nil {
		if errors.Is(err, payments.ErrLedgerRecordNotFound) {
			writeError(w, http.StatusNotFound, "payment_not_found", "Payment was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "bank_transfer_failed", "Could not load bank transfer details.")
		return
	}
	if payment.Method != payments.PaymentMethodBankTransfer {
		writeError(w, http.StatusConflict, "payment_method_mismatch", "This payment is not a bank transfer.")
		return
	}
	if isTerminalPaymentStatus(payment.Status) && payment.Status != payments.PaymentStatusPaid {
		writeError(w, http.StatusGone, "bank_transfer_closed", "This bank-transfer account is no longer available.")
		return
	}
	stored, err := h.checkout.GetCheckoutRecord(r.Context(), payment.ID)
	if err != nil || stored.State != payments.CheckoutInitializationReady || stored.Record.Session == nil ||
		stored.Record.Session.BankTransfer == nil {
		writeError(w, http.StatusConflict, "bank_transfer_initializing", "Bank transfer details are still being prepared.")
		return
	}
	instructions := stored.Record.Session.BankTransfer
	response := PublicBankTransferResponse{
		BookingToken: paymentBookingToken(payment.PriceSnapshot), Payment: buildPublicPaymentStatusResponse(payment),
		Instructions: PublicBankTransferInstructions{
			AccountName: instructions.AccountName, AccountNumber: instructions.AccountNumber,
			BankName: instructions.BankName, TransferReference: instructions.TransferReference,
		},
	}
	if stored.Record.Session.ExpiresAt != nil {
		response.ExpiresAt = stored.Record.Session.ExpiresAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) getPublicPaymentStatus(w http.ResponseWriter, r *http.Request) {
	setPublicFinancialHeaders(w)
	if h.checkout == nil {
		writeError(w, http.StatusServiceUnavailable, "payment_status_unavailable", "Payment status is currently unavailable.")
		return
	}
	payment, err := h.checkout.GetPaymentByPublicToken(r.Context(), chi.URLParam(r, "paymentToken"))
	if err != nil {
		if errors.Is(err, payments.ErrLedgerRecordNotFound) {
			writeError(w, http.StatusNotFound, "payment_not_found", "Payment was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "payment_status_failed", "Could not load payment status.")
		return
	}
	setPublicFinancialHeaders(w)
	writeJSON(w, http.StatusOK, buildPublicPaymentStatusResponse(payment))
}

func (h *Handler) requestPublicPaymentVerification(w http.ResponseWriter, r *http.Request) {
	setPublicFinancialHeaders(w)
	if h.checkout == nil || h.activePayments == nil {
		writeError(w, http.StatusServiceUnavailable, "payment_verification_unavailable", "Payment verification is currently unavailable.")
		return
	}
	token := chi.URLParam(r, "paymentToken")
	payment, err := h.checkout.GetPaymentByPublicToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, payments.ErrLedgerRecordNotFound) {
			writeError(w, http.StatusNotFound, "payment_not_found", "Payment was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "payment_verification_failed", "Could not request payment verification.")
		return
	}
	if !isTerminalPaymentStatus(payment.Status) && !recentlyReconciled(payment.LastReconciledAt, 2*time.Second) {
		h.activePayments.Nudge(token)
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func recentlyReconciled(reconciledAt *time.Time, within time.Duration) bool {
	if reconciledAt == nil {
		return false
	}
	elapsed := time.Since(*reconciledAt)
	return elapsed >= 0 && elapsed < within
}

func (h *Handler) streamPublicPaymentStatus(w http.ResponseWriter, r *http.Request) {
	if h.checkout == nil || h.paymentEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "payment_status_unavailable", "Payment status is currently unavailable.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "Streaming is not supported.")
		return
	}
	if err := disableResponseWriteDeadline(w); err != nil {
		writeError(w, http.StatusInternalServerError, "stream_deadline_failed", "Payment status streaming is not available.")
		return
	}

	token := chi.URLParam(r, "paymentToken")
	updates, unsubscribe := h.paymentEvents.Subscribe(token)
	defer unsubscribe()
	initial, err := h.checkout.GetPaymentByPublicToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, payments.ErrLedgerRecordNotFound) {
			writeError(w, http.StatusNotFound, "payment_not_found", "Payment was not found.")
			return
		}
		writeError(w, http.StatusInternalServerError, "payment_status_failed", "Could not load payment status.")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var lastVersion int64 = -1
	sendPayment := func(payment payments.FinancialPayment) (payments.PaymentStatus, error) {
		if payment.Version == lastVersion {
			return payment.Status, nil
		}
		payload, err := json.Marshal(buildPublicPaymentStatusResponse(payment))
		if err != nil {
			return "", err
		}
		if _, err := w.Write([]byte("event: payment-status\ndata: ")); err != nil {
			return "", err
		}
		if _, err := w.Write(payload); err != nil {
			return "", err
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return "", err
		}
		flusher.Flush()
		lastVersion = payment.Version
		return payment.Status, nil
	}
	sendCurrent := func() (payments.PaymentStatus, error) {
		payment, err := h.checkout.GetPaymentByPublicToken(r.Context(), token)
		if err != nil {
			return "", err
		}
		return sendPayment(payment)
	}

	status, err := sendPayment(initial)
	if err != nil {
		return
	}
	if isTerminalPaymentStatus(status) {
		return
	}
	stopActiveReconciliation := func() {}
	if h.activePayments != nil {
		stopActiveReconciliation = h.activePayments.Watch(token)
	}
	defer stopActiveReconciliation()
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
			status, err = sendCurrent()
			if err != nil || isTerminalPaymentStatus(status) {
				return
			}
		case <-heartbeatTicker.C:
			status, err = sendCurrent()
			if err != nil || isTerminalPaymentStatus(status) {
				return
			}
			if _, err := w.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func disableResponseWriteDeadline(w http.ResponseWriter) error {
	err := http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func buildPublicPaymentStatusResponse(payment payments.FinancialPayment) PublicPaymentStatusResponse {
	response := PublicPaymentStatusResponse{
		PaymentToken: payment.PublicToken, PaymentReference: payment.Reference,
		Status: payment.Status, RecoveryAction: publicPaymentRecoveryAction(payment),
		BookingToken: paymentBookingToken(payment.PriceSnapshot),
		AmountMinor:  payment.AmountMinor, CurrencyCode: payment.CurrencyCode,
		Method: payment.Method, ProviderChannel: payment.ProviderChannel,
		FailureCode:     payment.FailureCode,
		FailureMessage:  payment.FailureMessage,
		Version:         payment.Version,
		CheckoutReady:   strings.EqualFold(strings.TrimSpace(payment.ProviderStatus), "initialized"),
		StatusUpdatedAt: payment.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	return response
}

func publicPaymentRecoveryAction(payment payments.FinancialPayment) string {
	const providerRegistrationGrace = 2 * time.Minute
	if strings.EqualFold(strings.TrimSpace(payment.ProviderStatus), "initialization_unknown") {
		return "wait_for_initialization"
	}
	switch payment.Status {
	case payments.PaymentStatusFailed, payments.PaymentStatusExpired, payments.PaymentStatusCancelled:
		return "retry_checkout"
	case payments.PaymentStatusCreated:
		return "resume_checkout"
	case payments.PaymentStatusPending, payments.PaymentStatusRequiresAction:
		if strings.EqualFold(strings.TrimSpace(payment.ProviderStatus), "not_found") {
			if !payment.CreatedAt.IsZero() && time.Since(payment.CreatedAt) < providerRegistrationGrace {
				return "wait_for_confirmation"
			}
			return "resume_checkout"
		}
		return "wait_for_confirmation"
	default:
		return "none"
	}
}

func paymentBookingToken(snapshot json.RawMessage) string {
	var values map[string]string
	if json.Unmarshal(snapshot, &values) != nil {
		return ""
	}
	return values["booking_token"]
}

func isTerminalPaymentStatus(status payments.PaymentStatus) bool {
	switch status {
	case payments.PaymentStatusCreated, payments.PaymentStatusPending, payments.PaymentStatusRequiresAction:
		return false
	default:
		return true
	}
}

func setPublicFinancialHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

func decodeJSON[T any](r *http.Request) (T, error) {
	var payload T

	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&payload); err != nil {
		return payload, err
	}

	if err := decoder.Decode(new(struct{})); err != io.EOF {
		return payload, errors.New("request body must contain a single JSON object")
	}

	return payload, nil
}

func uuidFromURLParam(name string, r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, name))
}

func uuidFromQueryParam(name string, r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.URL.Query().Get(name))
}
