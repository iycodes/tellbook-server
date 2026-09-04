package appdata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderListsUseFilterBoundStableKeysetCursors(t *testing.T) {
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
	clientID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,'O3 pagination provider',$2,'test',NOW())
	`, clientID, fmt.Sprintf("o3-%s@example.test", clientID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, clientID) })

	seenAt := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	customerIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for index, customerID := range customerIDs {
		tags := []string{}
		if index == 0 {
			tags = []string{"new"}
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO customers (id,client_id,full_name,email,tags,last_seen_at)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, customerID, clientID, fmt.Sprintf("O3 Customer %d", index+1),
			fmt.Sprintf("o3-customer-%s@example.test", customerID), tags, seenAt); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(pool)
	firstCustomers, err := repo.ListCustomers(ctx, clientID, CustomerListInput{
		Filter: "all", Limit: 2, IncludeCounts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstCustomers.Items) != 2 || firstCustomers.NextCursor == "" || firstCustomers.Counts["all"] != 3 || firstCustomers.Counts["new"] != 1 {
		t.Fatalf("first customer page = %+v", firstCustomers)
	}
	secondCustomers, err := repo.ListCustomers(ctx, clientID, CustomerListInput{
		Filter: "all", Cursor: firstCustomers.NextCursor, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondCustomers.Items) != 1 || secondCustomers.NextCursor != "" ||
		secondCustomers.Items[0].ID == firstCustomers.Items[0].ID ||
		secondCustomers.Items[0].ID == firstCustomers.Items[1].ID {
		t.Fatalf("second customer page = %+v", secondCustomers)
	}
	if _, err := repo.ListCustomers(ctx, clientID, CustomerListInput{
		Filter: "new", Cursor: firstCustomers.NextCursor, Limit: 2,
	}); !errors.Is(err, ErrInvalidKeysetCursor) {
		t.Fatalf("reused customer cursor error = %v", err)
	}

	bookingStart := time.Date(2090, 1, 2, 12, 0, 0, 0, time.UTC)
	for index, customerID := range customerIDs {
		bookingID := uuid.New()
		if _, err := pool.Exec(ctx, `
			INSERT INTO bookings (
				id,client_id,customer_id,title,source,status,payment_status,agreement_status,
				start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code,duration_minutes
			) VALUES ($1,$2,$3,$4,'marketplace','booked','unpaid','not_required',
				$5,$6,$5,$6,'NGN','NG',60)
		`, bookingID, clientID, customerID, fmt.Sprintf("O3 Booking %d", index+1),
			bookingStart, bookingStart.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	bookingInput := BookingListInput{
		WindowStart: bookingStart.Add(-time.Hour), WindowEnd: bookingStart.Add(2 * time.Hour), Limit: 2,
	}
	firstBookings, err := repo.ListBookingsWindow(ctx, clientID, bookingInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstBookings.Items) != 2 || firstBookings.NextCursor == "" {
		t.Fatalf("first booking page = %+v", firstBookings)
	}
	bookingInput.Cursor = firstBookings.NextCursor
	secondBookings, err := repo.ListBookingsWindow(ctx, clientID, bookingInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondBookings.Items) != 1 || secondBookings.NextCursor != "" {
		t.Fatalf("second booking page = %+v", secondBookings)
	}
	bookingInput.WindowEnd = bookingInput.WindowEnd.Add(time.Hour)
	if _, err := repo.ListBookingsWindow(ctx, clientID, bookingInput); !errors.Is(err, ErrInvalidKeysetCursor) {
		t.Fatalf("reused booking cursor error = %v", err)
	}

	notificationCreatedAt := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		severity := "info"
		var readAt any
		if index == 0 {
			severity = "urgent"
		}
		if index == 2 {
			readAt = notificationCreatedAt
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO notifications (id,client_id,type,severity,title,description,read_at,created_at)
			VALUES ($1,$2,'booking',$3,$4,'O3 notification',$5,$6)
		`, uuid.New(), clientID, severity, fmt.Sprintf("O3 Notification %d", index+1), readAt, notificationCreatedAt); err != nil {
			t.Fatal(err)
		}
	}
	firstNotifications, err := repo.GetNotifications(ctx, clientID, NotificationListInput{
		Filter: "all", Limit: 2, IncludeCounts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstNotifications.Items) != 2 || firstNotifications.NextCursor == "" ||
		firstNotifications.UnreadCount != 2 || firstNotifications.ActionRequiredCount != 1 {
		t.Fatalf("first notification page = %+v", firstNotifications)
	}
	secondNotifications, err := repo.GetNotifications(ctx, clientID, NotificationListInput{
		Filter: "all", Cursor: firstNotifications.NextCursor, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondNotifications.Items) != 1 || secondNotifications.NextCursor != "" {
		t.Fatalf("second notification page = %+v", secondNotifications)
	}
	if _, err := repo.GetNotifications(ctx, clientID, NotificationListInput{
		Filter: "unread", Cursor: firstNotifications.NextCursor, Limit: 2,
	}); !errors.Is(err, ErrInvalidKeysetCursor) {
		t.Fatalf("reused notification cursor error = %v", err)
	}
}
