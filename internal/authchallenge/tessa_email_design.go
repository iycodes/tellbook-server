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

//go:embed templates/tessa.html
var tessaEmailHTMLSource string

var tessaEmailHTML = template.Must(template.New("tessa").Funcs(template.FuncMap{
	"outlookHead": func() template.HTML {
		return template.HTML(`<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->`)
	},
	"outlookStart": func() template.HTML {
		return template.HTML(`<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	},
	"outlookEnd": func() template.HTML {
		return template.HTML(`<!--[if mso]></td></tr></table><![endif]-->`)
	},
}).Parse(tessaEmailHTMLSource))

type tessaEmailView struct {
	Subject, Preheader, Headline, Intro, Status, PhoneSuffix string
	Code, OccurredAt, NoteTitle, NoteBody, Footer, Email     string
}

func renderTessaEmail(jobID uuid.UUID, templateKey string, payload deliveryPayload) (mailer.Message, error) {
	v := tessaEmailView{Email: payload.Destination}
	prefix := "tessa-security"
	switch templateKey {
	case tessaLinkEmailTemplate:
		if !validLinkEmailPayload(payload.Link) || !validCode(payload.Code) || payload.Security != nil {
			return mailer.Message{}, errors.New("invalid Tessa linking email content")
		}
		prefix = "tessa-link"
		v.Subject, v.Preheader = "Connect WhatsApp to your TellBook Tessa assistant", "Your code to connect WhatsApp to Tessa."
		v.Headline, v.Intro = "A little closer to Tessa.", "A request was made to connect this WhatsApp number to your provider account’s Tessa assistant."
		v.Status, v.PhoneSuffix, v.Code = "CONNECTION REQUEST", payload.Link.PhoneSuffix, payload.Code
		v.NoteTitle = "Continue in your Tessa conversation."
		v.NoteBody = "Enter this code only in the Tessa WhatsApp conversation where you requested this connection. This is not a sign-in code."
		v.Footer = "If you didn’t request this connection, do not share the code and ignore this email."
	case tessaSecurityEmailTemplate:
		if !validSecurityEmailPayload(payload.Security) || payload.Code != "" || payload.Link != nil {
			return mailer.Message{}, errors.New("invalid Tessa security email content")
		}
		p := payload.Security
		v.PhoneSuffix, v.OccurredAt = p.PhoneSuffix, p.OccurredAt.UTC().Format("2 January 2006 at 3:04 PM UTC")
		v.Subject, v.Preheader = "TellBook security: Tessa WhatsApp connection", "A change was recorded for your Tessa WhatsApp connection."
		switch p.Kind {
		case "linked":
			v.Headline, v.Status = "You’re connected to Tessa.", "WHATSAPP CONNECTED"
			v.Intro = "The WhatsApp number below was connected to your TellBook Tessa assistant."
		case "replaced":
			v.Headline, v.Status = "A new number for Tessa.", "NUMBER REPLACED"
			v.Intro = "The WhatsApp number below replaced the previous number connected to your TellBook Tessa assistant."
		case "disconnected":
			v.Headline, v.Status = "This connection has ended.", "WHATSAPP DISCONNECTED"
			v.Intro = "The WhatsApp number below was disconnected from your TellBook Tessa assistant."
		}
		v.NoteTitle = "Don’t recognise this change?"
		v.NoteBody = "Sign in to TellBook directly, review your Tessa WhatsApp connection and secure your account. This email contains no sign-in or verification code."
		v.Footer = "This is an assistant connection change, not a change to your login number or booking reminder preferences."
	default:
		return mailer.Message{}, errors.New("unsupported Tessa email template")
	}
	var body bytes.Buffer
	if err := tessaEmailHTML.Execute(&body, v); err != nil {
		return mailer.Message{}, errors.New("could not render Tessa email")
	}
	text := fmt.Sprintf("%s\n\n%s\n\n%s\nWhatsApp ending %s\n", v.Headline, v.Intro, v.Status, v.PhoneSuffix)
	if v.Code != "" {
		text += "\nYour linking code:\n\n" + v.Code + "\n\nIt expires 10 minutes after the request.\n"
	} else {
		text += "Recorded on: " + v.OccurredAt + "\n"
	}
	text += fmt.Sprintf("\n%s\n%s\n\n%s\n\nSent to: %s\n\n— TellBook", v.NoteTitle, v.NoteBody, v.Footer, v.Email)
	return mailer.Message{ToEmail: payload.Destination, Subject: v.Subject, Text: text, HTML: body.String(), MessageID: fmt.Sprintf("<%s-%s@mail.tellbook.app>", prefix, jobID)}, nil
}
