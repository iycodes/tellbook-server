package appdata

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"booking/go-server/internal/money"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagedServiceAggregateRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var clientID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM clients ORDER BY created_at LIMIT 1`).Scan(&clientID); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	locations, err := repo.ListBusinessLocations(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) == 0 {
		t.Fatal("expected a seeded business location")
	}

	created, err := repo.CreateManagedService(ctx, clientID, CreateManagedServiceInput{
		ServiceName:     "Integration consultation",
		Description:     "A focused online consultation.",
		DurationMinutes: 45,
		Pricing: ServicePricingConfig{
			PriceAmountMinor: money.Minor(25000),
			DepositType:      "fixed",
		},
		Fulfillment: ServiceFulfillmentConfig{Mode: "virtual"},
		Availability: ServiceAvailabilityConfig{
			Mode:                 "inherit_business_hours",
			MinimumNoticeMinutes: 120,
			PrepTimeMinutes:      5,
			BufferTimeMinutes:    10,
		},
		ShortNoticeRules: []ServiceShortNoticeRule{{
			ThresholdMinutes:       1440,
			SurchargeType:          "percentage",
			SurchargePercentageBPS: 1500,
		}},
		VirtualDelivery: ServiceVirtualDelivery{Label: "Video consultation"},
		PublishStatus:   "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	serviceID := uuid.MustParse(created.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM services WHERE id = $1`, serviceID)
	})

	duplicated, err := repo.DuplicateManagedService(ctx, clientID, serviceID)
	if err != nil {
		t.Fatal(err)
	}
	duplicatedID := uuid.MustParse(duplicated.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM services WHERE id = $1`, duplicatedID)
	})
	if duplicated.Status != "draft" || duplicated.Fulfillment.Mode != "virtual" {
		t.Fatalf("unexpected duplicate state: %#v", duplicated)
	}
	if len(duplicated.ShortNoticeRules) != 1 || duplicated.ShortNoticeRules[0].SurchargePercentageBPS != 1500 {
		t.Fatalf("duplicate did not preserve short-notice rules: %#v", duplicated.ShortNoticeRules)
	}

	wizardDraft, err := repo.CreateServiceWizardDraft(ctx, clientID, CreateServiceWizardDraftInput{
		ServiceID:   created.ID,
		Payload:     json.RawMessage(`{"serviceName":"Integration mobile consultation"}`),
		CurrentStep: "info",
	})
	if err != nil {
		t.Fatal(err)
	}
	wizardDraft, err = repo.UpdateServiceWizardDraft(ctx, clientID, uuid.MustParse(wizardDraft.ID), UpdateServiceWizardDraftInput{
		Revision:    wizardDraft.Revision,
		Payload:     json.RawMessage(`{"serviceName":"Integration mobile consultation","durationMinutes":60}`),
		CurrentStep: "duration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdateServiceWizardDraft(ctx, clientID, uuid.MustParse(wizardDraft.ID), UpdateServiceWizardDraftInput{
		Revision:    wizardDraft.Revision - 1,
		Payload:     wizardDraft.Payload,
		CurrentStep: wizardDraft.CurrentStep,
	}); !errors.Is(err, ErrServiceWizardDraftConflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}

	distance := 25000
	updated, err := repo.UpdateManagedService(ctx, clientID, serviceID, CreateManagedServiceInput{
		ServiceName:     "Integration mobile consultation",
		Description:     "A consultation delivered at the customer's address.",
		DurationMinutes: 60,
		Pricing: ServicePricingConfig{
			PriceAmountMinor:     money.Minor(30000),
			DepositRequired:      true,
			DepositType:          "percentage",
			DepositPercentageBPS: 2500,
		},
		Fulfillment: ServiceFulfillmentConfig{
			Mode:                    "customer_location",
			ProviderLocationID:      locations[0].ID,
			TravelFeeMinor:          money.Minor(5000),
			MaxTravelDistanceMeters: &distance,
		},
		Availability: ServiceAvailabilityConfig{
			Mode:                 "custom",
			MinimumNoticeMinutes: 120,
			MaxBookingsPerDay:    4,
			PrepTimeMinutes:      10,
			BufferTimeMinutes:    15,
			CustomWindows: []ServiceAvailabilityWindow{{
				DayOfWeek:           1,
				StartTime:           "09:00",
				EndTime:             "17:00",
				SlotIntervalMinutes: 30,
			}},
		},
		PublishStatus: "published",
		WizardDraftID: wizardDraft.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Fulfillment.Mode != "customer_location" || updated.Fulfillment.ProviderLocationID != locations[0].ID {
		t.Fatalf("unexpected fulfillment: %#v", updated.Fulfillment)
	}
	if len(updated.Availability.CustomWindows) != 1 || len(updated.ShortNoticeRules) != 0 {
		t.Fatalf("unexpected service children: %#v %#v", updated.Availability.CustomWindows, updated.ShortNoticeRules)
	}
	if _, err := repo.GetServiceWizardDraft(ctx, clientID, uuid.MustParse(wizardDraft.ID)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected consumed wizard draft, got %v", err)
	}
}
