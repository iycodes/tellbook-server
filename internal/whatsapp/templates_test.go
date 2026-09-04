package whatsapp

import (
	"encoding/json"
	"testing"
)

func TestBuildTemplateRequestUsesRegistryOrder(t *testing.T) {
	request, err := buildTemplateRequest(TemplateMessage{
		To:  "+2348012345678",
		Key: TemplateProviderNewBooking,
		Values: TemplateValues{
			Body: map[string]string{
				"amount_due": "NGN 2,000", "provider_name": "Ada", "amount_paid": "NGN 5,000",
				"appointment_datetime": "5 September 2026, 10:00 WAT", "customer_name": "Tayo",
				"service_title": "Hair styling",
			},
			Button:             map[string]string{"booking_route_suffix": "?booking=booking-id"},
			OpaqueCallbackData: "delivery-id",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.To != "2348012345678" || request.Template.Name != "provider_new_booking" || len(request.Template.Components) != 2 {
		t.Fatalf("unexpected request: %#v", request)
	}
	wantBody := []string{"Ada", "Tayo", "Hair styling", "5 September 2026, 10:00 WAT", "NGN 5,000", "NGN 2,000"}
	for index, want := range wantBody {
		if got := request.Template.Components[0].Parameters[index].Text; got != want {
			t.Fatalf("body parameter %d = %q, want %q", index, got, want)
		}
	}
	if got := request.Template.Components[1].Parameters[0].Text; got != "?booking=booking-id" {
		t.Fatalf("button suffix = %q", got)
	}
	if encoded, err := json.Marshal(request); err != nil || string(encoded) == "" {
		t.Fatalf("marshal request: body=%q err=%v", encoded, err)
	}
}

func TestBuildTemplateRequestRejectsIncompleteOrUnsafeValues(t *testing.T) {
	message := TemplateMessage{
		To: "+2348012345678", Key: TemplateProviderBookingReminder,
		Values: TemplateValues{
			Body: map[string]string{
				"provider_name": "Ada", "service_title": "Hair styling", "customer_name": "Tayo",
				"appointment_datetime": "Tomorrow", "booking_location": "Lagos",
			},
			Button: map[string]string{"booking_route_suffix": "https://attacker.invalid"},
		},
	}
	if _, err := buildTemplateRequest(message); err == nil {
		t.Fatal("buildTemplateRequest accepted a missing body parameter")
	}
	message.Values.Body["amount_due"] = "NGN 0"
	if _, err := buildTemplateRequest(message); err == nil {
		t.Fatal("buildTemplateRequest accepted an absolute button URL")
	}
}

func TestNormalizeE164(t *testing.T) {
	if got, err := NormalizeE164("+2348012345678"); err != nil || got != "2348012345678" {
		t.Fatalf("NormalizeE164 valid = %q, %v", got, err)
	}
	for _, value := range []string{"08012345678", "+234 801 234", "", "+123"} {
		if _, err := NormalizeE164(value); err == nil {
			t.Fatalf("NormalizeE164(%q) succeeded", value)
		}
	}
}
