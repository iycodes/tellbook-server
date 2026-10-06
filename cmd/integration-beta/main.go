// Serve an isolated, synthetic-only integration beta on a developer's Mac.
package main

import (
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"
	"booking/go-server/internal/integrations"
	"booking/go-server/internal/money"
	"booking/go-server/internal/observability"
	"booking/go-server/internal/server"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type reviewer struct {
	ID       uuid.UUID `json:"id"`
	Email    string    `json:"email"`
	Password string    `json:"password"`
}
type credentials struct {
	Database string     `json:"database"`
	Secret   string     `json:"session_secret"`
	Accounts []reviewer `json:"accounts"`
}

func token() string {
	v := make([]byte, 32)
	if _, e := rand.Read(v); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(v)
}
func main() {
	database := flag.String("database-url", os.Getenv("BETA_DATABASE_URL"), "isolated localhost tellbook_integrations_* database")
	publicURL := flag.String("public-url", "", "HTTPS API origin")
	clientURL := flag.String("client-url", "", "HTTPS provider app origin")
	claudeClient := flag.String("claude-client-url", "", "official Claude CIMD URL; empty permits discovery only")
	credentialsPath := flag.String("credentials-file", "/private/tmp/tellbook-beta-reviewers.json", "private reviewer credential file")
	writes := flag.Bool("writes", false, "enable beta mutations for synthetic reviewers")
	listen := flag.String("listen", "127.0.0.1:18200", "loopback listener")
	flag.Parse()
	ctx := context.Background()
	u, e := url.Parse(*database)
	if e != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tellbook_integrations_") || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		slog.Error("use an isolated localhost tellbook_integrations_* database")
		os.Exit(1)
	}
	for _, raw := range []string{*publicURL, *clientURL} {
		origin, e := url.Parse(raw)
		if e != nil || origin.Scheme != "https" || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" {
			slog.Error("HTTPS origins are required")
			os.Exit(1)
		}
	}
	if *claudeClient != "" {
		identity, err := url.Parse(*claudeClient)
		if err != nil || identity.Scheme != "https" || identity.Host != "claude.ai" || identity.User != nil || identity.Path == "" || identity.RawQuery != "" || identity.Fragment != "" {
			slog.Error("Claude metadata must be an exact official HTTPS identity URL")
			os.Exit(1)
		}
	}
	pool, e := pgxpool.New(ctx, *database)
	if e != nil {
		panic(e)
	}
	defer pool.Close()
	var c credentials
	raw, e := os.ReadFile(*credentialsPath)
	if e == nil {
		if e = json.Unmarshal(raw, &c); e != nil {
			panic(e)
		}
		if c.Database != u.Path {
			panic("reviewer credentials belong to another database")
		}
	} else if os.IsNotExist(e) {
		c = credentials{Database: u.Path, Secret: token(), Accounts: []reviewer{{uuid.New(), "reviewer-one@tellbook-beta.invalid", token()}, {uuid.New(), "reviewer-two@tellbook-beta.invalid", token()}}}
		if e = seed(ctx, pool, c.Accounts); e != nil {
			panic(e)
		}
		raw, _ = json.MarshalIndent(c, "", "  ")
		if e = os.WriteFile(*credentialsPath, raw, 0600); e != nil {
			panic(e)
		}
	} else {
		panic(e)
	}
	for _, account := range c.Accounts {
		_, err := pool.Exec(ctx, `INSERT INTO provider_auth_identities(client_id,identity_type,normalized_identifier,verified_at) VALUES($1,'email',$2,now()) ON CONFLICT(client_id,identity_type) DO NOTHING`, account.ID, account.Email)
		if err != nil {
			panic(err)
		}
	}
	if err := seedSetup(ctx, pool, c.Accounts); err != nil {
		panic(err)
	}
	providers := map[uuid.UUID]bool{}
	for _, account := range c.Accounts {
		providers[account.ID] = true
	}
	authCfg := config.Config{AuthIssuer: "tellbook-private-beta", AuthAccessTokenSecret: c.Secret, AuthAccessTokenTTL: 15 * time.Minute, AuthRefreshTokenTTL: 30 * 24 * time.Hour, AuthAccessCookieName: "tellbook_beta_access", AuthRefreshCookieName: "tellbook_beta_refresh", AuthCookieSecure: true, AuthBcryptCost: 12, HTTPAddr: *listen, WriteTimeout: 30 * time.Second, ReadHeaderTimeout: 10 * time.Second, CORSOrigins: []string{*clientURL}, HTTPRateLimitPerMinute: 300, HTTPRateLimitBurst: 100, ClientPublicBaseURL: *clientURL}
	authHandler := auth.NewHandler(auth.NewService(auth.NewRepository(pool), authCfg, nil, nil), authCfg)
	catalog := appdata.NewHandler(appdata.NewRepository(pool), authHandler, nil, nil, nil, nil, nil, nil, nil, nil, *publicURL)
	cfg := integrations.Config{Enabled: true, WritesEnabled: *writes, PublicURL: *publicURL, ClientURL: *clientURL, Providers: providers, Clients: map[string]integrations.Client{"chatgpt": {MetadataURL: "https://chatgpt.com/oauth/client.json", RedirectURI: "https://chatgpt.com/connector_platform_oauth_redirect"}, "claude": {MetadataURL: *claudeClient, RedirectURI: "https://claude.ai/api/mcp/auth_callback"}}}
	metrics := observability.New()
	integration := integrations.New(pool, catalog, authHandler, cfg).SetMetrics(metrics)
	srv := server.New(authCfg, slog.Default(), authHandler, nil, catalog, server.OperationalDependencies{IntegrationRoutes: integration.Routes, Metrics: metrics, Readiness: pool, Role: "api", ConfigurationReady: true})
	handler := srv.Handler
	// Observe only public metadata URLs during connector setup; never state, codes or tokens.
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/authorize" {
			candidate := r.URL.Query().Get("client_id")
			id, e := url.Parse(candidate)
			if e == nil && id.Scheme == "https" && (id.Host == "claude.ai" || id.Host == "chatgpt.com") && id.User == nil && id.RawQuery == "" && id.Fragment == "" {
				slog.Info("platform public client metadata", "url", candidate)
			}
		}
		handler.ServeHTTP(w, r)
	})
	slog.Info("synthetic integration beta listening", "address", *listen, "api", *publicURL, "app", *clientURL, "credentials_file", *credentialsPath, "writes", *writes)
	if e = srv.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		panic(e)
	}
}
func seed(ctx context.Context, pool *pgxpool.Pool, accounts []reviewer) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for n, account := range accounts {
		hash, e := bcrypt.GenerateFromPassword([]byte(account.Password), 12)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO clients(id,full_name,email,password_hash,email_verified_at) VALUES($1,$2,$3,$4,now())`, account.ID, fmt.Sprintf("Tellbook Reviewer %d", n+1), account.Email, string(hash)); e != nil {
			return e
		}
		handle := fmt.Sprintf("tellbook-reviewer-%d-%s", n+1, account.ID.String()[:8])
		if _, e = tx.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, account.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,$2,$3,'Africa/Lagos','NG','NGN','en-NG',now())`, account.ID, fmt.Sprintf("Synthetic Studio %d", n+1), handle); e != nil {
			return e
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	repo := appdata.NewRepository(pool)
	for _, account := range accounts {
		section, e := repo.CreateServiceSection(ctx, account.ID, appdata.CreateServiceSectionInput{Name: "Consultations"})
		if e != nil {
			return e
		}
		for _, name := range []string{"Design consultation", "Business consultation", "Draft consultation"} {
			status := "published"
			if strings.HasPrefix(name, "Draft") {
				status = "draft"
			}
			_, e = repo.CreateManagedService(ctx, account.ID, appdata.CreateManagedServiceInput{ServiceName: name, Description: "Synthetic review fixture. No real bookings or payments.", SectionID: section.ID, DurationMinutes: 45, Pricing: appdata.ServicePricingConfig{PriceAmountMinor: money.Minor(250000)}, Fulfillment: appdata.ServiceFulfillmentConfig{Mode: "virtual"}, Availability: appdata.ServiceAvailabilityConfig{Mode: "inherit_business_hours", MinimumNoticeMinutes: 120}, PublishStatus: status})
			if e != nil {
				return e
			}
		}
	}
	return nil
}
