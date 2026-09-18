# Booking and payment email design

Booking and payment deliveries render `internal/notifications/templates/notification.html`
through the existing notification worker. The template is embedded in the Go binary:
rebuild/redeploy the server to ship edits. No migration or template activation is needed.
Delivery flags, recipients, consent checks, event planning, SMTP configuration, and
booking deep links are unchanged.

## Design

- TellBook light palette: burgundy `#af1f4a`, ivory `#faf9f4`, blush `#f7e9ee`,
  dark plum `#2d2426`, muted plum `#584144`, and border `#e2d8d2`.
- Georgia headlines provide the editorial feel of the application's Fraunces font.
  Arial/Helvetica body text avoids remote font dependencies.
- A 600px fluid container with an Outlook conditional fixed-width wrapper.
  Layout tables have `role="presentation"`; essential colors, spacing, and type
  are inline, with `bgcolor` fallbacks on major surfaces.
- Payment emails put the booking balance before the appointment. Booking emails
  put the appointment first. The monetary values are the existing current ledger
  balance, not the amount of an individual charge/refund. "Net paid" reflects that.
- A single burgundy action and a secondary text link preserve the existing guest
  claim or signed-in booking destination. No tracking is added.
- The header uses the public emblem at
  `https://bookly-public.iycodes.com/bookings.png` with explicit 40px dimensions.
  The two-tone TellBook wordmark is live text, so it remains visible when images
  are blocked. The adjacent decorative emblem uses empty alt text to avoid
  repeating the brand name for screen readers.
- The remaining balance has its own full-width row for large amounts. Mobile
  CSS reduces margins. Essential content
  remains readable if an inbox removes the style block.
- Light color-scheme metadata requests a consistent light appearance. There is
  no separate dark theme or forced-color workaround. Some clients can still
  automatically recolor email; exact inbox color fidelity needs real-client QA.

Copy stays in `emailTemplateRegistry` in `email.go`. HTML uses contextual escaping
from Go's `html/template`; do not cast recipient content to `template.HTML`.
The only trusted HTML fragments are fixed Outlook conditional wrappers, because
`html/template` otherwise removes conditional comments.

## Offline preview

From the server directory:

```sh
EMAIL_PREVIEW_DIR="$PWD/../output/playwright/email-notifications" \
  go test ./internal/notifications -run TestWriteEmailPreviews -count=1
python3 -m http.server 8767 --bind 127.0.0.1 \
  --directory ../output/playwright/email-notifications
```

Open `http://127.0.0.1:8767/`. The gallery renders all 19 audience/event variants,
an awaiting-confirmation reminder, and a long-content case using the production
renderer. Each has HTML and plain-text output. Data is fictional and the command
does not connect to SMTP or send messages. If the default Go cache is read-only,
prefix the test command with `GOCACHE=/tmp/tellbook-email-go-cache`.

## Validation

```sh
go test ./internal/notifications ./internal/mailer
```

Tests cover all audience/event renders, escaped content, unsafe HTML link
handling, Outlook wrapper preservation, payment/booking section order, existing
guest links, optional provider contact, and pending-confirmation wording.

Browser previews check desktop, 375px, 320px, and removal of the style block.
Browser dark-preference emulation does not simulate Gmail/Outlook's automatic
color inversion. Before release, review actual messages in Gmail web/mobile,
Apple Mail, Outlook desktop/web, and with images disabled. This design pass does
not certify those inbox renderers or change production delivery configuration.

References: [Litmus on dark-mode behavior](https://www.litmus.com/blog/the-ultimate-guide-to-dark-mode-for-email-marketers),
[Can I Email support data](https://www.caniemail.com/features/).
