// Package transactionemail contains transactional email designs. Renderers perform no I/O; event capture and delivery are not wired yet.
package transactionemail

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"booking/go-server/internal/mailer"
	"github.com/google/uuid"
)

//go:embed templates/update.html
var source string

// Only fixed Outlook markup bypasses escaping, never event or recipient data.
var outlookFunctions = template.FuncMap{
	"outlookHead": func() template.HTML {
		return template.HTML(`<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->`)
	},
	"outlookStart": func() template.HTML {
		return template.HTML(`<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	},
	"outlookEnd": func() template.HTML { return template.HTML(`<!--[if mso]></td></tr></table><![endif]-->`) },
}

var layout = template.Must(template.New("update").Funcs(outlookFunctions).Parse(source))

// Event describes a committed event, not the time the worker renders the email.
// DeliveryID must be the persisted job ID so retries preserve Message-ID.
type Event struct {
	DeliveryID uuid.UUID
	Recipient  string
	OccurredAt time.Time
}

type row struct{ Label, Value string }
type view struct {
	AmountLabel, Action                                      string
	AmountSize                                               int
	Subject, Preheader, Category, Headline, Intro, Status    string
	Amount, Currency, ActionURL, NoteTitle, NoteBody, Footer string
	Signoff, FooterLabel, Email                              string
	Rows                                                     []row
}

var lastFour = regexp.MustCompile(`^[0-9]{4}$`)

func (e Event) validate() error {
	address, err := mail.ParseAddress(e.Recipient)
	if err != nil || address.Address != e.Recipient || e.DeliveryID == uuid.Nil || e.OccurredAt.IsZero() {
		return errors.New("invalid transactional email event")
	}
	return nil
}

func (e Event) recorded() row {
	return row{"RECORDED ON", e.OccurredAt.UTC().Format("2 January 2006 at 3:04 PM UTC")}
}

func render(e Event, family string, v view) (mailer.Message, error) {
	if err := e.validate(); err != nil {
		return mailer.Message{}, err
	}
	if v.ActionURL != "" {
		u, err := url.Parse(v.ActionURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return mailer.Message{}, errors.New("email action requires an HTTPS application URL")
		}
	}
	if v.AmountLabel == "" {
		v.AmountLabel = "Payout amount"
	}
	if v.Action == "" {
		v.Action = "Open TellBook"
	}
	v.Email = e.Recipient
	var html bytes.Buffer
	if err := layout.Execute(&html, v); err != nil {
		return mailer.Message{}, errors.New("could not render transactional email")
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%s\n\n%s\n\n%s\n", v.Headline, v.Intro, v.Status)
	if v.Amount != "" {
		fmt.Fprintf(&text, "%s: %s %s\n", v.AmountLabel, v.Currency, v.Amount)
	}
	for _, r := range v.Rows {
		fmt.Fprintf(&text, "\n%s\n%s\n", r.Label, r.Value)
	}
	if v.ActionURL != "" {
		fmt.Fprintf(&text, "\n%s: %s\n", v.Action, v.ActionURL)
	}
	fmt.Fprintf(&text, "\n%s\n%s\n\n%s\n\nSent to: %s\n— TellBook", v.NoteTitle, v.NoteBody, v.Footer, v.Email)
	return mailer.Message{ToEmail: e.Recipient, Subject: v.Subject, HTML: html.String(), Text: text.String(), MessageID: fmt.Sprintf("<%s-%s@mail.tellbook.app>", family, e.DeliveryID)}, nil
}
