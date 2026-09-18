package transactionemail

import (
	"errors"
	"strings"

	"booking/go-server/internal/mailer"
)

type RefundStatus string

const (
	RefundQueued       RefundStatus = "queued"
	RefundProcessing   RefundStatus = "processing"
	RefundFailed       RefundStatus = "failed"
	RefundManualReview RefundStatus = "manual_review"
)

type RefundInput struct {
	CustomerName string // Optional booking snapshot; shown only to the provider.
	Event
	Status                       RefundStatus
	Audience                     string // customer or provider
	Service, Provider, Reference string
	RequestedMinor               int64
	CurrencyCode                 string
	CurrencyExponent             uint8
	// Nil means not supplied; zero means confirmed that no part has completed.
	// Source this from successful attempts/adjustments, not aggregate status.
	ConfirmedMinor *int64
	BookingURL     string
}

func RenderRefund(in RefundInput) (mailer.Message, error) {
	if in.RequestedMinor <= 0 || strings.TrimSpace(in.Reference) == "" || strings.TrimSpace(in.Service) == "" || strings.TrimSpace(in.Provider) == "" || !bookingURLValid(in.BookingURL) {
		return mailer.Message{}, errors.New("invalid refund email details")
	}
	if in.Audience != "customer" && in.Audience != "provider" {
		return mailer.Message{}, errors.New("unsupported refund audience")
	}
	if in.ConfirmedMinor != nil && (*in.ConfirmedMinor < 0 || *in.ConfirmedMinor >= in.RequestedMinor || (in.Status == RefundQueued && *in.ConfirmedMinor != 0)) {
		return mailer.Message{}, errors.New("refund progress contradicts requested amount or status")
	}
	amount, err := formatEmailAmount(in.RequestedMinor, in.CurrencyCode, in.CurrencyExponent)
	if err != nil {
		return mailer.Message{}, err
	}
	v := view{Category: "REFUND UPDATE", AmountLabel: "Requested refund", Amount: amount, Currency: in.CurrencyCode, AmountSize: emailAmountSize(amount), Action: "View booking", ActionURL: in.BookingURL,
		Rows:      []row{{"SERVICE", in.Service}, {"PROVIDER", in.Provider}, {"REFUND REFERENCE", in.Reference}},
		NoteTitle: "Keep this reference handy.", NoteBody: "For help with this refund, contact your service provider using the contact details on your booking. Include the refund reference so they can identify the request.",
		Footer: "Check your booking for the latest payment and refund details.", Signoff: "Every update, a little clearer.", FooterLabel: "A refund progress update from TellBook."}
	if in.Audience == "provider" {
		v.Category = "CUSTOMER REFUND"
		if strings.TrimSpace(in.CustomerName) != "" {
			v.Rows[1] = row{"CUSTOMER", in.CustomerName}
		} else {
			v.Rows = append(v.Rows[:1], v.Rows[2:]...)
		}
		v.Action = "Review booking"
		v.NoteBody = "Review the booking and refund details in TellBook. If you need help, contact support with the refund reference."
	}
	switch in.Status {
	case RefundQueued:
		v.Status, v.Headline = "REQUEST RECORDED", "Your refund request is recorded."
		v.Intro = "A refund request has been recorded for this booking. It is awaiting processing."
		v.NoteTitle = "What happens next?"
		v.NoteBody = "The request will be checked before processing. A recorded request does not confirm that a refund has been completed."
		if in.Audience == "provider" {
			v.Headline = "The refund request is recorded."
		}
	case RefundProcessing:
		v.Status, v.Headline = "PROCESSING", "Your refund is in progress."
		v.Intro = "This refund request is being processed. Its full outcome has not yet been confirmed."
		v.NoteTitle = "A little more time to process."
		if in.Audience == "provider" {
			v.Headline = "The refund is in progress."
		}
	case RefundFailed:
		v.Status, v.Headline = "NEEDS ATTENTION", "There’s an update to your refund."
		v.Intro = "This refund request could not be completed as requested. Review the latest details on your booking."
		v.NoteTitle = "Check the outcome before the next step."
		if in.Audience == "provider" {
			v.Headline = "This refund needs attention."
			v.NoteBody = "Review any completed portions before requesting another refund. If you need help, contact TellBook support with the refund reference."
		}
	case RefundManualReview:
		v.Status, v.Headline = "AWAITING CONFIRMATION", "We’re awaiting a refund update."
		v.Intro = "The outcome of part or all of this refund request is unconfirmed. The request needs further checking."
		v.NoteTitle = "The outcome is still being checked."
		if in.Audience == "provider" {
			v.NoteBody = "Check the latest refund details before submitting another request. An unconfirmed outcome does not mean the refund failed. Contact support with the reference if you need help."
		}
	default:
		return mailer.Message{}, errors.New("refund status is not supported by progress emails")
	}
	if in.ConfirmedMinor != nil {
		confirmed, err := formatEmailAmount(*in.ConfirmedMinor, in.CurrencyCode, in.CurrencyExponent)
		if err != nil {
			return mailer.Message{}, err
		}
		v.Rows = append(v.Rows, row{"CONFIRMED REFUNDED SO FAR", in.CurrencyCode + " " + confirmed})
		if *in.ConfirmedMinor > 0 {
			v.Intro += " A portion has been confirmed refunded and is listed separately as part of the original request."
		}
	}
	v.Rows = append(v.Rows, in.recorded())
	v.Subject = "TellBook refund: " + strings.ToLower(v.Status)
	v.Preheader = v.Intro
	return render(in.Event, "refund-progress", v)
}
