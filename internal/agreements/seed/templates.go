package seed

import (
	"fmt"
	"sort"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/agreements/render"
	aiapi "booking/shared/ai_api"
)

type SystemTemplate struct {
	Key                string
	Title              string
	Description        string
	Category           string
	Tags               []string
	ConfirmationMethod domain.ConfirmationMethod
	Document           aiapi.DocumentSchema
	TemplateSchemaHash string
	UsedVariableKeys   []string
}

func SystemTemplates() ([]SystemTemplate, error) {
	templates := []SystemTemplate{
		newServiceAgreementTemplate(),
		newBeautyConsentTemplate(),
		newEventServicesTemplate(),
	}
	seen := make(map[string]struct{}, len(templates))
	for index := range templates {
		template := &templates[index]
		if template.Key == "" || template.Title == "" || template.Category == "" {
			return nil, fmt.Errorf("system agreement template %d has incomplete metadata", index)
		}
		if _, exists := seen[template.Key]; exists {
			return nil, fmt.Errorf("duplicate system agreement template key %q", template.Key)
		}
		seen[template.Key] = struct{}{}
		if err := domain.ValidateDocument(template.Document, template.ConfirmationMethod, domain.AgreementVariableKeySet()); err != nil {
			return nil, fmt.Errorf("validate system agreement template %q: %w", template.Key, err)
		}
		hash, err := domain.TemplateSchemaHash(template.Document, template.ConfirmationMethod)
		if err != nil {
			return nil, fmt.Errorf("hash system agreement template %q: %w", template.Key, err)
		}
		template.TemplateSchemaHash = hash
		template.UsedVariableKeys = template.Document.VariableKeys()
		sort.Strings(template.Tags)
	}
	return templates, nil
}

func PreviewSystemTemplate(template SystemTemplate, values map[string]string, summary render.BookingSummary) (render.Snapshot, error) {
	return render.BuildSnapshot(template.Title, summary, template.Document, template.ConfirmationMethod, values)
}

func newServiceAgreementTemplate() SystemTemplate {
	method := domain.ConfirmationMethodConfirmation
	return SystemTemplate{
		Key:                "general-service-agreement-v1",
		Title:              "Service Agreement",
		Description:        "Clear service, payment, cancellation, timing, and liability terms for appointment businesses.",
		Category:           "General services",
		Tags:               []string{"appointments", "general", "services"},
		ConfirmationMethod: method,
		Document: document(
			heading("Agreement"),
			paragraph(text("This agreement is between "), variable("BUSINESS_NAME", true), text(" and "), variable("CUSTOMER_NAME", true), text(" for "), variable("SERVICE_NAME", true), text(".")),
			heading("Services and schedule"),
			paragraph(text("The service is scheduled for "), variable("BOOKING_DATE", false), text(" during "), variable("BOOKING_TIME_RANGE", false), text(" at "), variable("BOOKING_LOCATION", false), text(".")),
			heading("Payment"),
			paragraph(text("The total is "), variable("TOTAL_AMOUNT", true), text(". A deposit of "), variable("DEPOSIT_AMOUNT", false), text(" reserves the booking, and the remaining balance is "), variable("REMAINING_AMOUNT", false), text(".")),
			heading("Changes and timing"),
			paragraph(text("Cancellations are governed by "), variable("CANCELLATION_POLICY", false), text(". Late arrival and service delays are governed by "), variable("LATENESS_POLICY", false), text(".")),
			heading("General terms"),
			paragraph(text("Each party is responsible for information and actions within its control. Neither party is responsible for delay caused by events beyond reasonable control. The Provider's liability is limited to the amount paid for the affected service where the law allows.")),
			acceptance(method),
		),
	}
}

func newBeautyConsentTemplate() SystemTemplate {
	method := domain.ConfirmationMethodSignature
	return SystemTemplate{
		Key:                "beauty-treatment-consent-v1",
		Title:              "Beauty Service Agreement and Consent",
		Description:        "Service, preparation, aftercare, consent, and safety terms for beauty professionals.",
		Category:           "Beauty and wellness",
		Tags:               []string{"beauty", "consent", "lash", "wellness"},
		ConfirmationMethod: method,
		Document: document(
			heading("Service agreement"),
			paragraph(variable("BUSINESS_NAME", true), text(" will provide "), variable("SERVICE_NAME", true), text(" to "), variable("CUSTOMER_NAME", true), text(" on "), variable("BOOKING_DATE", false), text(" at "), variable("BOOKING_LOCATION", false), text(".")),
			heading("Preparation and disclosure"),
			paragraph(text("The Customer will follow the preparation instructions provided for the service and disclose allergies, sensitivities, medical concerns, medication, or prior reactions that could affect safe treatment.")),
			heading("Consent and expected results"),
			paragraph(text("The Customer understands that beauty services can cause temporary redness, irritation, sensitivity, or an allergic reaction, and that results and retention vary by aftercare, skin or hair condition, lifestyle, and natural growth cycles.")),
			heading("Payment and booking changes"),
			paragraph(text("The total service amount is "), variable("TOTAL_AMOUNT", true), text(". Cancellations and rescheduling are governed by "), variable("CANCELLATION_POLICY", false), text(", and late arrival is governed by "), variable("LATENESS_POLICY", false), text(".")),
			heading("Aftercare and concerns"),
			paragraph(text("The Customer will follow the aftercare instructions provided by the Provider and report any material concern promptly. The Provider may pause or refuse a service when proceeding would be unsafe.")),
			acceptance(method),
		),
	}
}

func newEventServicesTemplate() SystemTemplate {
	method := domain.ConfirmationMethodSignature
	return SystemTemplate{
		Key:                "event-services-agreement-v1",
		Title:              "Event Services Agreement",
		Description:        "Scope, logistics, timing, payment, cancellation, and disruption terms for event work.",
		Category:           "Events",
		Tags:               []string{"events", "on-location", "services"},
		ConfirmationMethod: method,
		Document: document(
			heading("Event services"),
			paragraph(variable("BUSINESS_NAME", true), text(" will provide "), variable("SERVICE_NAME", true), text(" for "), variable("CUSTOMER_NAME", true), text(" on "), variable("BOOKING_DATE", false), text(" during "), variable("BOOKING_TIME_RANGE", false), text(" at "), variable("BOOKING_LOCATION", false), text(".")),
			paragraph(text("Only the services, participants, assistants, materials, and add-ons stated in the booking are included. Additional scope may be refused or charged separately if approved.")),
			heading("Logistics and timing"),
			paragraph(text("The Customer will provide accurate access, parking, contact, venue, and readiness details. Delays caused by the Customer, attendees, venue restrictions, weather, or third parties may shorten the service or result in approved additional charges.")),
			heading("Fees and cancellation"),
			paragraph(text("The total is "), variable("TOTAL_AMOUNT", true), text(", including a deposit of "), variable("DEPOSIT_AMOUNT", false), text(" and a remaining balance of "), variable("REMAINING_AMOUNT", false), text(". Cancellation and postponement are governed by "), variable("CANCELLATION_POLICY", false), text(".")),
			heading("Events beyond control"),
			paragraph(text("Neither party is responsible for failure caused by events beyond reasonable control, including severe weather, sudden illness, venue closure, government restriction, or transportation disruption. The parties will cooperate on a practical remedy required by the booking terms and applicable law.")),
			acceptance(method),
		),
	}
}

func document(blocks ...aiapi.AgreementDocumentBlock) aiapi.DocumentSchema {
	for index := range blocks {
		blocks[index].ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
	}
	return aiapi.DocumentSchema{SchemaVersion: aiapi.AgreementDocumentSchemaVersion, Blocks: blocks}
}

func heading(value string) aiapi.AgreementDocumentBlock {
	return aiapi.AgreementDocumentBlock{Type: aiapi.AgreementBlockHeading, Level: 2, Content: []aiapi.AgreementInlineNode{text(value)}}
}

func paragraph(nodes ...aiapi.AgreementInlineNode) aiapi.AgreementDocumentBlock {
	return aiapi.AgreementDocumentBlock{Type: aiapi.AgreementBlockParagraph, Content: nodes}
}

func acceptance(method domain.ConfirmationMethod) aiapi.AgreementDocumentBlock {
	return aiapi.AgreementDocumentBlock{Type: aiapi.AgreementBlockAcceptance, Method: method.AIAPIValue()}
}

func text(value string) aiapi.AgreementInlineNode {
	return aiapi.AgreementInlineNode{Type: aiapi.AgreementInlineText, Text: value}
}

func variable(key string, bold bool) aiapi.AgreementInlineNode {
	return aiapi.AgreementInlineNode{Type: aiapi.AgreementInlineVariable, Key: key, Bold: bold}
}
