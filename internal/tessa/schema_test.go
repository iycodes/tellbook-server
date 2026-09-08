package tessa

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanningSchemaUsesDiscriminatedToolArguments(t *testing.T) {
	var root struct {
		Properties struct {
			Tools struct {
				Items struct {
					AnyOf []struct {
						Properties json.RawMessage `json:"properties"`
						Required   []string        `json:"required"`
					} `json:"anyOf"`
				} `json:"items"`
			} `json:"tools"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(planningJSONSchema, &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Properties.Tools.Items.AnyOf) != 26 {
		t.Fatal("tool variants missing")
	}
	for _, variant := range root.Properties.Tools.Items.AnyOf {
		if !strings.HasPrefix(string(variant.Properties), `{"name":`) {
			t.Fatal("tool discriminator must precede arguments")
		}
		var fields map[string]json.RawMessage
		json.Unmarshal(variant.Properties, &fields)
		var name struct {
			Enum []string `json:"enum"`
		}
		json.Unmarshal(fields["name"], &name)
		if len(name.Enum) != 1 || !allowedToolName(name.Enum[0]) || len(variant.Required) != len(fields) {
			t.Fatal("invalid variant", string(variant.Properties))
		}
		if dateRangeTool(name.Enum[0]) {
			var period struct {
				Enum []string `json:"enum"`
			}
			if err := json.Unmarshal(fields["period"], &period); err != nil || len(period.Enum) == 0 {
				t.Fatal("missing period selector")
			}
			if period.Enum[0] != "custom" && !strings.Contains(string(fields["from"]), `"enum":[""]`) {
				t.Fatal("named period allows model date arithmetic")
			}
		}
		if strings.HasPrefix(name.Enum[0], "search_") && name.Enum[0] != "search_tellbook_help" {
			if !strings.Contains(string(fields["selection"]), `"first"`) || strings.Contains(string(fields["selection"]), `""`) {
				t.Fatal("search selection must be explicit")
			}
		}
	}
}

func TestPlanningSchemaResolvesRequestBeforeToolSelection(t *testing.T) {
	var root struct {
		Required   []string
		Properties json.RawMessage
	}
	if err := json.Unmarshal(planningJSONSchema, &root); err != nil {
		t.Fatal(err)
	}
	fields := string(root.Properties)
	if strings.Index(fields, `"resolved_question":`) >= strings.Index(fields, `"tools":`) {
		t.Fatal("request resolution must precede tools in grammar")
	}
	found := false
	for _, key := range root.Required {
		found = found || key == "resolved_question"
	}
	if !found {
		t.Fatal("resolved request is not required by model schema")
	}
}

func TestPlanningSchemaDeclaresCountRequestAndAggregateMode(t *testing.T) {
	var root struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(planningJSONSchema, &root); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(root.Properties["booking_count_only"]), `"type":"boolean"`) {
		t.Fatal("count intent missing")
	}
	joined := strings.Join(root.Required, ",")
	if strings.Index(joined, "booking_count_only") < 0 || strings.Index(joined, "booking_count_only") > strings.Index(joined, "tools") {
		t.Fatal("count intent must be required before tools")
	}
	if !strings.Contains(string(planningJSONSchema), `"metric":{"enum":["count","summary"]`) {
		t.Fatal("aggregate mode missing")
	}
}

func TestAnswerSchemaConstrainsEvidenceWithoutConstrainingProse(t *testing.T) {
	for _, partial := range []bool{false, true} {
		payload := groundedAnswerSchema([]Coverage{{Tool: "search_bookings", EntityIDs: []string{groundingFirst}, Partial: partial}})
		var schema map[string]any
		if err := json.Unmarshal(payload, &schema); err != nil {
			t.Fatal(err)
		}
		properties := schema["properties"].(map[string]any)
		fields := properties["parts"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if strings.Contains(string(payload), groundingFirst) {
			t.Fatal("per-provider evidence must not create unbounded compiled-schema cache entries")
		}
		text := fields["text"].(map[string]any)
		if _, canned := text["enum"]; canned {
			t.Fatal("answer prose was constrained to canned text")
		}
		notice := properties["partial_notice"].(map[string]any)
		if partial && notice["minLength"] != float64(1) {
			t.Fatal("truncation notice not required")
		}
		if !partial && len(notice["enum"].([]any)) != 1 {
			t.Fatal("fulfilled first selection permits a misleading notice")
		}
	}
}

func TestAggregateAnswerSchemaHasNoInventedEntityIDs(t *testing.T) {
	for _, optional := range []bool{false, true} {
		coverage := []Coverage{}
		if optional {
			coverage = []Coverage{{Tool: "get_booking_attention_summary", OptionalEntityIDs: []string{groundingFirst}}}
		}
		var schema map[string]any
		if err := json.Unmarshal(groundedAnswerSchema(coverage), &schema); err != nil {
			t.Fatal(err)
		}
		field := schema["properties"].(map[string]any)["parts"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["entity_id"].(map[string]any)
		_, emptyOnly := field["enum"]
		if emptyOnly == optional {
			t.Fatal("aggregate metadata constraint lost optional examples", field)
		}
	}
}
