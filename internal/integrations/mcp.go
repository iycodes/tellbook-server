package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/money"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type toolDefinition struct {
	Name        string
	Description string
	Body        reflect.Type
}

// Only serveMCP can attach this identity, after validating the token and audience.
type identityContextKey struct{}

var catalogTools = []toolDefinition{
	{"get_connected_profile", "Identify the connected Tellbook business and its currency and timezone.", nil},
	{"list_services", "Find services in the connected account. Returns summaries with stable IDs and revisions, filters and pagination.", nil},
	{"get_service", "Read one owned service, including pricing, availability, policies, and its current revision.", nil},
	{"list_service_sections", "List the connected provider's service sections and revisions.", nil},
	{"get_service_section", "Read an owned section, its current revision and its services.", nil},
	{"get_service_setup_options", "Read valid sections, locations, agreement templates, business hours, currency and service setup choices.", nil},
	{"create_service", "Create a draft service. Supply the requested name, duration, pricing, fulfillment and availability; publishing is a separate action.", reflect.TypeOf(appdata.CreateManagedServiceInput{})},
	{"update_service", "Change only supplied service fields, preserving omitted fields. Nested fields merge; supplied arrays replace their collection. Publication is a separate action.", reflect.TypeOf(appdata.CreateManagedServiceInput{})},
	{"duplicate_service", "Copy an owned service and its settings into a new draft.", nil},
	{"set_service_visibility", "Set whether an owned service is hidden. This does not change publication status.", reflect.TypeOf(appdata.UpdateManagedServiceVisibilityInput{})},
	{"create_service_section", "Create a service section for the connected business.", reflect.TypeOf(appdata.CreateServiceSectionInput{})},
	{"update_service_section", "Change supplied section fields, preserving omitted fields.", reflect.TypeOf(appdata.UpdateServiceSectionInput{})},
	{"set_service_status", "Explicitly publish, pause, or return an owned service to draft. Publishing validates all required service settings.", nil},
	{"delete_service", "Delete an owned service. Referenced services may require pausing instead; booking history is preserved.", nil},
	{"delete_service_section", "Delete an owned section while preserving its services, either uncategorized or moved into another owned section.", reflect.TypeOf(appdata.DeleteServiceSectionInput{})},
}

const catalogInstructions = `Manage only the connected Tellbook business and the catalog actions the user requests.
Reuse the connected profile, setup choices, IDs and resource revisions already returned for this connection. Read the profile when the business or currency is unknown, setup options when valid choices are needed, and resource details when missing information is needed for the requested edit. A uniquely identified list summary with a revision is sufficient for a simple partial edit. Resolve ambiguous names with reads; never guess IDs or revisions. Refresh reads on a conflict or after switching connections.
Combine ordinary field changes to the same resource in one partial update. Use a successful mutation receipt's returned resource and revision for subsequent actions on that resource. Section membership or renames can also change related revisions; read affected resources when needed. Keep the original key, arguments and expected revision for an identical retry after a lost response; a changed request needs a new idempotency key.
Create and duplicate as drafts. Publication, pausing and deletion require explicit user intent, and section deletion requires an explicit choice for preserving its services. Use the host's approval flow for clear requests without adding a separate conversational confirmation; ask for missing choices. Respect denied consent and host permission settings. A missing permission requires fresh Tellbook consent; an ordinary edit with sufficient permissions does not require reconnecting. Money inputs are exact decimal strings in the business currency; outputs ending in _minor are quoted minor-unit integers.`

func boolPointer(v bool) *bool { return &v }
func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}
func fieldSchema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		inner := fieldSchema(t.Elem())
		return map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}
	}
	if t == reflect.TypeOf(money.Minor(0)) {
		return map[string]any{"type": "string", "pattern": "^[0-9]+(?:\\.[0-9]+)?$", "description": "Exact decimal amount in the connected business currency, for example 25.00. Do not use currency symbols or grouping separators."}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if f.PkgPath != "" || name == "" || name == "-" || name == "publish_status" || name == "wizard_draft_id" || name == "provider_location_label" {
				continue
			}
			props[strings.TrimSuffix(name, "_minor")] = fieldSchema(f.Type)
		}
		return objectSchema(props)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": fieldSchema(t.Elem()), "maxItems": 100}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	default:
		return map[string]any{"type": "string", "maxLength": 16000}
	}
}
func toolSchema(tool toolDefinition) map[string]any {
	props := map[string]any{}
	required := []string{}
	read := appdata.CatalogScope(tool.Name) == "catalog.read"
	if strings.Contains(tool.Name, "service_section") && tool.Name != "list_service_sections" && tool.Name != "create_service_section" {
		props["section_id"] = map[string]any{"type": "string", "format": "uuid"}
		required = append(required, "section_id")
	} else if slices.Contains([]string{"get_service", "update_service", "duplicate_service", "set_service_visibility", "set_service_status", "delete_service"}, tool.Name) {
		props["service_id"] = map[string]any{"type": "string", "format": "uuid"}
		required = append(required, "service_id")
	}
	if !read {
		props["idempotency_key"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
		required = append(required, "idempotency_key")
		if !strings.HasPrefix(tool.Name, "create_") {
			props["expected_revision"] = map[string]any{"type": "integer", "minimum": 1}
			required = append(required, "expected_revision")
		}
	}
	if tool.Body != nil {
		body := fieldSchema(tool.Body)
		switch tool.Name {
		case "create_service":
			body["required"] = []string{"service_name", "duration_minutes", "pricing", "fulfillment", "availability"}
		case "create_service_section":
			body["required"] = []string{"name"}
		case "set_service_visibility":
			body["required"] = []string{"is_hidden"}
		case "delete_service_section":
			body["required"] = []string{"mode"}
			body["properties"].(map[string]any)["mode"] = map[string]any{"type": "string", "enum": []string{"uncategorized", "move"}}
		}
		props["input"] = body
		required = append(required, "input")
	}
	if tool.Name == "set_service_status" {
		props["input"] = objectSchema(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"draft", "published", "paused"}}}, "status")
		required = append(required, "input")
	}
	if tool.Name == "list_services" {
		props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
		props["cursor"] = map[string]any{"type": "string", "maxLength": 64}
		props["search"] = map[string]any{"type": "string", "maxLength": 200}
		props["status"] = map[string]any{"type": "string", "enum": []string{"draft", "published", "paused"}}
		props["section_id"] = map[string]any{"type": "string", "format": "uuid"}
	}
	return objectSchema(props, required...)
}
func (s *Service) buildMCP() {
	for _, platform := range []string{"chatgpt", "claude"} {
		p := platform
		server := mcp.NewServer(&mcp.Implementation{Name: "tellbook", Version: "0.1.0"}, &mcp.ServerOptions{Instructions: catalogInstructions})
		for _, definition := range catalogTools {
			tool := definition
			schema := toolSchema(tool)
			compiler := jsonschema.NewCompiler()
			uri := "https://tellbook.local/schema/" + tool.Name
			encodedSchema, _ := json.Marshal(schema)
			var validationSchema any
			_ = json.Unmarshal(encodedSchema, &validationSchema)
			_ = compiler.AddResource(uri, validationSchema)
			validator, err := compiler.Compile(uri)
			if err != nil {
				panic(fmt.Errorf("tool schema %s: %w", tool.Name, err))
			}
			read := appdata.CatalogScope(tool.Name) == "catalog.read"
			destructive := !read && (p == "claude" || strings.HasPrefix(tool.Name, "delete_") || tool.Name == "update_service" || tool.Name == "update_service_section" || tool.Name == "set_service_status" || tool.Name == "set_service_visibility")
			meta := mcp.Meta{"securitySchemes": []map[string]any{{"type": "oauth2", "scopes": []string{appdata.CatalogScope(tool.Name)}}}}
			if tool.Name == "get_connected_profile" {
				meta["openai/profile"] = true
			}
			server.AddTool(&mcp.Tool{Name: tool.Name, Title: strings.ReplaceAll(tool.Name, "_", " "), Description: tool.Description, InputSchema: schema, OutputSchema: toolOutputSchema(tool.Name), Meta: meta, Annotations: &mcp.ToolAnnotations{Title: strings.ReplaceAll(tool.Name, "_", " "), ReadOnlyHint: read, DestructiveHint: boolPointer(destructive), IdempotentHint: !read, OpenWorldHint: boolPointer(false)}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return s.callTool(ctx, p, tool, validator, req), nil
			})
		}
		s.mcpHandlers[p] = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true})
	}
}
func (s *Service) authChallenge(platform, scope string) string {
	return `Bearer resource_metadata="` + s.cfg.PublicURL + `/.well-known/oauth-protected-resource/mcp/` + platform + `", scope="` + scope + `"`
}

// Offer the enabled permission groups together so the provider can make all
// optional choices in one consent screen. This is a request, never a grant:
// approve still requires each selected scope and rejects unrequested scopes.
func (s *Service) connectionChallenge(platform, code, description string) string {
	return s.authChallenge(platform, strings.Join(s.availableScopes(), " ")) + `, error="` + code + `", error_description="` + description + `"`
}
func (s *Service) permissionMessage(required string) string {
	return "This action requires " + required + ". Choose it and any other catalog permissions you need on the Tellbook consent screen."
}

// The SDK's loopback-only host guard cannot distinguish a trusted HTTPS tunnel.
// This explicit guard replaces it, retaining DNS-rebinding protection and allowing
// only the configured public authority and literal localhost authorities.
func allowedMCPHost(publicURL, authority string) bool {
	public, err := url.Parse(publicURL)
	if err == nil && public.Host != "" && authority == public.Host {
		return true
	}
	host := authority
	if parsed, _, err := net.SplitHostPort(authority); err == nil {
		host = parsed
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}
func (s *Service) serveMCP(w http.ResponseWriter, r *http.Request, platform string) {
	start := time.Now()
	if !s.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	if !allowedMCPHost(s.cfg.PublicURL, r.Host) {
		http.Error(w, "Unrecognized MCP host.", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.ClientURL && origin != s.cfg.PublicURL && origin != "https://chatgpt.com" && origin != "https://claude.ai" {
		http.Error(w, "Origin is not permitted", 403)
		return
	}
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		raw = ""
	}
	identity, err := s.Authenticate(r.Context(), raw, s.resource(platform))
	if err != nil {
		s.metrics.ObserveIntegration(platform, "authenticate", "authorization_failure", time.Since(start))
		w.Header().Set("WWW-Authenticate", s.connectionChallenge(platform, "invalid_token", "Connect your Tellbook account and choose the permissions you need."))
		oauthError(w, 401, "unauthorized", "Connect your Tellbook account.")
		return
	}
	// Claude starts permission expansion on an HTTP challenge before the SDK
	// wraps the call in a 200 tool result. ChatGPT uses the tool-result metadata.
	if platform == "claude" && r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		_ = r.Body.Close()
		if err != nil {
			oauthError(w, http.StatusRequestEntityTooLarge, "invalid_request", "MCP request is too large.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var call struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &call) == nil && call.Method == "tools/call" {
			for _, tool := range catalogTools {
				if call.Params.Name != tool.Name {
					continue
				}
				required := appdata.CatalogScope(tool.Name)
				if required != "catalog.read" && !s.cfg.WritesEnabled {
					// An unavailable beta feature cannot be enabled by consent.
					// Let the tool return writes_disabled without triggering relinking.
					break
				}
				if !slices.Contains(identity.Scopes, required) {
					s.metrics.ObserveIntegration(platform, tool.Name, "insufficient_scope", time.Since(start))
					message := s.permissionMessage(required)
					w.Header().Set("WWW-Authenticate", s.connectionChallenge(platform, "insufficient_scope", message))
					oauthError(w, http.StatusForbidden, "insufficient_scope", message)
					return
				}
				break
			}
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity))
	s.mcpHandlers[platform].ServeHTTP(w, r)
}
func toolResult(value any, err error) *mcp.CallToolResult {
	if err != nil {
		var public *appdata.CatalogError
		if errors.As(err, &public) {
			value = map[string]any{"error": public}
		} else {
			value = map[string]any{"error": map[string]string{"code": "operation_failed", "message": "Could not complete this operation. Try again."}}
		}
	}
	encoded, _ := json.Marshal(value)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}}, StructuredContent: value, IsError: err != nil}
}
func (s *Service) callTool(ctx context.Context, platform string, tool toolDefinition, validator *jsonschema.Schema, req *mcp.CallToolRequest) (result *mcp.CallToolResult) {
	start := time.Now()
	defer func() {
		outcome := "success"
		if result != nil && result.IsError {
			outcome = "error"
			if contents, ok := result.StructuredContent.(map[string]any); ok {
				if ce, ok := contents["error"].(*appdata.CatalogError); ok {
					outcome = ce.Code
				}
			}
		}
		s.metrics.ObserveIntegration(platform, tool.Name, outcome, time.Since(start))
	}()
	var arguments map[string]any
	if req.Extra == nil {
		return toolResult(nil, &appdata.CatalogError{Code: "unauthorized", Message: "Connect your account."})
	}
	identity, ok := ctx.Value(identityContextKey{}).(Identity)
	if !ok || identity.Resource != s.resource(platform) {
		return toolResult(nil, &appdata.CatalogError{Code: "unauthorized", Message: "Reconnect your Tellbook account."})
	}
	var err error
	scope := appdata.CatalogScope(tool.Name)
	if scope != "catalog.read" && !s.cfg.WritesEnabled {
		return toolResult(nil, &appdata.CatalogError{Code: "writes_disabled", Message: "Write actions are not enabled for this beta."})
	}
	if !slices.Contains(identity.Scopes, scope) {
		message := s.permissionMessage(scope)
		result := toolResult(nil, &appdata.CatalogError{Code: "insufficient_scope", Message: message})
		result.Meta = mcp.Meta{"mcp/www_authenticate": []string{s.connectionChallenge(platform, "insufficient_scope", message)}}
		return result
	}
	if err = json.Unmarshal(req.Params.Arguments, &arguments); err != nil {
		return toolResult(nil, &appdata.CatalogError{Code: "invalid_request", Message: "Invalid tool arguments."})
	}
	if err = validator.Validate(arguments); err != nil {
		return toolResult(nil, &appdata.CatalogError{Code: "invalid_request", Message: err.Error()})
	}
	id := uuid.Nil
	for _, field := range []string{"service_id", "section_id"} {
		if raw, ok := arguments[field].(string); ok {
			id, err = uuid.Parse(raw)
			if err != nil {
				return toolResult(nil, &appdata.CatalogError{Code: "invalid_id", Message: "Use an ID returned by Tellbook."})
			}
		}
	}
	var value any
	switch tool.Name {
	case "get_connected_profile":
		value, err = s.catalog.CatalogProfile(ctx, identity.ProviderID)
	case "get_service_setup_options":
		value, err = s.catalog.CatalogSetup(ctx, identity.ProviderID)
	case "list_services":
		value, err = s.listServices(ctx, identity, arguments)
	case "list_service_sections":
		var items any
		items, err = s.catalog.ReadCatalog(ctx, identity.ProviderID, tool.Name, id)
		value = map[string]any{"items": items}
	case "get_service", "get_service_section":
		value, err = s.catalog.ReadCatalog(ctx, identity.ProviderID, tool.Name, id)
	default:
		body, ok := arguments["input"].(map[string]any)
		if !ok {
			body = map[string]any{}
		}
		if tool.Body != nil {
			if err = convertAmounts(body, tool.Body, memoizedExponent(func() (uint8, error) { return s.catalog.CatalogExponent(ctx, identity.ProviderID) })); err != nil {
				return toolResult(nil, &appdata.CatalogError{Code: "invalid_money", Message: err.Error()})
			}
		}
		input, _ := json.Marshal(body)
		revision := int64(0)
		if v, ok := arguments["expected_revision"].(float64); ok {
			revision = int64(v)
		}
		key, _ := arguments["idempotency_key"].(string)
		receipt, mutationErr := s.catalog.MutateCatalog(ctx, identity.Actor(), tool.Name, id, input, revision, key)
		value, err = receipt, mutationErr
		if receipt.Replayed {
			s.metrics.ObserveIntegration(platform, tool.Name, "retry", 0)
		}
	}
	if err == nil {
		_, _ = s.db.Exec(ctx, `UPDATE integration_grants SET last_used_at=now() WHERE id=$1 AND (last_used_at IS NULL OR last_used_at<now()-interval '1 minute')`, identity.GrantID)
	}
	return toolResult(value, err)
}

func memoizedExponent(load func() (uint8, error)) func() (uint8, error) {
	var loaded bool
	var value uint8
	var err error
	return func() (uint8, error) {
		if !loaded {
			value, err = load()
			loaded = true
		}
		return value, err
	}
}
func convertAmounts(input map[string]any, t reflect.Type, exponent func() (uint8, error)) error {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		wire := strings.TrimSuffix(name, "_minor")
		value, exists := input[wire]
		if !exists {
			continue
		}
		if field.Type == reflect.TypeOf(money.Minor(0)) {
			decimal, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s must be a decimal string", wire)
			}
			scale, err := exponent()
			if err != nil {
				return err
			}
			minor, err := money.ParseDecimal(decimal, scale)
			if err != nil || minor < 0 {
				return fmt.Errorf("%s is invalid for the business currency", wire)
			}
			delete(input, wire)
			input[name] = strconv.FormatInt(minor, 10)
		} else if child, ok := value.(map[string]any); ok && field.Type.Kind() == reflect.Struct {
			if err := convertAmounts(child, field.Type, exponent); err != nil {
				return err
			}
		} else if array, ok := value.([]any); ok && field.Type.Kind() == reflect.Slice && field.Type.Elem().Kind() == reflect.Struct {
			for _, element := range array {
				if child, ok := element.(map[string]any); ok {
					if err := convertAmounts(child, field.Type.Elem(), exponent); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (s *Service) listServices(ctx context.Context, identity Identity, input map[string]any) (any, error) {
	limit := 25
	if v, ok := input["limit"].(float64); ok {
		limit = int(v)
	}
	cursor, _ := input["cursor"].(string)
	search, _ := input["search"].(string)
	status, _ := input["status"].(string)
	section, _ := input["section_id"].(string)
	return s.catalog.ListCatalogServices(ctx, identity.ProviderID, limit, cursor, search, status, section)
}
