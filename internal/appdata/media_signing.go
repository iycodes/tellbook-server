package appdata

import (
	"context"
	"strings"
	"time"
)

const signedMediaURLTTL = time.Hour

func (h *Handler) signedMediaURL(ctx context.Context, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || h.storage == nil {
		return trimmed
	}

	signed, err := h.storage.ResolveBrowserURL(ctx, trimmed, signedMediaURLTTL)
	if err != nil {
		return trimmed
	}

	return signed
}

func (h *Handler) signDashboardResponse(ctx context.Context, response DashboardResponse) DashboardResponse {
	response.Profile.AvatarURL = h.signedMediaURL(ctx, response.Profile.AvatarURL)
	return response
}

func (h *Handler) signBookingDetailsResponse(ctx context.Context, response BookingDetailsResponse) BookingDetailsResponse {
	response.ImageURL = h.signedMediaURL(ctx, response.ImageURL)
	return response
}

func (h *Handler) signCustomerItems(ctx context.Context, items []CustomerItem) []CustomerItem {
	for index := range items {
		items[index].AvatarURL = h.signedMediaURL(ctx, items[index].AvatarURL)
	}
	return items
}

func (h *Handler) signCustomerDetailsResponse(ctx context.Context, response CustomerDetailsResponse) CustomerDetailsResponse {
	response.ImageURL = h.signedMediaURL(ctx, response.ImageURL)
	return response
}

func (h *Handler) signNotificationsResponse(ctx context.Context, response NotificationsResponse) NotificationsResponse {
	for index := range response.ActionRequired {
		response.ActionRequired[index].ImageURL = h.signedMediaURL(ctx, response.ActionRequired[index].ImageURL)
	}
	for index := range response.Today {
		response.Today[index].ImageURL = h.signedMediaURL(ctx, response.Today[index].ImageURL)
	}
	return response
}

func (h *Handler) signClientProfileResponse(ctx context.Context, response ClientProfileResponse) ClientProfileResponse {
	response.AvatarURL = h.signedMediaURL(ctx, response.AvatarURL)
	response.HeroImageURL = h.signedMediaURL(ctx, response.HeroImageURL)
	return response
}

func (h *Handler) signPublicProfileResponse(ctx context.Context, response PublicProfileResponse) PublicProfileResponse {
	response.Profile.AvatarURL = h.signedMediaURL(ctx, response.Profile.AvatarURL)
	response.Profile.HeroImageURL = h.signedMediaURL(ctx, response.Profile.HeroImageURL)

	for index := range response.FeaturedServices {
		response.FeaturedServices[index].ImageURL = h.signedMediaURL(ctx, response.FeaturedServices[index].ImageURL)
	}

	for index := range response.Portfolio {
		response.Portfolio[index].ImageURL = h.signedMediaURL(ctx, response.Portfolio[index].ImageURL)
	}

	return response
}

func (h *Handler) signPublicServices(ctx context.Context, items []PublicServiceItem) []PublicServiceItem {
	for index := range items {
		items[index].ImageURL = h.signedMediaURL(ctx, items[index].ImageURL)
	}
	return items
}

func (h *Handler) signPublicBookingSummaryResponse(ctx context.Context, response PublicBookingSummaryResponse) PublicBookingSummaryResponse {
	response.ServiceImageURL = h.signedMediaURL(ctx, response.ServiceImageURL)
	return response
}

func (h *Handler) signServiceSectionItems(ctx context.Context, items []ServiceSectionItem) []ServiceSectionItem {
	for index := range items {
		items[index].CoverImageURL = h.signedMediaURL(ctx, items[index].CoverImageURL)
	}
	return items
}

func (h *Handler) signManagedServiceItems(ctx context.Context, items []ManagedServiceItem) []ManagedServiceItem {
	for index := range items {
		items[index].ImageURL = h.signedMediaURL(ctx, items[index].ImageURL)
	}
	return items
}

func (h *Handler) signServiceSectionDetailsResponse(ctx context.Context, response ServiceSectionDetailsResponse) ServiceSectionDetailsResponse {
	response.Section.CoverImageURL = h.signedMediaURL(ctx, response.Section.CoverImageURL)
	response.Services = h.signManagedServiceItems(ctx, response.Services)
	return response
}
