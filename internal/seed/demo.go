package seed

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type Input struct {
	ClientID uuid.UUID
	Email    string
	Password string
	FullName string
	Reset    bool
}

type Result struct {
	ClientID             uuid.UUID
	Email                string
	Password             string
	CredentialsPreserved bool
}

func SeedDemoProvider(ctx context.Context, tx pgx.Tx, input Input) (Result, error) {
	if input.ClientID == uuid.Nil {
		input.ClientID = uuid.New()
	}

	existingEmail, clientExists, err := getExistingClientEmail(ctx, tx, input.ClientID)
	if err != nil {
		return Result{}, err
	}

	if strings.TrimSpace(input.Email) == "" {
		input.Email = "demo-provider@example.com"
	}
	if strings.TrimSpace(input.Password) == "" {
		input.Password = "Password123!"
	}
	if strings.TrimSpace(input.FullName) == "" {
		input.FullName = "Alex Rivera Studio"
	}

	now := time.Now().UTC()
	var passwordHash []byte
	if !clientExists {
		passwordHash, err = bcrypt.GenerateFromPassword([]byte(input.Password), 12)
		if err != nil {
			return Result{}, fmt.Errorf("hash password: %w", err)
		}
	}

	if input.Reset {
		if err := resetProviderData(ctx, tx, input.ClientID); err != nil {
			return Result{}, err
		}
	}

	if err := upsertUser(ctx, tx, input, passwordHash, now, clientExists); err != nil {
		return Result{}, err
	}

	if err := insertClientProfile(ctx, tx, input.ClientID, now); err != nil {
		return Result{}, err
	}
	if err := insertAvailability(ctx, tx, input.ClientID); err != nil {
		return Result{}, err
	}
	providerLocationID, err := insertBusinessLocation(ctx, tx, input.ClientID, now)
	if err != nil {
		return Result{}, err
	}

	serviceIDs, err := insertServices(ctx, tx, input.ClientID, providerLocationID, now)
	if err != nil {
		return Result{}, err
	}

	customerIDs, err := insertCustomers(ctx, tx, input.ClientID, now)
	if err != nil {
		return Result{}, err
	}

	bookingIDs, err := insertBookings(ctx, tx, input.ClientID, serviceIDs, customerIDs, now)
	if err != nil {
		return Result{}, err
	}

	if err := insertConversations(ctx, tx, input.ClientID, customerIDs, bookingIDs, now); err != nil {
		return Result{}, err
	}
	if err := insertNotifications(ctx, tx, input.ClientID, customerIDs, bookingIDs, now); err != nil {
		return Result{}, err
	}
	if err := insertAutomationSettings(ctx, tx, input.ClientID, now); err != nil {
		return Result{}, err
	}
	if err := insertPortfolioItems(ctx, tx, input.ClientID, serviceIDs, now); err != nil {
		return Result{}, err
	}
	if err := insertReviews(ctx, tx, input.ClientID, customerIDs, serviceIDs, bookingIDs, now); err != nil {
		return Result{}, err
	}
	return Result{
		ClientID:             input.ClientID,
		Email:                pickSeedEmail(input.Email, existingEmail, clientExists),
		Password:             pickSeedPassword(input.Password, clientExists),
		CredentialsPreserved: clientExists,
	}, nil
}

func getExistingClientEmail(ctx context.Context, tx pgx.Tx, clientID uuid.UUID) (string, bool, error) {
	const query = `SELECT email FROM clients WHERE id = $1`

	var email sql.NullString
	err := tx.QueryRow(ctx, query, clientID).Scan(&email)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("select existing client: %w", err)
	}

	return email.String, true, nil
}

func pickSeedEmail(inputEmail, existingEmail string, clientExists bool) string {
	if clientExists {
		return existingEmail
	}

	return inputEmail
}

func pickSeedPassword(inputPassword string, clientExists bool) string {
	if clientExists {
		return "(unchanged)"
	}

	return inputPassword
}

func resetProviderData(ctx context.Context, tx pgx.Tx, clientID uuid.UUID) error {
	queries := []string{
		`DELETE FROM auth_refresh_sessions WHERE client_id = $1`,
		`DELETE FROM payouts WHERE client_id = $1`,
		`DELETE FROM payment_allocations WHERE client_id = $1`,
		`DELETE FROM payment_adjustments WHERE payment_id IN (SELECT id FROM payments WHERE client_id = $1)`,
		`DELETE FROM payments WHERE client_id = $1`,
		`DELETE FROM payout_destinations WHERE client_id = $1`,
		`DELETE FROM notifications WHERE client_id = $1`,
		`DELETE FROM inbox_messages WHERE conversation_id IN (SELECT id FROM inbox_conversations WHERE client_id = $1)`,
		`DELETE FROM inbox_conversations WHERE client_id = $1`,
		`DELETE FROM provider_reviews WHERE client_id = $1`,
		`DELETE FROM provider_portfolio_items WHERE client_id = $1`,
		`DELETE FROM agreement_instances WHERE client_id = $1`,
		`DELETE FROM agreement_template_families WHERE client_id = $1`,
		`DELETE FROM automation_settings WHERE client_id = $1`,
		`DELETE FROM booking_quotes WHERE client_id = $1`,
		`DELETE FROM bookings WHERE client_id = $1`,
		`DELETE FROM customers WHERE client_id = $1`,
		`DELETE FROM provider_availability_windows WHERE client_id = $1`,
		`DELETE FROM services WHERE client_id = $1`,
		`DELETE FROM business_locations WHERE client_id = $1`,
		`DELETE FROM client_profiles WHERE client_id = $1`,
	}

	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, clientID); err != nil {
			return fmt.Errorf("reset provider data: %w", err)
		}
	}

	return nil
}

func upsertUser(ctx context.Context, tx pgx.Tx, input Input, passwordHash []byte, now time.Time, preserveCredentials bool) error {
	query := `
		INSERT INTO clients (id, full_name, bio, cover_image_url, email, password_hash, email_verified_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			bio = EXCLUDED.bio,
			cover_image_url = EXCLUDED.cover_image_url,
			email = EXCLUDED.email,
			password_hash = EXCLUDED.password_hash,
			email_verified_at = EXCLUDED.email_verified_at,
			updated_at = EXCLUDED.updated_at
	`

	if preserveCredentials {
		query = `
			INSERT INTO clients (id, full_name, bio, cover_image_url, email, password_hash, email_verified_at, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (id) DO UPDATE SET
				full_name = EXCLUDED.full_name,
				bio = EXCLUDED.bio,
				cover_image_url = EXCLUDED.cover_image_url,
				updated_at = EXCLUDED.updated_at
		`
	}

	_, err := tx.Exec(
		ctx,
		query,
		input.ClientID,
		input.FullName,
		"Premium creative studio focused on portraits, events, and brand storytelling.",
		"https://images.unsplash.com/photo-1517841905240-472988babdf9?auto=format&fit=crop&w=1200&q=80",
		strings.ToLower(strings.TrimSpace(input.Email)),
		string(passwordHash),
		now,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO provider_auth_identities (
			client_id, identity_type, normalized_identifier, verified_at
		)
		SELECT id, 'email', lower(btrim(email)), email_verified_at
		FROM clients
		WHERE id = $1 AND email IS NOT NULL AND email_verified_at IS NOT NULL
		ON CONFLICT ON CONSTRAINT provider_auth_identities_client_type_unique
		DO UPDATE SET
			normalized_identifier = EXCLUDED.normalized_identifier,
			verified_at = EXCLUDED.verified_at,
			updated_at = NOW()
	`, input.ClientID); err != nil {
		return fmt.Errorf("upsert provider email identity: %w", err)
	}

	return nil
}

func insertClientProfile(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, now time.Time) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO client_profile_handles (handle_slug, client_id, created_at, updated_at)
		VALUES ('alex-rivera-studio', $1, $2, $2)
		ON CONFLICT (handle_slug) DO UPDATE
		SET client_id = EXCLUDED.client_id, updated_at = EXCLUDED.updated_at
	`, clientID, now); err != nil {
		return fmt.Errorf("insert client profile handle: %w", err)
	}

	const query = `
		INSERT INTO client_profiles (
			client_id, business_name, handle_slug, category, headline, short_bio, public_location_label,
			city, region, timezone, hero_image_url, avatar_url, verified, years_experience,
			review_rating, review_count, country_code, currency_code, locale, market_configured_at,
			marketplace_enabled, marketplace_category_id, marketplace_location_visibility,
			created_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
			'NG', 'NGN', 'en-NG', $17, TRUE, '10000000-0000-4000-8000-000000000004', 'approximate', $18, $19
		)
	`

	_, err := tx.Exec(
		ctx,
		query,
		clientID,
		"Alex Rivera Studio",
		"alex-rivera-studio",
		"Photography",
		"Premium Photographer",
		"Luxury portrait, event, and branding sessions built for modern personal and business storytelling.",
		"Lekki, Lagos",
		"Lagos",
		"Lagos",
		"Africa/Lagos",
		"https://images.unsplash.com/photo-1511285560929-80b456fea0bc?auto=format&fit=crop&w=1600&q=80",
		"https://images.unsplash.com/photo-1500648767791-00dcc994a43e?auto=format&fit=crop&w=800&q=80",
		true,
		8,
		4.67,
		3,
		now,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("insert client profile: %w", err)
	}

	return nil
}

func insertAvailability(ctx context.Context, tx pgx.Tx, clientID uuid.UUID) error {
	const query = `
		INSERT INTO provider_availability_windows (
			id, client_id, day_of_week, start_time, end_time, slot_interval_minutes
		)
		VALUES ($1,$2,$3,$4,$5,$6)
	`

	for day := 1; day <= 6; day++ {
		if _, err := tx.Exec(
			ctx,
			query,
			demoID(clientID, fmt.Sprintf("availability:%d", day)),
			clientID,
			day,
			"09:00:00",
			"17:00:00",
			30,
		); err != nil {
			return fmt.Errorf("insert availability: %w", err)
		}
	}

	return nil
}

func insertBusinessLocation(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, now time.Time) (uuid.UUID, error) {
	locationID := demoID(clientID, "business-location:primary")
	_, err := tx.Exec(ctx, `
		INSERT INTO business_locations (
			id, client_id, label, formatted_address, address_source,
			latitude, longitude, resolution_status, country_code, state_region_id, lga_region_id,
			locality, timezone, is_primary, is_active, created_at, updated_at
		)
		VALUES ($1,$2,'Main studio','Lekki Phase 1, Lagos','manual',6.447800,3.472300,
			'coordinates_resolved','NG',
			(SELECT id FROM administrative_regions WHERE level='state' AND ST_Covers(boundary, ST_SetSRID(ST_MakePoint(3.472300,6.447800),4326)) LIMIT 1),
			(SELECT id FROM administrative_regions WHERE level='lga' AND ST_Covers(boundary, ST_SetSRID(ST_MakePoint(3.472300,6.447800),4326)) LIMIT 1),
			'Lekki','Africa/Lagos',TRUE,TRUE,$3,$3)
	`, locationID, clientID, now)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert business location: %w", err)
	}
	return locationID, nil
}

func insertServices(ctx context.Context, tx pgx.Tx, clientID, providerLocationID uuid.UUID, now time.Time) (map[string]uuid.UUID, error) {
	type service struct {
		key      string
		title    string
		slug     string
		desc     string
		category string
		icon     string
		image    string
		duration int
		price    int64
		order    int
	}

	records := []service{
		{"portrait", "Portrait Session", "portrait-session", "90 min portrait session with 15 edited photos included.", "Photography", "photo_camera", "https://images.unsplash.com/photo-1494790108377-be9c29b29330?auto=format&fit=crop&w=900&q=80", 90, 250000, 1},
		{"event", "Event Coverage", "event-coverage", "Per-hour event coverage with raw images and premium edits.", "Photography", "event", "https://images.unsplash.com/photo-1511578314322-379afb476865?auto=format&fit=crop&w=900&q=80", 180, 120000, 2},
		{"brand", "Brand Strategy Shoot", "brand-strategy-shoot", "Creative direction, stills, and short-form social assets for founders.", "Branding", "campaign", "https://images.unsplash.com/photo-1521737604893-d14cc237f11d?auto=format&fit=crop&w=900&q=80", 120, 350000, 3},
		{"wedding", "Wedding Highlight Package", "wedding-highlight-package", "Editorial wedding coverage with ceremony and portrait storytelling.", "Events", "favorite", "https://images.unsplash.com/photo-1519741497674-611481863552?auto=format&fit=crop&w=900&q=80", 240, 650000, 4},
	}

	const query = `
		INSERT INTO services (
			id, client_id, title, slug, description, category, icon_name, image_url,
			duration_minutes, price_amount_minor, currency_code, is_active, sort_order,
			fulfillment_mode, provider_location_id, availability_mode, agreement_timing,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'NGN',TRUE,$11,
			'provider_location',$12,'inherit_business_hours',NULL,$13,$14)
	`

	ids := make(map[string]uuid.UUID, len(records))
	for _, record := range records {
		serviceID := demoID(clientID, "service:"+record.key)
		ids[record.key] = serviceID
		if _, err := tx.Exec(
			ctx,
			query,
			serviceID,
			clientID,
			record.title,
			record.slug,
			record.desc,
			record.category,
			record.icon,
			record.image,
			record.duration,
			record.price,
			record.order,
			providerLocationID,
			now,
			now,
		); err != nil {
			return nil, fmt.Errorf("insert service: %w", err)
		}
	}

	return ids, nil
}

func insertCustomers(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, now time.Time) (map[string]uuid.UUID, error) {
	type customer struct {
		key       string
		name      string
		email     string
		phone     string
		avatarURL string
		tier      string
		status    string
		badge     string
		badgeTone string
		tags      []string
		notes     string
	}

	records := []customer{
		{"marcus", "Marcus Holloway", "marcus@example.com", "+1-917-555-0101", "https://images.unsplash.com/photo-1500648767791-00dcc994a43e?auto=format&fit=crop&w=600&q=80", "VIP", "Active", "VIP", "vip", []string{"Repeat"}, "Prefers morning sessions and neutral backdrops."},
		{"elena", "Elena Rodriguez", "elena@example.com", "+1-917-555-0102", "https://images.unsplash.com/photo-1544005313-94ddf0286df2?auto=format&fit=crop&w=600&q=80", "VIP", "Active", "Unpaid", "unpaid", []string{"VIP"}, "Requested flexible payment terms for next appointment."},
		{"julian", "Julian Vane", "julian@example.com", "+1-917-555-0103", "https://images.unsplash.com/photo-1506794778202-cad84cf45f1d?auto=format&fit=crop&w=600&q=80", "Premium Tier Member", "At Risk", "No-show risk", "risk", []string{"New"}, "Responds best to short morning messages and minimalist creative direction."},
		{"sarah", "Sarah Chen", "sarah@example.com", "+1-917-555-0104", "https://images.unsplash.com/photo-1488426862026-3ee34a7d66df?auto=format&fit=crop&w=600&q=80", "Repeat Client", "Active", "", "", []string{"Repeat"}, "Enjoys bright editorial styling and fast delivery."},
		{"oscar", "Oscar Thompson", "oscar@example.com", "+1-917-555-0105", "", "New Lead", "New", "", "", []string{"New"}, "Corporate inquiry, interested in quarterly content package."},
	}

	const query = `
		INSERT INTO customers (
			id, client_id, full_name, email, phone, avatar_url, tier_label, status_label,
			badge_label, badge_tone, tags, private_notes, last_seen_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
	`

	ids := make(map[string]uuid.UUID, len(records))
	for i, record := range records {
		customerID := demoID(clientID, "customer:"+record.key)
		ids[record.key] = customerID
		lastSeenAt := now.Add(-time.Duration(24*(i+1)) * time.Hour)
		if _, err := tx.Exec(
			ctx,
			query,
			customerID,
			clientID,
			record.name,
			record.email,
			record.phone,
			nullIfEmpty(record.avatarURL),
			record.tier,
			record.status,
			record.badge,
			record.badgeTone,
			record.tags,
			record.notes,
			lastSeenAt,
			now,
			now,
		); err != nil {
			return nil, fmt.Errorf("insert customer: %w", err)
		}
	}

	return ids, nil
}

func insertBookings(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, serviceIDs, customerIDs map[string]uuid.UUID, now time.Time) (map[string]uuid.UUID, error) {
	type booking struct {
		key          string
		clientKey    string
		serviceKey   string
		title        string
		status       string
		payment      string
		agreement    string
		startAt      time.Time
		endAt        time.Time
		rateMinor    int64
		totalMinor   int64
		durationMins int
		notes        string
		location     string
		imageURL     string
	}

	loc := time.FixedZone("Africa/Lagos", 1*60*60)
	startOfDay := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)

	records := []booking{
		{"portrait_today", "marcus", "portrait", "Portrait Session", "confirmed", "paid", "signed", startOfDay.Add(10 * time.Hour), startOfDay.Add(11 * time.Hour), 115000, 115000, 60, "Focus on a natural finish and clean background selection.", "423 Artisan Way, Brooklyn, NY", "https://images.unsplash.com/photo-1517841905240-472988babdf9?auto=format&fit=crop&w=800&q=80"},
		{"event_today", "sarah", "event", "Event Coverage", "pending", "pending_deposit", "sent", startOfDay.Add(14*time.Hour + 30*time.Minute), startOfDay.Add(17*time.Hour + 30*time.Minute), 360000, 360000, 180, "Need wide and candid coverage for keynote moments.", "Downtown Studio, New York, NY", "https://images.unsplash.com/photo-1511578314322-379afb476865?auto=format&fit=crop&w=800&q=80"},
		{"brand_tomorrow", "julian", "brand", "Brand Strategy Shoot", "booked", "deposit_paid", "signed", startOfDay.Add(35 * time.Hour), startOfDay.Add(37 * time.Hour), 350000, 350000, 120, "Prefers minimalist sets and dark-themed product styling.", "Hudson Creative Loft, New York, NY", "https://images.unsplash.com/photo-1521737604893-d14cc237f11d?auto=format&fit=crop&w=800&q=80"},
		{"wedding_future", "elena", "wedding", "Wedding Highlight Package", "booked", "paid", "signed", startOfDay.Add(72 * time.Hour), startOfDay.Add(76 * time.Hour), 650000, 650000, 240, "Need extra bridal party portraits and family lineup coverage.", "Riverside Manor, New York, NY", "https://images.unsplash.com/photo-1519741497674-611481863552?auto=format&fit=crop&w=800&q=80"},
		{"portrait_review", "marcus", "portrait", "Portrait Session", "completed", "paid", "signed", startOfDay.Add(-11 * 24 * time.Hour), startOfDay.Add(-11*24*time.Hour + 90*time.Minute), 250000, 250000, 90, "Completed marketplace review booking.", "Lagos studio", ""},
		{"wedding_review", "elena", "wedding", "Wedding Highlight Package", "completed", "paid", "signed", startOfDay.Add(-13 * 24 * time.Hour), startOfDay.Add(-13*24*time.Hour + 4*time.Hour), 650000, 650000, 240, "Completed marketplace review booking.", "Lagos venue", ""},
		{"event_review", "sarah", "event", "Event Coverage", "completed", "paid", "signed", startOfDay.Add(-15 * 24 * time.Hour), startOfDay.Add(-15*24*time.Hour + 3*time.Hour), 120000, 120000, 180, "Completed marketplace review booking.", "Lagos event venue", ""},
	}

	const query = `
		INSERT INTO bookings (
			id, client_id, customer_id, service_id, title, stylist_name, source, status,
			payment_status, agreement_status, start_at, end_at, timezone, base_service_amount_minor,
			discounted_service_amount_minor, total_amount_minor, currency_code, country_code,
			duration_minutes, notes, location_label, image_url, fulfillment_mode,
			provider_location_label, occupied_start_at, occupied_end_at,
			created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			'NG',$18,$19,$20,$21,'provider_location',$20,$11,$12,$22,$23)
	`

	ids := make(map[string]uuid.UUID, len(records))
	for _, record := range records {
		bookingID := demoID(clientID, "booking:"+record.key)
		ids[record.key] = bookingID
		if _, err := tx.Exec(
			ctx,
			query,
			bookingID,
			clientID,
			customerIDs[record.clientKey],
			serviceIDs[record.serviceKey],
			record.title,
			"Alex Rivera",
			"manual",
			record.status,
			record.payment,
			record.agreement,
			record.startAt.UTC(),
			record.endAt.UTC(),
			"Africa/Lagos",
			record.rateMinor,
			record.totalMinor,
			record.totalMinor,
			"NGN",
			record.durationMins,
			record.notes,
			record.location,
			record.imageURL,
			now,
			now,
		); err != nil {
			return nil, fmt.Errorf("insert booking: %w", err)
		}
	}

	return ids, nil
}

func insertConversations(
	ctx context.Context,
	tx pgx.Tx,
	clientID uuid.UUID,
	customerIDs, bookingIDs map[string]uuid.UUID,
	now time.Time,
) error {
	marketplaceCustomerID := demoID(clientID, "marketplace-customer:sarah")
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customers (
			id, full_name, email, email_verified_at, created_at, updated_at
		) VALUES ($1,'Sarah Johnson',$2,NOW(),$3,$3)
		ON CONFLICT (id) DO UPDATE SET
			full_name=EXCLUDED.full_name,
			email=EXCLUDED.email,
			email_verified_at=COALESCE(marketplace_customers.email_verified_at,NOW()),
			updated_at=EXCLUDED.updated_at
	`, marketplaceCustomerID, "marketplace-sarah-"+clientID.String()+"@example.com", now); err != nil {
		return fmt.Errorf("insert native inbox marketplace customer: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO marketplace_customer_identities (
			marketplace_customer_id, identifier_type, normalized_identifier, verified_at
		)
		SELECT id, 'email', lower(btrim(email)), email_verified_at
		FROM marketplace_customers
		WHERE id=$1 AND email IS NOT NULL AND email_verified_at IS NOT NULL
		ON CONFLICT ON CONSTRAINT marketplace_customer_identities_customer_type_unique
		DO UPDATE SET
			normalized_identifier=EXCLUDED.normalized_identifier,
			verified_at=EXCLUDED.verified_at,
			updated_at=NOW()
	`, marketplaceCustomerID); err != nil {
		return fmt.Errorf("insert native inbox marketplace customer identity: %w", err)
	}

	bookingID := bookingIDs["event_today"]
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET marketplace_customer_id=$1, source='marketplace', updated_at=$3 WHERE id=$2
	`, marketplaceCustomerID, bookingID, now); err != nil {
		return fmt.Errorf("link native inbox demo booking owner: %w", err)
	}

	conversationID := demoID(clientID, "conversation:sarah:tellbook")
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_conversations (
			id, client_id, customer_id, marketplace_customer_id, channel, preview,
			created_at, updated_at
		) VALUES ($1,$2,$3,$4,'tellbook','Thanks — I will bring the reference photos.',$5,$5)
	`, conversationID, clientID, customerIDs["sarah"], marketplaceCustomerID, now); err != nil {
		return fmt.Errorf("insert native inbox conversation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_conversation_bookings (conversation_id, booking_id, linked_by_actor)
		VALUES ($1,$2,'system')
	`, conversationID, bookingID); err != nil {
		return fmt.Errorf("insert native inbox booking link: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_participant_states (conversation_id, participant_type, participant_id)
		VALUES ($1,'provider',$2),($1,'marketplace_customer',$3)
	`, conversationID, clientID, marketplaceCustomerID); err != nil {
		return fmt.Errorf("insert native inbox participant states: %w", err)
	}

	type seededMessage struct {
		key, senderType, content string
		senderID                 uuid.UUID
		when                     time.Time
	}
	messages := []seededMessage{
		{"customer", "marketplace_customer", "Hi, is there anything I should bring to the event session?", marketplaceCustomerID, now.Add(-20 * time.Minute)},
		{"provider", "provider", "Please bring any reference photos you want us to match.", clientID, now.Add(-16 * time.Minute)},
		{"customer-confirmation", "marketplace_customer", "Thanks — I will bring the reference photos.", marketplaceCustomerID, now.Add(-12 * time.Minute)},
	}
	var lastSequence int64
	for _, message := range messages {
		if err := tx.QueryRow(ctx, `
			INSERT INTO inbox_messages (
				id, conversation_id, sender_type, sender_id, booking_id, content, sent_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7)
			RETURNING sequence
		`, demoID(clientID, "conversation:sarah:message:"+message.key), conversationID,
			message.senderType, message.senderID, bookingID, message.content, message.when,
		).Scan(&lastSequence); err != nil {
			return fmt.Errorf("insert native inbox message: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inbox_conversations
		SET last_message_sequence=$2, last_message_at=$3, updated_at=$4
		WHERE id=$1
	`, conversationID, lastSequence, messages[len(messages)-1].when, now); err != nil {
		return fmt.Errorf("update native inbox conversation summary: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inbox_events (
			conversation_id, client_id, marketplace_customer_id, event_type, payload, created_at
		) VALUES ($1,$2,$3,'conversation.created',jsonb_build_object('conversation_id',$1::text),$4)
	`, conversationID, clientID, marketplaceCustomerID, now); err != nil {
		return fmt.Errorf("insert native inbox event: %w", err)
	}
	return nil
}

func insertNotifications(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, customerIDs, bookingIDs map[string]uuid.UUID, now time.Time) error {
	type notification struct {
		key         string
		clientKey   string
		bookingKey  string
		typ         string
		severity    string
		title       string
		description string
		actionLabel string
		actionRoute string
		imageURL    string
		iconName    string
		iconTone    string
	}

	records := []notification{
		{"inquiry", "sarah", "", "new_inquiry", "urgent", "Missed inquiry from Jessica M.", "Sent 15 minutes ago • Event: Weekend", "Reply Now", "/inbox", "https://images.unsplash.com/photo-1494790108377-be9c29b29330?auto=format&fit=crop&w=300&q=80", "", ""},
		{"deposit", "elena", "", "deposit_overdue", "urgent", "Deposit overdue: Mike T.", "$50.00 Overdue for session", "Send Invoice", "/payments", "", "payments", "danger"},
		{"agreement", "julian", "", "agreement_unsigned", "normal", "Agreement unsigned", "Smith Wedding • Expires in 2 days", "Resend Link", "/templates", "", "edit_document", "muted"},
		{"booking", "sarah", "portrait_today", "booking_reminder", "normal", "Bridal Makeup Reminder", "14:00 PM • Sarah Jenkins", "View Details", "/bookings", "", "calendar_today", "muted"},
	}

	const query = `
		INSERT INTO notifications (
			id, client_id, customer_id, booking_id, type, severity, title, description, action_label,
			action_route, image_url, icon_name, icon_tone, metadata, read_at, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
	`

	for i, record := range records {
		var customerID any
		if record.clientKey != "" {
			customerID = customerIDs[record.clientKey]
		}
		var bookingID any
		if record.bookingKey != "" {
			bookingID = bookingIDs[record.bookingKey]
		}
		if _, err := tx.Exec(
			ctx,
			query,
			demoID(clientID, "notification:"+record.key),
			clientID,
			customerID,
			bookingID,
			record.typ,
			record.severity,
			record.title,
			record.description,
			record.actionLabel,
			record.actionRoute,
			nullIfEmpty(record.imageURL),
			record.iconName,
			record.iconTone,
			`{}`,
			nil,
			now.Add(-time.Duration((i+1)*15)*time.Minute),
			now,
		); err != nil {
			return fmt.Errorf("insert notification: %w", err)
		}
	}

	return nil
}

func insertAutomationSettings(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, now time.Time) error {
	records := []struct {
		key         string
		title       string
		description string
		actionLabel string
		enabled     bool
		config      string
	}{
		{"rebooking_reminders", "Rebooking Reminders", "Automatically nudge clients to schedule their next session before their current package expires.", "Customize flow", true, `{"days_after": 21}`},
		{"deposit_reminders", "Deposit Reminders", "Send a friendly nudge for pending deposits to secure booking slots and reduce no-shows.", "Set threshold", false, `{"hours_before": 48}`},
		{"agreement_reminders", "Agreement Reminders", "Remind clients to sign waivers or service agreements before their scheduled arrival.", "View templates", true, `{"hours_before": 24}`},
		{"follow_up_reminders", "Follow-up Reminders", "Check in with clients 24 hours after a session to gather feedback and ensure satisfaction.", "Edit messages", false, `{"hours_after": 24}`},
	}

	const query = `
		INSERT INTO automation_settings (
			id, client_id, automation_key, title, description, action_label, enabled, config, updated_at, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10)
	`

	for _, record := range records {
		if _, err := tx.Exec(
			ctx,
			query,
			demoID(clientID, "automation:"+record.key),
			clientID,
			record.key,
			record.title,
			record.description,
			record.actionLabel,
			record.enabled,
			record.config,
			now,
			now,
		); err != nil {
			return fmt.Errorf("insert automation setting: %w", err)
		}
	}

	return nil
}

func insertPortfolioItems(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, serviceIDs map[string]uuid.UUID, now time.Time) error {
	records := []struct {
		key        string
		serviceKey string
		title      string
		imageURL   string
		sortOrder  int
	}{
		{"portfolio1", "portrait", "Editorial portrait", "https://images.unsplash.com/photo-1494790108377-be9c29b29330?auto=format&fit=crop&w=800&q=80", 1},
		{"portfolio2", "event", "Luxury event detail", "https://images.unsplash.com/photo-1511578314322-379afb476865?auto=format&fit=crop&w=800&q=80", 2},
		{"portfolio3", "brand", "Brand campaign", "https://images.unsplash.com/photo-1521737604893-d14cc237f11d?auto=format&fit=crop&w=800&q=80", 3},
	}

	const query = `
		INSERT INTO provider_portfolio_items (
			id, client_id, service_id, title, image_url, sort_order, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`

	for _, record := range records {
		if _, err := tx.Exec(
			ctx,
			query,
			demoID(clientID, "portfolio:"+record.key),
			clientID,
			serviceIDs[record.serviceKey],
			record.title,
			record.imageURL,
			record.sortOrder,
			now,
		); err != nil {
			return fmt.Errorf("insert portfolio item: %w", err)
		}
	}

	return nil
}

func insertReviews(ctx context.Context, tx pgx.Tx, clientID uuid.UUID, customerIDs, serviceIDs, bookingIDs map[string]uuid.UUID, now time.Time) error {
	records := []struct {
		key        string
		clientKey  string
		serviceKey string
		bookingKey string
		author     string
		rating     int
		body       string
		imageURL   string
	}{
		{"review1", "marcus", "portrait", "portrait_review", "Marcus Holloway", 5, "Fast turnaround, strong creative direction, and a premium session experience from start to finish.", ""},
		{"review2", "elena", "wedding", "wedding_review", "Elena Rodriguez", 5, "The communication was effortless and the final gallery felt polished and luxurious.", "https://images.unsplash.com/photo-1519741497674-611481863552?auto=format&fit=crop&w=1000&q=80"},
		{"review3", "sarah", "event", "event_review", "Sarah Chen", 4, "Loved the attention to detail and how easy the booking process was.", ""},
	}

	const query = `
		INSERT INTO provider_reviews (
			id, client_id, customer_id, booking_id, service_id, author_name, rating,
			review_text, image_url, status, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'approved',$10,$10)
	`

	for i, record := range records {
		if _, err := tx.Exec(
			ctx,
			query,
			demoID(clientID, "review:"+record.key),
			clientID,
			customerIDs[record.clientKey],
			bookingIDs[record.bookingKey],
			serviceIDs[record.serviceKey],
			record.author,
			record.rating,
			record.body,
			nullIfEmpty(record.imageURL),
			now.Add(-time.Duration((i+7)*24)*time.Hour),
		); err != nil {
			return fmt.Errorf("insert review: %w", err)
		}
	}

	return nil
}

func demoID(clientID uuid.UUID, key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("booking-demo:"+clientID.String()+":"+key))
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
