package appdata

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublicReviewsUseApprovedProviderDataAndFilters(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clientID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	serviceID := insertMarketplaceTestService(t, ctx, pool, clientID, uuid.Nil, "reviewed-virtual-service", "virtual", 25000, nil)
	handle := "marketplace-test-" + clientID.String()[:12]
	createdAt := time.Now().UTC().Add(-24 * time.Hour)
	customerID := uuid.New()
	bookingID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO customers (id, client_id, full_name, email, created_at, updated_at)
		VALUES ($1,$2,'Marcus Holloway','marcus@example.test',NOW(),NOW())
	`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bookings (
			id, client_id, customer_id, service_id, title, status, start_at, end_at,
			currency_code, country_code, occupied_start_at, occupied_end_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,'Reviewed service','completed',$5,$6,'NGN','NG',$5,$6,$6,$6)
	`, bookingID, clientID, customerID, serviceID, createdAt.Add(-2*time.Hour), createdAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM bookings WHERE id = $1`, bookingID); err != nil {
			t.Errorf("delete public review booking fixtures: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customerID); err != nil {
			t.Errorf("delete public review customer fixture: %v", err)
		}
	})
	for _, review := range []struct {
		rating    int
		status    string
		image     any
		customer  any
		bookingID any
	}{
		{rating: 5, status: "approved", image: "https://example.com/review.jpg", customer: customerID, bookingID: bookingID},
		{rating: 4, status: "approved", image: nil},
		{rating: 1, status: "hidden", image: nil},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO provider_reviews (
				id, client_id, customer_id, booking_id, service_id, author_name, rating,
				review_text, image_url, status, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,'Marcus Holloway',$6,'Real review text',$7,$8,$9,$9)
		`, uuid.New(), clientID, review.customer, review.bookingID, serviceID, review.rating, review.image, review.status, createdAt.Add(time.Duration(review.rating)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM provider_reviews WHERE client_id = $1`, clientID); err != nil {
			t.Errorf("delete public review fixtures: %v", err)
		}
	})

	repo := NewRepository(pool)
	profileResponse, err := repo.GetPublicProfileBySlug(ctx, handle, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(profileResponse.Services) != 1 || len(profileResponse.FeaturedServices) != 1 {
		t.Fatalf("profile services = %d/%d, want one included and one featured service", len(profileResponse.Services), len(profileResponse.FeaturedServices))
	}
	all, err := repo.ListPublicReviewsBySlug(ctx, handle, PublicReviewsInput{Sort: "newest", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if all.TotalCount == nil || *all.TotalCount != 2 || len(all.Items) != 2 || all.Summary == nil || all.Summary.Count != 2 {
		t.Fatalf("public reviews = %+v, want two approved rows", all)
	}
	if math.Abs(all.Summary.Rating-4.5) > 0.001 || all.Summary.Breakdown[5] != 1 || all.Summary.Breakdown[4] != 1 {
		t.Fatalf("review summary = %+v, want 4.5 with one four-star and one five-star", all.Summary)
	}
	if !all.Items[0].VerifiedBooking || all.Items[1].VerifiedBooking {
		t.Fatalf("verified flags = %+v, want only the matching completed booking verified", all.Items)
	}
	if all.Items[0].AuthorName != "Marcus H." {
		t.Fatalf("public author = %q, want a surname initial", all.Items[0].AuthorName)
	}
	firstPage, err := repo.ListPublicReviewsBySlug(ctx, handle, PublicReviewsInput{Sort: "newest", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || firstPage.NextCursor == "" || firstPage.Summary == nil || firstPage.TotalCount == nil {
		t.Fatalf("first review page = %+v, want one row, summary, total, and cursor", firstPage)
	}
	secondPage, err := repo.ListPublicReviewsBySlug(ctx, handle, PublicReviewsInput{
		Sort: "newest", Limit: 1, Cursor: firstPage.NextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID || secondPage.Summary != nil || secondPage.TotalCount != nil {
		t.Fatalf("second review page = %+v, want a distinct row without repeated aggregates", secondPage)
	}

	withPhoto := true
	filtered, err := repo.ListPublicReviewsBySlug(ctx, handle, PublicReviewsInput{
		ServiceID: &serviceID,
		Rating:    5,
		HasPhotos: &withPhoto,
		Sort:      "highest",
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.TotalCount == nil || *filtered.TotalCount != 1 || len(filtered.Items) != 1 || filtered.Items[0].Rating != 5 || filtered.Items[0].ImageURL == "" {
		t.Fatalf("filtered reviews = %+v, want the approved five-star photo review", filtered)
	}
}

func TestPublicReviewAuthorName(t *testing.T) {
	for input, expected := range map[string]string{
		"":                  "Tellbook customer",
		"Ada":               "Ada",
		"Ada Nwankwo":       "Ada N.",
		"  Élodie  Ọnwụka ": "Élodie Ọ.",
	} {
		if actual := publicReviewAuthorName(input); actual != expected {
			t.Errorf("publicReviewAuthorName(%q) = %q, want %q", input, actual, expected)
		}
	}
}
