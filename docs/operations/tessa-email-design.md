# Tessa connection emails

The authentication email worker renders the existing `tessa_link_email` and
`tessa_security_email` jobs as HTML and plain text through
`internal/authchallenge/tessa_email_design.go`. The four variants are a linking
code, a connected notice, a number-replaced notice, and a disconnected notice.

The design uses the public emblem, two-tone TellBook wordmark, burgundy accent,
ivory connection panel, and a blush code panel. Essential styles are inline;
fluid presentation tables and conditional 600px Outlook wrappers provide the
layout. Light color-scheme metadata requests the intended palette; inbox apps
may still recolour messages. No remote fonts, interactive elements, or action
links are required.

Only the last four digits of the WhatsApp number are available to the renderer.
The linking code stays selectable with leading zeros and is absent from the
subject and preheader. Its expiry is ten minutes after the request, matching
the existing challenge lifecycle. Instructions direct the recipient back to
the originating Tessa WhatsApp conversation, not to the account sign-in flow.

Security notices include the recorded event time in explicit UTC, contain no
code, and direct recipients to sign in to TellBook directly if they do not
recognise the change. The wording distinguishes assistant connections from
login numbers and booking reminder preferences. Dynamic values are escaped by
`html/template`; only constant Outlook markup is trusted HTML.

Queue selection, dispatchability checks, deadlines, message IDs, SMTP outcome
handling, feature flags, and event acceptance are unchanged. No migration or
new configuration is needed. Deploying the worker code applies this design.

## Validation and previews

Run `go test ./internal/authchallenge ./internal/notifications` for renderer and
existing package checks. Database lifecycle tests require `TEST_DATABASE_URL`;
they are skipped when it is unset. The security lifecycle test additionally
checks that the worker sends the new HTML.

Set `TESSA_EMAIL_PREVIEW_DIR` and run
`go test ./internal/authchallenge -run TestWriteTessaEmailPreviews -count=1`
to generate the gallery and HTML/plain-text fixtures. The four variants also
have long-recipient examples. All sample addresses, codes, and events are
fictional.

Local browser inspection passed 32 layout checks: eight fixtures at 320px,
375px, and 760px, plus each at 320px with style elements removed. Desktop and
mobile screenshots were visually reviewed. This is not actual Gmail, Outlook,
or Zoho inbox verification. No sample emails were sent during this design pass.
