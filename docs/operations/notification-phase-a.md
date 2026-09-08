# Notification Phase A operations

Phase A sends transactional booking email and approved WhatsApp utility templates from durable
queues. API requests only commit booking/domain state and queue work. The planner, email sender,
WhatsApp sender, and WhatsApp status processor run asynchronously and have independent bounded
concurrency.

## Certification gate

Use a disposable local or staging database with the current schema. The certification creates its
own booking fixture and refuses to run when any notification queue already contains rows, so never
point it at production or a shared environment. The gate also runs with Go's race detector.

```bash
export TEST_DATABASE_URL='postgres://.../tellbook_notification_certification'
export NOTIFICATION_CERTIFICATION_ALLOW_WRITES=true
make notification-phase-a-certify
```

The defaults certify 250 event jobs, two delivery waves of 250 messages per channel, and 50 signed
webhook requests containing 500 configured-phone statuses plus 500 other-phone statuses. Increase
the workload without changing code when sizing a staging topology:

```bash
export NOTIFICATION_CERTIFICATION_VOLUME=5000
export NOTIFICATION_CERTIFICATION_WEBHOOK_REQUESTS=500
export NOTIFICATION_CERTIFICATION_WEBHOOK_P95=750ms
make notification-phase-a-certify
```

The gate fails unless concurrent planning completes, replayed backfill remains idempotent, backfill
creates no customer external deliveries, each simulated external side effect occurs once, an SMTP
failure leaves WhatsApp healthy, a Meta rate limit leaves email healthy, no tested runnable queue
age remains, expired leases are reclaimed, signed webhook ingress meets the configured persistence
p95, other WABA phone-number events are ignored, and persisted statuses drain asynchronously.

This deterministic test does not certify a deployment topology or a third party. Before enabling a
channel, retain the test output with the build SHA and separately capture:

- booking API p95 while the planner and both delivery queues are under the intended peak load;
- PostgreSQL acquire-wait p95 and peak pool utilization;
- queue depth, oldest runnable age, drain rate, retry count, and dead letters from `/metrics`;
- SMTP sandbox acceptance/rejection evidence for every email type being enabled;
- a real-recipient Meta send and matching callback for every enabled template key;
- the WABA app-subscription inventory, phone quality state, and template status.

Enable one channel/template at a time after its recipient checks pass. `user_reminder` requires a
verified business contact shared with booked customers; see [contact settings](business-customer-contact.md).
Status callbacks may remain enabled while outbound WhatsApp is disabled.

### Local live gate, 2026-09-06

The authorized-recipient run passed SMTP acceptance for all 19 email types and genuine Meta
sent/delivered callbacks for both provider templates, with separate-process no-resend verification.
See [the retained evidence and safe rerun procedure](notification-live-acceptance-2026-09-06.md).
Only provider WhatsApp is now enabled in local development. Email remains held because existing
seeded bookings contain other recipients; this local result does not close staging load gates.

## Health snapshot

Run the read-only queue snapshot with the same database environment used by the process:

```bash
make notification-phase-a-health
```

The Prometheus equivalents are `tellbook_queue_depth`, `tellbook_queue_oldest_age_seconds`,
`tellbook_queue_retry_attempts`, `tellbook_queue_completed_last_5m`, and
`tellbook_queue_dead_letter_depth`, labeled by notification queue. Email/WhatsApp result counters
and WhatsApp callback lag are exposed separately. Page on sustained growth, not a momentary burst.

## Incident actions

### Expired or revoked WABA token

1. Set `NOTIFICATION_WHATSAPP_ENABLED=false` on worker replicas and restart them. Callback
   processing stays active when WABA and phone IDs remain configured.
2. Rotate `WABA_TOKEN` using the intended system user; never print or paste it into logs or an
   artifact.
3. Confirm the token has access to the configured WABA and phone number, run
   `make whatsapp-template-conformance`, then perform one authorized recipient/callback test.
4. Re-enable WhatsApp. Retryable pre-acceptance rows will resume from their durable schedule;
   never manually resubmit `unknown` rows.

### Missing or unintended app subscription

1. Keep outbound WhatsApp disabled until the configured `META_APP_ID` appears in the WABA's
   subscribed-app inventory and the callback verification succeeds.
2. Remove any unintended app subscription in Meta Business Manager/API. A token grants the system
   user's asset permissions; it does not select which app receives webhooks.
3. Send a controlled callback. Confirm configured-phone receipts appear and another phone number on
   the WABA cannot mutate a TellBook delivery.

### Paused, disabled, or changed template

1. Remove the key from `WHATSAPP_ENABLED_TEMPLATE_KEYS` and restart workers. The dispatch fence
   cancels queued rows whose template is no longer enabled.
2. Compare the approved Meta template structure with the local registry using
   `make whatsapp-template-conformance`.
3. Re-enable only after approval and a real-recipient conformance test. Do not rename a key or map
   it to a different template to bypass the registry.

### Degraded phone quality or Meta rate limiting

1. Disable outbound WhatsApp if retry depth or oldest runnable age grows continuously. Email remains
   independently available.
2. Check Meta phone quality, messaging limits, error class, and `Retry-After`. Do not add an
   in-memory retry loop or increase concurrency against an active rate limit.
3. Restore at low worker concurrency, observe drain rate, then raise concurrency gradually within
   the configured maximum.

### SMTP rejection or outage

1. Disable `NOTIFICATION_EMAIL_ENABLED` if retry depth grows continuously. WhatsApp remains
   independent.
2. Separate permanent recipient rejection from provider/auth/network failure. Permanent mailbox
   rejection suppresses that destination; transient pre-commit failures follow the durable retry
   schedule.
3. A post-`DATA` ambiguous result is `manual_review`. Verify it with the SMTP provider before any
   human-approved resend; do not move it back to pending automatically.

### Queue backlog

1. Capture `make notification-phase-a-health`, queue metrics, database acquire wait, pool
   utilization, worker logs, and the active deployment configuration.
2. Identify whether the planner, one delivery channel, or callback processing is growing. Do not
   scale every worker indiscriminately.
3. Correct the failing dependency first. If PostgreSQL has headroom, scale only the responsible
   worker replicas or bounded concurrency and verify oldest runnable age decreases on consecutive
   observations.
4. If leases remain in `processing` after a terminated worker, wait for expiry; replacement workers
   reclaim them. Do not clear leases by hand during normal recovery.

### `unknown`, `manual_review`, and dead letters

- `unknown` means the WhatsApp request may have crossed the external side-effect boundary. Wait for
  its reconciliation deadline and callback; maintenance moves unresolved rows to `manual_review`.
- `manual_review` must not be automatically resent. Compare the delivery UUID/correlation, WAMID,
  provider console, recipient evidence, and booking state before recording an operator decision.
- A dead-letter planner/status row indicates repeated internal processing failure. Preserve the row
  and its bounded error code, fix the cause, and replay only through a reviewed administrative
  procedure. Never edit a terminal delivery into `pending` as a backlog shortcut.
