package transactionemail

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/money"
)

//go:embed templates/booking.html
var bookingSource string

var bookingLayout = template.Must(template.New("booking").Funcs(outlookFunctions).Parse(bookingSource))

type BookingDetails struct {
	Event
	Service, Provider string
	StartsAt          time.Time
	Timezone          string
	// BookingURL must come from the existing customer booking/claim URL builder.
	BookingURL string
}

type ReminderKind string

const (
	DepositReminder   ReminderKind = "deposit"
	BalanceReminder   ReminderKind = "balance"
	AgreementReminder ReminderKind = "agreement"
)

type ReminderInput struct {
	BookingDetails
	Kind ReminderKind
	// These must reflect current dispatch-time state, not the original job.
	BookingActive, StepOutstanding bool
	CheckedAt                      time.Time
	DueMinor                       int64
	Currency                       money.FormatSpec
	AgreementMethod                string
	// Only pass an actual step deadline. A missing deadline is not invented.
	Deadline *time.Time
}

type CompletionInput struct {
	BookingDetails
	BookingCompleted bool
	// ReviewEnabled must remain false until the review submission flow is live.
	ReviewEnabled, ReviewEligible, ReviewAlreadySubmitted bool
	ReviewURL                                             string
}

type bookingView struct {
	AmountSize                                     int
	StackDetails                                   bool
	Subject, Preheader, Category, Headline, Intro  string
	Month, Day, Service, Provider, When            string
	Status, Amount, PanelBody, Deadline            string
	Action, ActionURL, NoteTitle, NoteBody, Footer string
	Signoff, FooterLabel, Email                    string
}

func bookingBase(in BookingDetails) (bookingView, *time.Location, error) {
	if err := in.Event.validate(); err != nil {
		return bookingView{}, nil, err
	}
	if strings.TrimSpace(in.Service) == "" || strings.TrimSpace(in.Provider) == "" || in.StartsAt.IsZero() || strings.TrimSpace(in.Timezone) == "" || !bookingURLValid(in.BookingURL) {
		return bookingView{}, nil, errors.New("invalid booking email details")
	}
	zone, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return bookingView{}, nil, errors.New("invalid booking email timezone")
	}
	date := in.StartsAt.In(zone)
	return bookingView{StackDetails: len([]rune(in.Service)) > 55, Service: in.Service, Provider: in.Provider, Month: strings.ToUpper(date.Format("Jan")), Day: date.Format("02"), When: bookingTime(date), ActionURL: in.BookingURL, Email: in.Recipient}, zone, nil
}

func bookingTime(t time.Time) string {
	return t.Format("Monday, 2 January 2006 · 3:04 PM MST (UTC-07:00)")
}
func bookingURLValid(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func RenderReminder(in ReminderInput) (mailer.Message, error) {
	if !in.BookingActive || !in.StepOutstanding || in.CheckedAt.IsZero() {
		return mailer.Message{}, errors.New("booking step reminder is no longer eligible")
	}
	v, zone, err := bookingBase(in.BookingDetails)
	if err != nil {
		return mailer.Message{}, err
	}
	v.Category, v.Preheader = "A LITTLE REMINDER", "There’s an outstanding step for your TellBook booking."
	v.Intro = "Here’s a reminder about an outstanding step for your booking with " + in.Provider + "."
	v.NoteTitle, v.NoteBody = "Already taken care of?", "Your booking page shows the latest status. If you’ve just completed this step, check there before taking any further action."
	v.Footer = "Your booking page shows any remaining steps and the provider’s confirmation status."
	v.Signoff, v.FooterLabel = "A little closer to your appointment.", "A booking-step reminder from TellBook."
	switch in.Kind {
	case DepositReminder, BalanceReminder:
		if in.DueMinor <= 0 {
			return mailer.Message{}, errors.New("payment reminder requires an outstanding amount")
		}
		v.Amount, err = money.Format(in.DueMinor, in.Currency)
		if err != nil {
			return mailer.Message{}, err
		}
		v.AmountSize = 22
		if len([]rune(v.Amount)) > 14 {
			v.AmountSize = 15
		}
		if len([]rune(v.Amount)) > 20 {
			v.AmountSize = 12
		}
		v.Action = "View payment details"
		v.NoteBody = "If you’ve already paid, check your booking for the latest payment status before making another payment."
		if in.Kind == DepositReminder {
			v.Subject, v.Headline, v.Status = "A reminder about your booking deposit", "Your booking has a next step.", "DEPOSIT OUTSTANDING"
			v.PanelBody = "This is the deposit amount still outstanding. Open your booking to review the payment details."
		} else {
			v.Subject, v.Headline, v.Status = "A reminder about your remaining balance", "A little reminder about your balance.", "REMAINING BALANCE"
			v.PanelBody = "This is the remaining balance recorded for your booking. Review the details before making a payment."
		}
	case AgreementReminder:
		v.Subject, v.Headline, v.Status = "Your booking agreement is awaiting you", "A moment for the details.", "AGREEMENT OUTSTANDING"
		v.Action = "Review agreement"
		switch in.AgreementMethod {
		case "signature":
			v.PanelBody = "Your agreement still needs your signature. Read the terms carefully before signing."
		case "confirmation":
			v.PanelBody = "Your agreement still needs your confirmation. Read the terms carefully before confirming."
		default:
			return mailer.Message{}, errors.New("unsupported agreement reminder method")
		}
	default:
		return mailer.Message{}, errors.New("unsupported booking reminder")
	}
	if in.Deadline != nil {
		if !in.Deadline.After(in.CheckedAt) {
			return mailer.Message{}, errors.New("booking step deadline has passed")
		}
		v.Deadline = bookingTime(in.Deadline.In(zone))
	}
	return renderBooking(in.Event, "booking-step", v)
}

func RenderCompletion(in CompletionInput) (mailer.Message, error) {
	if !in.BookingCompleted {
		return mailer.Message{}, errors.New("booking completion email is not eligible")
	}
	v, _, err := bookingBase(in.BookingDetails)
	if err != nil {
		return mailer.Message{}, err
	}
	v.Subject, v.Preheader = "Your TellBook appointment is complete", "Your completed appointment, kept in one place."
	v.Category, v.Headline = "AFTER YOUR APPOINTMENT", "Thanks for booking with TellBook."
	v.Intro = "Your appointment with " + in.Provider + " has been marked complete. Here’s a record of the visit."
	v.Status, v.PanelBody = "APPOINTMENT COMPLETED", "You can return to your booking to view the appointment details and any payment updates."
	v.Action = "View booking"
	v.NoteTitle, v.NoteBody = "Keep the details close.", "Your booking page is still available if you need to check the appointment or contact your provider."
	v.Footer = "For payment and refund updates, check the latest details on your booking page."
	v.Signoff, v.FooterLabel = "Good plans. Thoughtfully kept.", "An appointment follow-up from TellBook."
	if in.ReviewEnabled && in.ReviewEligible && !in.ReviewAlreadySubmitted {
		if !bookingURLValid(in.ReviewURL) {
			return mailer.Message{}, errors.New("review invitation requires a valid review URL")
		}
		v.Subject, v.Preheader = "How was your appointment?", "Share your experience of your completed appointment."
		v.Headline, v.Status = "How did it go?", "YOUR EXPERIENCE, IN YOUR WORDS"
		v.PanelBody = "Share an honest review of your appointment. What worked well, and what could have been better?"
		v.Action, v.ActionURL = "Share your experience", in.ReviewURL
		v.NoteTitle, v.NoteBody = "A few words can help.", "Your feedback helps other customers decide and gives your provider a chance to learn. Leaving a review is optional."
	}
	return renderBooking(in.Event, "booking-completed", v)
}

func renderBooking(event Event, family string, v bookingView) (mailer.Message, error) {
	var html bytes.Buffer
	if err := bookingLayout.Execute(&html, v); err != nil {
		return mailer.Message{}, errors.New("could not render booking follow-up")
	}
	text := fmt.Sprintf("%s\n\n%s\n\nService: %s\nProvider: %s\nWhen: %s\n\n%s\n", v.Headline, v.Intro, v.Service, v.Provider, v.When, v.Status)
	if v.Amount != "" {
		text += v.Amount + "\n"
	}
	text += v.PanelBody + "\n"
	if v.Deadline != "" {
		text += "\nComplete by: " + v.Deadline + "\n"
	}
	text += fmt.Sprintf("\n%s: %s\n\n%s\n%s\n\n%s\n\nSent to: %s\n— TellBook", v.Action, v.ActionURL, v.NoteTitle, v.NoteBody, v.Footer, v.Email)
	return mailer.Message{ToEmail: event.Recipient, Subject: v.Subject, Text: text, HTML: html.String(), MessageID: fmt.Sprintf("<%s-%s@mail.tellbook.app>", family, event.DeliveryID)}, nil
}
