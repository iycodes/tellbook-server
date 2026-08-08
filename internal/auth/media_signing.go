package auth

import "context"

func (h *Handler) signUserMedia(ctx context.Context, user User) User {
	if h == nil || h.service == nil || h.service.storage == nil || user.CoverImageURL == "" {
		return user
	}

	signed, err := h.service.storage.ResolveBrowserURL(ctx, user.CoverImageURL, 0)
	if err != nil {
		return user
	}

	user.CoverImageURL = signed
	return user
}
