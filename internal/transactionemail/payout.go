package transactionemail

import (
	"errors"
	"strings"

	"booking/go-server/internal/mailer"
)

type PayoutStatus string

const (
	PayoutStatusCreated        PayoutStatus = "created"
	PayoutStatusPending        PayoutStatus = "pending"
	PayoutStatusSuccessful     PayoutStatus = "successful"
	PayoutStatusFailed         PayoutStatus = "failed"
	PayoutStatusReversed       PayoutStatus = "reversed"
	PayoutStatusRequiresAction PayoutStatus = "requires_action"
	PayoutStatusUnknown        PayoutStatus = "unknown"
	PayoutStatusCancelled      PayoutStatus = "cancelled"
)

type PayoutInput struct {
	Event
	Status       PayoutStatus
	AmountMinor  int64
	CurrencyCode string
	// CurrencyExponent must come from the configured currency metadata.
	CurrencyExponent                            uint8
	Reference, InstitutionName, AccountLastFour string
	// ApplicationURL must be built from trusted application configuration.
	ApplicationURL string
}

func RenderPayout(in PayoutInput) (mailer.Message, error) {
	if in.AmountMinor <= 0 || strings.TrimSpace(in.Reference) == "" || strings.TrimSpace(in.InstitutionName) == "" || !lastFour.MatchString(in.AccountLastFour) {
		return mailer.Message{}, errors.New("invalid payout email details")
	}
	amount, err := formatEmailAmount(in.AmountMinor, in.CurrencyCode, in.CurrencyExponent)
	if err != nil {
		return mailer.Message{}, err
	}
	v := view{Category: "PROVIDER PAYOUT", Amount: amount, Currency: in.CurrencyCode, ActionURL: in.ApplicationURL,
		NoteTitle: "Keep this reference handy.", NoteBody: "Open TellBook to review this payout. If you need help, include the payout reference when contacting support.",
		Footer:  "This update is about a payout to your account. Check TellBook for its latest status.",
		Signoff: "Your work. Every detail accounted for.", FooterLabel: "A provider payout update from TellBook.",
		Rows: []row{{"DESTINATION", in.InstitutionName + " · ending " + in.AccountLastFour}, {"PAYOUT REFERENCE", in.Reference}, in.recorded()}}
	switch in.Status {
	case PayoutStatusPending:
		v.Status, v.Headline = "PROCESSING", "Your payout is in progress."
		v.Intro = "Your payout is being processed. We’ll confirm the outcome when an update is available."
		v.NoteTitle, v.NoteBody = "No action needed right now.", "This transfer is still in progress. Check TellBook for the latest status before requesting another payout."
	case PayoutStatusSuccessful:
		v.Status, v.Headline = "SUCCESSFUL", "Your payout is complete."
		v.Intro = "The payment provider has confirmed this payout as successful. The destination and reference are recorded below."
	case PayoutStatusFailed:
		v.Status, v.Headline = "FAILED", "Your payout couldn’t be completed."
		v.Intro = "The payment provider reported that this payout failed. Review its details in TellBook before taking another step."
		v.NoteTitle, v.NoteBody = "Review before trying again.", "Check your payout destination and the latest payout details in TellBook. Contact support if you need help deciding what to do next."
	case PayoutStatusReversed:
		v.Status, v.Headline = "REVERSED", "There’s an update to your payout."
		v.Intro = "The payment provider reported a reversal for this payout. Its previous status has changed."
		v.NoteTitle, v.NoteBody = "Check your current balance.", "Review the payout and available balance in TellBook. A reversal notice does not confirm that funds are available for another payout."
	case PayoutStatusRequiresAction:
		v.Status, v.Headline = "ACTION REQUIRED", "Your payout needs attention."
		v.Intro = "The payment provider reported that action is required for this payout. Open TellBook to review the available details."
		v.NoteTitle, v.NoteBody = "Review the next step.", "Check this payout in TellBook. If no next step is shown, contact support with the payout reference."
	case PayoutStatusUnknown:
		v.Status, v.Headline = "AWAITING CONFIRMATION", "We’re awaiting a payout update."
		v.Intro = "The outcome of this payout has not been confirmed. It is not yet marked successful or failed."
		v.NoteTitle, v.NoteBody = "Wait for confirmation.", "Check TellBook for the latest status before requesting another payout. Contact support if you need help with this transfer."
	case PayoutStatusCancelled:
		v.Status, v.Headline = "CANCELLED", "This payout was cancelled."
		v.Intro = "This payout has been marked cancelled. Review your current balance and payout details in TellBook."
	default:
		return mailer.Message{}, errors.New("payout status is not ready for a notification")
	}
	v.AmountSize = emailAmountSize(amount)
	v.Subject = "TellBook payout: " + strings.ToLower(v.Status)
	v.Preheader = v.Headline
	return render(in.Event, "provider-payout", v)
}
