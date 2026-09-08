# Notification Phase A — local live acceptance, 2026-09-06

Result: real SMTP acceptance and the two provider WhatsApp send/callback gates passed.
This is local functional evidence, **not** a staging capacity or production-readiness certificate.

## Environment and scope

- Server base commit: `0974234e03749bb1984648c6156b97126ecc46cc`, plus the current uncommitted working tree; this is not an immutable release build.
- Dedicated migrated local database: `tellbook_notification_live_20260906`. Evidence is retained there, separate from ordinary development bookings.
- Only the explicitly authorized Gmail inbox and WhatsApp number ending `1683` received test messages.
- Fixture bookings are labeled `TEST ONLY — notification certification`. Contact verification/consent in this isolated fixture is synthetic; the test does not certify real enrollment.
- The test inserts delivery fixtures, then exercises production claims, dispatch fences, rendering, SMTP/Meta sends and durable finalization. Planner/load certification remains a separate test.
- Opt-in runner: `internal/notifications/live_conformance_test.go`. It refuses an unrelated database, restricts actual sends to the supplied recipients, retains terminal rows, and refuses automatic retries of existing failed/ambiguous rows.

## Live evidence (Africa/Lagos, UTC+1)

### Email

All **19** registered audience/type combinations were accepted by the configured Zoho SMTP server between **01:27:59 and 01:28:27**. Every row has `status=accepted`, `attempt_count=1`, and an acceptance timestamp. No row claims email delivery or read confirmation.

- Customer-only: booking received, booking secured.
- Provider-only: new booking.
- Both audiences: appointment reminder, booking rescheduled/cancelled/expired, payment satisfied/failed/refunded/action required.

SMTP acceptance does not prove Gmail inbox placement. Real rejection/failure injection was not performed against this mailbox; transport rejection and ambiguous-outcome coverage remains in the deterministic SMTP tests.

### WhatsApp

| Registry key | Delivery ID | Sent | Delivered |
| --- | --- | --- | --- |
| `provider_new_booking` | `6d0ac62e-9724-5d59-b1ae-930c826ce8f1` | 01:31:41 | 01:31:42 |
| `provider_booking_reminder` | `d230bcc2-b67c-5ad3-9b5e-68d926f801d7` | 01:31:39 | 01:31:42 |

Both sends were accepted at approximately 01:31:31, each with one attempt. Four genuine Meta receipts (two `sent`, two `delivered`) passed the signed webhook ingress and completed asynchronous status processing, matching the persisted WAMIDs. Both deliveries reached `delivered`; callbacks were not simulated.

Read-only Meta inventory confirmed:

- Tellbookapp (`1392371812286789`) is subscribed to WABA `3042371725948779`.
- Configured phone `1255635344308720` is **Tessa by TellBook**, with verified registration and GREEN quality.
- The two provider templates are approved and match their registry contracts.
- `booking_status_update`, `provider_account_created`, and `user_account_created` remain pending and were not enabled or sent.
- Other existing app subscriptions were not changed.

### Restart-safe deduplication

The `verify` phase ran in a separate Go process and reopened the same database. It retained **19 email + 2 WhatsApp** rows, all with one attempt, and found **zero reclaimable deliveries** in either channel. It also required both real sent/delivered receipts for each WhatsApp template. No resend was issued.

This proves persisted accepted/delivered work is not automatically resent after a normal process restart. It does not claim exactly-once SMTP delivery through an arbitrary crash between external acceptance and local commit; ambiguous results still require manual review.

## Gradual local rollout

The main API was restarted on port **8200** with:

```dotenv
NOTIFICATION_EMAIL_ENABLED=false
NOTIFICATION_WHATSAPP_ENABLED=true
WHATSAPP_ENABLED_TEMPLATE_KEYS=provider_new_booking,provider_booking_reminder
```

No provider currently has booking WhatsApp opted in with a verified destination. The main delivery queue remained empty after activation. Provider verification and opt-in remain required; the isolated test did not enroll a real account.

**Email rollout is held for development-data safety.** The read-only audit found 2,512 future eligible-status bookings belonging to other verified provider email addresses, three future customer reminder consents for other email addresses, and 27 booking-level customer email snapshots for other addresses. Existing event jobs were completed, so these counts are exposure on future planning/change, not a claim that all would send immediately. Do not enable email globally or rewrite/delete these bookings without an explicit cleanup decision.

`user_reminder` is not in the allowlist: this run certified provider WhatsApp only. The three pending templates remain excluded.

The temporary certificate API on 8201 was stopped. The live tunnel was restored to the main API at 8200, exposing only `/v1/webhooks/meta/whatsapp`. Public verification returned 200 with the exact challenge; non-webhook routes return 404. The running tunnel uses `/tmp/tellbook-notification-webhooks.yml`; it is not installed as a persistent service, and the existing home-directory tunnel config was not overwritten.

## Webapp/state checks

- API 8200 and both webapp proxies (5275 and 5375) return HTTP 200 and identical public reminder capabilities: email false, customer WhatsApp false. Provider WhatsApp enablement must not advertise customer WhatsApp availability.
- Provider notification settings/booking-detail/checkout component tests passed (9 tests), including unavailable-channel gating and no fabricated delivery results.
- Focused marketplace checkout/booking tests passed (6 tests). Source inspection confirms booking status labels retain `accepted` rather than relabeling it as delivered, and checkout uses the server capability response.
- Focused Go packages passed: notifications, WhatsApp, config, appdata and marketplace auth. No authenticated browser walkthrough was performed for this certificate.

## Repeating the gate safely

Use a fully migrated, dedicated **local** `tellbook_notification_live_*` database, not the main development database. Configure SMTP/Meta through the normal server environment and supply authorized destinations explicitly. While checking real callbacks, route the webhook-only tunnel to an API using this same isolated database with `PROCESS_ROLE=api`; restore it afterward. Do not start planner/sending workers against these fixtures.

```bash
# TEST_DATABASE_URL and both NOTIFICATION_LIVE_* recipients must already be set.
# Each send phase uses real external services. Obtain recipient authorization first.
RUN_NOTIFICATION_LIVE_CONFORMANCE=email go test ./internal/notifications -run '^TestNotificationLiveConformance$' -count=1 -timeout=5m -v
RUN_NOTIFICATION_LIVE_CONFORMANCE=whatsapp go test ./internal/notifications -run '^TestNotificationLiveConformance$' -count=1 -timeout=5m -v
# Safe separate-process receipt/deduplication check; does not send messages:
RUN_NOTIFICATION_LIVE_CONFORMANCE=verify go test ./internal/notifications -run '^TestNotificationLiveConformance$' -count=1 -timeout=5m -v
```

Keep this database for receipt/restart evidence; never reset accepted/unknown/manual-review deliveries to pending to obtain a green test. SMTP inbox placement, real provider enrollment, customer-template certification, development-data cleanup, and target-topology API/database load measurements are distinct remaining checks.
