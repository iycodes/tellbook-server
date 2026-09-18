# Agreement email design

The agreement lifecycle worker renders HTML and plain text from
`internal/agreements/worker/templates/agreement.html`. This is embedded in the
server binary and requires a rebuild/deployment, not a database template
migration. Email delivery still uses the existing agreement jobs and SMTP sender.

The design uses TellBook’s emblem, two-tone wordmark, ivory document card with
a burgundy rule, and a blush status label. It includes the agreement title,
provider, customer, and a single primary action plus a fallback text link.

| State | Confirmation method | Action / record |
| --- | --- | --- |
| Awaiting customer | Signature | Review & sign / Signature needed |
| Awaiting customer | Confirmation | Review & confirm / Confirmation needed |
| Completed | Signature | View agreement / Signed & recorded |
| Completed | Confirmation | View agreement / Confirmed & recorded |

Completion time comes from `agreement_acceptances.accepted_at` and is explicitly
shown in UTC. Missing acceptance time is omitted; an email never invents a date.
The message does not claim a PDF is attached or ready for download. The primary
action keeps the existing `/agreement/{token}` destination, which resolves the
current agreement view. No raw signature, document terms, or acceptance evidence
is inserted into the email body.

HTML uses contextual escaping. Existing status guards, recipient selection,
token recovery, stable per-job Message-ID, resend event recording, retries, and
PDF generation are preserved. Unknown confirmation methods fail rendering rather
than receiving invented instructions. A completed agreement is not described as
a completed payment or a confirmed appointment.

## Preview

From the server directory:

```sh
AGREEMENT_EMAIL_PREVIEW_DIR="$PWD/../output/playwright/email-notifications/agreements" \
  go test ./internal/agreements/worker -run TestWriteAgreementEmailPreviews -count=1
```

Open `/agreements/index.html` on the local email preview server. There are four
state/method variants and two long-content cases, each with HTML and plain-text
output. All data and links are fictional; preview generation does not connect
to a database, recover real tokens, or send email. The main booking gallery links
to this gallery. Set `GOCACHE=/tmp/tellbook-email-go-cache` if required by a
restricted workspace.

## Validation

```sh
go test ./internal/agreements/worker
```

Tests cover method-specific instructions, completed versus pending timestamps,
optional data, escaped titles/names/URLs, unsafe links, stable message identity,
and Outlook conditional markup. Browser checks include 320px, 375px, desktop,
and stripped head styles. Actual Gmail, Outlook, and Zoho review remains a
separate inbox check; no automatic dark-mode color fidelity is promised.
