package ai

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseSemiPilotTurnDecision(t *testing.T) {
	serviceID := "00000000-0000-4000-8000-000000000001"
	tests := []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			name:    "bounded service lookup",
			payload: `{"protocol_version":1,"reply":"Let me check the matching services.","next_state":"qualifying","tool_call":{"name":"list_relevant_services","arguments":{"query":"bridal makeup"}},"missing_facts":[],"handoff_reason":""}`,
		},
		{
			name:    "pidgin booking link request",
			payload: `{"protocol_version":1,"reply":"I don find the service. Make I prepare the booking option for you.","next_state":"service_identified","tool_call":{"name":"get_booking_link","arguments":{"service_id":"` + serviceID + `"}},"missing_facts":[],"handoff_reason":""}`,
		},
		{
			name:    "safe handoff",
			payload: `{"protocol_version":1,"reply":"I’ll bring the provider into this conversation.","next_state":"handoff","tool_call":null,"missing_facts":[],"handoff_reason":"unsupported_request"}`,
		},
		{
			name:    "unknown top-level field",
			payload: `{"protocol_version":1,"reply":"Hello","next_state":"qualifying","tool_call":null,"missing_facts":[],"handoff_reason":"","chain_of_thought":"secret"}`,
			wantErr: true,
		},
		{
			name:    "missing required tool call property",
			payload: `{"protocol_version":1,"reply":"Hello","next_state":"qualifying","missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "missing required missing facts property",
			payload: `{"protocol_version":1,"reply":"Hello","next_state":"qualifying","tool_call":null,"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "null missing facts",
			payload: `{"protocol_version":1,"reply":"Hello","next_state":"qualifying","tool_call":null,"missing_facts":null,"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "unknown tool",
			payload: `{"protocol_version":1,"reply":"Done","next_state":"completed","tool_call":{"name":"create_booking","arguments":{}},"missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "invented URL",
			payload: `{"protocol_version":1,"reply":"Book at https://fake.example/booking","next_state":"link_ready","tool_call":null,"missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "malformed tool arguments",
			payload: `{"protocol_version":1,"reply":"Checking","next_state":"service_identified","tool_call":{"name":"get_service_details","arguments":{"service_id":"bad","extra":true}},"missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "system-owned takeover state",
			payload: `{"protocol_version":1,"reply":"Taking over","next_state":"provider_takeover","tool_call":null,"missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
		{
			name:    "null tool arguments",
			payload: `{"protocol_version":1,"reply":"Checking","next_state":"qualifying","tool_call":{"name":"list_relevant_services","arguments":null},"missing_facts":[],"handoff_reason":""}`,
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := ParseSemiPilotTurnDecision([]byte(test.payload))
			if test.wantErr {
				if !errors.Is(err, ErrInvalidSemiPilotDecision) {
					t.Fatalf("error = %v, want ErrInvalidSemiPilotDecision", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decision.ProtocolVersion != SemiPilotProtocolVersion {
				t.Fatalf("protocol version = %d", decision.ProtocolVersion)
			}
		})
	}
}

func TestSemiPilotDecisionJSONSchemaIsValidAndStrict(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(SemiPilotDecisionJSONSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("schema is not strict: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	protocolVersion := properties["protocol_version"].(map[string]any)
	if protocolVersion["type"] != "integer" || protocolVersion["const"] != float64(1) {
		t.Fatalf("protocol version schema = %#v", protocolVersion)
	}
	if SemiPilotMaxToolCallsPerTurn != 4 {
		t.Fatalf("tool call budget = %d", SemiPilotMaxToolCallsPerTurn)
	}
	assertStrictToolArgumentUnion(t, schema)
}

func assertStrictToolArgumentUnion(t *testing.T, schema map[string]any) {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", schema["properties"])
	}
	toolCall, ok := properties["tool_call"].(map[string]any)
	if !ok {
		t.Fatalf("tool_call schema = %#v", properties["tool_call"])
	}
	toolProperties, ok := toolCall["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool_call properties = %#v", toolCall["properties"])
	}
	arguments, ok := toolProperties["arguments"].(map[string]any)
	if !ok {
		t.Fatalf("arguments schema = %#v", toolProperties["arguments"])
	}
	variants, ok := arguments["anyOf"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("argument variants = %#v", arguments["anyOf"])
	}
	for _, variant := range variants {
		object, ok := variant.(map[string]any)
		if !ok || object["additionalProperties"] != false {
			t.Fatalf("argument variant is not strict: %#v", variant)
		}
		required, ok := object["required"].([]any)
		variantProperties, propertiesOK := object["properties"].(map[string]any)
		if !ok || !propertiesOK || len(required) != len(variantProperties) {
			t.Fatalf("argument variant must require every property: %#v", variant)
		}
	}
}
