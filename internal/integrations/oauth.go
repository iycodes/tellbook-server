package integrations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/auth"
	"booking/go-server/internal/observability"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var scopes = []string{"catalog.read", "catalog.write", "catalog.publish", "catalog.delete"}

type clientDocument struct {
	ClientID     string   `json:"client_id"`
	RedirectURIs []string `json:"redirect_uris"`
	AuthMethod   string   `json:"token_endpoint_auth_method"`
	AuthMethods  []string `json:"token_endpoint_auth_methods_supported"`
}

func (d clientDocument) supportsPublicPKCE() bool {
	if len(d.AuthMethods) > 0 {
		return slices.Contains(d.AuthMethods, "none")
	}
	return d.AuthMethod == "none"
}

type cachedClient struct {
	Document clientDocument
	Expires  time.Time
}
type Service struct {
	db          *pgxpool.Pool
	metrics     *observability.Metrics
	catalog     *appdata.Handler
	auth        *auth.Handler
	cfg         Config
	httpClient  *http.Client
	clients     sync.Map
	mcpHandlers map[string]http.Handler
}
type Identity struct {
	ProviderID       uuid.UUID
	GrantID          uuid.UUID
	Platform         string
	Resource         string
	Scopes           []string
	SecurityRevision int64
}

func (i Identity) Actor() appdata.CatalogActor {
	return appdata.CatalogActor{ProviderID: i.ProviderID, GrantID: i.GrantID, Resource: i.Resource, SecurityRevision: i.SecurityRevision}
}
func New(db *pgxpool.Pool, catalog *appdata.Handler, authHandler *auth.Handler, cfg Config) *Service {
	s := &Service{db: db, catalog: catalog, auth: authHandler, cfg: cfg, httpClient: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, mcpHandlers: map[string]http.Handler{}}
	s.buildMCP()
	return s
}
func (s *Service) resource(platform string) string { return s.cfg.PublicURL + "/mcp/" + platform }
func (s *Service) allowed(id uuid.UUID) bool {
	return s.cfg.Enabled && id != uuid.Nil && (s.cfg.AllProviders || s.cfg.Providers[id])
}
func (s *Service) Routes(r chi.Router) {
	r.Get("/.well-known/oauth-authorization-server", s.discovery)
	for _, platform := range []string{"chatgpt", "claude"} {
		p := platform
		r.Get("/.well-known/oauth-protected-resource/mcp/"+p, func(w http.ResponseWriter, r *http.Request) { s.resourceDiscovery(w, r, p) })
		r.Handle("/mcp/"+p, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serveMCP(w, r, p) }))
	}
	r.Get("/oauth/authorize", s.observeOAuth("authorize", s.authorize))
	r.Post("/oauth/token", s.observeOAuth("token", s.token))
	r.Post("/oauth/revoke", s.observeOAuth("revoke", s.revoke))
	r.Route("/v1/app/integrations", func(r chi.Router) {
		r.Use(s.auth.AuthMiddleware())
		r.Get("/connections", s.connections)
		r.Delete("/connections/{id}", s.disconnect)
		r.Get("/authorization-requests/{id}", s.consent)
		r.Post("/authorization-requests/{id}", s.approve)
	})
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func oauthError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]string{"error": code, "error_description": message})
}
func randomToken() string {
	v := make([]byte, 32)
	if _, e := rand.Read(v); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(v)
}
func digest(raw string) []byte         { hash := sha256.Sum256([]byte(raw)); return hash[:] }
func challenge(verifier string) string { return base64.RawURLEncoding.EncodeToString(digest(verifier)) }
func validVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}
func parseScopes(raw string) ([]string, error) {
	out := []string{"catalog.read"}
	for _, v := range strings.Fields(raw) {
		if v == "offline_access" {
			continue
		}
		if !slices.Contains(scopes, v) {
			return nil, fmt.Errorf("unsupported scope")
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *Service) availableScopes() []string {
	if !s.cfg.WritesEnabled {
		return []string{"catalog.read"}
	}
	return slices.Clone(scopes)
}
func (s *Service) discovery(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	jsonResponse(w, 200, map[string]any{"issuer": s.cfg.PublicURL, "authorization_endpoint": s.cfg.PublicURL + "/oauth/authorize", "token_endpoint": s.cfg.PublicURL + "/oauth/token", "revocation_endpoint": s.cfg.PublicURL + "/oauth/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "client_id_metadata_document_supported": true, "token_endpoint_auth_methods_supported": []string{"none"}, "authorization_response_iss_parameter_supported": true, "scopes_supported": append(s.availableScopes(), "offline_access")})
}
func (s *Service) resourceDiscovery(w http.ResponseWriter, r *http.Request, platform string) {
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	jsonResponse(w, 200, map[string]any{"resource": s.resource(platform), "authorization_servers": []string{s.cfg.PublicURL}, "scopes_supported": s.availableScopes(), "bearer_methods_supported": []string{"header"}})
}
func (s *Service) client(ctx context.Context, platform, id, redirect string) error {
	configured := s.cfg.Clients[platform]
	if configured.MetadataURL == "" || id != configured.MetadataURL || redirect != configured.RedirectURI {
		return fmt.Errorf("client or redirect URI is not configured")
	}
	if cached, ok := s.clients.Load(id); ok {
		entry := cached.(cachedClient)
		if time.Now().Before(entry.Expires) && entry.Document.supportsPublicPKCE() && slices.Contains(entry.Document.RedirectURIs, redirect) {
			return nil
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	resp, e := s.httpClient.Do(req)
	if e != nil {
		return fmt.Errorf("client metadata is unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("client metadata is unavailable")
	}
	var document clientDocument
	if e = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&document); e != nil || document.ClientID != id || !document.supportsPublicPKCE() || !slices.Contains(document.RedirectURIs, redirect) {
		return fmt.Errorf("client metadata does not authorize this redirect")
	}
	s.clients.Store(id, cachedClient{document, time.Now().Add(time.Hour)})
	return nil
}
func (s *Service) authorize(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	platform := ""
	for _, p := range []string{"chatgpt", "claude"} {
		if q.Get("resource") == s.resource(p) {
			platform = p
		}
	}
	if platform == "" || s.client(r.Context(), platform, q.Get("client_id"), q.Get("redirect_uri")) != nil {
		oauthError(w, 400, "invalid_request", "Use a configured platform connection and exact callback URL.")
		return
	}
	callbackError := func(code, message string) {
		u, _ := url.Parse(q.Get("redirect_uri"))
		v := u.Query()
		v.Set("error", code)
		v.Set("error_description", message)
		v.Set("state", q.Get("state"))
		v.Set("iss", s.cfg.PublicURL)
		u.RawQuery = v.Encode()
		http.Redirect(w, r, u.String(), 303)
	}
	requested, e := parseScopes(q.Get("scope"))
	hash, e2 := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if e != nil {
		callbackError("invalid_scope", "Unsupported permission.")
		return
	}
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || e2 != nil || len(hash) != 32 || len(q.Get("state")) > 4096 {
		callbackError("invalid_request", "Authorization code with S256 PKCE is required.")
		return
	}
	id := uuid.New()
	_, e = s.db.Exec(r.Context(), `INSERT INTO integration_authorization_requests(id,platform,client_id,redirect_uri,resource,scopes,state,code_challenge,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()+interval '10 minutes')`, id, platform, q.Get("client_id"), q.Get("redirect_uri"), q.Get("resource"), requested, q.Get("state"), q.Get("code_challenge"))
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try connecting again.")
		return
	}
	http.Redirect(w, r, s.cfg.ClientURL+"/connect/authorize?request_id="+id.String(), 303)
}
func (s *Service) consent(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if !s.allowed(user.ID) {
		oauthError(w, 403, "access_denied", "Integrations are not enabled for this account.")
		return
	}
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		oauthError(w, 404, "invalid_request", "Connection request was not found.")
		return
	}
	nonce := randomToken()
	var platform string
	var requested []string
	e = s.db.QueryRow(r.Context(), `UPDATE integration_authorization_requests SET provider_id=$2,csrf_hash=$3 WHERE id=$1 AND expires_at>now() AND decided_at IS NULL AND (provider_id IS NULL OR provider_id=$2) RETURNING platform,scopes`, id, user.ID, digest(nonce)).Scan(&platform, &requested)
	if e != nil {
		oauthError(w, 410, "invalid_request", "This connection request expired or was already used.")
		return
	}
	profile, err := s.catalog.CatalogProfile(r.Context(), user.ID)
	if err != nil {
		oauthError(w, 503, "temporarily_unavailable", "Business identity is unavailable.")
		return
	}
	jsonResponse(w, 200, map[string]any{"business": map[string]any{"id": profile["id"], "business_name": profile["business_name"]}, "request_id": id.String(), "platform": platform, "scopes": requested, "csrf_token": nonce, "writes_enabled": s.cfg.WritesEnabled})
}
func (s *Service) approve(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	if !s.allowed(user.ID) {
		oauthError(w, 403, "access_denied", "Integration access is unavailable.")
		return
	}
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		oauthError(w, 400, "invalid_request", "Invalid request ID.")
		return
	}
	var decision struct {
		Approve bool     `json:"approve"`
		Scopes  []string `json:"scopes"`
		CSRF    string   `json:"csrf_token"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&decision) != nil || decoder.Decode(new(any)) != io.EOF {
		oauthError(w, 400, "invalid_request", "Invalid consent decision.")
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	defer tx.Rollback(r.Context())
	var requested []string
	var csrf []byte
	var redirect, state string
	e = tx.QueryRow(r.Context(), `SELECT scopes,csrf_hash,redirect_uri,state FROM integration_authorization_requests WHERE id=$1 AND provider_id=$2 AND expires_at>now() AND decided_at IS NULL FOR UPDATE`, id, user.ID).Scan(&requested, &csrf, &redirect, &state)
	if e != nil || subtle.ConstantTimeCompare(csrf, digest(decision.CSRF)) != 1 {
		oauthError(w, 403, "access_denied", "This approval is invalid or expired. Restart the connection.")
		return
	}
	approved := []string{}
	if decision.Approve {
		approved, e = parseScopes(strings.Join(decision.Scopes, " "))
		if e != nil {
			oauthError(w, 400, "invalid_scope", "Invalid permission.")
			return
		}
		for _, scope := range approved {
			if !slices.Contains(requested, scope) || (!s.cfg.WritesEnabled && scope != "catalog.read") {
				oauthError(w, 400, "invalid_scope", "This permission is not available in this request.")
				return
			}
		}
	}
	u, _ := url.Parse(redirect)
	q := u.Query()
	q.Set("state", state)
	q.Set("iss", s.cfg.PublicURL)
	code := ""
	if decision.Approve {
		code = randomToken()
		q.Set("code", code)
	} else {
		q.Set("error", "access_denied")
	}
	var codeHash []byte
	if decision.Approve {
		codeHash = digest(code)
	}
	_, e = tx.Exec(r.Context(), `UPDATE integration_authorization_requests SET decided_at=now(),approved_scopes=$2,security_revision=$3,code_hash=$4,code_expires_at=now()+interval '10 minutes' WHERE id=$1`, id, approved, user.SecurityRevision, codeHash)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try connecting again.")
		return
	}
	u.RawQuery = q.Encode()
	jsonResponse(w, 200, map[string]string{"redirect_uri": u.String()})
}
func (s *Service) token(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		oauthError(w, 400, "invalid_request", "Use a form-encoded token request.")
		return
	}
	if r.Header.Get("Authorization") != "" || r.PostForm.Get("client_secret") != "" || r.PostForm.Get("client_assertion") != "" || r.PostForm.Get("client_assertion_type") != "" {
		oauthError(w, 400, "invalid_client", "This connection uses public-client PKCE.")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchange(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		oauthError(w, 400, "unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
}
func (s *Service) exchange(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	defer tx.Rollback(r.Context())
	var provider, id uuid.UUID
	var platform, client, redirect, resource, pkce string
	var approved []string
	var revision int64
	e = tx.QueryRow(r.Context(), `SELECT id,provider_id,platform,client_id,redirect_uri,resource,code_challenge,approved_scopes,security_revision FROM integration_authorization_requests WHERE code_hash=$1 AND decided_at IS NOT NULL AND consumed_at IS NULL AND code_expires_at>now()`, digest(f.Get("code"))).Scan(&id, &provider, &platform, &client, &redirect, &resource, &pkce, &approved, &revision)
	if e != nil || f.Get("code") == "" || !validVerifier(f.Get("code_verifier")) || subtle.ConstantTimeCompare([]byte(pkce), []byte(challenge(f.Get("code_verifier")))) != 1 || client != f.Get("client_id") || redirect != f.Get("redirect_uri") || resource != f.Get("resource") || !s.allowed(provider) {
		oauthError(w, 400, "invalid_grant", "The authorization code or connection is invalid.")
		return
	}
	var current int64
	e = tx.QueryRow(r.Context(), `SELECT security_revision FROM clients WHERE id=$1 FOR UPDATE`, provider).Scan(&current)
	if e != nil || current != revision {
		oauthError(w, 400, "invalid_grant", "Sign in and reconnect your account.")
		return
	}
	tag, e := tx.Exec(r.Context(), `UPDATE integration_authorization_requests SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL`, id)
	if e != nil || tag.RowsAffected() != 1 {
		oauthError(w, 400, "invalid_grant", "This code was already used.")
		return
	}
	grant := uuid.New()
	_, e = tx.Exec(r.Context(), `INSERT INTO integration_grants(id,provider_id,platform,client_id,resource,scopes,security_revision,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,now()+interval '30 days')`, grant, provider, platform, client, resource, approved, revision)
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	s.issue(w, r, tx, grant, approved)
}
func (s *Service) issue(w http.ResponseWriter, r *http.Request, tx pgx.Tx, grant uuid.UUID, permissions []string) {
	access, refresh := randomToken(), randomToken()
	var grantExpiry time.Time
	e := tx.QueryRow(r.Context(), `SELECT expires_at FROM integration_grants WHERE id=$1`, grant).Scan(&grantExpiry)
	accessExpiry := time.Now().Add(15 * time.Minute)
	if grantExpiry.Before(accessExpiry) {
		accessExpiry = grantExpiry
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO integration_tokens(token_hash,grant_id,kind,expires_at) VALUES($1,$2,'access',$3),($4,$2,'refresh',$5)`, digest(access), grant, accessExpiry, digest(refresh), grantExpiry)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	jsonResponse(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": int(time.Until(accessExpiry).Seconds()), "scope": strings.Join(permissions, " ")})
}
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	f := r.PostForm
	hash := digest(f.Get("refresh_token"))
	var provider, grant uuid.UUID
	e := s.db.QueryRow(r.Context(), `SELECT g.provider_id,g.id FROM integration_tokens t JOIN integration_grants g ON g.id=t.grant_id WHERE t.token_hash=$1 AND t.kind='refresh'`, hash).Scan(&provider, &grant)
	if e != nil || !s.allowed(provider) {
		oauthError(w, 400, "invalid_grant", "Reconnect your Tellbook account.")
		return
	}
	tx, e := s.db.Begin(r.Context())
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	defer tx.Rollback(r.Context())
	var current int64
	e = tx.QueryRow(r.Context(), `SELECT security_revision FROM clients WHERE id=$1 FOR UPDATE`, provider).Scan(&current)
	if e != nil {
		oauthError(w, 400, "invalid_grant", "Reconnect your account.")
		return
	}
	var consumed *time.Time
	var expires time.Time
	var resource, client string
	var permissions []string
	var revision int64
	var revoked *time.Time
	e = tx.QueryRow(r.Context(), `SELECT t.consumed_at,LEAST(t.expires_at,g.expires_at),g.resource,g.client_id,g.scopes,g.security_revision,g.revoked_at FROM integration_tokens t JOIN integration_grants g ON g.id=t.grant_id WHERE t.token_hash=$1 AND t.kind='refresh' FOR UPDATE OF t,g`, hash).Scan(&consumed, &expires, &resource, &client, &permissions, &revision, &revoked)
	if e != nil || client != f.Get("client_id") || resource != f.Get("resource") {
		oauthError(w, 400, "invalid_grant", "Invalid refresh token or resource.")
		return
	}
	if consumed != nil {
		_, _ = tx.Exec(r.Context(), `UPDATE integration_grants SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, grant)
		_ = tx.Commit(r.Context())
		oauthError(w, 400, "invalid_grant", "Refresh token reuse detected. Reconnect your account.")
		return
	}
	if revoked != nil || time.Now().After(expires) || revision != current {
		oauthError(w, 400, "invalid_grant", "Reconnect your Tellbook account.")
		return
	}
	if requested := f.Get("scope"); requested != "" {
		narrowed, e := parseScopes(requested)
		if e != nil || !subset(narrowed, permissions) {
			oauthError(w, 400, "invalid_scope", "Refresh cannot expand permissions.")
			return
		}
		permissions = narrowed
		_, e = tx.Exec(r.Context(), `UPDATE integration_grants SET scopes=$2 WHERE id=$1`, grant, permissions)
		if e != nil {
			oauthError(w, 503, "temporarily_unavailable", "Try again.")
			return
		}
	}
	if _, e = tx.Exec(r.Context(), `UPDATE integration_tokens SET consumed_at=now() WHERE token_hash=$1`, hash); e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	s.issue(w, r, tx, grant, permissions)
}
func subset(values, allowed []string) bool {
	for _, value := range values {
		if !slices.Contains(allowed, value) {
			return false
		}
	}
	return true
}
func (s *Service) Authenticate(ctx context.Context, raw, resource string) (Identity, error) {
	var i Identity
	if len(raw) > 1024 || raw == "" {
		return i, errors.New("missing token")
	}
	e := s.db.QueryRow(ctx, `SELECT g.provider_id,g.id,g.platform,g.resource,g.scopes,g.security_revision FROM integration_tokens t JOIN integration_grants g ON g.id=t.grant_id JOIN clients c ON c.id=g.provider_id WHERE t.token_hash=$1 AND t.kind='access' AND t.expires_at>now() AND g.expires_at>now() AND g.revoked_at IS NULL AND g.security_revision=c.security_revision AND g.resource=$2`, digest(raw), resource).Scan(&i.ProviderID, &i.GrantID, &i.Platform, &i.Resource, &i.Scopes, &i.SecurityRevision)
	if e != nil || !s.allowed(i.ProviderID) {
		return Identity{}, errors.New("invalid token")
	}
	return i, nil
}
func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if r.ParseForm() != nil || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		oauthError(w, 400, "invalid_request", "Invalid revocation request.")
		return
	}
	_, e := s.db.Exec(r.Context(), `UPDATE integration_grants g SET revoked_at=COALESCE(revoked_at,now()) FROM integration_tokens t WHERE g.id=t.grant_id AND t.token_hash=$1 AND g.client_id=$2`, digest(r.PostForm.Get("token")), r.PostForm.Get("client_id"))
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Try again.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
}

type Connection struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Scopes     []string   `json:"scopes"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (s *Service) connections(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	items := []Connection{}
	// Retain every active grant and a bounded recent history. Providers must be
	// able to disconnect older grants even when beta availability is turned off.
	rows, e := s.db.Query(r.Context(), `SELECT id,platform,scopes,CASE WHEN revoked_at IS NOT NULL THEN 'disconnected' WHEN expires_at<=now() OR security_revision<>$2 THEN 'expired' ELSE 'connected' END,created_at,expires_at,last_used_at FROM integration_grants WHERE provider_id=$1 AND ((revoked_at IS NULL AND expires_at>now() AND security_revision=$2) OR id IN (SELECT id FROM integration_grants WHERE provider_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100)) ORDER BY created_at DESC,id DESC`, user.ID, user.SecurityRevision)
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Could not load connections.")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var item Connection
		if e = rows.Scan(&item.ID, &item.Platform, &item.Scopes, &item.Status, &item.CreatedAt, &item.ExpiresAt, &item.LastUsedAt); e != nil {
			break
		}
		items = append(items, item)
	}
	if e != nil || rows.Err() != nil {
		oauthError(w, 503, "temporarily_unavailable", "Could not load connections.")
		return
	}
	jsonResponse(w, 200, map[string]any{"available": s.allowed(user.ID), "writes_enabled": s.allowed(user.ID) && s.cfg.WritesEnabled, "items": items})
}
func (s *Service) disconnect(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	id, e := uuid.Parse(chi.URLParam(r, "id"))
	if e != nil {
		oauthError(w, 404, "not_found", "Connection was not found.")
		return
	}
	tag, e := s.db.Exec(r.Context(), `UPDATE integration_grants SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND provider_id=$2`, id, user.ID)
	if e != nil {
		oauthError(w, 503, "temporarily_unavailable", "Could not disconnect.")
		return
	}
	if tag.RowsAffected() == 0 {
		oauthError(w, 404, "not_found", "Connection was not found.")
		return
	}
	w.WriteHeader(204)
}

func (s *Service) SetMetrics(metrics *observability.Metrics) *Service { s.metrics = metrics; return s }

type oauthResponse struct {
	http.ResponseWriter
	status int
}

func (w *oauthResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (s *Service) observeOAuth(operation string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		record := &oauthResponse{ResponseWriter: w, status: 200}
		next(record, r)
		outcome := "success"
		if record.status >= 400 {
			outcome = "authorization_failure"
		}
		if operation == "authorize" {
			if callback, err := url.Parse(record.Header().Get("Location")); err == nil && callback.Query().Get("error") != "" {
				outcome = "authorization_failure"
			}
		}
		s.metrics.ObserveIntegration("oauth", operation, outcome, time.Since(start))
	}
}
