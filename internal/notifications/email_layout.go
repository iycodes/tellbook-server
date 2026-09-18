package notifications

import (
	"bytes"
	_ "embed"
	"html/template"
	"strings"
)

//go:embed templates/notification.html
var notificationHTMLSource string

// html/template removes HTML comments, including Outlook conditional comments.
// Only these fixed, developer-owned fragments bypass escaping; recipient data
// and action URLs always pass through html/template's contextual escaping.
var notificationHTML = template.Must(template.New("notification").Funcs(template.FuncMap{
	"outlookHead": func() template.HTML {
		return template.HTML(`<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->`)
	},
	"outlookStart": func() template.HTML {
		return template.HTML(`<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`)
	},
	"outlookEnd": func() template.HTML {
		return template.HTML(`<!--[if mso]></td></tr></table><![endif]-->`)
	},
}).Parse(notificationHTMLSource))

type notificationView struct {
	emailTemplateData
	Copy         emailCopy
	Greeting     string
	Category     string
	Preheader    string
	PaymentFirst bool
	ShowContact  bool
}

func renderNotificationHTML(data emailTemplateData, copy emailCopy, greeting string) (string, error) {
	category := "Booking update"
	if data.Type == "appointment_reminder" {
		category = "Appointment reminder"
	} else if strings.HasPrefix(data.Type, "payment_") {
		category = "Payment update"
	}
	view := notificationView{
		emailTemplateData: data, Copy: copy, Greeting: greeting,
		Category: category, Preheader: copy.Subject + " · " + data.ServiceTitle,
		PaymentFirst: strings.HasPrefix(data.Type, "payment_"),
		ShowContact:  data.Audience == "customer" && data.ProviderContactPhone != "",
	}
	var body bytes.Buffer
	if err := notificationHTML.Execute(&body, view); err != nil {
		return "", err
	}
	return body.String(), nil
}
