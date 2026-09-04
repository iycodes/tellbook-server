package llm

import (
	"encoding/json"
	"testing"
)

func TestValidateStructuredJSONSupportsLocalDefs(t *testing.T) {
	schema := json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"value":{"$ref":"#/$defs/nonempty"}},
		"required":["value"],
		"additionalProperties":false,
		"$defs":{"nonempty":{"type":"string","minLength":1}}
	}`)
	if err := validateStructuredJSON(schema, json.RawMessage(`{"value":"ready"}`)); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	if err := validateStructuredJSON(schema, json.RawMessage(`{"value":""}`)); err == nil {
		t.Fatal("schema mismatch was accepted")
	}
}

func TestValidateStructuredJSONRejectsRemoteReferences(t *testing.T) {
	schema := json.RawMessage(`{"$ref":"https://attacker.invalid/schema.json"}`)
	if err := validateStructuredJSON(schema, json.RawMessage(`{}`)); err == nil {
		t.Fatal("remote schema reference was accepted")
	}
}
