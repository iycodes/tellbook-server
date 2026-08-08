package domain

type VariableValueType string

const (
	VariableValueText     VariableValueType = "text"
	VariableValueEmail    VariableValueType = "email"
	VariableValuePhone    VariableValueType = "phone"
	VariableValueDate     VariableValueType = "date"
	VariableValueTime     VariableValueType = "time"
	VariableValueMoney    VariableValueType = "money"
	VariableValueInteger  VariableValueType = "integer"
	VariableValueDuration VariableValueType = "duration"
)

type VariableEmptyBehavior string

const VariableEmptyError VariableEmptyBehavior = "error"

type VariableResolutionContext string

const (
	VariableContextTemplatePreview VariableResolutionContext = "template_preview"
	VariableContextQuote           VariableResolutionContext = "quote"
	VariableContextBooking         VariableResolutionContext = "booking"
	VariableContextManualAgreement VariableResolutionContext = "manual_agreement"
)

type VariableDefinition struct {
	Key            string                      `json:"key"`
	Label          string                      `json:"label"`
	Description    string                      `json:"description"`
	ValueType      VariableValueType           `json:"value_type"`
	EmptyBehavior  VariableEmptyBehavior       `json:"empty_value_behavior"`
	Contexts       []VariableResolutionContext `json:"resolution_contexts"`
	PreviewExample string                      `json:"preview_example"`
}

var allAgreementVariableContexts = []VariableResolutionContext{
	VariableContextTemplatePreview,
	VariableContextQuote,
	VariableContextBooking,
	VariableContextManualAgreement,
}

var agreementVariableRegistry = []VariableDefinition{
	newVariable("BUSINESS_NAME", "Business name", "The provider or business name.", VariableValueText, "Amara Lash Studio"),
	newVariable("BUSINESS_LOCATION", "Business location", "The provider's business location label.", VariableValueText, "Lekki, Lagos"),
	newVariable("CUSTOMER_NAME", "Customer name", "The customer's full name.", VariableValueText, "Ada Okafor"),
	newVariable("CUSTOMER_EMAIL", "Customer email", "The customer's email address.", VariableValueEmail, "ada@example.com"),
	newVariable("CUSTOMER_PHONE", "Customer phone", "The customer's phone number.", VariableValuePhone, "+234 801 234 5678"),
	newVariable("SERVICE_NAME", "Service name", "The booked service title.", VariableValueText, "Classic lash extensions"),
	newVariable("BOOKING_DATE", "Booking date", "The booking date in long readable form.", VariableValueDate, "18 August 2026"),
	newVariable("BOOKING_START_TIME", "Booking start time", "The booking start time.", VariableValueTime, "10:00 AM"),
	newVariable("BOOKING_END_TIME", "Booking end time", "The booking end time.", VariableValueTime, "11:30 AM"),
	newVariable("BOOKING_TIME_RANGE", "Booking time range", "The full booking time range.", VariableValueText, "10:00 AM to 11:30 AM"),
	newVariable("BOOKING_LOCATION", "Booking location", "The booking or service location.", VariableValueText, "Amara Lash Studio, Lekki"),
	newVariable("TOTAL_AMOUNT", "Total amount", "The total amount due for the booking.", VariableValueMoney, "NGN 25,000"),
	newVariable("DEPOSIT_AMOUNT", "Deposit amount", "The deposit amount due for the booking.", VariableValueMoney, "NGN 10,000"),
	newVariable("REMAINING_AMOUNT", "Remaining amount", "The remaining amount after the deposit.", VariableValueMoney, "NGN 15,000"),
	newVariable("DURATION_MINUTES", "Duration in minutes", "The numeric service duration in minutes.", VariableValueInteger, "90"),
	newVariable("SERVICE_DURATION", "Service duration", "The service duration as a readable label.", VariableValueDuration, "1 hour 30 minutes"),
	newVariable("BOOKING_NOTES", "Booking notes", "Booking notes supplied by the customer or provider.", VariableValueText, "Sensitive eyes; use a light-volume finish."),
	newVariable("CANCELLATION_POLICY", "Cancellation policy", "The service cancellation policy text.", VariableValueText, "Cancel at least 24 hours before the appointment."),
	newVariable("LATENESS_POLICY", "Lateness policy", "The service lateness policy text.", VariableValueText, "Appointments may be shortened after a 15-minute delay."),
}

var agreementVariableByKey = buildAgreementVariableLookup()

func AgreementVariableRegistry() []VariableDefinition {
	result := make([]VariableDefinition, len(agreementVariableRegistry))
	for index, definition := range agreementVariableRegistry {
		result[index] = cloneVariableDefinition(definition)
	}
	return result
}

func AgreementVariable(key string) (VariableDefinition, bool) {
	definition, ok := agreementVariableByKey[key]
	if !ok {
		return VariableDefinition{}, false
	}
	return cloneVariableDefinition(definition), true
}

func AgreementVariableKeySet() map[string]struct{} {
	result := make(map[string]struct{}, len(agreementVariableRegistry))
	for _, definition := range agreementVariableRegistry {
		result[definition.Key] = struct{}{}
	}
	return result
}

func newVariable(key, label, description string, valueType VariableValueType, previewExample string) VariableDefinition {
	return VariableDefinition{
		Key:            key,
		Label:          label,
		Description:    description,
		ValueType:      valueType,
		EmptyBehavior:  VariableEmptyError,
		Contexts:       cloneVariableContexts(allAgreementVariableContexts),
		PreviewExample: previewExample,
	}
}

func buildAgreementVariableLookup() map[string]VariableDefinition {
	result := make(map[string]VariableDefinition, len(agreementVariableRegistry))
	for _, definition := range agreementVariableRegistry {
		result[definition.Key] = definition
	}
	return result
}

func cloneVariableDefinition(definition VariableDefinition) VariableDefinition {
	definition.Contexts = cloneVariableContexts(definition.Contexts)
	return definition
}

func cloneVariableContexts(contexts []VariableResolutionContext) []VariableResolutionContext {
	return append([]VariableResolutionContext(nil), contexts...)
}
