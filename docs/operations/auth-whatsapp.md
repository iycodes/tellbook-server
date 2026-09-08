# WhatsApp authentication rollout

WhatsApp passwordless authentication is shared by the provider and marketplace account realms,
but its sender and rollout flag are independent from booking notifications. The auth sender can
send only the registered `v_c_x` template. Its approved Utility-template wrapper is pinned in the
typed registry, and its only variable is exactly `Use the code <six digits>`.

## Readiness gate

Keep `AUTH_WHATSAPP_ENABLED=false` until the target deployment has all of the following:

- current database migrations and working auth queue access;
- `AUTH_DELIVERY_ENCRYPTION_KEYS`, `AUTH_DELIVERY_ACTIVE_KEY`, and the separate
  `AUTH_DESTINATION_HMAC_KEY`;
- `WABA_TOKEN`, `WHATSAPP_BUSINESS_ACCOUNT_ID`, and `WABA_PHONE_NUMBER_ID` on worker processes;
- `META_APP_SECRET`, `META_VERIFY_TOKEN`, the same WABA ID, and the same phone-number ID on API
  processes;
- the Meta app subscribed to the WABA and its signed callback routed to the API webhook;
- a passing auth-only template check:

```bash
make auth-whatsapp-template-conformance
```

The broader `make whatsapp-template-conformance` may still fail for unrelated pending booking or
welcome templates. Those results do not authorize or block the separate auth-only sender.

Before enabling the flag, use an explicitly authorized recipient to start one provider and one
marketplace WhatsApp challenge. Retain evidence that each job moves from `queued` to `accepted`,
the received message contains the expected six-digit code, and that code creates or signs into the
correct realm. Then confirm a signed Meta callback advances that delivery to `sent` or `delivered`.
Do not retain the raw destination or code in the evidence.

The opt-in sender/verification check performs both realm journeys and cleans up only the two
challenges it creates:

```bash
export AUTH_WHATSAPP_TEST_DESTINATION='+234...'
make auth-whatsapp-live-conformance
```

This command sends two real WhatsApp messages. Run it only for a recipient who has authorized the
test. The deterministic PostgreSQL integration suite covers the lifecycle without external sends;
the live check proves the configured Meta sender for each realm.

## Runtime behavior

- A worker never starts a Graph request after the 90-second delivery deadline.
- Meta transient and rate-limit failures use the durable queue's bounded retry schedule; the HTTP
  client has no retry layer.
- An ambiguous send becomes `unknown` and is never automatically resent. A matching signed Meta
  callback may resolve it before the deadline; otherwise it expires.
- Graph acceptance starts the ten-minute verification lifetime transactionally and clears the
  encrypted payload. Permanent failure or expiry also clears it.
- Booking-notification and auth status processors partition receipts by the opaque delivery UUID,
  so they cannot consume each other's callbacks.

If the token, phone quality, template, or callback subscription becomes unhealthy, turn
`AUTH_WHATSAPP_ENABLED` off and restart API/worker processes. Email remains independently
available. Turning the flag off also pauses processing of auth-specific status receipts until the
path is safely re-enabled. Do not manually replay `unknown` rows; the person can request a new
challenge after the cooldown.
