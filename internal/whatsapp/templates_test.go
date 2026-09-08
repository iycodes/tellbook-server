package whatsapp

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestCustomerReminderTemplateUsesProviderContactAndFragmentClaim(t *testing.T) {
	suffix := "#claim=" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	message := TemplateMessage{To: "+2348012345678", Key: TemplateUserReminder, Values: TemplateValues{
		Body: map[string]string{"customer_name": "Customer", "service_title": "Service", "provider_name": "Business",
			"appointment_datetime": "Tomorrow", "booking_location": "Lagos", "provider_contact_phone": "+2348055555555", "amount_due": "NGN 0"},
		Button: map[string]string{"booking_route_suffix": suffix},
	}}
	request, err := buildTemplateRequest(message)
	if err != nil {
		t.Fatal(err)
	}
	if request.Template.Components[0].Parameters[5].Text != "+2348055555555" || request.Template.Components[1].Parameters[0].Text != suffix {
		t.Fatal("customer template parameter order mismatch")
	}
	message.Values.Button["booking_route_suffix"] = "#claim=invalid&redirect=example.com"
	if _, err := buildTemplateRequest(message); err == nil {
		t.Fatal("invalid claim suffix accepted")
	}
}

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

func TestBuildTemplateRequestOmitsButtonForBodyOnlyTemplate(t *testing.T) {
	request, err := buildTemplateRequest(TemplateMessage{
		To:  "+2348012345678",
		Key: TemplateUserAccountCreated,
		Values: TemplateValues{
			Body:               map[string]string{"user_name": "Tayo"},
			OpaqueCallbackData: "delivery-id",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Template.Components) != 1 || request.Template.Components[0].Type != "body" {
		t.Fatalf("unexpected components: %#v", request.Template.Components)
	}
	if got := request.Template.Components[0].Parameters[0].Text; got != "Tayo" {
		t.Fatalf("body parameter = %q", got)
	}
}

func TestPendingAccountTemplateContractsAreEncodable(t *testing.T) {
	tests := []struct {
		key    TemplateKey
		values map[string]string
	}{
		{TemplateProviderAccountCreated, map[string]string{"provider_name": "Ada"}},
		{TemplateUserAccountCreated, map[string]string{"user_name": "Tayo"}},
	}
	for _, test := range tests {
		t.Run(string(test.key), func(t *testing.T) {
			request, err := buildTemplateRequest(TemplateMessage{
				To: "+2348012345678", Key: test.key, Values: TemplateValues{Body: test.values},
			})
			if err != nil {
				t.Fatal(err)
			}
			if request.Template.Name != string(test.key) || len(request.Template.Components) != 1 {
				t.Fatalf("unexpected request: %#v", request)
			}
		})
	}
}

func TestAuthCodeTemplateUsesOneOpaqueInstructionParameter(t *testing.T) {
	request, err := buildTemplateRequest(TemplateMessage{
		To:  "+2348012345678",
		Key: TemplateAuthCode,
		Values: TemplateValues{
			Body:               map[string]string{"code_instruction": "Use the code 238500"},
			OpaqueCallbackData: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.Template.Name != "v_c_x" || len(request.Template.Components) != 1 {
		t.Fatalf("unexpected auth template request: %#v", request)
	}
	parameters := request.Template.Components[0].Parameters
	if len(parameters) != 1 || parameters[0].Text != "Use the code 238500" {
		t.Fatalf("auth template parameters = %#v", parameters)
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
