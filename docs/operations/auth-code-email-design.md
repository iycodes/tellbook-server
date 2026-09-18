# Verification code email design

The `auth_code_email` delivery worker now renders HTML and plain text using
`internal/authchallenge/templates/code.html`. It is embedded in the Go binary;
rebuild and deploy the server to use this design. No database migration or
template activation is required. Authentication delivery remains controlled by
`AUTH_EMAIL_ENABLED` and the existing SMTP worker configuration.

The design keeps the TellBook emblem and two-tone wordmark, a compact white
card, and a blush code panel. The six-digit code is one selectable text node
with CSS letter spacing; leading zeroes survive copying. There are no action
links, remote fonts, or scripts. The image is decorative and the wordmark and
code remain readable when images are blocked. The light layout uses inline
styles, HTML tables, explicit background colors, and an Outlook width fallback.

New encrypted delivery payloads include the existing challenge purpose:

| Purpose | Email |
| --- | --- |
| `sign_in` | “Let’s get you in.” Sign in or finish creating an account. |
| `password_reset` | “A new password starts here.” Verify before choosing a password. |
| `link_identity` | “Make this email yours.” Link the email address to the account. |
| Missing/unknown | Neutral verification copy, including already queued payloads. |

The code remains in the encrypted payload and rendered message body; it is
not added to subjects or preview text. No authentication lifetime, verification,
retry, destination, or synthetic password-reset behavior changes. HTML rendering
failure follows the worker’s permanent-failure path with a generic error code.
Tessa connection-security and WhatsApp-linking messages keep their existing
separate content and delivery checks.

## Offline preview

From the server directory:

```sh
AUTH_CODE_EMAIL_PREVIEW_DIR="$PWD/../output/playwright/email-notifications/auth" \
  go test ./internal/authchallenge -run TestWriteAuthCodeEmailPreviews -count=1
```

Use the email gallery’s existing local server and open `/auth/index.html`.
The four purposes and four long-address cases use the fictional code `028461`.
No authentication challenge is issued and no email is sent. Each preview has a
plain-text file. The main booking gallery links to this gallery.

## Validation

```sh
go test ./internal/authchallenge ./internal/auth ./internal/marketplaceauth
```

Tests cover purpose-specific subjects, old payload compatibility, exact code
text and leading zeroes, expiry guidance, stable message IDs, escaped recipient
values, invalid codes, Tessa payload separation, and Outlook conditional markup.
Database and live-email conformance tests remain opt-in. Use
`GOCACHE=/tmp/tellbook-email-go-cache` when the default cache is read-only.

Browser checks include 320px, 375px, desktop, and stripped head styles. Actual
Gmail, Outlook, and Zoho rendering still needs separate inbox review; browser
dark-preference emulation does not reproduce inbox automatic color inversion.
