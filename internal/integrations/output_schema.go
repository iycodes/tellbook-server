package integrations

import (
	"booking/go-server/internal/appdata"
	"booking/go-server/internal/money"
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

// Outputs retain REST's quoted minor units; only MCP monetary inputs are decimal.
func outputField(t reflect.Type) map[string]any {
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == reflect.TypeOf(money.Minor(0)) {
		return map[string]any{"type": "string", "pattern": "^-?[0-9]+$"}
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return map[string]any{"type": "object"}
	}
	if t.Kind() == reflect.Pointer {
		return map[string]any{"anyOf": []any{outputField(t.Elem()), map[string]string{"type": "null"}}}
	}
	switch t.Kind() {
	case reflect.Struct:
		props := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if f.PkgPath != "" || name == "" || name == "-" {
				continue
			}
			props[name] = outputField(f.Type)
			if len(tag) == 1 {
				required = append(required, name)
			}
		}
		return objectSchema(props, required...)
	case reflect.Slice:
		return map[string]any{"type": "array", "items": outputField(t.Elem())}
	case reflect.Int, reflect.Int64, reflect.Uint8:
		return map[string]any{"type": "integer"}
	case reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	default:
		return map[string]any{"type": "string"}
	}
}
func profileOutputSchema() map[string]any {
	props := map[string]any{}
	for _, key := range []string{"id", "business_name", "handle_slug", "timezone", "country_code", "currency_code"} {
		props[key] = map[string]string{"type": "string"}
	}
	props["minor_unit_exponent"] = map[string]string{"type": "integer"}
	props["market_configured"] = map[string]string{"type": "boolean"}
	return objectSchema(props, "id", "business_name", "handle_slug", "timezone", "country_code", "currency_code", "minor_unit_exponent", "market_configured")
}
func toolSuccessSchema(name string) map[string]any {
	service := outputField(reflect.TypeOf(appdata.ManagedServiceItem{}))
	section := outputField(reflect.TypeOf(appdata.ServiceSectionItem{}))
	array := func(item any) any { return map[string]any{"type": "array", "items": item} }
	switch name {
	case "get_connected_profile":
		return profileOutputSchema()
	case "get_service":
		return service
	case "get_service_section":
		return outputField(reflect.TypeOf(appdata.ServiceSectionDetailsResponse{}))
	case "list_service_sections":
		return objectSchema(map[string]any{"items": array(section)}, "items")
	case "list_services":
		summary := objectSchema(map[string]any{"id": map[string]string{"type": "string"}, "name": map[string]string{"type": "string"}, "revision": map[string]string{"type": "integer"}, "status": map[string]string{"type": "string"}, "is_hidden": map[string]string{"type": "boolean"}, "section_id": map[string]string{"type": "string"}, "currency_code": map[string]string{"type": "string"}, "price_amount_minor": map[string]string{"type": "string"}, "duration_minutes": map[string]string{"type": "integer"}}, "id", "name", "revision", "status", "is_hidden", "section_id", "currency_code", "price_amount_minor", "duration_minutes")
		return objectSchema(map[string]any{"items": array(summary), "next_cursor": map[string]string{"type": "string"}, "total": map[string]string{"type": "integer"}}, "items", "next_cursor", "total")
	case "get_service_setup_options":
		return objectSchema(map[string]any{"profile": profileOutputSchema(), "sections": array(section), "locations": array(outputField(reflect.TypeOf(appdata.BusinessLocationItem{}))), "business_hours": outputField(reflect.TypeOf(appdata.BusinessHoursResponse{})), "agreement_templates": array(objectSchema(map[string]any{"id": map[string]string{"type": "string"}, "title": map[string]string{"type": "string"}}, "id", "title")), "fulfillment_modes": array(map[string]string{"type": "string"}), "availability_modes": array(map[string]string{"type": "string"})}, "profile", "sections", "locations", "business_hours", "agreement_templates", "fulfillment_modes", "availability_modes")
	default:
		data := service
		if strings.Contains(name, "service_section") {
			data = section
		}
		if strings.HasPrefix(name, "delete_") {
			data = objectSchema(map[string]any{"id": map[string]string{"type": "string"}, "deleted": map[string]string{"type": "boolean"}}, "id", "deleted")
		}
		props := map[string]any{"receipt_id": map[string]string{"type": "string"}, "operation": map[string]string{"type": "string"}, "resource_id": map[string]string{"type": "string"}, "revision": map[string]string{"type": "integer"}, "data": data}
		return objectSchema(props, "receipt_id", "operation", "resource_id", "data")
	}
}

func toolOutputSchema(name string) map[string]any {
	failure := objectSchema(map[string]any{"error": objectSchema(map[string]any{"code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}}, "code", "message")}, "error")
	return map[string]any{"type": "object", "oneOf": []any{toolSuccessSchema(name), failure}}
}
