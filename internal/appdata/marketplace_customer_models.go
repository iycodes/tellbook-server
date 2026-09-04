package appdata

import "time"

type MarketplaceSavedProvider struct {
	MarketplaceProvider
	SavedAt time.Time `json:"saved_at"`
}

type MarketplaceSavedProviderList struct {
	Items      []MarketplaceSavedProvider `json:"items"`
	NextCursor string                     `json:"next_cursor,omitempty"`
}

type MarketplaceNotification struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	EventType      string     `json:"event_type"`
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	ProviderID     string     `json:"provider_id,omitempty"`
	BookingID      string     `json:"booking_id,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	ReadAt         *time.Time `json:"read_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type MarketplaceNotificationList struct {
	Items       []MarketplaceNotification `json:"items"`
	UnreadCount int                       `json:"unread_count"`
	NextCursor  string                    `json:"next_cursor,omitempty"`
}
