package seed

import (
	"testing"

	"booking/go-server/internal/agreements/render"
)

func TestSystemTemplatesAreValidAndResolvable(t *testing.T) {
	templates, err := SystemTemplates()
	if err != nil {
		t.Fatalf("SystemTemplates() error = %v", err)
	}
	if len(templates) < 3 {
		t.Fatalf("template count = %d", len(templates))
	}
	values := map[string]string{
		"BUSINESS_NAME":       "Kemi Beauty",
		"CUSTOMER_NAME":       "Ada Okafor",
		"SERVICE_NAME":        "Classic lash set",
		"BOOKING_DATE":        "12 August 2026",
		"BOOKING_TIME_RANGE":  "10:00 AM to 12:00 PM",
		"BOOKING_LOCATION":    "Lekki studio",
		"TOTAL_AMOUNT":        "₦20,000.00",
		"DEPOSIT_AMOUNT":      "₦5,000.00",
		"REMAINING_AMOUNT":    "₦15,000.00",
		"CANCELLATION_POLICY": "24 hours' notice is required.",
		"LATENESS_POLICY":     "Late arrival may shorten the appointment.",
	}
	for _, template := range templates {
		t.Run(template.Key, func(t *testing.T) {
			if len(template.TemplateSchemaHash) != 64 || len(template.UsedVariableKeys) == 0 {
				t.Fatalf("invalid template metadata: %+v", template)
			}
			snapshot, err := PreviewSystemTemplate(template, values, render.BookingSummary{ServiceName: "Classic lash set"})
			if err != nil {
				t.Fatalf("PreviewSystemTemplate() error = %v", err)
			}
			if snapshot.RenderedHTML == "" || len(snapshot.ResolvedTermsHash) != 64 {
				t.Fatalf("invalid preview: %+v", snapshot)
			}
		})
	}
}
