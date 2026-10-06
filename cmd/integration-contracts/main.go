// Generate the provider integration contract from the service input/output models.
package main

import (
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/integrations"
	"booking/go-server/internal/money"
	"encoding/json"
	"os"
	"reflect"
	"strings"
)

var schemas = map[string]any{}

func schema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{schema(t.Elem()), map[string]any{"type": "null"}}}
	}
	if t == reflect.TypeOf(money.Minor(0)) {
		return map[string]any{"type": "string", "pattern": "^-?[0-9]+$", "description": "Quoted exact minor-unit integer in the business currency."}
	}
	if t.PkgPath() == "time" {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Struct:
		name := t.Name()
		if _, ok := schemas[name]; !ok {
			schemas[name] = map[string]any{}
			properties := map[string]any{}
			required := []string{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				tag := strings.Split(f.Tag.Get("json"), ",")
				if f.PkgPath != "" || tag[0] == "" || tag[0] == "-" {
					continue
				}
				properties[tag[0]] = schema(f.Type)
				if len(tag) == 1 {
					required = append(required, tag[0])
				}
			}
			value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
			if len(required) > 0 {
				value["required"] = required
			}
			schemas[name] = value
		}
		return map[string]any{"$ref": "#/components/schemas/" + name}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schema(t.Elem())}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int64, reflect.Uint8:
		return map[string]any{"type": "integer"}
	default:
		return map[string]any{"type": "string"}
	}
}
func object(properties map[string]any, required ...string) map[string]any {
	value := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		value["required"] = required
	}
	return value
}
func editable(s map[string]any) map[string]any {
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		target := "Editable" + name
		if _, ok := schemas[target]; !ok {
			schemas[target] = map[string]any{}
			schemas[target] = editable(schemas[name].(map[string]any))
		}
		return map[string]any{"$ref": "#/components/schemas/" + target}
	}
	copy := map[string]any{}
	for k, v := range s {
		if k == "required" {
			continue
		}
		copy[k] = v
	}
	if properties, ok := s["properties"].(map[string]any); ok {
		next := map[string]any{}
		for k, v := range properties {
			next[k] = editable(v.(map[string]any))
		}
		copy["properties"] = next
	}
	return copy
}
func jsonContent(s any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": s}}
}
func operation(summary string, response any, body any, mutation, existing bool) map[string]any {
	status := "200"
	if response == nil {
		status = "204"
	}
	out := map[string]any{"summary": summary, "security": []any{map[string]any{"ProviderSession": []string{}}}, "responses": map[string]any{status: map[string]any{"description": "Success", "content": jsonContent(response)}, "400": map[string]any{"description": "Validation error"}, "401": map[string]any{"description": "Authentication required"}, "403": map[string]any{"description": "Permission denied"}, "404": map[string]any{"description": "Owned resource not found"}, "409": map[string]any{"description": "Revision, idempotency, collection or dependency conflict"}}}
	if response == nil {
		out["responses"].(map[string]any)[status] = map[string]any{"description": "Success"}
	}
	if body != nil {
		out["requestBody"] = map[string]any{"required": true, "content": jsonContent(body)}
	}
	params := []any{}
	if mutation {
		params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header", "required": true, "schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "description": "Reuse for an identical retry; changed input with the same key conflicts."})
	}
	if existing {
		params = append(params, map[string]any{"name": "If-Match", "in": "header", "required": true, "schema": map[string]any{"type": "string"}, "description": "Quoted revision returned by the last read."})
	}
	if len(params) > 0 {
		out["parameters"] = params
	}
	return out
}
func main() {
	service := schema(reflect.TypeOf(appdata.ManagedServiceItem{}))
	section := schema(reflect.TypeOf(appdata.ServiceSectionItem{}))
	details := schema(reflect.TypeOf(appdata.ServiceSectionDetailsResponse{}))
	input := schema(reflect.TypeOf(appdata.CreateManagedServiceInput{}))
	sectionInput := schema(reflect.TypeOf(appdata.CreateServiceSectionInput{}))
	connection := schema(reflect.TypeOf(integrations.Connection{}))
	connectionProps := schemas["Connection"].(map[string]any)["properties"].(map[string]any)
	connectionProps["platform"] = map[string]any{"type": "string", "enum": []string{"chatgpt", "claude"}}
	connectionProps["status"] = map[string]any{"type": "string", "enum": []string{"connected", "disconnected", "expired"}}
	connectionProps["scopes"] = map[string]any{"type": "array", "items": map[string]string{"$ref": "#/components/schemas/CatalogScope"}}
	input = editable(input)
	sectionInput = editable(sectionInput)
	schemas["EditableCreateManagedServiceInput"].(map[string]any)["required"] = []string{"service_name", "duration_minutes", "pricing", "fulfillment", "availability"}
	schemas["EditableCreateServiceSectionInput"].(map[string]any)["required"] = []string{"name"}
	schemas["CatalogScope"] = map[string]any{"type": "string", "enum": []string{"catalog.read", "catalog.write", "catalog.publish", "catalog.delete"}}
	scope := map[string]any{"$ref": "#/components/schemas/CatalogScope"}
	scopeArray := map[string]any{"type": "array", "items": scope}
	schemas["Connections"] = object(map[string]any{"available": map[string]string{"type": "boolean"}, "writes_enabled": map[string]string{"type": "boolean"}, "items": map[string]any{"type": "array", "items": connection}}, "available", "writes_enabled", "items")
	schemas["ConsentRequest"] = object(map[string]any{"business": object(map[string]any{"id": map[string]string{"type": "string", "format": "uuid"}, "business_name": map[string]string{"type": "string"}}, "id", "business_name"), "request_id": map[string]string{"type": "string", "format": "uuid"}, "platform": map[string]any{"type": "string", "enum": []string{"chatgpt", "claude"}}, "scopes": scopeArray, "csrf_token": map[string]string{"type": "string"}, "writes_enabled": map[string]string{"type": "boolean"}}, "request_id", "platform", "scopes", "csrf_token", "writes_enabled", "business")
	schemas["ConsentDecision"] = object(map[string]any{"approve": map[string]string{"type": "boolean"}, "scopes": scopeArray, "csrf_token": map[string]string{"type": "string"}}, "approve", "scopes", "csrf_token")
	// PATCH root and nested objects are optional; arrays replace. Published status is separate.
	for _, pair := range []struct{ source, dest string }{{"CreateManagedServiceInput", "ServicePatch"}, {"CreateServiceSectionInput", "SectionPatch"}} {
		raw, _ := json.Marshal(editable(schemas[pair.source].(map[string]any)))
		var cloned map[string]any
		_ = json.Unmarshal(raw, &cloned)
		delete(cloned, "required")
		if pair.dest == "ServicePatch" {
			props := cloned["properties"].(map[string]any)
			delete(props, "publish_status")
			delete(props, "wizard_draft_id")
		}
		schemas[pair.dest] = cloned
	}
	paths := map[string]any{}
	paths["/v1/app/services"] = map[string]any{"get": operation("List owned services", object(map[string]any{"items": map[string]any{"type": "array", "items": service}}, "items"), nil, false, false), "post": operation("Create draft service", service, input, true, false)}

	paths["/v1/app/services/{id}"] = map[string]any{"get": operation("Read owned service", service, nil, false, false), "put": operation("Replace service fields; publication is preserved", service, input, true, true), "patch": operation("Merge supplied fields; arrays replace", service, map[string]string{"$ref": "#/components/schemas/ServicePatch"}, true, true), "delete": operation("Delete service; booking dependencies may block deletion", nil, nil, true, true)}
	paths["/v1/app/services/{id}/status"] = map[string]any{"patch": operation("Explicit publication action", service, object(map[string]any{"status": map[string]any{"type": "string", "enum": []string{"draft", "published", "paused"}}}, "status"), true, true)}
	paths["/v1/app/services/{id}/visibility"] = map[string]any{"patch": operation("Set hidden visibility", service, object(map[string]any{"is_hidden": map[string]string{"type": "boolean"}}, "is_hidden"), true, true)}
	paths["/v1/app/services/{id}/duplicate"] = map[string]any{"post": operation("Duplicate to draft", service, nil, true, true)}
	paths["/v1/app/service-sections"] = map[string]any{"get": operation("List owned sections", object(map[string]any{"items": map[string]any{"type": "array", "items": section}}, "items"), nil, false, false), "post": operation("Create section", section, sectionInput, true, false)}
	paths["/v1/app/service-sections/{id}"] = map[string]any{"get": operation("Read owned section", details, nil, false, false), "put": operation("Replace section fields", section, sectionInput, true, true), "patch": operation("Merge supplied section fields", section, map[string]string{"$ref": "#/components/schemas/SectionPatch"}, true, true), "delete": operation("Preserve services by moving or uncategorizing them", nil, nil, true, true)}
	sectionDelete := paths["/v1/app/service-sections/{id}"].(map[string]any)["delete"].(map[string]any)
	sectionDelete["parameters"] = append(sectionDelete["parameters"].([]any), map[string]any{"name": "mode", "in": "query", "required": true, "schema": map[string]any{"type": "string", "enum": []string{"move", "uncategorized"}}}, map[string]any{"name": "target_section_id", "in": "query", "schema": map[string]string{"type": "string", "format": "uuid"}, "description": "Required for move; must be another owned section."})
	for _, path := range []string{"/v1/app/services", "/v1/app/service-sections"} {
		if ops, ok := paths[path].(map[string]any); ok {
			op := ops["post"].(map[string]any)
			responses := op["responses"].(map[string]any)
			responses["201"] = responses["200"]
			delete(responses, "200")
		}
	}
	reorder := schema(reflect.TypeOf(appdata.ReorderItemsInput{}))
	schemas["ReorderItemsInput"].(map[string]any)["required"] = []string{"ordered_ids", "expected_revisions"}
	paths["/v1/app/service-sections/reorder"] = map[string]any{"put": operation("Reorder owned sections with a revision for every item", nil, reorder, true, false)}
	paths["/v1/app/service-sections/{id}/services/reorder"] = map[string]any{"put": operation("Reorder section services with section and item revisions", nil, reorder, true, true)}
	paths["/v1/app/services/uncategorized/reorder"] = map[string]any{"put": operation("Reorder uncategorized services with item revisions", nil, reorder, true, false)}
	paths["/v1/app/catalog-receipts/{id}"] = map[string]any{"get": operation("Read an owned mutation receipt", object(map[string]any{"receipt_id": map[string]string{"type": "string", "format": "uuid"}, "operation": map[string]string{"type": "string"}, "resource_id": map[string]string{"type": "string"}, "revision": map[string]string{"type": "integer"}, "data": map[string]string{"type": "object"}}, "receipt_id", "operation", "resource_id", "data"), nil, false, false)}
	paths["/v1/app/integrations/connections"] = map[string]any{"get": operation("List account connections", map[string]string{"$ref": "#/components/schemas/Connections"}, nil, false, false)}
	paths["/v1/app/integrations/connections/{id}"] = map[string]any{"delete": operation("Immediately revoke an owned connection", nil, nil, false, false)}
	paths["/v1/app/integrations/authorization-requests/{id}"] = map[string]any{"get": operation("Bind pending consent to signed-in account", map[string]string{"$ref": "#/components/schemas/ConsentRequest"}, nil, false, false), "post": operation("Approve selected scopes or deny; CSRF required", object(map[string]any{"redirect_uri": map[string]string{"type": "string", "format": "uri"}}, "redirect_uri"), map[string]string{"$ref": "#/components/schemas/ConsentDecision"}, false, false)}
	for path, ops := range paths {
		if strings.Contains(path, "{id}") {
			ops.(map[string]any)["parameters"] = []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]string{"type": "string", "format": "uuid"}}}
		}
	}
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp/chatgpt", "/.well-known/oauth-protected-resource/mcp/claude"} {
		op := operation("OAuth discovery", map[string]string{"type": "object"}, nil, false, false)
		delete(op, "security")
		paths[path] = map[string]any{"get": op}
	}
	authop := operation("Begin code + S256 PKCE + configured CIMD authorization", nil, nil, false, false)
	delete(authop, "security")
	params := []any{}
	for _, name := range []string{"response_type", "client_id", "redirect_uri", "resource", "scope", "state", "code_challenge", "code_challenge_method"} {
		params = append(params, map[string]any{"name": name, "in": "query", "required": name != "state", "schema": map[string]string{"type": "string"}})
	}
	authop["parameters"] = params
	authop["responses"] = map[string]any{"303": map[string]string{"description": "Browser consent or validated callback error"}, "400": map[string]string{"description": "Invalid platform/callback"}}
	paths["/oauth/authorize"] = map[string]any{"get": authop}
	for _, path := range []string{"/oauth/token", "/oauth/revoke"} {
		op := operation("Public-client form-encoded OAuth; no client secret", map[string]string{"type": "object"}, nil, false, false)
		delete(op, "security")
		props := map[string]any{}
		for _, name := range []string{"grant_type", "code", "code_verifier", "client_id", "redirect_uri", "resource", "refresh_token", "scope", "token"} {
			props[name] = map[string]string{"type": "string"}
		}
		op["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/x-www-form-urlencoded": map[string]any{"schema": object(props)}}}
		paths[path] = map[string]any{"post": op}
	}
	doc := map[string]any{"openapi": "3.1.0", "info": map[string]string{"title": "Tellbook catalog and provider integrations", "version": "0.1.0", "description": "Existing REST amounts remain minor-unit strings. MCP decimal amounts are converted exactly. Mutations require receipts and optimistic revision preconditions."}, "paths": paths, "components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"ProviderSession": map[string]string{"type": "http", "scheme": "bearer"}}}}
	data, e := json.MarshalIndent(doc, "", "  ")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile("contracts/provider-integrations.openapi.yaml", append(data, '\n'), 0644); e != nil {
		panic(e)
	}
}
