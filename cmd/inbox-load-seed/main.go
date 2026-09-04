package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	marketplaceCookieName  = "tellbook_marketplace_session"
	defaultPassword        = "TellbookLoad123!"
	targetIdentityCeiling  = 1_000
	idleSSEIdentityCeiling = 10_000
)

type seedRow struct {
	Number                int
	ClientID              uuid.UUID
	MarketplaceCustomerID uuid.UUID
	ConversationID        uuid.UUID
	ProviderEmail         string
	MarketplaceEmail      string
}

type identity struct {
	ActorType      string            `json:"actor_type"`
	Headers        map[string]string `json:"headers"`
	ConversationID string            `json:"conversation_id"`
	Streams        int               `json:"streams,omitempty"`
}

func main() {
	if err := config.LoadDotEnv(); err != nil && !errors.Is(err, config.ErrNoEnvFileFound) &&
		!errors.Is(err, os.ErrNotExist) {
		exitError("load environment", err)
	}

	conversationCount := flag.Int("conversations", 10_000, "conversation count for the large provider")
	messagesPerConversation := flag.Int("messages-per-conversation", 10, "messages in each large-provider conversation")
	actorCount := flag.Int("actors", 334, "isolated authenticated provider/customer pairs")
	outputDir := flag.String("output-dir", filepath.Join(os.TempDir(), "tellbook-inbox-load"), "private artifact directory")
	password := flag.String("password", defaultPassword, "password for generated local accounts")
	confirmLocal := flag.Bool("confirm-local", false, "confirm replacement of dedicated inbox-load records in a loopback database")
	flag.Parse()

	if !*confirmLocal {
		exitError("--confirm-local is required", nil)
	}
	if *conversationCount < 1 || *messagesPerConversation < 1 || *actorCount < 1 {
		exitError("conversations, messages-per-conversation, and actors must be positive", nil)
	}
	if *actorCount > 50_000 || *conversationCount > 1_000_000 || *messagesPerConversation > 1_000 {
		exitError("requested seed exceeds the local safety ceiling", nil)
	}
	if len(*password) < 12 {
		exitError("password must be at least 12 characters", nil)
	}

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if err := requireLocalDatabase(databaseURL); err != nil {
		exitError("refusing database target", err)
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production") {
		exitError("refusing APP_ENV=production", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		exitError("open database", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		exitError("ping database", err)
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(*password), bcrypt.DefaultCost)
	if err != nil {
		exitError("hash local password", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		exitError("begin seed transaction", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := seed(ctx, tx, *conversationCount, *messagesPerConversation, *actorCount, string(passwordHash)); err != nil {
		exitError("seed inbox load fixtures", err)
	}
	rows, err := loadSeedRows(ctx, tx)
	if err != nil {
		exitError("read generated actors", err)
	}
	marketplaceTokens, err := insertMarketplaceSessions(ctx, tx, rows)
	if err != nil {
		exitError("create marketplace sessions", err)
	}
	if err := tx.Commit(ctx); err != nil {
		exitError("commit seed transaction", err)
	}
	if err := analyzeSeedTables(ctx, pool); err != nil {
		exitError("refresh inbox planner statistics", err)
	}

	if err := writeArtifacts(*outputDir, rows, marketplaceTokens, *password); err != nil {
		exitError("write private load artifacts", err)
	}
	totalMessages := (*conversationCount * *messagesPerConversation) + *actorCount
	fmt.Printf("seeded conversations=%d messages=%d load_actors=%d artifacts=%s\n",
		*conversationCount+*actorCount,
		totalMessages,
		*actorCount,
		filepath.Clean(*outputDir),
	)
}

func analyzeSeedTables(ctx context.Context, pool *pgxpool.Pool) error {
	for _, table := range []string{
		"clients",
		"client_profiles",
		"business_locations",
		"services",
		"provider_reviews",
		"marketplace_customers",
		"bookings",
		"inbox_conversations",
		"inbox_messages",
		"inbox_participant_states",
		"inbox_events",
		"inbox_conversation_bookings",
		"marketplace_saved_providers",
		"marketplace_notifications",
	} {
		if _, err := pool.Exec(ctx, "ANALYZE "+table); err != nil {
			return fmt.Errorf("analyze %s: %w", table, err)
		}
	}
	return nil
}

func requireLocalDatabase(raw string) error {
	if raw == "" {
		return errors.New("DATABASE_URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("database host %q is not loopback", host)
	}
	return nil
}

func seed(ctx context.Context, tx pgx.Tx, conversations, messagesPerConversation, actors int, passwordHash string) error {
	statements := []struct {
		name string
		sql  string
		args []any
	}{
		{
			name: "validate marketplace categories",
			sql: `DO $$ BEGIN
				IF NOT EXISTS (SELECT 1 FROM marketplace_categories WHERE is_active) THEN
					RAISE EXCEPTION 'at least one active marketplace category is required';
				END IF;
			END $$;`,
		},
		{
			name: "remove prior provider fixtures",
			sql: `DELETE FROM clients
				WHERE email = 'inbox-load-bulk-provider@example.invalid'
				   OR email LIKE 'inbox-load-provider-%@example.invalid'`,
		},
		{
			name: "remove prior marketplace fixtures",
			sql: `DELETE FROM marketplace_customers
				WHERE email LIKE 'inbox-load-bulk-customer-%@example.invalid'
				   OR email LIKE 'inbox-load-customer-%@example.invalid'`,
		},
		{
			name: "create fixture tables",
			sql: `
				CREATE TEMP TABLE inbox_load_bulk (
					n integer PRIMARY KEY,
					marketplace_id uuid NOT NULL,
					customer_id uuid NOT NULL,
					booking_id uuid NOT NULL,
					conversation_id uuid NOT NULL,
					service_id uuid NOT NULL
				) ON COMMIT DROP;
				CREATE TEMP TABLE inbox_load_actors (
					n integer PRIMARY KEY,
					client_id uuid NOT NULL,
					marketplace_id uuid NOT NULL,
					customer_id uuid NOT NULL,
					booking_id uuid NOT NULL,
					conversation_id uuid NOT NULL,
					location_id uuid NOT NULL,
					service_id uuid NOT NULL
				) ON COMMIT DROP;`,
		},
		{
			name: "populate fixture keys",
			sql: `
				INSERT INTO inbox_load_bulk
				SELECT n, gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
					'b019a06b-55c1-4f08-a4c3-da96e86b3302'::uuid
				FROM generate_series(1, $1) AS n;
				INSERT INTO inbox_load_actors
				SELECT n, gen_random_uuid(), gen_random_uuid(), gen_random_uuid(), gen_random_uuid(),
					gen_random_uuid(), gen_random_uuid(), gen_random_uuid()
				FROM generate_series(1, $2) AS n;`,
			args: []any{conversations, actors},
		},
		{
			name: "insert provider accounts",
			sql: `
				INSERT INTO clients (id, full_name, email, password_hash, email_verified_at)
				VALUES ('b019a06b-55c1-4f08-a4c3-da96e86b33a2', 'Inbox Load Bulk Provider',
					'inbox-load-bulk-provider@example.invalid', $1, NOW());
				INSERT INTO clients (id, full_name, email, password_hash, email_verified_at)
				SELECT client_id, 'Inbox Load Provider ' || n,
					format('inbox-load-provider-%s@example.invalid', lpad(n::text, 4, '0')), $1, NOW()
				FROM inbox_load_actors;`,
			args: []any{passwordHash},
		},
		{
			name: "insert provider profiles",
			sql: `
				INSERT INTO client_profile_handles (handle_slug, client_id)
				VALUES ('inbox-load-bulk-provider', 'b019a06b-55c1-4f08-a4c3-da96e86b33a2');
				INSERT INTO client_profile_handles (handle_slug, client_id)
				SELECT format('inbox-load-provider-%s', lpad(n::text, 5, '0')), client_id FROM inbox_load_actors;
				INSERT INTO client_profiles (
					client_id, business_name, handle_slug, category, headline, public_location_label,
					country_code, currency_code, timezone, locale, market_configured_at,
					marketplace_enabled, marketplace_category_id
				)
				SELECT 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', 'Inbox Load Bulk Provider',
					'inbox-load-bulk-provider', category.name, 'Local scale fixture provider', 'Lagos',
					'NG', 'NGN', 'Africa/Lagos', 'en-NG', NOW(), true, category.id
				FROM marketplace_categories category WHERE category.is_active
				ORDER BY category.sort_order, category.name LIMIT 1;
				INSERT INTO client_profiles (
					client_id, business_name, handle_slug, category, headline, public_location_label,
					country_code, currency_code, timezone, locale, market_configured_at,
					marketplace_enabled, marketplace_category_id
				)
				SELECT client_id, 'Inbox Load Provider ' || n,
					format('inbox-load-provider-%s', lpad(n::text, 5, '0')), category.name,
					'Local scale fixture provider ' || n, 'Lagos',
					'NG', 'NGN', 'Africa/Lagos', 'en-NG', NOW(), true, category.id
				FROM inbox_load_actors
				CROSS JOIN LATERAL (
					SELECT id, name FROM marketplace_categories WHERE is_active
					ORDER BY sort_order, name OFFSET ((inbox_load_actors.n - 1) %
						(SELECT COUNT(*) FROM marketplace_categories WHERE is_active)) LIMIT 1
				) category;`,
		},
		{
			name: "insert marketplace services",
			sql: `
				INSERT INTO business_locations (
					id, client_id, label, formatted_address, latitude, longitude, address_source,
					resolution_status, timezone, is_primary, country_code, locality
				) VALUES (
					'b019a06b-55c1-4f08-a4c3-da96e86b3301',
					'b019a06b-55c1-4f08-a4c3-da96e86b33a2', 'Studio', 'Lagos, Nigeria',
					6.524400, 3.379200, 'manual', 'coordinates_resolved', 'Africa/Lagos', true, 'NG', 'Lagos'
				);
				INSERT INTO business_locations (
					id, client_id, label, formatted_address, latitude, longitude, address_source,
					resolution_status, timezone, is_primary, country_code, locality
				)
				SELECT location_id, client_id, 'Studio', 'Lagos, Nigeria',
					6.40 + ((n % 200)::numeric / 1000), 3.20 + ((n % 300)::numeric / 1000),
					'manual', 'coordinates_resolved', 'Africa/Lagos', true, 'NG', 'Lagos'
				FROM inbox_load_actors;

				INSERT INTO services (
					id, client_id, title, slug, description, category, duration_minutes,
					price_amount_minor, status, availability_mode, fulfillment_mode,
					currency_code, provider_location_id, agreement_timing
				) VALUES (
					'b019a06b-55c1-4f08-a4c3-da96e86b3302',
					'b019a06b-55c1-4f08-a4c3-da96e86b33a2', 'Scale fixture service',
					'scale-fixture-service', 'Synthetic local load fixture', 'General', 60,
					10000, 'published', 'inherit_business_hours', 'provider_location', 'NGN',
					'b019a06b-55c1-4f08-a4c3-da96e86b3301', NULL
				);
				INSERT INTO services (
					id, client_id, title, slug, description, category, duration_minutes,
					price_amount_minor, status, availability_mode, fulfillment_mode,
					currency_code, provider_location_id, agreement_timing
				)
				SELECT service_id, client_id, 'Scale fixture service ' || n,
					format('scale-service-%s', lpad(n::text, 5, '0')),
					'Synthetic local load fixture', 'General', 60, 5000 + (n % 100) * 100,
					'published', 'inherit_business_hours', 'provider_location', 'NGN', location_id, NULL
				FROM inbox_load_actors;

				INSERT INTO provider_availability_windows
					(id, client_id, day_of_week, start_time, end_time, slot_interval_minutes)
				SELECT gen_random_uuid(), provider.client_id, day, '09:00', '17:00', 30
				FROM (
					SELECT 'b019a06b-55c1-4f08-a4c3-da96e86b33a2'::uuid AS client_id
					UNION ALL SELECT client_id FROM inbox_load_actors
				) provider CROSS JOIN generate_series(1, 6) day;`,
		},
		{
			name: "insert marketplace accounts",
			sql: `
				INSERT INTO marketplace_customers
					(id, full_name, email, email_verified_at, password_hash)
				SELECT marketplace_id, 'Inbox Bulk Customer ' || n,
					format('inbox-load-bulk-customer-%s@example.invalid', lpad(n::text, 5, '0')), NOW(), $1
				FROM inbox_load_bulk;
				INSERT INTO marketplace_customers
					(id, full_name, email, email_verified_at, password_hash)
				SELECT marketplace_id, 'Inbox Load Customer ' || n,
					format('inbox-load-customer-%s@example.invalid', lpad(n::text, 4, '0')), NOW(), $1
				FROM inbox_load_actors;
				INSERT INTO marketplace_customer_identities (
					marketplace_customer_id, identifier_type, normalized_identifier, verified_at
				)
				SELECT marketplace_id, 'email',
					format('inbox-load-bulk-customer-%s@example.invalid', lpad(n::text, 5, '0')), NOW()
				FROM inbox_load_bulk
				UNION ALL
				SELECT marketplace_id, 'email',
					format('inbox-load-customer-%s@example.invalid', lpad(n::text, 4, '0')), NOW()
				FROM inbox_load_actors;`,
			args: []any{passwordHash},
		},
		{
			name: "insert provider customer records",
			sql: `
				INSERT INTO customers (id, client_id, full_name, email)
				SELECT customer_id, 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', 'Inbox Bulk Customer ' || n,
					format('inbox-load-bulk-customer-%s@example.invalid', lpad(n::text, 5, '0'))
				FROM inbox_load_bulk;
				INSERT INTO customers (id, client_id, full_name, email)
				SELECT customer_id, client_id, 'Inbox Load Customer ' || n,
					format('inbox-load-customer-%s@example.invalid', lpad(n::text, 4, '0'))
				FROM inbox_load_actors;`,
		},
		{
			name: "insert bookings",
			sql: `
				INSERT INTO bookings (
					id, client_id, customer_id, title, source, status, start_at, end_at,
					base_service_amount_minor, discounted_service_amount_minor, total_amount_minor,
					currency_code, country_code, duration_minutes, fulfillment_mode,
					occupied_start_at, occupied_end_at, marketplace_customer_id
				)
				SELECT booking_id, 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', customer_id,
					'Inbox load booking ' || n, 'marketplace', 'confirmed',
					NOW() + (n || ' minutes')::interval, NOW() + ((n + 60) || ' minutes')::interval,
					10000, 10000, 10000, 'NGN', 'NG', 60, 'provider_location',
					NOW() + (n || ' minutes')::interval, NOW() + ((n + 60) || ' minutes')::interval,
					marketplace_id
				FROM inbox_load_bulk;
				INSERT INTO bookings (
					id, client_id, customer_id, title, source, status, start_at, end_at,
					base_service_amount_minor, discounted_service_amount_minor, total_amount_minor,
					currency_code, country_code, duration_minutes, fulfillment_mode,
					occupied_start_at, occupied_end_at, marketplace_customer_id
				)
				SELECT booking_id, client_id, customer_id,
					'Inbox actor booking ' || n, 'marketplace', 'confirmed',
					NOW() + (n || ' minutes')::interval, NOW() + ((n + 60) || ' minutes')::interval,
					10000, 10000, 10000, 'NGN', 'NG', 60, 'provider_location',
					NOW() + (n || ' minutes')::interval, NOW() + ((n + 60) || ' minutes')::interval,
					marketplace_id
				FROM inbox_load_actors;`,
		},
		{
			name: "link scale booking services",
			sql: `
				UPDATE bookings booking SET service_id=fixture.service_id, location_label='Lagos, Nigeria',
					provider_location_label='Lagos, Nigeria'
				FROM inbox_load_bulk fixture WHERE booking.id=fixture.booking_id;
				UPDATE bookings booking SET service_id=fixture.service_id, location_label='Lagos, Nigeria',
					provider_location_label='Lagos, Nigeria'
				FROM inbox_load_actors fixture WHERE booking.id=fixture.booking_id;

				INSERT INTO provider_reviews (
					id, client_id, customer_id, author_name, rating, review_text,
					booking_id, service_id, status, created_at
				)
				SELECT gen_random_uuid(), client_id, customer_id, 'Scale fixture customer',
					4 + (n % 2), 'Synthetic review for local query planning.',
					booking_id, service_id, 'approved', NOW() - (n || ' seconds')::interval
				FROM inbox_load_actors;

				INSERT INTO booking_domain_events (
					id, client_id, booking_id, event_type, dedupe_key, payload, created_at
				)
				SELECT gen_random_uuid(), 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', booking_id,
					'booking_created', 'scale-booking-created:' || booking_id::text,
					jsonb_build_object('booking_id', booking_id::text, 'source', 'scale_fixture'),
					NOW() - (n || ' seconds')::interval
				FROM inbox_load_bulk
				UNION ALL
				SELECT gen_random_uuid(), client_id, booking_id, 'booking_created',
					'scale-booking-created:' || booking_id::text,
					jsonb_build_object('booking_id', booking_id::text, 'source', 'scale_fixture'),
					NOW() - (n || ' seconds')::interval
				FROM inbox_load_actors;`,
		},
		{
			name: "insert conversations",
			sql: `
				INSERT INTO inbox_conversations (id, client_id, customer_id, marketplace_customer_id, created_at, updated_at)
				SELECT conversation_id, 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', customer_id, marketplace_id,
					NOW() - (($1 - n) || ' seconds')::interval, NOW() - (($1 - n) || ' seconds')::interval
				FROM inbox_load_bulk;
				INSERT INTO inbox_conversations (id, client_id, customer_id, marketplace_customer_id)
				SELECT conversation_id, client_id, customer_id, marketplace_id FROM inbox_load_actors;
				INSERT INTO inbox_conversation_bookings (conversation_id, booking_id, linked_by_actor)
				SELECT conversation_id, booking_id, 'system' FROM inbox_load_bulk
				UNION ALL
				SELECT conversation_id, booking_id, 'system' FROM inbox_load_actors;
				INSERT INTO inbox_participant_states (conversation_id, participant_type, participant_id)
				SELECT conversation_id, 'provider', 'b019a06b-55c1-4f08-a4c3-da96e86b33a2' FROM inbox_load_bulk
				UNION ALL
				SELECT conversation_id, 'marketplace_customer', marketplace_id FROM inbox_load_bulk
				UNION ALL
				SELECT conversation_id, 'provider', client_id FROM inbox_load_actors
				UNION ALL
				SELECT conversation_id, 'marketplace_customer', marketplace_id FROM inbox_load_actors;`,
			args: []any{conversations},
		},
		{
			name: "insert messages",
			sql: `
				INSERT INTO inbox_messages (id, conversation_id, sender_type, sender_id, booking_id, content, sent_at)
				SELECT gen_random_uuid(), b.conversation_id,
					CASE WHEN m % 2 = 0 THEN 'provider' ELSE 'marketplace_customer' END,
					CASE WHEN m % 2 = 0 THEN 'b019a06b-55c1-4f08-a4c3-da96e86b33a2'::uuid ELSE b.marketplace_id END,
					b.booking_id, format('Inbox scale fixture conversation %s message %s', b.n, m),
					NOW() - (($1 - b.n) || ' seconds')::interval + (m || ' milliseconds')::interval
				FROM inbox_load_bulk b CROSS JOIN generate_series(1, $2) AS m;
				INSERT INTO inbox_messages (id, conversation_id, sender_type, sender_id, booking_id, content)
				SELECT gen_random_uuid(), conversation_id, 'marketplace_customer', marketplace_id, booking_id,
					'Local two-browser acceptance conversation ' || n
				FROM inbox_load_actors;`,
			args: []any{conversations, messagesPerConversation},
		},
		{
			name: "summarize conversations",
			sql: `
				WITH latest AS (
					SELECT DISTINCT ON (conversation_id) conversation_id, sequence, content, sent_at
					FROM inbox_messages
					WHERE conversation_id IN (
						SELECT conversation_id FROM inbox_load_bulk
						UNION ALL SELECT conversation_id FROM inbox_load_actors
					)
					ORDER BY conversation_id, sequence DESC
				)
				UPDATE inbox_conversations c
				SET preview = left(l.content, 240), last_message_sequence = l.sequence,
					last_message_at = l.sent_at, updated_at = l.sent_at
				FROM latest l WHERE c.id = l.conversation_id;
				INSERT INTO inbox_events (conversation_id, client_id, marketplace_customer_id, event_type, payload)
				SELECT conversation_id, 'b019a06b-55c1-4f08-a4c3-da96e86b33a2', marketplace_id,
					'conversation.created', jsonb_build_object('conversation_id', conversation_id)
				FROM inbox_load_bulk
				UNION ALL
				SELECT conversation_id, client_id, marketplace_id,
					'conversation.created', jsonb_build_object('conversation_id', conversation_id)
				FROM inbox_load_actors;`,
		},
		{
			name: "insert saved-provider fixtures",
			sql: `
				INSERT INTO marketplace_saved_providers (marketplace_customer_id, provider_id, created_at)
				SELECT marketplace_id, 'b019a06b-55c1-4f08-a4c3-da96e86b33a2',
					NOW() - (n || ' seconds')::interval FROM inbox_load_bulk
				UNION ALL
				SELECT marketplace_id, client_id, NOW() - (n || ' seconds')::interval FROM inbox_load_actors
				ON CONFLICT DO NOTHING;`,
		},
	}

	for _, statement := range statements {
		execArgs := statement.args
		if len(execArgs) > 0 {
			execArgs = append([]any{pgx.QueryExecModeSimpleProtocol}, execArgs...)
		}
		if _, err := tx.Exec(ctx, statement.sql, execArgs...); err != nil {
			return fmt.Errorf("%s: %w", statement.name, err)
		}
	}
	return nil
}

func loadSeedRows(ctx context.Context, tx pgx.Tx) ([]seedRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT n, client_id, marketplace_id, conversation_id,
			format('inbox-load-provider-%s@example.invalid', lpad(n::text, 4, '0')),
			format('inbox-load-customer-%s@example.invalid', lpad(n::text, 4, '0'))
		FROM inbox_load_actors ORDER BY n`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]seedRow, 0)
	for rows.Next() {
		var row seedRow
		if err := rows.Scan(&row.Number, &row.ClientID, &row.MarketplaceCustomerID, &row.ConversationID,
			&row.ProviderEmail, &row.MarketplaceEmail); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func insertMarketplaceSessions(ctx context.Context, tx pgx.Tx, rows []seedRow) (map[uuid.UUID]string, error) {
	tokens := make(map[uuid.UUID]string, len(rows))
	for _, row := range rows {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(token))
		if _, err := tx.Exec(ctx, `
			INSERT INTO marketplace_auth_sessions
				(id, marketplace_customer_id, token_hash, user_agent, ip_address, expires_at, last_used_at)
			VALUES ($1, $2, $3, 'tellbook-local-load-seed', '127.0.0.1', NOW() + INTERVAL '4 hours', NOW())`,
			uuid.New(), row.MarketplaceCustomerID, hash[:]); err != nil {
			return nil, err
		}
		tokens[row.MarketplaceCustomerID] = token
	}
	return tokens, nil
}

func writeArtifacts(outputDir string, rows []seedRow, marketplaceTokens map[uuid.UUID]string, password string) error {
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(outputDir, 0o700); err != nil {
		return err
	}
	secret := strings.TrimSpace(os.Getenv("AUTH_ACCESS_TOKEN_SECRET"))
	issuer := strings.TrimSpace(os.Getenv("AUTH_ISSUER"))
	if issuer == "" {
		issuer = "booking-api"
	}
	cookieName := strings.TrimSpace(os.Getenv("AUTH_ACCESS_COOKIE_NAME"))
	if cookieName == "" {
		cookieName = "booking_access"
	}
	if secret == "" {
		return errors.New("AUTH_ACCESS_TOKEN_SECRET is required")
	}

	providerIdentities := make([]identity, 0, len(rows))
	marketplaceIdentities := make([]identity, 0, len(rows))
	now := time.Now().UTC()
	for index, row := range rows {
		claims := auth.AccessTokenClaims{
			Email: row.ProviderEmail, FullName: fmt.Sprintf("Inbox Load Provider %d", row.Number),
			RegisteredClaims: jwt.RegisteredClaims{
				Subject: row.ClientID.String(), Issuer: issuer,
				IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
				ExpiresAt: jwt.NewNumericDate(now.Add(4 * time.Hour)),
			},
		}
		signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
		if err != nil {
			return err
		}
		providerIdentities = append(providerIdentities, identity{
			ActorType: "provider", ConversationID: row.ConversationID.String(),
			Headers: map[string]string{
				"Cookie":          cookieName + "=" + signed,
				"X-Forwarded-For": benchmarkIP(index),
			},
		})
		marketplaceIdentities = append(marketplaceIdentities, identity{
			ActorType: "marketplace_customer", ConversationID: row.ConversationID.String(),
			Headers: map[string]string{
				"Cookie":          marketplaceCookieName + "=" + marketplaceTokens[row.MarketplaceCustomerID],
				"X-Forwarded-For": benchmarkIP(index + len(rows)),
			},
		})
	}
	providerPath := filepath.Join(outputDir, "provider-identities.ndjson")
	marketplacePath := filepath.Join(outputDir, "marketplace-identities.ndjson")
	if err := writeNDJSON(providerPath, providerIdentities); err != nil {
		return err
	}
	if err := writeNDJSON(marketplacePath, marketplaceIdentities); err != nil {
		return err
	}

	baseURL := "http://127.0.0.1:8200/v1"
	configs := map[string]map[string]any{
		"smoke.json": {
			"base_url": baseURL, "duration_seconds": 20, "ramp_seconds": 5,
			"identity_limit":       20,
			"streams_per_identity": 1, "reconnect_each_stream": true, "reconnect_after_seconds": 8,
			"message_rate_per_second": 5, "max_in_flight_messages": 100,
			"read_probe_rate_per_second": 10, "max_in_flight_reads": 100,
			"identities_file": providerPath,
		},
		"target.json": {
			"base_url": baseURL, "duration_seconds": 60, "ramp_seconds": 30,
			"identity_limit":       targetIdentityLimit(len(rows)),
			"streams_per_identity": 1, "reconnect_each_stream": true, "reconnect_after_seconds": 25,
			"message_rate_per_second": 50, "max_in_flight_messages": 300,
			"read_probe_rate_per_second": 30, "max_in_flight_reads": 300,
			"identities_file": providerPath,
		},
		"idle-sse.json": {
			"base_url": baseURL, "duration_seconds": 60, "ramp_seconds": 60,
			"identity_limit":       min(len(rows), idleSSEIdentityCeiling),
			"streams_per_identity": 1, "reconnect_each_stream": false,
			"message_rate_per_second": 0, "read_probe_rate_per_second": 0,
			"identities_file": providerPath,
		},
	}
	for name, value := range configs {
		if err := writeJSON(filepath.Join(outputDir, name), value); err != nil {
			return err
		}
	}
	if len(rows) > 0 {
		e2e := map[string]any{
			"provider_email":    rows[0].ProviderEmail,
			"marketplace_email": rows[0].MarketplaceEmail,
			"password":          password,
			"conversation_id":   rows[0].ConversationID.String(),
		}
		if err := writeJSON(filepath.Join(outputDir, "e2e.json"), e2e); err != nil {
			return err
		}
	}
	return nil
}

func targetIdentityLimit(available int) int {
	if available > targetIdentityCeiling {
		return targetIdentityCeiling
	}
	return available
}

func benchmarkIP(index int) string {
	third := (index / 254) % 256
	fourth := (index % 254) + 1
	return fmt.Sprintf("198.18.%d.%d", third, fourth)
}

func writeNDJSON(path string, values []identity) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			_ = file.Close()
			return err
		}
	}
	return file.Close()
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}

func exitError(message string, err error) {
	if err != nil {
		slog.Error(message, "error", err)
	} else {
		slog.Error(message)
	}
	os.Exit(1)
}
