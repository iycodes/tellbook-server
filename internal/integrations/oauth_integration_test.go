package integrations

import (
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type betaFixture struct {
	s               *Service
	router          http.Handler
	pool            *pgxpool.Pool
	provider, other uuid.UUID
	browser         string
	inspectorURL    string
}

func integrationFixture(t *testing.T) betaFixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	f := betaFixture{pool: pool, provider: uuid.New(), other: uuid.New()}
	for _, id := range []uuid.UUID{f.provider, f.other} {
		if _, e = pool.Exec(ctx, `INSERT INTO clients(id,full_name,email,password_hash) VALUES($1,'Synthetic Beta',$2,'test-only')`, id, id.String()+"@example.invalid"); e != nil {
			t.Fatal(e)
		}
		handle := "beta-" + id.String()
		_, e = pool.Exec(ctx, `INSERT INTO client_profile_handles(handle_slug,client_id) VALUES($1,$2)`, handle, id)
		if e != nil {
			t.Fatal(e)
		}
		_, e = pool.Exec(ctx, `INSERT INTO client_profiles(client_id,business_name,handle_slug,timezone,country_code,currency_code,locale,market_configured_at) VALUES($1,'Synthetic Beta',$2,'Africa/Lagos','NG','NGN','en-NG',now())`, id, handle)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, id) })
	}
	authCfg := config.Config{AuthAccessTokenSecret: "synthetic-test-secret", AuthIssuer: "tellbook-beta-test", AuthAccessCookieName: "booking_access"}
	authHandler := auth.NewHandler(auth.NewService(auth.NewRepository(pool), authCfg, nil, nil), authCfg)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.AccessTokenClaims{SecurityRevision: 1, RegisteredClaims: jwt.RegisteredClaims{Subject: f.provider.String(), Issuer: authCfg.AuthIssuer, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}})
	f.browser, e = token.SignedString([]byte(authCfg.AuthAccessTokenSecret))
	if e != nil {
		t.Fatal(e)
	}
	cfg := Config{Enabled: true, WritesEnabled: true, PublicURL: "https://api.tellbook.test", ClientURL: "https://app.tellbook.test", Providers: map[uuid.UUID]bool{f.provider: true, f.other: true}, Clients: map[string]Client{
		"chatgpt": {"https://chatgpt.com/oauth/client.json", "https://chatgpt.com/connector_platform_oauth_redirect"}, "claude": {"https://claude.ai/test-client.json", "https://claude.ai/api/mcp/auth_callback"},
	}}
	catalog := appdata.NewHandler(appdata.NewRepository(pool), authHandler, nil, nil, nil, nil, nil, nil, nil, nil, cfg.PublicURL)
	f.s = New(pool, catalog, authHandler, cfg)
	for _, c := range cfg.Clients {
		f.s.clients.Store(c.MetadataURL, cachedClient{clientDocument{ClientID: c.MetadataURL, RedirectURIs: []string{c.RedirectURI}, AuthMethod: "none"}, time.Now().Add(time.Hour)})
	}
	r := chi.NewRouter()
	f.s.Routes(r)
	r.Route("/v1", func(r chi.Router) { catalog.Routes(r) })
	f.router = r
	return f
}
func (f betaFixture) request(t *testing.T, method, path, body, token, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	public, _ := url.Parse(f.s.cfg.PublicURL)
	r.Host = public.Host
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
		r.AddCookie(&http.Cookie{Name: "booking_access", Value: token})
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func decodeMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &value); e != nil {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
	return value
}
func (f betaFixture) begin(t *testing.T, platform, permissions string) (string, string, map[string]any) {
	t.Helper()
	v := randomToken()
	c := f.s.cfg.Clients[platform]
	q := url.Values{"response_type": {"code"}, "client_id": {c.MetadataURL}, "redirect_uri": {c.RedirectURI}, "resource": {f.s.resource(platform)}, "scope": {permissions}, "state": {"roundtrip"}, "code_challenge_method": {"S256"}, "code_challenge": {challenge(v)}}
	w := f.request(t, "GET", "/oauth/authorize?"+q.Encode(), "", "", "")
	if w.Code != 303 {
		t.Fatalf("authorize %d %s", w.Code, w.Body)
	}
	u, e := url.Parse(w.Header().Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	id := u.Query().Get("request_id")
	w = f.request(t, "GET", "/v1/app/integrations/authorization-requests/"+id, "", f.browser, "")
	if w.Code != 200 {
		t.Fatalf("consent %d %s", w.Code, w.Body)
	}
	return id, v, decodeMap(t, w)
}
func (f betaFixture) decide(t *testing.T, id string, consent map[string]any, approve bool, permissions []string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"approve": approve, "scopes": permissions, "csrf_token": consent["csrf_token"]})
	return f.request(t, "POST", "/v1/app/integrations/authorization-requests/"+id, string(body), f.browser, "application/json")
}
func (f betaFixture) link(t *testing.T, platform string, permissions []string) map[string]any {
	t.Helper()
	return f.linkConsent(t, platform, permissions, permissions)
}
func (f betaFixture) linkConsent(t *testing.T, platform string, requested, approved []string) map[string]any {
	t.Helper()
	id, v, c := f.begin(t, platform, strings.Join(requested, " "))
	w := f.decide(t, id, c, true, approved)
	if w.Code != 200 {
		t.Fatalf("approve %d %s", w.Code, w.Body)
	}
	callback, _ := url.Parse(decodeMap(t, w)["redirect_uri"].(string))
	if callback.Query().Get("iss") != f.s.cfg.PublicURL || callback.Query().Get("state") != "roundtrip" {
		t.Fatal("issuer/state missing")
	}
	fields := url.Values{"grant_type": {"authorization_code"}, "client_id": {f.s.cfg.Clients[platform].MetadataURL}, "redirect_uri": {f.s.cfg.Clients[platform].RedirectURI}, "resource": {f.s.resource(platform)}, "code": {callback.Query().Get("code")}, "code_verifier": {v}}
	w = f.request(t, "POST", "/oauth/token", fields.Encode(), "", "application/x-www-form-urlencoded")
	if w.Code != 200 {
		t.Fatalf("exchange %d %s", w.Code, w.Body)
	}
	pair := decodeMap(t, w)
	replay := f.request(t, "POST", "/oauth/token", fields.Encode(), "", "application/x-www-form-urlencoded")
	if replay.Code != 400 {
		t.Fatal("code replay accepted")
	}
	return pair
}

func TestMCPConnectionOffersPermissionsWithoutGrantingUncheckedScopes(t *testing.T) {
	for _, platform := range []string{"chatgpt", "claude"} {
		for _, writes := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/writes=%t", platform, writes), func(t *testing.T) {
				f := integrationFixture(t)
				f.s.cfg.WritesEnabled = writes
				offered := []string{"catalog.read"}
				approved := []string{"catalog.read"}
				if writes {
					offered = append(offered, "catalog.write", "catalog.publish", "catalog.delete")
					approved = append(approved, "catalog.write")
				}
				w := f.mcpRequest(t, platform, "", "initialize", map[string]any{})
				header := w.Header().Get("WWW-Authenticate")
				if w.Code != http.StatusUnauthorized || !strings.Contains(header, `scope="`+strings.Join(offered, " ")+`"`) || !strings.Contains(header, `error_description="`) {
					t.Fatal("initial connection did not offer enabled permissions", w.Code, header)
				}
				for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp/" + platform} {
					metadata := decodeMap(t, f.request(t, "GET", path, "", "", ""))
					advertised := metadata["scopes_supported"].([]any)
					wantCount := len(offered)
					if path == "/.well-known/oauth-authorization-server" {
						wantCount++ // offline_access
					}
					if len(advertised) != wantCount {
						t.Fatal("discovery advertised disabled permissions", metadata)
					}
					for i, scope := range offered {
						if advertised[i] != scope {
							t.Fatal("challenge and discovery disagree", metadata)
						}
					}
				}
				pair := f.linkConsent(t, platform, offered, approved)
				access := pair["access_token"].(string)
				identity, err := f.s.Authenticate(context.Background(), access, f.s.resource(platform))
				if err != nil || strings.Join(identity.Scopes, " ") != strings.Join(approved, " ") || pair["scope"] != strings.Join(approved, " ") {
					t.Fatal("consent granted unchecked permissions", err)
				}
				if !writes {
					return
				}
				// Consecutive edits use the same connection and returned revisions.
				params := map[string]any{"name": "create_service_section", "arguments": map[string]any{"idempotency_key": "one-consent-create", "input": map[string]any{"name": "One connection"}}}
				result := f.rpc(t, platform, access, "tools/call", params)["result"].(map[string]any)
				for _, name := range []string{"First edit", "Second edit"} {
					if result["isError"] == true || result["_meta"] != nil {
						t.Fatal("ordinary edit requested reconnection", result)
					}
					receipt := result["structuredContent"].(map[string]any)
					params = map[string]any{"name": "update_service_section", "arguments": map[string]any{"section_id": receipt["resource_id"], "expected_revision": receipt["revision"], "idempotency_key": name, "input": map[string]any{"name": name}}}
					result = f.rpc(t, platform, access, "tools/call", params)["result"].(map[string]any)
				}
				if result["isError"] == true || result["_meta"] != nil {
					t.Fatal("ordinary edit requested reconnection", result)
				}
				var grants int
				if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM integration_grants WHERE provider_id=$1`, f.provider).Scan(&grants); err != nil || grants != 1 {
					t.Fatal("routine edits created another connection", grants, err)
				}
				// Offering publication and deletion does not permit either action.
				for _, name := range []string{"set_service_status", "delete_service"} {
					args := map[string]any{"service_id": uuid.NewString(), "expected_revision": 1, "idempotency_key": name}
					if name == "set_service_status" {
						args["input"] = map[string]any{"status": "published"}
					}
					w := f.mcpRequest(t, platform, access, "tools/call", map[string]any{"name": name, "arguments": args})
					if platform == "claude" {
						if w.Code != http.StatusForbidden || !strings.Contains(w.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
							t.Fatal("unchecked permission allowed", name, w.Code)
						}
					} else {
						result := decodeMap(t, w)["result"].(map[string]any)
						if result["isError"] != true || result["structuredContent"].(map[string]any)["error"].(map[string]any)["code"] != "insufficient_scope" {
							t.Fatal("unchecked permission allowed", name, result)
						}
					}
				}
			})
		}
	}
}
func TestOAuthLinkRotationRevocationAndConsent(t *testing.T) {
	f := integrationFixture(t)
	ctx := context.Background()
	all := []string{"catalog.read", "catalog.write", "catalog.publish", "catalog.delete"}
	for _, platform := range []string{"chatgpt", "claude"} {
		t.Run(platform, func(t *testing.T) {
			for range 2 {
				id, _, consent := f.begin(t, platform, "catalog.read catalog.write")
				bad := map[string]any{"csrf_token": "wrong"}
				if f.decide(t, id, bad, true, all[:2]).Code != 403 {
					t.Fatal("CSRF not enforced")
				}
				w := f.decide(t, id, consent, false, all[:1])
				if w.Code != 200 {
					t.Fatalf("deny %d %s", w.Code, w.Body)
				}
				callback, _ := url.Parse(decodeMap(t, w)["redirect_uri"].(string))
				if callback.Query().Get("error") != "access_denied" || callback.Query().Get("code") != "" {
					t.Fatal("denial created code")
				}
			}
			pair := f.link(t, platform, all)
			access := pair["access_token"].(string)
			identity, e := f.s.Authenticate(ctx, access, f.s.resource(platform))
			if e != nil || identity.ProviderID != f.provider {
				t.Fatal("linked wrong provider", e)
			}
			otherPlatform := "chatgpt"
			if platform == otherPlatform {
				otherPlatform = "claude"
			}
			if _, e = f.s.Authenticate(ctx, access, f.s.resource(otherPlatform)); e == nil {
				t.Fatal("audience confused")
			}
			fields := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.s.cfg.Clients[platform].MetadataURL}, "resource": {f.s.resource(platform)}, "refresh_token": {pair["refresh_token"].(string)}}
			w := f.request(t, "POST", "/oauth/token", fields.Encode(), "", "application/x-www-form-urlencoded")
			if w.Code != 200 {
				t.Fatalf("refresh %s", w.Body)
			}
			rotated := decodeMap(t, w)
			if rotated["refresh_token"] == pair["refresh_token"] {
				t.Fatal("refresh did not rotate")
			}
			if f.request(t, "POST", "/oauth/token", fields.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
				t.Fatal("refresh reuse accepted")
			}
			if _, e = f.s.Authenticate(ctx, rotated["access_token"].(string), f.s.resource(platform)); e == nil {
				t.Fatal("reused grant survived")
			}
			pair = f.link(t, platform, all[:1])
			fields = url.Values{"token": {pair["access_token"].(string)}, "client_id": {f.s.cfg.Clients[platform].MetadataURL}}
			if f.request(t, "POST", "/oauth/revoke", fields.Encode(), "", "application/x-www-form-urlencoded").Code != 200 {
				t.Fatal("revoke failed")
			}
			if _, e = f.s.Authenticate(ctx, pair["access_token"].(string), f.s.resource(platform)); e == nil {
				t.Fatal("revoked access survived")
			}
		})
	}
	pair := f.link(t, "chatgpt", all[:1])
	id, e := f.s.Authenticate(ctx, pair["access_token"].(string), f.s.resource("chatgpt"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.pool.Exec(ctx, `UPDATE integration_tokens SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, digest(pair["access_token"].(string)))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Authenticate(ctx, pair["access_token"].(string), id.Resource); e == nil {
		t.Fatal("expired access survived")
	}
	pair = f.link(t, "chatgpt", all[:1])
	_, e = f.pool.Exec(ctx, `UPDATE clients SET security_revision=security_revision+1 WHERE id=$1`, f.provider)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Authenticate(ctx, pair["access_token"].(string), id.Resource); e == nil {
		t.Fatal("security change did not invalidate")
	}
}
func (f betaFixture) rpc(t *testing.T, platform, token, method string, params any) map[string]any {
	t.Helper()
	if f.inspectorURL != "" && strings.HasPrefix(method, "tools/") {
		args := []string{"-y", "@modelcontextprotocol/inspector@2.9.0", "--cli", f.inspectorURL + "/mcp/" + platform, "--transport", "http", "--method", method, "--format", "json", "--header", "Authorization: Bearer " + token}
		if method == "tools/call" {
			p := params.(map[string]any)
			raw, _ := json.Marshal(p["arguments"])
			args = append(args, "--tool-name", p["name"].(string), "--tool-args-json", string(raw))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "npx", args...)
		var diagnostics bytes.Buffer
		command.Stderr = &diagnostics
		output, err := command.Output()
		if err != nil && command.ProcessState.ExitCode() != 5 {
			t.Fatalf("Inspector %s failed: %v (%s)", method, err, strings.ReplaceAll(diagnostics.String(), token, "[redacted]"))
		}
		var result map[string]any
		if err = json.Unmarshal(output, &result); err != nil {
			t.Fatalf("Inspector returned invalid JSON: %v", err)
		}
		if _, ok := result["result"]; ok {
			return result
		}
		return map[string]any{"result": result}
	}
	w := f.mcpRequest(t, platform, token, method, params)
	if w.Code != 200 {
		t.Fatalf("MCP HTTP %d %s", w.Code, w.Body)
	}
	return decodeMap(t, w)
}
func (f betaFixture) mcpRequest(t *testing.T, platform, token, method string, params any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	r := httptest.NewRequest("POST", "/mcp/"+platform, bytes.NewReader(body))
	public, _ := url.Parse(f.s.cfg.PublicURL)
	r.Host = public.Host
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}
func TestMCPAllToolsAcrossPlatforms(t *testing.T) {
	for _, platform := range []string{"chatgpt", "claude"} {
		t.Run(platform, func(t *testing.T) {
			f := integrationFixture(t)
			if os.Getenv("RUN_MCP_INSPECTOR") == "1" {
				server := httptest.NewServer(f.router)
				t.Cleanup(server.Close)
				f.inspectorURL = server.URL
			}
			pair := f.link(t, platform, scopes)
			token := pair["access_token"].(string)
			init := f.rpc(t, platform, token, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "tellbook-test", "version": "1"}})
			if init["result"] == nil || init["result"].(map[string]any)["instructions"] != catalogInstructions {
				t.Fatal(init)
			}
			listed := f.rpc(t, platform, token, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
			if len(listed) != 15 {
				t.Fatal("missing tools")
			}
			for _, item := range listed {
				tool := item.(map[string]any)
				if platform == "claude" && appdata.CatalogScope(tool["name"].(string)) != "catalog.read" && tool["annotations"].(map[string]any)["destructiveHint"] != true {
					t.Fatal("Claude write not annotated")
				}
			}
			call := func(name string, args map[string]any) map[string]any {
				t.Helper()
				response := f.rpc(t, platform, token, "tools/call", map[string]any{"name": name, "arguments": args})
				result, ok := response["result"].(map[string]any)
				if !ok || result["isError"] == true {
					t.Fatalf("%s failed: %v", name, response)
				}
				value := result["structuredContent"].(map[string]any)
				compiler := jsonschema.NewCompiler()
				schemaJSON, _ := json.Marshal(toolOutputSchema(name))
				var schema any
				_ = json.Unmarshal(schemaJSON, &schema)
				_ = compiler.AddResource("https://tellbook.test/output", schema)
				validator, err := compiler.Compile("https://tellbook.test/output")
				if err != nil {
					t.Fatal(err)
				}
				if err = validator.Validate(value); err != nil {
					t.Fatalf("%s violated output contract: %v", name, err)
				}
				return value
			}
			for _, name := range []string{"get_connected_profile", "get_service_setup_options", "list_services", "list_service_sections"} {
				call(name, map[string]any{})
			}
			section := call("create_service_section", map[string]any{"idempotency_key": "section", "input": map[string]any{"name": "Consultations"}})
			sid := section["resource_id"].(string)
			sec := call("get_service_section", map[string]any{"section_id": sid})
			rev := sec["section"].(map[string]any)["revision"]
			updatedSection := call("update_service_section", map[string]any{"section_id": sid, "expected_revision": rev, "idempotency_key": "rename", "input": map[string]any{"name": "Beta consultations"}})
			created := call("create_service", map[string]any{"idempotency_key": "create", "input": map[string]any{"service_name": "Synthetic consultation", "duration_minutes": 45, "pricing": map[string]any{"price_amount": "2500.50"}, "fulfillment": map[string]any{"mode": "virtual"}, "availability": map[string]any{"mode": "inherit_business_hours"}}})
			serviceID := created["resource_id"].(string)
			serviceRev := created["revision"]
			detail := call("get_service", map[string]any{"service_id": serviceID})
			if detail["pricing"].(map[string]any)["price_amount_minor"] != "250050" {
				t.Fatal("money conversion failed")
			}
			mutate := func(name string, input map[string]any) map[string]any {
				t.Helper()
				args := map[string]any{"service_id": serviceID, "expected_revision": serviceRev, "idempotency_key": name}
				if input != nil {
					args["input"] = input
				}
				result := call(name, args)
				if name != "duplicate_service" {
					serviceRev = result["revision"]
				}
				return result
			}
			mutate("update_service", map[string]any{"description": "Edited through MCP"})
			mutate("set_service_status", map[string]any{"status": "published"})
			mutate("set_service_visibility", map[string]any{"is_hidden": true})
			copy := mutate("duplicate_service", nil)
			mutate("delete_service", nil)
			call("delete_service", map[string]any{"service_id": copy["resource_id"], "expected_revision": copy["revision"], "idempotency_key": "delete-copy"})
			call("delete_service_section", map[string]any{"section_id": sid, "expected_revision": updatedSection["revision"], "idempotency_key": "delete-section", "input": map[string]any{"mode": "uncategorized"}})
			readonly := f.link(t, platform, []string{"catalog.read"})
			deniedParams := map[string]any{"name": "create_service_section", "arguments": map[string]any{"idempotency_key": "denied", "input": map[string]any{"name": "Denied"}}}
			if platform == "claude" {
				denied := f.mcpRequest(t, platform, readonly["access_token"].(string), "tools/call", deniedParams)
				if denied.Code != http.StatusForbidden || !strings.Contains(denied.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
					t.Fatal("missing Claude HTTP relink challenge", denied.Code, denied.Header())
				}
			} else {
				response := f.rpc(t, platform, readonly["access_token"].(string), "tools/call", deniedParams)["result"].(map[string]any)
				if response["isError"] != true || response["_meta"] == nil {
					t.Fatal("missing relink prompt", response)
				}
			}
			identity, e := f.s.Authenticate(context.Background(), token, f.s.resource(platform))
			if e != nil {
				t.Fatal(e)
			}
			w := f.request(t, "DELETE", "/v1/app/integrations/connections/"+identity.GrantID.String(), "", f.browser, "")
			if w.Code != 204 {
				t.Fatal("disconnect", w.Code, w.Body)
			}
			if _, e = f.s.Authenticate(context.Background(), token, f.s.resource(platform)); e == nil {
				t.Fatal("disconnect left valid token")
			}
			fmt.Sprint(identity) // ensure no credential logging is needed for diagnostics
		})
	}
}

func TestMCPClaudeScopeStepUp(t *testing.T) {
	f := integrationFixture(t)
	pair := f.link(t, "claude", []string{"catalog.read", "catalog.publish"})
	params := map[string]any{"name": "create_service_section", "arguments": map[string]any{"idempotency_key": "step-up", "input": map[string]any{"name": "Coaching"}}}
	w := f.mcpRequest(t, "claude", pair["access_token"].(string), "tools/call", params)
	header := w.Header().Get("WWW-Authenticate")
	if w.Code != http.StatusForbidden || !strings.Contains(header, `error="insufficient_scope"`) || !strings.Contains(header, `scope="catalog.read catalog.write catalog.publish catalog.delete"`) || !strings.Contains(header, "This action requires catalog.write.") {
		t.Fatal("step-up must offer permission choices including existing and required scopes", w.Code, header)
	}
	if decodeMap(t, w)["error"] != "insufficient_scope" {
		t.Fatal("missing structured permission error")
	}
	var receipts int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM catalog_mutation_receipts WHERE provider_id=$1`, f.provider).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("denied call created a receipt", receipts, err)
	}
	expanded := f.link(t, "claude", []string{"catalog.read", "catalog.publish", "catalog.write"})
	w = f.mcpRequest(t, "claude", expanded["access_token"].(string), "tools/call", params)
	if w.Code != http.StatusOK || decodeMap(t, w)["result"].(map[string]any)["isError"] == true {
		t.Fatal("explicit permission expansion did not permit the call", w.Code)
	}
}

func TestMCPWritesDisabledDoesNotRequestConsent(t *testing.T) {
	for _, platform := range []string{"chatgpt", "claude"} {
		t.Run(platform, func(t *testing.T) {
			f := integrationFixture(t)
			pair := f.link(t, platform, []string{"catalog.read"})
			f.s.cfg.WritesEnabled = false
			w := f.mcpRequest(t, platform, pair["access_token"].(string), "tools/call", map[string]any{"name": "update_service", "arguments": map[string]any{"service_id": uuid.NewString(), "expected_revision": 1, "idempotency_key": "disabled", "input": map[string]any{"description": "Unavailable edit"}}})
			if w.Code != 200 || w.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("disabled writes triggered relinking: HTTP %d", w.Code)
			}
			result := decodeMap(t, w)["result"].(map[string]any)
			failure := result["structuredContent"].(map[string]any)["error"].(map[string]any)
			if result["isError"] != true || failure["code"] != "writes_disabled" || result["_meta"] != nil {
				t.Fatal("disabled writes did not return an actionable feature error", result)
			}
		})
	}
}

func TestMCPChatGPTScopeStepUpRetainsPermissions(t *testing.T) {
	f := integrationFixture(t)
	pair := f.link(t, "chatgpt", []string{"catalog.read", "catalog.publish"})
	params := map[string]any{"name": "create_service_section", "arguments": map[string]any{"idempotency_key": "step-up", "input": map[string]any{"name": "Coaching"}}}
	w := f.mcpRequest(t, "chatgpt", pair["access_token"].(string), "tools/call", params)
	if w.Code != http.StatusOK {
		t.Fatal("ChatGPT requires an MCP tool-result challenge", w.Code)
	}
	result := decodeMap(t, w)["result"].(map[string]any)
	header := result["_meta"].(map[string]any)["mcp/www_authenticate"].([]any)[0].(string)
	if result["isError"] != true || !strings.Contains(header, `error="insufficient_scope"`) || !strings.Contains(header, `scope="catalog.read catalog.write catalog.publish catalog.delete"`) || !strings.Contains(header, "This action requires catalog.write.") {
		t.Fatal("ChatGPT challenge lost permissions or error identifier", header)
	}
}

func TestOAuthExpiryAndPermissionExpansion(t *testing.T) {
	f := integrationFixture(t)
	ctx := context.Background()
	id, v, consent := f.begin(t, "chatgpt", "catalog.read")
	if f.decide(t, id, consent, true, []string{"catalog.read", "catalog.delete"}).Code != 400 {
		t.Fatal("unrequested permissions accepted")
	}
	_, e := f.pool.Exec(ctx, `UPDATE integration_authorization_requests SET expires_at=now()-interval '1 second' WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	if f.decide(t, id, consent, true, []string{"catalog.read"}).Code != 403 {
		t.Fatal("expired consent accepted")
	}
	id, v, consent = f.begin(t, "chatgpt", "catalog.read")
	w := f.decide(t, id, consent, true, []string{"catalog.read"})
	callback, _ := url.Parse(decodeMap(t, w)["redirect_uri"].(string))
	values := url.Values{"grant_type": {"authorization_code"}, "client_id": {f.s.cfg.Clients["chatgpt"].MetadataURL}, "redirect_uri": {f.s.cfg.Clients["chatgpt"].RedirectURI}, "resource": {f.s.resource("chatgpt")}, "code": {callback.Query().Get("code")}, "code_verifier": {randomToken()}}
	if f.request(t, "POST", "/oauth/token", values.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("wrong PKCE accepted")
	}
	values.Set("code_verifier", v)
	values.Set("redirect_uri", "https://chatgpt.com/unconfigured-callback")
	if f.request(t, "POST", "/oauth/token", values.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("wrong callback accepted")
	}
	values.Set("redirect_uri", f.s.cfg.Clients["chatgpt"].RedirectURI)
	_, e = f.pool.Exec(ctx, `UPDATE integration_authorization_requests SET code_expires_at=now()-interval '1 second' WHERE id=$1`, id)
	if e != nil {
		t.Fatal(e)
	}
	if f.request(t, "POST", "/oauth/token", values.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("expired code accepted")
	}
	pair := f.link(t, "chatgpt", []string{"catalog.read"})
	refresh := url.Values{"grant_type": {"refresh_token"}, "client_id": {f.s.cfg.Clients["chatgpt"].MetadataURL}, "resource": {f.s.resource("chatgpt")}, "refresh_token": {pair["refresh_token"].(string)}, "scope": {"catalog.read catalog.write"}}
	if f.request(t, "POST", "/oauth/token", refresh.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("refresh expanded permissions")
	}
	// A fresh request and fresh consent are required to expand privileges.
	expanded := f.link(t, "chatgpt", []string{"catalog.read", "catalog.write"})
	identity, e := f.s.Authenticate(ctx, expanded["access_token"].(string), f.s.resource("chatgpt"))
	if e != nil || len(identity.Scopes) != 2 {
		t.Fatal("new consent did not expand permissions", e)
	}
	_, e = f.pool.Exec(ctx, `UPDATE integration_grants SET expires_at=now()-interval '1 second' WHERE id=$1`, identity.GrantID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Authenticate(ctx, expanded["access_token"].(string), identity.Resource); e == nil {
		t.Fatal("expired grant accepted")
	}
	refresh.Del("scope")
	refresh.Set("refresh_token", expanded["refresh_token"].(string))
	if f.request(t, "POST", "/oauth/token", refresh.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("expired grant refreshed")
	}
	values.Set("client_assertion", "unsupported")
	values.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	if f.request(t, "POST", "/oauth/token", values.Encode(), "", "application/x-www-form-urlencoded").Code != 400 {
		t.Fatal("unsupported client authentication accepted")
	}
}

func TestConsentDenialAfterWritesDisabled(t *testing.T) {
	f := integrationFixture(t)
	id, _, consent := f.begin(t, "claude", "catalog.read catalog.write")
	f.s.cfg.WritesEnabled = false
	w := f.decide(t, id, consent, false, []string{"catalog.read", "catalog.write"})
	if w.Code != 200 {
		t.Fatalf("denial failed: HTTP %d %s", w.Code, w.Body)
	}
	callback, err := url.Parse(decodeMap(t, w)["redirect_uri"].(string))
	if err != nil || callback.Query().Get("error") != "access_denied" || callback.Query().Get("code") != "" {
		t.Fatal("denial did not return access_denied", err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM integration_grants WHERE provider_id=$1`, f.provider).Scan(&count); err != nil || count != 0 {
		t.Fatal("denial created a grant", err)
	}
}

func TestConnectionsRemainRevocableWhenBetaDisabled(t *testing.T) {
	f := integrationFixture(t)
	ctx := context.Background()
	pair := f.link(t, "chatgpt", []string{"catalog.read"})
	identity, err := f.s.Authenticate(ctx, pair["access_token"].(string), f.s.resource("chatgpt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE integration_grants SET created_at=now()-interval '1 hour' WHERE id=$1`, identity.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO integration_grants(id,provider_id,platform,client_id,resource,scopes,security_revision,expires_at,revoked_at,created_at) SELECT gen_random_uuid(),$1,'chatgpt',$2,$3,ARRAY['catalog.read'],1,now()+interval '30 days',now(),now()-interval '30 minutes'+n*interval '1 second' FROM generate_series(1,105) n`, f.provider, f.s.cfg.Clients["chatgpt"].MetadataURL, f.s.resource("chatgpt")); err != nil {
		t.Fatal(err)
	}
	f.s.cfg.Enabled = false
	w := f.request(t, "GET", "/v1/app/integrations/connections", "", f.browser, "")
	if w.Code != 200 {
		t.Fatalf("list failed: %d %s", w.Code, w.Body)
	}
	response := decodeMap(t, w)
	if response["available"] != false || response["writes_enabled"] != false {
		t.Fatal("disabled beta reported availability")
	}
	found := false
	for _, raw := range response["items"].([]any) {
		item := raw.(map[string]any)
		if item["id"] == identity.GrantID.String() {
			found = true
		}
	}
	if !found {
		t.Fatal("older active connection was hidden by feature flags or recent history")
	}
	w = f.request(t, "DELETE", "/v1/app/integrations/connections/"+identity.GrantID.String(), "", f.browser, "")
	if w.Code != 204 {
		t.Fatalf("disconnect failed: %d %s", w.Code, w.Body)
	}
	f.s.cfg.Enabled = true
	if _, err = f.s.Authenticate(ctx, pair["access_token"].(string), identity.Resource); err == nil {
		t.Fatal("disconnected grant resumed when beta was enabled")
	}
}

func TestMCPZeroExponentBusinessCurrency(t *testing.T) {
	f := integrationFixture(t)
	_, err := f.pool.Exec(context.Background(), `UPDATE client_profiles SET country_code='CI',currency_code='XOF',locale='fr-CI',timezone='Africa/Abidjan' WHERE client_id=$1`, f.provider)
	if err != nil {
		t.Fatal(err)
	}
	pair := f.link(t, "chatgpt", scopes)
	token := pair["access_token"].(string)
	for _, decimal := range []string{"1500", "1500.50"} {
		result := f.rpc(t, "chatgpt", token, "tools/call", map[string]any{"name": "create_service", "arguments": map[string]any{"idempotency_key": decimal, "input": map[string]any{"service_name": "XOF consultation", "duration_minutes": 45, "pricing": map[string]any{"price_amount": decimal}, "fulfillment": map[string]any{"mode": "virtual"}, "availability": map[string]any{"mode": "inherit_business_hours"}}}})["result"].(map[string]any)
		if decimal == "1500.50" {
			if result["isError"] != true {
				t.Fatal("fractional XOF accepted")
			}
			continue
		}
		data := result["structuredContent"].(map[string]any)["data"].(map[string]any)
		if data["currency_code"] != "XOF" || data["pricing"].(map[string]any)["price_amount_minor"] != "1500" {
			t.Fatal("XOF amount changed")
		}
	}
}
