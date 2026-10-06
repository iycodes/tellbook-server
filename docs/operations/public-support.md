# Public support form

The provider client exposes `/support` without requiring sign-in. Its progressive
SvelteKit action proxies validated requests to `POST /v1/support/requests`. The Go
API sends plain-text email immediately through the existing SMTP transport.
`GET /v1/support` returns only `{ "available": true | false }`; it never exposes
the support destination or SMTP configuration.

## Deployment configuration

Set `SUPPORT_EMAIL` on the Go **API/all process** to a single operator-controlled
mailbox. No address is invented or derived from `SMTP_USERNAME`. A malformed
nonempty destination fails configuration validation. Leave it blank to keep the
public page readable with an explicit unavailable state.

Reuse server-only `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`,
`SMTP_SECURITY`, and `SMTP_CONNECT_TIMEOUT`. `SMTP_FROM_EMAIL` is recommended;
the existing SMTP sender falls back to `SMTP_USERNAME` when From is blank, but
support is available only when that effective From is a valid bare mailbox.
`SMTP_FROM_NAME` supplies its display name. The visitor's validated email is
`Reply-To`; it never replaces the configured From or recipient. SMTP must be
available on the API process even in a deployment with separate delivery workers.
Support does not enable or depend on notification, authentication, or welcome
email rollout gates.

Set `CLIENT_PUBLIC_BASE_URL` to the actual public client origin. Support POSTs
require an exact matching `Origin`, independently of general API CORS settings.
The SvelteKit action validates the browser origin, forwards its own site origin,
and uses the existing server-only `PRIVATE_API_BASE_URL` routing configuration.
Do not add support destinations or SMTP credentials to client `PUBLIC_*` vars.

For per-visitor limits, include only the SvelteKit service's network/CIDR in
`TRUSTED_PROXY_CIDRS` on the API. The action sets one `X-Forwarded-For` value from
SvelteKit's `getClientAddress()` and never copies incoming forwarding headers.
Configure the Node adapter's client-address handling for your trusted edge proxy
(for example `ADDRESS_HEADER` / `XFF_DEPTH`), and ensure that edge strips spoofed
address headers and that untrusted clients cannot connect directly to the Node
service. Without a trusted API peer, requests share the Node server IP bucket.
Direct API requests use their socket peer; untrusted forwarding headers are
ignored by the existing Go middleware.

## Delivery and abuse limits

The dedicated support limiter permits a burst of three submissions and refills
one token per minute per trusted client IP. Caller-supplied cookies and
Authorization values cannot change that identity. It uses the existing shared
Redis limiter when configured and fails closed if that shared limiter fails.

Both the action and API bound payloads at 32 KiB, including encoded/multipart
overhead. Fields are bounded at 120 characters for name, 254 bytes for email,
180 characters for subject, and 20–6,000 characters for the message. Only known
topics are accepted. A hidden honeypot rejects automated form fills; unknown JSON
properties, including recipient overrides, are rejected. Header controls and
email lists/display addresses are rejected. User content remains plain text.

The send has a ten-second context deadline, including SMTP connect, greeting,
authentication, and transmission. There is no automatic retry or queued ticket:
success means the SMTP server accepted the message, not that a reply is scheduled
or that final mailbox delivery has been verified. Missing configuration and SMTP
failure return an error and preserve the submitted values. A connection loss
after SMTP acceptance can leave the outcome uncertain; callers should avoid
immediate repeated submissions. Requests and transport errors do not log message
bodies, visitor addresses, or credentials.

## Verification

Run `go test ./internal/publicsupport ./internal/mailer ./internal/config
./internal/server` and the client support Vitest tests. These use fake/in-process
senders and SMTP; they send no real email. A delivery check against a live mailbox
requires a known configured recipient and an explicitly scoped test.
