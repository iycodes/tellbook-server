package authchallenge

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"

	"booking/go-server/internal/mailer"
	"github.com/google/uuid"
)

//go:embed templates/code.html
var authCodeHTMLSource string

// Only constant Outlook markup bypasses contextual escaping. Codes, addresses,
// and all other values always remain ordinary template strings.
var authCodeHTML = template.Must(template.New("code").Funcs(template.FuncMap{
	"outlookHead": func() template.HTML {
		return template.HTML(`<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->`)
	},
	"outlookStart": func() template.HTML {
		return template.HTML(`<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	},
	"outlookEnd": func() template.HTML {
		return template.HTML(`<!--[if mso]></td></tr></table><![endif]-->`)
	},
}).Parse(authCodeHTMLSource))

type authCodeView struct {
	Subject, Preheader, Eyebrow, Headline, Intro, CodeLabel string
	Code, Email, Expiry                                     string
}

func renderAuthCodeEmail(jobID uuid.UUID, payload deliveryPayload) (mailer.Message, error) {
	if !validCode(payload.Code) || payload.Security != nil || payload.Link != nil {
		return mailer.Message{}, errors.New("invalid authentication email content")
	}
	view := authCodeView{
		Subject: "Your TellBook verification code", Preheader: "Use your one-time code to continue in TellBook.",
		Eyebrow: "ACCOUNT VERIFICATION", Headline: "Let’s make sure it’s you.",
		Intro: "Enter this code in the TellBook window where you requested it to continue.", CodeLabel: "YOUR VERIFICATION CODE",
		Code: payload.Code, Email: payload.Destination,
		Expiry: fmt.Sprintf("This code expires %d minutes after delivery.", int(verificationTTL.Minutes())),
	}
	switch payload.Purpose {
	case PurposeSignIn:
		view.Subject, view.Preheader = "Your TellBook sign-in code", "Your one-time code to sign in or get started with TellBook."
		view.Eyebrow, view.Headline = "YOUR ACCESS TO TELLBOOK", "Let’s get you in."
		view.Intro, view.CodeLabel = "Enter this code in TellBook to sign in or finish creating your account.", "YOUR SIGN-IN CODE"
	case PurposePasswordReset:
		view.Subject, view.Preheader = "Reset your TellBook password", "Verify it’s you, then choose a new password."
		view.Eyebrow, view.Headline = "PASSWORD RESET", "A new password starts here."
		view.Intro, view.CodeLabel = "Enter this code in the TellBook password reset window. You can then choose a new password.", "YOUR RESET CODE"
	case PurposeLinkIdentity:
		view.Subject, view.Preheader = "Verify your email for TellBook", "Confirm this email address to link it to your TellBook account."
		view.Eyebrow, view.Headline = "EMAIL VERIFICATION", "Make this email yours."
		view.Intro, view.CodeLabel = "Enter this code in TellBook to link this email address to your account.", "YOUR EMAIL VERIFICATION CODE"
		// Empty or unrecognized purpose uses the neutral copy, so older encrypted
		// jobs remain deliverable without inventing a purpose for their code.
	}
	var htmlBody bytes.Buffer
	if err := authCodeHTML.Execute(&htmlBody, view); err != nil {
		return mailer.Message{}, errors.New("could not render authentication email")
	}
	textBody := fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s\n\nEnter this code only in TellBook where you requested it. Don’t share it in a message or over a call.\n\nIf you didn’t request this code, you can ignore this email.\n\nRequested for: %s\n\n— TellBook", view.Headline, view.Intro, view.Code, view.Expiry, view.Email)
	return mailer.Message{
		ToEmail: payload.Destination, Subject: view.Subject, Text: textBody, HTML: htmlBody.String(),
		MessageID: "<auth-code-" + jobID.String() + "@mail.tellbook.app>",
	}, nil
}
