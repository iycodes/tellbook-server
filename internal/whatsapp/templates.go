package whatsapp

import (
	"errors"
	"fmt"
	"strings"
)

type TemplateKey string

const (
	TemplateUserReminder            TemplateKey = "user_reminder"
	TemplateProviderBookingReminder TemplateKey = "provider_booking_reminder"
	TemplateProviderNewBooking      TemplateKey = "provider_new_booking"
	TemplateBookingStatusUpdate     TemplateKey = "booking_status_update"
	TemplateProviderAccountCreated  TemplateKey = "provider_account_created"
	TemplateUserAccountCreated      TemplateKey = "user_account_created"
	TemplateAuthCode                TemplateKey = "v_c_x"
)

type TemplateDefinition struct {
	Key                  TemplateKey
	Name                 string
	Language             string
	Category             string
	BodyText             string
	BodyParameters       []string
	FooterText           string
	ButtonText           string
	ButtonURL            string
	ButtonParameters     []string
	RequiresContractHold bool
}

var templateRegistry = map[TemplateKey]TemplateDefinition{
	TemplateUserReminder: {
		Key: TemplateUserReminder, Name: "user_reminder", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, this is a reminder for your {{2}} appointment with {{3}}.\n\n" +
			"Date & Time: {{4}}\nLocation details: {{5}}\nPhone: {{6}}\nAmount Due: {{7}}\n\n" +
			"If you need to make changes, please manage your booking below.",
		BodyParameters: []string{
			"customer_name", "service_title", "provider_name", "appointment_datetime",
			"booking_location", "provider_contact_phone", "amount_due",
		},
		ButtonText: "Manage Booking", ButtonURL: "https://tellbook.africa/bookings{{1}}",
		ButtonParameters: []string{"booking_route_suffix"},
	},
	TemplateProviderBookingReminder: {
		Key: TemplateProviderBookingReminder, Name: "provider_booking_reminder", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, this is a reminder for your {{2}} appointment with {{3}}.\n\n" +
			"Date & Time: {{4}}\nLocation details: {{5}}\nAmount Due:{{6}}\n\n" +
			"If you need to make changes, please manage your booking below.",
		BodyParameters: []string{
			"provider_name", "service_title", "customer_name", "appointment_datetime",
			"booking_location", "amount_due",
		},
		FooterText: "Powered by TellBook",
		ButtonText: "View Booking Details", ButtonURL: "https://tellbook.app/bookings{{1}}",
		ButtonParameters: []string{"booking_route_suffix"},
	},
	TemplateProviderNewBooking: {
		Key: TemplateProviderNewBooking, Name: "provider_new_booking", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, a new appointment has been successfully scheduled on your calendar.\n\n" +
			"Client Name: {{2}}\nService Requested: {{3}}\nDate & Time: {{4}}\n" +
			"Amount Paid: {{5}}\nAmount Due: {{6}}\nPlease ensure you are prepared for this appointment.",
		BodyParameters: []string{
			"provider_name", "customer_name", "service_title", "appointment_datetime",
			"amount_paid", "amount_due",
		},
		FooterText: "Powered By TellBook",
		ButtonText: "View Booking", ButtonURL: "https://tellbook.app/bookings{{1}}",
		ButtonParameters: []string{"booking_route_suffix"},
	},
	TemplateBookingStatusUpdate: {
		Key: TemplateBookingStatusUpdate, Name: "booking_status_update", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, booking {{2}} has been updated.\n\n" +
			"Update: {{3}}\nService: {{4}}\nDate & time: {{5}}\n\n" +
			"Please review the booking in Tellbook if action is required.",
		BodyParameters: []string{
			"recipient_name", "booking_reference", "update_summary", "service_title",
			"appointment_datetime",
		},
		FooterText: "Tellbook booking notification",
	},
	TemplateProviderAccountCreated: {
		Key: TemplateProviderAccountCreated, Name: "provider_account_created", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, this confirms that your Tellbook provider account was created successfully. " +
			"If you did not create this account, please contact Tellbook support.",
		BodyParameters: []string{"provider_name"},
		FooterText:     "Tellbook account notification",
	},
	TemplateUserAccountCreated: {
		Key: TemplateUserAccountCreated, Name: "user_account_created", Language: "en", Category: "UTILITY",
		BodyText: "Hello {{1}}, this confirms that your Tellbook account was created successfully. " +
			"If you did not create this account, please contact Tellbook support.",
		BodyParameters: []string{"user_name"},
		FooterText:     "Tellbook account notification",
	},
	TemplateAuthCode: {
		Key: TemplateAuthCode, Name: "v_c_x", Language: "en", Category: "UTILITY",
		BodyText:       "Reminder, Quick check before we lock this in.\n\n{{1}}\n\nThank you.",
		BodyParameters: []string{"code_instruction"},
	},
}

func RegisteredTemplates() []TemplateDefinition {
	keys := []TemplateKey{
		TemplateUserReminder,
		TemplateProviderBookingReminder,
		TemplateProviderNewBooking,
		TemplateBookingStatusUpdate,
		TemplateProviderAccountCreated,
		TemplateUserAccountCreated,
		TemplateAuthCode,
	}
	definitions := make([]TemplateDefinition, 0, len(keys))
	for _, key := range keys {
		definition := templateRegistry[key]
		definition.BodyParameters = append([]string(nil), definition.BodyParameters...)
		definition.ButtonParameters = append([]string(nil), definition.ButtonParameters...)
		definitions = append(definitions, definition)
	}
	return definitions
}

func LookupTemplate(key TemplateKey) (TemplateDefinition, bool) {
	definition, ok := templateRegistry[key]
	if !ok {
		return TemplateDefinition{}, false
	}
	definition.BodyParameters = append([]string(nil), definition.BodyParameters...)
	definition.ButtonParameters = append([]string(nil), definition.ButtonParameters...)
	return definition, true
}

type TemplateValues struct {
	Body               map[string]string
	Button             map[string]string
	OpaqueCallbackData string
}

type TemplateMessage struct {
	To     string
	Key    TemplateKey
	Values TemplateValues
}

type templateRequest struct {
	MessagingProduct      string          `json:"messaging_product"`
	RecipientType         string          `json:"recipient_type"`
	To                    string          `json:"to"`
	Type                  string          `json:"type"`
	Template              templatePayload `json:"template"`
	BizOpaqueCallbackData string          `json:"biz_opaque_callback_data,omitempty"`
}

type templatePayload struct {
	Name       string              `json:"name"`
	Language   templateLanguage    `json:"language"`
	Components []templateComponent `json:"components"`
}

type templateLanguage struct {
	Code string `json:"code"`
}

type templateComponent struct {
	Type       string              `json:"type"`
	SubType    string              `json:"sub_type,omitempty"`
	Index      string              `json:"index,omitempty"`
	Parameters []templateParameter `json:"parameters"`
}

type templateParameter struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func buildTemplateRequest(message TemplateMessage) (templateRequest, error) {
	definition, ok := LookupTemplate(message.Key)
	if !ok {
		return templateRequest{}, fmt.Errorf("unknown WhatsApp template key %q", message.Key)
	}
	to, err := NormalizeE164(message.To)
	if err != nil {
		return templateRequest{}, err
	}
	body, err := orderedTemplateParameters(definition.BodyParameters, message.Values.Body, false)
	if err != nil {
		return templateRequest{}, fmt.Errorf("%s body: %w", definition.Key, err)
	}
	button, err := orderedTemplateParameters(definition.ButtonParameters, message.Values.Button, true)
	if err != nil {
		return templateRequest{}, fmt.Errorf("%s button: %w", definition.Key, err)
	}
	if len(message.Values.OpaqueCallbackData) > 512 || containsControlCharacter(message.Values.OpaqueCallbackData) {
		return templateRequest{}, errors.New("opaque callback data is invalid")
	}
	components := []templateComponent{{Type: "body", Parameters: body}}
	if len(definition.ButtonParameters) > 0 {
		components = append(components, templateComponent{
			Type: "button", SubType: "url", Index: "0", Parameters: button,
		})
	}
	return templateRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               to,
		Type:             "template",
		Template: templatePayload{
			Name:       definition.Name,
			Language:   templateLanguage{Code: definition.Language},
			Components: components,
		},
		BizOpaqueCallbackData: message.Values.OpaqueCallbackData,
	}, nil
}

func orderedTemplateParameters(names []string, values map[string]string, routeSuffix bool) ([]templateParameter, error) {
	if len(values) != len(names) {
		return nil, fmt.Errorf("expected %d parameters, received %d", len(names), len(values))
	}
	parameters := make([]templateParameter, 0, len(names))
	for _, name := range names {
		value, ok := values[name]
		value = strings.TrimSpace(value)
		if !ok || value == "" || len(value) > 1024 || containsControlCharacter(value) {
			return nil, fmt.Errorf("parameter %q is missing or invalid", name)
		}
		if routeSuffix && !validBookingRouteSuffix(value) {
			return nil, fmt.Errorf("parameter %q must be a relative booking suffix", name)
		}
		parameters = append(parameters, templateParameter{Type: "text", Text: value})
	}
	return parameters, nil
}

func validBookingRouteSuffix(value string) bool {
	if strings.HasPrefix(value, "#claim=") {
		token := strings.TrimPrefix(value, "#claim=")
		if len(token) < 32 || len(token) > 128 {
			return false
		}
		for _, c := range token {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
		return true
	}
	return strings.HasPrefix(value, "?") && !strings.Contains(value, "://")
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func NormalizeE164(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "+")
	if len(value) < 8 || len(value) > 15 || value[0] == '0' {
		return "", errors.New("WhatsApp destination must be an E.164 number")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return "", errors.New("WhatsApp destination must be an E.164 number")
		}
	}
	return value, nil
}
