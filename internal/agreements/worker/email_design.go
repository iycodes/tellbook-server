package worker

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"strings"

	"booking/go-server/internal/agreements/domain"
	"booking/go-server/internal/mailer"
)

//go:embed templates/agreement.html
var agreementEmailSource string

// Outlook conditional comments are fixed markup. All agreement and recipient
// values, including the private action URL, use contextual HTML escaping.
var agreementEmailHTML = template.Must(template.New("agreement").Funcs(template.FuncMap{
	"outlookHead": func() template.HTML {
		return template.HTML(`<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->`)
	},
	"outlookStart": func() template.HTML {
		return template.HTML(`<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	},
	"outlookEnd": func() template.HTML {
		return template.HTML(`<!--[if mso]></td></tr></table><![endif]-->`)
	},
}).Parse(agreementEmailSource))

type agreementEmailView struct {
	Subject, Preheader, Eyebrow, Headline, Intro, Status, Action  string
	Greeting, Title, BusinessName, CustomerName, Email, ActionURL string
	AcceptedAt, NoteTitle, NoteBody, Footer                       string
}

func renderAgreementEmail(agreement lifecycleAgreement, jobDedupeKey, actionURL string, completed bool) (mailer.Message, error) {
	method, err := domain.ParseConfirmationMethod(agreement.ConfirmationMethod)
	if err != nil {
		return mailer.Message{}, fmt.Errorf("invalid agreement email confirmation method: %w", err)
	}
	view := agreementEmailView{
		Subject:   "Review your " + agreement.Title,
		Preheader: "Your agreement is ready to review in TellBook.",
		Eyebrow:   "AN AGREEMENT FOR YOUR REVIEW", Headline: "The details, before the day.",
		Title: agreement.Title, BusinessName: strings.TrimSpace(agreement.BusinessName),
		Greeting: strings.TrimSpace(agreement.CustomerName), CustomerName: agreement.CustomerName,
		Email: agreement.SentToEmail, ActionURL: actionURL,
		Status: "Confirmation needed", Action: "Review & confirm",
		NoteTitle: "Take a moment with the details.",
		NoteBody:  "Read the agreement before confirming. If anything is unclear, contact your provider before you continue.",
		Footer:    "Completing an agreement is one step in your booking. Check your booking page for payment and appointment updates.",
	}
	if view.Greeting == "" {
		view.Greeting = "there"
	}
	if strings.TrimSpace(view.CustomerName) == "" {
		view.CustomerName = agreement.SentToEmail
	}
	if view.BusinessName == "" {
		view.BusinessName = "Your service provider"
	}
	view.Intro = view.BusinessName + " has shared an agreement with you. Review the terms and confirm when you’re ready."
	if method == domain.ConfirmationMethodSignature {
		view.Status, view.Action = "Signature needed", "Review & sign"
		view.Intro = view.BusinessName + " has shared an agreement with you. Review the terms and add your signature when you’re ready."
		view.NoteBody = "Read the agreement before signing. If anything is unclear, contact your provider before you continue."
	}
	if completed {
		view.Subject = agreement.Title + " completed"
		view.Preheader = "Your completed agreement is recorded in TellBook."
		view.Eyebrow, view.Headline = "AGREEMENT COMPLETE", "All agreed. All recorded."
		view.Intro = "Your agreement has been completed and recorded. You can return to it using the link below."
		view.Status, view.Action = "Confirmed & recorded", "View agreement"
		if method == domain.ConfirmationMethodSignature {
			view.Status = "Signed & recorded"
		}
		if agreement.AcceptedAt != nil {
			view.AcceptedAt = agreement.AcceptedAt.UTC().Format("2 January 2006 at 3:04 PM UTC")
		}
		view.NoteTitle, view.NoteBody = "Keep the details close.", "Use the link above whenever you need to revisit your agreement. The completed record is available on the agreement page."
	}
	var rendered bytes.Buffer
	if err := agreementEmailHTML.Execute(&rendered, view); err != nil {
		return mailer.Message{}, fmt.Errorf("render agreement email: %w", err)
	}
	accepted := ""
	if view.AcceptedAt != "" {
		accepted = "\nRecorded on: " + view.AcceptedAt
	}
	plain := fmt.Sprintf("Hi %s,\n\n%s\n\nAgreement: %s\nProvider: %s\nFor: %s\nStatus: %s%s\n\n%s: %s\n\n%s\n%s\n\n%s\n\nThis link gives access to your agreement. Keep it private.\n\n— TellBook", view.Greeting, view.Intro, view.Title, view.BusinessName, view.CustomerName, view.Status, accepted, view.Action, view.ActionURL, view.NoteTitle, view.NoteBody, view.Footer)
	return mailer.Message{
		ToEmail: agreement.SentToEmail, ToName: agreement.CustomerName, Subject: view.Subject,
		Text: plain, HTML: rendered.String(), MessageID: agreementEmailMessageID(agreement.ID, jobDedupeKey),
	}, nil
}
