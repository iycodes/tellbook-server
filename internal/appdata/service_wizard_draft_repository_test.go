package appdata

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateServiceWizardDraftState(t *testing.T) {
	t.Run("normalizes an object", func(t *testing.T) {
		payload, step, err := validateServiceWizardDraftState(
			json.RawMessage(`{ "serviceName": "Lash refill", "durationMinutes": 60 }`),
			"info",
		)
		if err != nil {
			t.Fatal(err)
		}
		if step != "info" || string(payload) != `{"durationMinutes":60,"serviceName":"Lash refill"}` {
			t.Fatalf("unexpected normalized state: %s %s", step, payload)
		}
	})

	for name, testCase := range map[string][2]string{
		"array payload":   {`[]`, "info"},
		"multiple values": {`{} {}`, "info"},
		"unknown step":    {`{}`, "payment"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := validateServiceWizardDraftState(json.RawMessage(testCase[0]), testCase[1]); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	if _, _, err := validateServiceWizardDraftState(
		json.RawMessage(`{"value":"`+strings.Repeat("x", maxServiceWizardDraftPayloadBytes)+`"}`),
		"info",
	); err == nil {
		t.Fatal("expected oversized payload error")
	}
}
