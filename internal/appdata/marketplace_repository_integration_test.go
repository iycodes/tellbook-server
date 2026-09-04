package appdata

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	beautyMarketplaceCategoryID   = uuid.MustParse("10000000-0000-4000-8000-000000000001")
	homeMarketplaceCategoryID     = uuid.MustParse("10000000-0000-4000-8000-000000000002")
	creativeMarketplaceCategoryID = uuid.MustParse("10000000-0000-4000-8000-000000000004")
)

const marketplaceIntegrationHomeLimit = 10000

func TestMarketplaceDiscoveryUsesRealEligibilityAndLocationRules(t *testing.T) {
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

	visitorToken := "marketplace-test-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO resolved_locations (
			id, public_token, provider, formatted_address, latitude, longitude,
			address_source, resolution_status, country_code, expires_at, created_at
		) VALUES ($1,$2,'manual','Test visitor in Lekki',6.447800,3.472300,
			'current_location','coordinates_resolved','NG',$3,NOW())
	`, uuid.New(), visitorToken, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM resolved_locations WHERE public_token = $1`, visitorToken); err != nil {
			t.Errorf("delete test visitor location: %v", err)
		}
	})

	creativeClientID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	nearLocationID := insertMarketplaceTestLocation(t, ctx, pool, creativeClientID, "Near studio full private address", "Near area", 6.448000, 3.472500)
	farLocationID := insertMarketplaceTestLocation(t, ctx, pool, creativeClientID, "Far studio full private address", "Far area", 6.500000, 3.472500)
	insertMarketplaceTestService(t, ctx, pool, creativeClientID, farLocationID, "cheap-far-service", "provider_location", 10000, nil)
	nearServiceID := insertMarketplaceTestService(t, ctx, pool, creativeClientID, nearLocationID, "near-service", "provider_location", 20000, nil)
	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET review_rating = 1, review_count = 99 WHERE client_id = $1`, creativeClientID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_reviews (
			id, client_id, service_id, author_name, rating, review_text, status, created_at, updated_at
		) VALUES
			($2,$1,$4,'Marketplace customer',5,'Five stars','approved',NOW(),NOW()),
			($3,$1,$4,'Marketplace customer',4,'Four stars','approved',NOW(),NOW())
	`, creativeClientID, uuid.New(), uuid.New(), nearServiceID); err != nil {
		t.Fatal(err)
	}

	virtualClientID := insertMarketplaceTestProvider(t, ctx, pool, beautyMarketplaceCategoryID, "approximate")
	insertMarketplaceTestService(t, ctx, pool, virtualClientID, uuid.Nil, "virtual-service", "virtual", 30000, nil)

	travelClientID := insertMarketplaceTestProvider(t, ctx, pool, homeMarketplaceCategoryID, "approximate")
	travelLocationID := insertMarketplaceTestLocation(t, ctx, pool, travelClientID, "Travel base private address", "Travel base", 6.630000, 3.472300)
	maxTravelMeters := 30000
	insertMarketplaceTestService(t, ctx, pool, travelClientID, travelLocationID, "travel-service", "customer_location", 40000, &maxTravelMeters)
	lagos, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	availableDate := time.Now().In(lagos).AddDate(0, 0, 1)
	insertMarketplaceTestSchedule(t, ctx, pool, virtualClientID, availableDate.Weekday())
	insertMarketplaceTestSchedule(t, ctx, pool, travelClientID, availableDate.Weekday())
	processMarketplaceProjectionProvider(t, ctx, pool, virtualClientID)
	processMarketplaceProjectionProvider(t, ctx, pool, travelClientID)

	repo := NewRepository(pool)
	var creativeSlug string
	if err := pool.QueryRow(ctx, `SELECT handle_slug FROM client_profiles WHERE client_id = $1`, creativeClientID).Scan(&creativeSlug); err != nil {
		t.Fatal(err)
	}
	settingsWithoutSchedule, err := repo.GetMarketplaceProfileSettings(ctx, creativeClientID)
	if err != nil {
		t.Fatal(err)
	}
	if settingsWithoutSchedule.Readiness.Ready ||
		!strings.Contains(strings.Join(settingsWithoutSchedule.Readiness.BlockingReasons, " "), "availability hours") {
		t.Fatalf("readiness without schedule = %+v, want an availability reason", settingsWithoutSchedule.Readiness)
	}
	creativeHome, err := repo.GetMarketplaceHome(ctx, visitorToken, nil, nil, &creativeMarketplaceCategoryID, marketplaceIntegrationHomeLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range creativeHome.Providers {
		if provider.ID == creativeClientID.String() {
			t.Fatalf("provider without a usable schedule = %+v, want it excluded from discovery", provider)
		}
	}
	servicesWithoutSchedule, err := repo.ListPublicServicesBySlug(ctx, creativeSlug)
	if err != nil {
		t.Fatal(err)
	}
	if marketplaceServiceByID(t, servicesWithoutSchedule, nearServiceID).IsBookable {
		t.Fatal("service without a usable schedule was marked bookable")
	}
	insertMarketplaceTestSchedule(t, ctx, pool, creativeClientID, availableDate.Weekday())
	processMarketplaceProjectionProvider(t, ctx, pool, creativeClientID)
	creativeHome, err = repo.GetMarketplaceHome(ctx, visitorToken, nil, nil, &creativeMarketplaceCategoryID, marketplaceIntegrationHomeLimit)
	if err != nil {
		t.Fatal(err)
	}
	creative := marketplaceProviderByID(t, creativeHome.Providers, creativeClientID)
	servicesWithSchedule, err := repo.ListPublicServicesBySlug(ctx, creativeSlug)
	if err != nil {
		t.Fatal(err)
	}
	if !marketplaceServiceByID(t, servicesWithSchedule, nearServiceID).IsBookable {
		t.Fatal("service with a usable schedule was not marked bookable")
	}
	if creative.ServiceSlug != "near-service" {
		t.Fatalf("representative service = %q, want nearest eligible branch service", creative.ServiceSlug)
	}
	if creative.LocationLabel != "Near area" {
		t.Fatalf("approximate location = %q, want locality without private address", creative.LocationLabel)
	}
	if creative.DistanceMeters == nil || *creative.DistanceMeters > 15000 || *creative.DistanceMeters%1000 != 0 {
		t.Fatalf("near distance = %v, want a privacy-safe regional distance bucket within the search radius", creative.DistanceMeters)
	}
	if creative.DurationMinutes != 60 {
		t.Fatalf("home duration = %d, want the representative service's real duration", creative.DurationMinutes)
	}
	if creative.ReviewRating != 4.5 || creative.ReviewCount != 2 {
		t.Fatalf("home reviews = %.1f/%d, want approved review rows instead of stale profile values", creative.ReviewRating, creative.ReviewCount)
	}

	beautyHome, err := repo.GetMarketplaceHome(ctx, visitorToken, nil, nil, &beautyMarketplaceCategoryID, marketplaceIntegrationHomeLimit)
	if err != nil {
		t.Fatal(err)
	}
	virtual := marketplaceProviderByID(t, beautyHome.Providers, virtualClientID)
	if virtual.FulfillmentMode != "virtual" || virtual.LocationLabel != "Online" || virtual.DistanceMeters != nil {
		t.Fatalf("virtual provider = %+v, want location-independent discovery", virtual)
	}
	for _, provider := range beautyHome.Providers {
		if provider.ID == creativeClientID.String() {
			t.Fatal("creative provider leaked through beauty category filter")
		}
	}

	homeDiscovery, err := repo.GetMarketplaceHome(ctx, visitorToken, nil, nil, &homeMarketplaceCategoryID, marketplaceIntegrationHomeLimit)
	if err != nil {
		t.Fatal(err)
	}
	travel := marketplaceProviderByID(t, homeDiscovery.Providers, travelClientID)
	if travel.FulfillmentMode != "customer_location" || travel.DistanceMeters == nil || *travel.DistanceMeters <= 15000 {
		t.Fatalf("travel provider = %+v, want configured service area to match beyond default 15km", travel)
	}

	settingsWithSchedule, err := repo.GetMarketplaceProfileSettings(ctx, creativeClientID)
	if err != nil {
		t.Fatal(err)
	}
	if !settingsWithSchedule.Readiness.Ready {
		t.Fatalf("readiness with schedule = %+v, want provider to be publishable", settingsWithSchedule.Readiness)
	}
	search, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{
		Query: "Integration service", LocationToken: visitorToken, CategoryID: &creativeMarketplaceCategoryID,
		RadiusMeters: 15000, MaximumPriceMinor: int64Pointer(25000), AvailableOn: &availableDate,
		Sort: "recommended", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := marketplaceProviderByID(t, search.Items, creativeClientID)
	if result.ServiceID != nearServiceID.String() || result.NextAvailableAt == nil {
		t.Fatalf("search result = %+v, want matching service with real availability", result)
	}
	if result.ReviewRating != 4.5 || result.ReviewCount != 2 {
		t.Fatalf("search reviews = %.1f/%d, want approved review rows", result.ReviewRating, result.ReviewCount)
	}
	if len(search.Items) != 1 || search.Location.Mode != "nearby" {
		t.Fatalf("search metadata = %+v, want one nearby result without an exact-count query", search)
	}
	noMatch, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{Query: "not-a-real-service", Limit: 20, Sort: "recommended"})
	if err != nil {
		t.Fatal(err)
	}
	if len(noMatch.Items) != 0 {
		t.Fatalf("unmatched search = %+v, want no results", noMatch)
	}
	tooHighlyRated, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{
		Query: "near-service", MinimumRating: 4.6, Limit: 20, Sort: "recommended",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tooHighlyRated.Items) != 0 {
		t.Fatalf("minimum-rating result = %+v, want derived 4.5 rating to fail a 4.6 filter", tooHighlyRated)
	}

	secondCreativeID := insertMarketplaceTestProvider(t, ctx, pool, creativeMarketplaceCategoryID, "approximate")
	secondCreativeLocationID := insertMarketplaceTestLocation(
		t, ctx, pool, secondCreativeID,
		"Second private creative address", "Second creative area", 6.449000, 3.473000,
	)
	insertMarketplaceTestService(
		t, ctx, pool, secondCreativeID, secondCreativeLocationID,
		"near-service-secondary", "provider_location", 22000, nil,
	)
	insertMarketplaceTestSchedule(t, ctx, pool, secondCreativeID, availableDate.Weekday())
	processMarketplaceProjectionProvider(t, ctx, pool, secondCreativeID)

	allPagedProviders, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{
		Query: "Integration service", LocationToken: visitorToken, CategoryID: &creativeMarketplaceCategoryID,
		RadiusMeters: 15000, Sort: "price", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(allPagedProviders.Items) < 2 {
		t.Fatalf("pagination fixture results = %+v, want both creative providers", allPagedProviders.Items)
	}

	firstPage, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{
		Query: "Integration service", LocationToken: visitorToken, CategoryID: &creativeMarketplaceCategoryID,
		RadiusMeters: 15000, Sort: "price", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.Items) != 1 || firstPage.nextCursor == nil {
		t.Fatalf("first keyset page = %+v, want one item and a cursor", firstPage)
	}
	secondPage, err := repo.SearchMarketplaceProviders(ctx, MarketplaceProviderSearchInput{
		Query: "Integration service", LocationToken: visitorToken, CategoryID: &creativeMarketplaceCategoryID,
		RadiusMeters: 15000, Sort: "price", Limit: 1, Cursor: firstPage.nextCursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].ID == firstPage.Items[0].ID {
		t.Fatalf("cursor=%+v second keyset page = %+v, want a different provider", firstPage.nextCursor, secondPage)
	}

	var revisionBeforeReviewEdit, serviceRevisionBeforeReviewEdit, availabilityRevisionBeforeReviewEdit int64
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision FROM marketplace_discovery_jobs WHERE client_id = $1
	`, creativeClientID).Scan(&revisionBeforeReviewEdit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT document_revision FROM marketplace_service_documents WHERE service_id = $1
	`, nearServiceID).Scan(&serviceRevisionBeforeReviewEdit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT MAX(document_revision) FROM marketplace_service_availability_days WHERE service_id = $1
	`, nearServiceID).Scan(&availabilityRevisionBeforeReviewEdit); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE provider_reviews SET review_text = 'Edited public review text', updated_at = NOW()
		WHERE client_id = $1
	`, creativeClientID); err != nil {
		t.Fatal(err)
	}
	var revisionAfterTextEdit int64
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision FROM marketplace_discovery_jobs WHERE client_id = $1
	`, creativeClientID).Scan(&revisionAfterTextEdit); err != nil {
		t.Fatal(err)
	}
	if revisionAfterTextEdit != revisionBeforeReviewEdit {
		t.Fatalf("review text edit requested revision %d, want unchanged %d", revisionAfterTextEdit, revisionBeforeReviewEdit)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE provider_reviews SET rating = 3, updated_at = NOW()
		WHERE id = (SELECT id FROM provider_reviews WHERE client_id = $1 AND rating = 5 LIMIT 1)
	`, creativeClientID); err != nil {
		t.Fatal(err)
	}
	var reviewRevision int64
	var refreshCounts, refreshServices, refreshAvailability bool
	if err := pool.QueryRow(ctx, `
		SELECT requested_revision, refresh_counts, refresh_services, refresh_availability
		FROM marketplace_discovery_jobs WHERE client_id = $1
	`, creativeClientID).Scan(
		&reviewRevision, &refreshCounts, &refreshServices, &refreshAvailability,
	); err != nil {
		t.Fatal(err)
	}
	if reviewRevision != revisionBeforeReviewEdit+1 || refreshCounts || refreshServices || refreshAvailability {
		t.Fatalf("review aggregate job revision=%d flags=%t/%t/%t", reviewRevision, refreshCounts, refreshServices, refreshAvailability)
	}
	processMarketplaceProjectionProvider(t, ctx, pool, creativeClientID)
	var projectedRating float64
	var serviceRevisionAfterReviewEdit, availabilityRevisionAfterReviewEdit int64
	if err := pool.QueryRow(ctx, `
		SELECT provider.review_rating, service.document_revision,
			(SELECT MAX(day.document_revision) FROM marketplace_service_availability_days day WHERE day.service_id = service.service_id)
		FROM marketplace_provider_documents provider
		JOIN marketplace_service_documents service ON service.provider_id = provider.client_id
		WHERE provider.client_id = $1 AND service.service_id = $2
	`, creativeClientID, nearServiceID).Scan(
		&projectedRating, &serviceRevisionAfterReviewEdit, &availabilityRevisionAfterReviewEdit,
	); err != nil {
		t.Fatal(err)
	}
	if projectedRating != 3.5 || serviceRevisionAfterReviewEdit != serviceRevisionBeforeReviewEdit ||
		availabilityRevisionAfterReviewEdit != availabilityRevisionBeforeReviewEdit {
		t.Fatalf("review-only projection rating=%.1f service_revision=%d availability_revision=%d",
			projectedRating, serviceRevisionAfterReviewEdit, availabilityRevisionAfterReviewEdit)
	}
}

func TestMarketplaceSavedProviderKeepsHiddenRecordButOmitsItFromResults(t *testing.T) {
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

	providerID := insertMarketplaceTestProvider(t, ctx, pool, beautyMarketplaceCategoryID, "approximate")
	insertMarketplaceTestService(t, ctx, pool, providerID, uuid.Nil, "saved-virtual-service", "virtual", 25000, nil)
	insertMarketplaceTestSchedule(t, ctx, pool, providerID, time.Now().Weekday())

	customerID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketplace_customers (id, full_name, email, email_verified_at)
		VALUES ($1, 'Saved provider test customer', $2, NOW())
	`, customerID, customerID.String()+"@tellbook.test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, customerID); err != nil {
			t.Errorf("delete marketplace test customer: %v", err)
		}
	})

	repo := NewRepository(pool)
	processMarketplaceProjectionProvider(t, ctx, pool, providerID)
	if err := repo.SaveMarketplaceProvider(ctx, customerID, providerID); err != nil {
		t.Fatal(err)
	}
	visible, err := repo.ListMarketplaceSavedProviders(ctx, customerID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.Items) != 1 || visible.Items[0].ID != providerID.String() {
		t.Fatalf("visible saved providers = %+v, want provider %s", visible.Items, providerID)
	}

	if _, err := pool.Exec(ctx, `UPDATE client_profiles SET marketplace_enabled=FALSE WHERE client_id=$1`, providerID); err != nil {
		t.Fatal(err)
	}
	processMarketplaceProjectionProvider(t, ctx, pool, providerID)
	hidden, err := repo.ListMarketplaceSavedProviders(ctx, customerID, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Items) != 0 {
		t.Fatalf("hidden saved providers = %+v, want no stale provider data", hidden.Items)
	}
	stillSaved, err := repo.IsMarketplaceProviderSaved(ctx, customerID, providerID)
	if err != nil {
		t.Fatal(err)
	}
	if !stillSaved {
		t.Fatal("hidden provider's saved record was removed")
	}
}

func insertMarketplaceTestProvider(t *testing.T, ctx context.Context, pool *pgxpool.Pool, categoryID uuid.UUID, visibility string) uuid.UUID {
	t.Helper()
	clientID := uuid.New()
	handle := "marketplace-test-" + clientID.String()[:12]
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id, full_name, email, password_hash, created_at, updated_at)
		VALUES ($1,'Marketplace integration provider',$2,'test',NOW(),NOW())
	`, clientID, fmt.Sprintf("%s@tellbook.test", clientID)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		queries := []string{
			`DELETE FROM services WHERE client_id = $1`,
			`DELETE FROM business_locations WHERE client_id = $1`,
			`DELETE FROM client_profiles WHERE client_id = $1`,
			`DELETE FROM client_profile_handles WHERE client_id = $1`,
			`DELETE FROM clients WHERE id = $1`,
		}
		for _, query := range queries {
			if _, err := pool.Exec(ctx, query, clientID); err != nil {
				t.Errorf("delete marketplace test provider %s: %v", clientID, err)
				return
			}
		}
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profile_handles (handle_slug, client_id, created_at, updated_at)
		VALUES ($2,$1,NOW(),NOW())
	`, clientID, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO client_profiles (
			client_id, business_name, handle_slug, headline, public_location_label,
			city, region, timezone, country_code, currency_code, locale, market_configured_at,
			marketplace_enabled, marketplace_category_id, marketplace_location_visibility,
			created_at, updated_at
		) VALUES ($1,'Marketplace integration provider',$2,'Integration service','Lagos',
			'Lagos','Lagos','Africa/Lagos','NG','NGN','en-NG',NOW(),TRUE,$3,$4,NOW(),NOW())
	`, clientID, handle, categoryID, visibility); err != nil {
		t.Fatal(err)
	}
	return clientID
}

func insertMarketplaceTestLocation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clientID uuid.UUID, address, locality string, latitude, longitude float64) uuid.UUID {
	t.Helper()
	locationID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO business_locations (
			id, client_id, label, formatted_address, latitude, longitude, address_source,
			resolution_status, country_code, locality, timezone, is_primary, is_active,
			created_at, updated_at
		) VALUES ($1,$2,'Test location',$3,$4,$5,'current_location',
			'coordinates_resolved','NG',$6,'Africa/Lagos',FALSE,TRUE,NOW(),NOW())
	`, locationID, clientID, address, latitude, longitude, locality); err != nil {
		t.Fatal(err)
	}
	return locationID
}

func insertMarketplaceTestService(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clientID, locationID uuid.UUID, slug, fulfillmentMode string, price int64, maxTravelMeters *int) uuid.UUID {
	t.Helper()
	serviceID := uuid.New()
	var providerLocation any
	if locationID != uuid.Nil {
		providerLocation = locationID
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO services (
			id, client_id, title, slug, description, category, duration_minutes,
			price_amount_minor, currency_code, status, is_active, is_hidden,
			fulfillment_mode, provider_location_id, max_travel_distance_meters,
			agreement_timing, created_at, updated_at
		) VALUES ($1,$2,$3,$3,'Integration service','Integration',60,$4,'NGN',
			'published',TRUE,FALSE,$5,$6,$7,NULL,NOW(),NOW())
	`, serviceID, clientID, slug, price, fulfillmentMode, providerLocation, maxTravelMeters); err != nil {
		t.Fatal(err)
	}
	return serviceID
}

func int64Pointer(value int64) *int64 { return &value }

func insertMarketplaceTestSchedule(t *testing.T, ctx context.Context, pool *pgxpool.Pool, clientID uuid.UUID, day time.Weekday) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO provider_availability_windows (
			id, client_id, day_of_week, start_time, end_time, slot_interval_minutes, created_at
		) VALUES ($1,$2,$3,'09:00','17:00',30,NOW())
	`, uuid.New(), clientID, int(day)); err != nil {
		t.Fatal(err)
	}
}

func marketplaceProviderByID(t *testing.T, providers []MarketplaceProvider, clientID uuid.UUID) MarketplaceProvider {
	t.Helper()
	for _, provider := range providers {
		if provider.ID == clientID.String() {
			return provider
		}
	}
	t.Fatalf("provider %s not found in marketplace response", clientID)
	return MarketplaceProvider{}
}

func marketplaceServiceByID(t *testing.T, services []PublicServiceItem, serviceID uuid.UUID) PublicServiceItem {
	t.Helper()
	for _, service := range services {
		if service.ID == serviceID.String() {
			return service
		}
	}
	t.Fatalf("service %s not found in public service response", serviceID)
	return PublicServiceItem{}
}
