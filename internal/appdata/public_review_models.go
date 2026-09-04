package appdata

import (
	"time"

	"github.com/google/uuid"
)

type PublicReviewItem struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"service_id,omitempty"`
	ServiceTitle    string    `json:"service_title,omitempty"`
	AuthorName      string    `json:"author_name"`
	Rating          int       `json:"rating"`
	ReviewText      string    `json:"review_text"`
	ImageURL        string    `json:"image_url,omitempty"`
	VerifiedBooking bool      `json:"verified_booking"`
	CreatedAt       time.Time `json:"created_at"`
}

type PublicReviewSummary struct {
	Rating    float64     `json:"rating"`
	Count     int         `json:"count"`
	Breakdown map[int]int `json:"breakdown"`
}

type PublicReviewsResponse struct {
	Items      []PublicReviewItem   `json:"items"`
	TotalCount *int                 `json:"total_count,omitempty"`
	NextCursor string               `json:"next_cursor,omitempty"`
	Summary    *PublicReviewSummary `json:"summary,omitempty"`
}

type PublicReviewsInput struct {
	ServiceID *uuid.UUID
	Rating    int
	HasPhotos *bool
	Sort      string
	Cursor    string
	Limit     int
}
