package appdata

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type publicReviewListCursor struct {
	Rating    int       `json:"rating,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func (r *Repository) ListPublicReviewsBySlug(ctx context.Context, slug string, input PublicReviewsInput) (PublicReviewsResponse, error) {
	trimmedSlug := strings.TrimSpace(slug)
	if trimmedSlug == "" {
		return PublicReviewsResponse{}, ErrNotFound
	}

	var hasPhotos any
	hasPhotosFingerprint := ""
	if input.HasPhotos != nil {
		hasPhotos = *input.HasPhotos
		hasPhotosFingerprint = fmt.Sprintf("%t", *input.HasPhotos)
	}
	serviceFingerprint := ""
	if input.ServiceID != nil {
		serviceFingerprint = input.ServiceID.String()
	}
	fingerprint := keysetFilterFingerprint("public-provider-reviews", trimmedSlug, serviceFingerprint, fmt.Sprintf("%d", input.Rating), hasPhotosFingerprint, input.Sort)
	var cursor publicReviewListCursor
	if err := decodeKeysetCursor(input.Cursor, fingerprint, &cursor); err != nil {
		return PublicReviewsResponse{}, err
	}
	var cursorRating any
	var cursorCreatedAt any
	var cursorID any
	if strings.TrimSpace(input.Cursor) != "" {
		if cursor.CreatedAt.IsZero() || cursor.ID == uuid.Nil || (input.Sort == "highest" && (cursor.Rating < 1 || cursor.Rating > 5)) {
			return PublicReviewsResponse{}, ErrInvalidKeysetCursor
		}
		cursorRating, cursorCreatedAt, cursorID = cursor.Rating, cursor.CreatedAt.UTC(), cursor.ID
	}
	var totalCount *int
	var summary *PublicReviewSummary
	if strings.TrimSpace(input.Cursor) == "" {
		const countQuery = `
		SELECT COUNT(*)::int
		FROM provider_reviews review
		INNER JOIN client_profile_handles handle ON handle.client_id = review.client_id
		WHERE handle.handle_slug = $1
		  AND review.status = 'approved'
		  AND ($2::uuid IS NULL OR review.service_id = $2)
		  AND ($3::int = 0 OR review.rating = $3)
		  AND ($4::boolean IS NULL OR (NULLIF(BTRIM(review.image_url), '') IS NOT NULL) = $4)
	`
		count := 0
		if err := r.db.QueryRow(ctx, countQuery, trimmedSlug, input.ServiceID, input.Rating, hasPhotos).Scan(&count); err != nil {
			return PublicReviewsResponse{}, fmt.Errorf("count public reviews: %w", err)
		}
		totalCount = &count

		const summaryQuery = `
		SELECT
			COALESCE(AVG(review.rating), 0)::double precision,
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE review.rating = 1)::int,
			COUNT(*) FILTER (WHERE review.rating = 2)::int,
			COUNT(*) FILTER (WHERE review.rating = 3)::int,
			COUNT(*) FILTER (WHERE review.rating = 4)::int,
			COUNT(*) FILTER (WHERE review.rating = 5)::int
		FROM provider_reviews review
		INNER JOIN client_profile_handles handle ON handle.client_id = review.client_id
		WHERE handle.handle_slug = $1 AND review.status = 'approved'
	`
		summaryValue := PublicReviewSummary{Breakdown: make(map[int]int, 5)}
		var oneStar, twoStar, threeStar, fourStar, fiveStar int
		if err := r.db.QueryRow(ctx, summaryQuery, trimmedSlug).Scan(
			&summaryValue.Rating,
			&summaryValue.Count,
			&oneStar,
			&twoStar,
			&threeStar,
			&fourStar,
			&fiveStar,
		); err != nil {
			return PublicReviewsResponse{}, fmt.Errorf("summarize public reviews: %w", err)
		}
		summaryValue.Breakdown[1] = oneStar
		summaryValue.Breakdown[2] = twoStar
		summaryValue.Breakdown[3] = threeStar
		summaryValue.Breakdown[4] = fourStar
		summaryValue.Breakdown[5] = fiveStar
		summary = &summaryValue
		if count == 0 {
			var exists bool
			if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client_profile_handles WHERE handle_slug = $1)`, trimmedSlug).Scan(&exists); err != nil {
				return PublicReviewsResponse{}, fmt.Errorf("check public review provider: %w", err)
			}
			if !exists {
				return PublicReviewsResponse{}, ErrNotFound
			}
		}
	}

	const listQuery = `
		WITH page AS MATERIALIZED (
			SELECT review.*
			FROM provider_reviews review
			INNER JOIN client_profile_handles handle ON handle.client_id = review.client_id
			WHERE handle.handle_slug = $1
			  AND review.status = 'approved'
			  AND ($2::uuid IS NULL OR review.service_id = $2)
			  AND ($3::int = 0 OR review.rating = $3)
			  AND ($4::boolean IS NULL OR (NULLIF(BTRIM(review.image_url), '') IS NOT NULL) = $4)
			  AND ($7::timestamptz IS NULL OR
				($5='highest' AND (review.rating, review.created_at, review.id) < ($8::int, $7, $9::uuid)) OR
				($5='newest' AND (review.created_at, review.id) < ($7, $9::uuid)))
			ORDER BY
				CASE WHEN $5 = 'highest' THEN review.rating END DESC,
				review.created_at DESC,
				review.id DESC
			LIMIT $6
		)
		SELECT
			review.id,
			COALESCE(review.service_id::text, ''),
			COALESCE(service.title, ''),
			review.author_name,
			review.rating,
			review.review_text,
			COALESCE(review.image_url, ''),
			EXISTS (
				SELECT 1
				FROM bookings booking
				WHERE booking.id = review.booking_id
				  AND booking.client_id = review.client_id
				  AND review.customer_id IS NOT NULL
				  AND booking.customer_id = review.customer_id
				  AND review.service_id IS NOT NULL
				  AND booking.service_id = review.service_id
				  AND booking.status = 'completed'
				  AND booking.end_at <= review.created_at
			),
			review.created_at
		FROM page review
		LEFT JOIN services service ON service.id = review.service_id AND service.client_id = review.client_id
		ORDER BY
			CASE WHEN $5 = 'highest' THEN review.rating END DESC,
			review.created_at DESC,
			review.id DESC
	`
	rows, err := r.db.Query(ctx, listQuery, trimmedSlug, input.ServiceID, input.Rating, hasPhotos, input.Sort, input.Limit+1, cursorCreatedAt, cursorRating, cursorID)
	if err != nil {
		return PublicReviewsResponse{}, fmt.Errorf("list public reviews: %w", err)
	}
	defer rows.Close()

	items := make([]PublicReviewItem, 0, input.Limit+1)
	for rows.Next() {
		var item PublicReviewItem
		if err := rows.Scan(
			&item.ID,
			&item.ServiceID,
			&item.ServiceTitle,
			&item.AuthorName,
			&item.Rating,
			&item.ReviewText,
			&item.ImageURL,
			&item.VerifiedBooking,
			&item.CreatedAt,
		); err != nil {
			return PublicReviewsResponse{}, fmt.Errorf("scan public review: %w", err)
		}
		item.AuthorName = publicReviewAuthorName(item.AuthorName)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return PublicReviewsResponse{}, fmt.Errorf("iterate public reviews: %w", err)
	}
	hasMore := len(items) > input.Limit
	if hasMore {
		items = items[:input.Limit]
	}
	response := PublicReviewsResponse{Items: items, TotalCount: totalCount, Summary: summary}
	if hasMore {
		last := items[len(items)-1]
		lastID, err := uuid.Parse(last.ID)
		if err != nil {
			return PublicReviewsResponse{}, fmt.Errorf("encode public review cursor: %w", err)
		}
		response.NextCursor, err = encodeKeysetCursor(fingerprint, publicReviewListCursor{Rating: last.Rating, CreatedAt: last.CreatedAt.UTC(), ID: lastID})
		if err != nil {
			return PublicReviewsResponse{}, fmt.Errorf("encode public review cursor: %w", err)
		}
	}
	return response, nil
}

func publicReviewAuthorName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "Tellbook customer"
	}
	if len(parts) == 1 {
		return parts[0]
	}
	initial, _ := utf8.DecodeRuneInString(parts[len(parts)-1])
	return fmt.Sprintf("%s %c.", parts[0], initial)
}
