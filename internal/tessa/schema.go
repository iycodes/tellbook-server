package tessa

import (
	"bytes"
	"encoding/json"
)

// Tool-specific shapes avoid asking the model to fill unrelated arguments with
// empty values. The same normalized Go request is used for execution/replay.
func planningSchema() json.RawMessage {
	stringField := func(max int) map[string]any { return map[string]any{"type": "string", "maxLength": max} }
	fields := map[string]any{
		"metric":        map[string]any{"type": "string", "enum": []string{"count", "summary"}},
		"payment_state": map[string]any{"type": "string", "enum": []string{"any", "unpaid", "balance_due", "paid_in_full"}},
		"query":         stringField(240), "booking_id": stringField(36), "service_id": stringField(36), "customer_id": stringField(36),
		"from":       map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
		"to":         map[string]any{"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
		"status":     map[string]any{"type": "string", "enum": []string{"", "draft", "published", "paused"}},
		"statuses":   map[string]any{"type": "array", "maxItems": 9, "items": map[string]any{"type": "string", "enum": []string{"booked", "pending", "confirmed", "completed", "cancelled", "canceled", "declined", "expired", "no_show"}}},
		"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": MaxRequestedResults},
		"selection":  map[string]any{"type": "string", "enum": []string{"list", "first", "top"}},
		"time_scope": map[string]any{"type": "string", "enum": []string{"period", "upcoming"}}, "compare_previous": map[string]any{"type": "boolean"},
	}
	fields["excluded_statuses"] = fields["statuses"]
	specs := [][]string{
		{"search_tellbook_help", "query"}, {"get_business_snapshot"},
		{"search_bookings", "query", "statuses", "excluded_statuses", "payment_state", "from", "to", "selection", "limit", "time_scope"},
		{"get_booking", "booking_id"}, {"get_schedule", "from", "to", "time_scope"},
		{"get_availability", "service_id", "query", "from", "to"}, {"get_booking_attention_summary", "from", "to"},
		{"search_services", "query", "status", "selection", "limit"}, {"get_service", "service_id"},
		{"search_customers", "query", "selection", "limit"}, {"get_customer_booking_summary", "customer_id"},
		{"get_payment_summary", "from", "to"}, {"get_booking_payment_status", "booking_id"},
		{"get_payout_summary", "from", "to"}, {"get_booking_metrics", "metric", "query", "statuses", "excluded_statuses", "payment_state", "from", "to", "time_scope", "compare_previous"},
		{"get_inbox_summary"}, {"get_review_summary", "from", "to"}, {"get_public_profile_status"},
	}
	variants := make([]any, 0, len(specs))
	for _, spec := range specs {
		periodKinds := []string{""}
		if dateRangeTool(spec[0]) {
			periodKinds = []string{"named", "custom"}
		}
		for _, kind := range periodKinds {
			properties := map[string]any{"name": map[string]any{"type": "string", "enum": []string{spec[0]}}}
			required := []string{"name"}
			if kind != "" {
				periods := namedPeriods
				if kind == "custom" {
					periods = []string{"custom"}
				}
				properties["period"] = map[string]any{"type": "string", "enum": periods}
				required = append(required, "period")
			}
			for _, key := range spec[1:] {
				properties[key] = fields[key]
				if kind == "named" && (key == "from" || key == "to") {
					properties[key] = map[string]any{"type": "string", "enum": []string{""}}
				}
				required = append(required, key)
			}
			variants = append(variants, map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": orderedSchemaProperties(required, properties)})
		}
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"scope", "intent", "resolved_question", "booking_count_only", "answer_mode", "tools", "direct_response"}, "properties": map[string]any{
		"resolved_question":  map[string]any{"type": "string", "minLength": 1, "maxLength": 600},
		"booking_count_only": map[string]any{"type": "boolean"},
		"scope":              map[string]any{"type": "string", "enum": []string{"in_scope", "needs_clarification", "out_of_scope"}},
		"intent":             map[string]any{"type": "string", "minLength": 1, "maxLength": 80, "pattern": "^[a-z0-9_]+$"},
		"answer_mode":        map[string]any{"type": "string", "enum": []string{"direct", "tools"}},
		"tools":              map[string]any{"type": "array", "maxItems": MaxToolCalls, "items": map[string]any{"anyOf": variants}},
		"direct_response":    stringField(800),
	}}
	schema["properties"] = orderedSchemaProperties(schema["required"].([]string), schema["properties"].(map[string]any))
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return encoded
}

// llama-server's JSON grammar follows property order. Put the discriminator
// first so choosing an argument cannot accidentally lock decoding into the
// wrong tool variant. Go's map encoder would sort these keys alphabetically.
func orderedSchemaProperties(names []string, fields map[string]any) json.RawMessage {
	var result bytes.Buffer
	result.WriteByte('{')
	for index, name := range names {
		if index > 0 {
			result.WriteByte(',')
		}
		key, _ := json.Marshal(name)
		value, err := json.Marshal(fields[name])
		if err != nil {
			panic(err)
		}
		result.Write(key)
		result.WriteByte(':')
		result.Write(value)
	}
	result.WriteByte('}')
	return result.Bytes()
}

func groundedAnswerSchema(coverage []Coverage) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal(answerJSONSchema, &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	part := properties["parts"].(map[string]any)["items"].(map[string]any)
	partFields := part["properties"].(map[string]any)
	seen := map[string]bool{}
	partial := false
	hasOptionalEntities := false
	for _, group := range coverage {
		partial = partial || group.Partial
		hasOptionalEntities = hasOptionalEntities || len(group.OptionalEntityIDs) > 0
		for _, id := range group.EntityIDs {
			if !seen[id] {
				seen[id] = true
			}
		}
	}
	// Keep a small fixed set of reusable schema variants. Per-entity enums would create
	// a new compiled schema/cache entry for every conversation. Membership is
	// checked against current evidence by validateAnswer instead.
	partFields["entity_id"] = map[string]any{"type": "string", "maxLength": 36}
	if len(seen) == 0 && !hasOptionalEntities {
		// Aggregates and empty results have no entity IDs to cite. This constrains
		// metadata only, not the model-written wording or numerical answer.
		partFields["entity_id"] = map[string]any{"type": "string", "enum": []string{""}}
	}
	part["properties"] = orderedSchemaProperties([]string{"entity_id", "text"}, partFields)
	if partial {
		properties["partial_notice"] = map[string]any{"type": "string", "minLength": 1}
	} else if len(seen) <= 1 {
		// With no truncation and at most one entity, there is no smaller valid
		// nonempty selection. A partial-list disclaimer would be misleading.
		properties["partial_notice"] = map[string]any{"type": "string", "enum": []string{""}}
	}
	schema["properties"] = orderedSchemaProperties([]string{"parts", "partial_notice"}, properties)
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return encoded
}
