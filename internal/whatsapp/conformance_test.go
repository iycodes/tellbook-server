package whatsapp

import (
	"encoding/json"
	"strings"
	"testing"
)

const approvedTemplateFixture = `[
  {"name":"user_reminder","status":"APPROVED","category":"UTILITY","language":"en","components":[
	{"type":"BODY","text":"Hello {{1}}, this is a reminder for your {{2}} appointment with {{3}}.\n\nDate & Time: {{4}}\nLocation details: {{5}}\nPhone: {{6}}\nAmount Due: {{7}}\n\nIf you need to make changes, please manage your booking below."},
	{"type":"BUTTONS","buttons":[{"type":"URL","text":"Manage Booking","url":"https://tellbook.africa/bookings{{1}}"}]}
  ]},
  {"name":"provider_booking_reminder","status":"APPROVED","category":"UTILITY","language":"en","components":[
	{"type":"BODY","text":"Hello {{1}}, this is a reminder for your {{2}} appointment with {{3}}.\n\nDate & Time: {{4}}\nLocation details: {{5}}\nAmount Due:{{6}}\n\nIf you need to make changes, please manage your booking below."},
	{"type":"FOOTER","text":"Powered by TellBook"},
	{"type":"BUTTONS","buttons":[{"type":"URL","text":"View Booking Details","url":"https://tellbook.app/bookings{{1}}"}]}
  ]},
  {"name":"provider_new_booking","status":"APPROVED","category":"UTILITY","language":"en","components":[
	{"type":"BODY","text":"Hello {{1}}, a new appointment has been successfully scheduled on your calendar.\n\nClient Name: {{2}}\nService Requested: {{3}}\nDate & Time: {{4}}\nAmount Paid: {{5}}\nAmount Due: {{6}}\nPlease ensure you are prepared for this appointment."},
    {"type":"FOOTER","text":"Powered By TellBook"},
    {"type":"BUTTONS","buttons":[{"type":"URL","text":"View Booking","url":"https://tellbook.app/bookings{{1}}"}]}
  ]}
]`

func TestConformTemplateInventoryMatchesApprovedContracts(t *testing.T) {
	var templates []graphTemplate
	if err := json.Unmarshal([]byte(approvedTemplateFixture), &templates); err != nil {
		t.Fatal(err)
	}
	report := ConformTemplateInventory(RegisteredTemplates(), templates)
	if !report.Valid() {
		t.Fatalf("conformance errors: %v", report.Errors)
	}
	if len(report.Holds) != 1 || report.Holds[0] != "user_reminder" {
		t.Fatalf("holds = %v", report.Holds)
	}
}

func TestConformTemplateInventoryRejectsContractDrift(t *testing.T) {
	var templates []graphTemplate
	if err := json.Unmarshal([]byte(strings.Replace(
		approvedTemplateFixture,
		"https://tellbook.app/bookings{{1}}",
		"https://wrong.example/bookings{{1}}",
		1,
	)), &templates); err != nil {
		t.Fatal(err)
	}
	report := ConformTemplateInventory(RegisteredTemplates(), templates)
	if report.Valid() || !strings.Contains(strings.Join(report.Errors, " "), "URL button contract changed") {
		t.Fatalf("report = %#v", report)
	}
}

func TestConformTemplateInventoryRejectsBodyMeaningDriftWithSamePlaceholders(t *testing.T) {
	var templates []graphTemplate
	fixture := strings.Replace(
		approvedTemplateFixture,
		"Client Name: {{2}}",
		"Provider Name: {{2}}",
		1,
	)
	if err := json.Unmarshal([]byte(fixture), &templates); err != nil {
		t.Fatal(err)
	}
	report := ConformTemplateInventory(RegisteredTemplates(), templates)
	if report.Valid() || !strings.Contains(strings.Join(report.Errors, " "), "body parameter contract changed") {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateEnabledTemplateKeysHonorsContractHold(t *testing.T) {
	if err := ValidateEnabledTemplateKeys([]string{"provider_new_booking", "provider_booking_reminder"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEnabledTemplateKeys([]string{"user_reminder"}); err == nil {
		t.Fatal("enabled template validation ignored the user reminder contract hold")
	}
	if err := ValidateEnabledTemplateKeys([]string{"unknown"}); err == nil {
		t.Fatal("enabled template validation accepted an unknown key")
	}
}

func TestCountSequentialPlaceholdersRejectsGapsAndDuplicates(t *testing.T) {
	for _, value := range []string{"{{1}} {{3}}", "{{1}} {{1}}", "{{2}}"} {
		if got := countSequentialPlaceholders(value); got != -1 {
			t.Fatalf("countSequentialPlaceholders(%q) = %d", value, got)
		}
	}
	if got := countSequentialPlaceholders("{{1}} then {{2}}"); got != 2 {
		t.Fatalf("valid placeholder count = %d", got)
	}
}
