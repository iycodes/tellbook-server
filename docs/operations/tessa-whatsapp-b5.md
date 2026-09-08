# Tessa WhatsApp B5 — lifecycle and retention

Lifecycle/retention, provider reload/slide fixes and bounded multi-provider workload checks are
implemented and locally verified on 2026-09-06. **Authorized live end-to-end acceptance remains
open.** The original local verification did not activate runtime delivery or migrate the main database.
The temporary fixture API/client were stopped after testing. The subsequent authorized, linking-only
activation on 2026-09-07 is recorded below; it does not establish live conversation acceptance.

## Implemented

- Existing maintenance performs bounded, child-first Tessa transport cleanup. Each task uses ordered
  `FOR UPDATE SKIP LOCKED`, at most 1,000 rows per batch and four batches every 15 minutes. No new
  worker, timer, request-path cleanup or retention framework was added.
- Terminal delivery diagnostics remain for 90 days after the last status change. Terminal ingress
  remains for 30 days, and expired challenge/onboarding metadata for at least 24 hours. Security
  events remain for 90 days. Outstanding dependencies defer deletion, rather than cascading through
  runnable work. See [the retention policy](data-retention.md) for exact eligibility.
- Shared webhook cleanup now protects receipts referenced by Tessa outbox, ingress or email-link
  challenges. Its previous 30-day cascade could erase a retained answer's delivery evidence early.
- Migration `20260906018000_tessa_whatsapp_retention.sql` supplies ordered retention and parent-reference
  indexes; existing unique receipt indexes support the webhook guards. Applied only to the isolated
  `tellbook_tessa_b2_certification` database.
- Conversation reset now immediately cancels provably unsent answer deliveries in its existing
  transaction. It takes the provider lock before thread/outbox locks and emits the existing durable
  delivery event. Accepted/in-flight/ambiguous attempts keep their reconciliation state.
- Account deletion cascades through grants, pending private ingress, transcripts, runs and delivery
  intents. This verifies the existing database deletion boundary; it does not introduce a new account
  deletion API or claim that messages already on WhatsApp can be erased.
- Real dashboard-link replacement is exercised locally through receipt processing: it advances the
  grant revision, cancels the previous number's queued answer and never redirects that private answer
  to the replacement number. Expired grants are also checked at answer dispatch.
- Tessa bootstrap refresh now updates its existing WhatsApp modal. A superseded in-flight modal
  request is aborted/ignored; it cannot overwrite the newer bootstrap. Known connection expiry is
  reflected while the modal is visible using a bounded deadline timer, not network polling. Long
  grants cap the browser timer delay so a 30-day grant does not expire through integer overflow.
- ResponsiveModal preserves the browser's restored SvelteKit state before copying `page.state`.
  This fixes opening connection controls after reload dropping the parent Tessa slide's persistence.
  Tokens, consent input and phone input are still not persisted. No new route or SSE connection.

## Local evidence

Race-enabled PostgreSQL tests cover retention age/dependency ordering, preserving queued/admitted
work and canonical transcripts, bounded lock-skipping cleanup, account deletion, replacement,
grant expiry and reset's unsent-versus-ambiguous boundary. All maintenance queries are validated
against the migrated isolated database. Models and senders are fakes; these are not live Meta or
throughput certificates. Suites sharing fixture phone numbers must run sequentially.

### Bounded workload — 2026-09-06

`TestTessaWhatsAppMultiProviderWorkloadIntegration` uses the isolated PostgreSQL database, fake
inference (2 ms delay per model call) and fake transport. It queues two turns per provider in each
of two waves; two reconstructed AI worker instances share four concurrent calls and a four-slot
limiter. It verifies provider-scoped bounded context, follow-up history across reconstruction,
ordered messages and exactly one committed answer/intent per turn. It then drains the real core
sender with fake transport and reconstructs it to prove accepted/ambiguous work is not resent.

Observed race-enabled local run (Go 1.26.1 darwin/arm64, pool maximum 16 connections):

| Measure | Result |
| --- | ---: |
| Providers / committed turns | 24 / 96 |
| Peak simultaneous model calls | 4 |
| Total generation/drain time across both waves | 386 ms |
| Ingress-to-completion p95 | 193 ms |
| Separate answer-dispatch drain | 502 ms |
| Accepted / injected ambiguous outcomes | 88 / 8 |
| Duplicate external attempts after sender reconstruction | 0 |

The test driver supplies wake hints every 50 ms; that is not an application poller or a measurement
of deployed LISTEN/NOTIFY latency. Generation and dispatch were measured separately. Reconstruction
means new worker instances over committed database state, not an OS/process crash. These figures
exclude real model, Meta, network and staging pool-wait latency and must not be extrapolated to a
user-count capacity promise. Earlier focused suites separately cover safe retry and early callbacks.

### Browser/UI evidence — 2026-09-06

Playwright CLI drove the actual SvelteKit client on isolated port 5277 with an explicit fixture API
on port 5488 (no production data, credentials or Meta transport). Desktop and 390×844 mobile checks
verified restored transcript/delivery labels, a restored parent slide surviving modal-open/reload,
pending replacement-token removal on reload, explicit refresh to expired state, disconnect and
disconnected bootstrap after reload. The final page had no browser console errors or warnings.
The complete fixture log recorded seven bootstraps and seven SSE opens across opening/restoration,
one explicit status GET, one fixture link POST and one fixture disconnect DELETE: no status polling.
The connection modal itself is transient; reloading restores Tessa and fetches current state, not
the modal's old token/form input. Mobile screenshot: client `output/playwright/tessa-b5-mobile-connection.png`.

Unit/component regressions cover the same history boundary, one fresh bootstrap on provider-shell
restoration, failed bootstrap without stale content, stale modal requests, bootstrap propagation,
grant deadlines and the long-timer limit. They retain the existing slide and bootstrap/SSE architecture.

Final verification: all 252 frontend tests across 68 files passed; `pnpm check` reported zero errors
and warnings; `pnpm contracts:check:tessa` and the production build passed. The race-enabled isolated
database workload, Tessa/maintenance and WhatsApp/notification/auth regressions passed, as did
`go vet ./internal/appdata ./internal/whatsapp` and both repositories' whitespace checks.
Temporary browser/API test servers were stopped. The main database, server environment and live
delivery channels were not changed for this verification.

## Remaining live gates

### Requested booking presentation update — 2026-09-07

Implemented one native WhatsApp `interactive/cta_url` message per booking answer: one distinct
booking action produces **View booking** and its validated detail URL; multiple booking actions
produce **View bookings** and the bookings screen. No individual booking URLs are appended to
the body. Targets still come from committed, server-generated presentation metadata and the
configured HTTPS origin, never a model-generated URL. Other non-booking navigation is unchanged.

The new transport uses the existing correlation ID, one-attempt Graph client, outbox authority
checks and status/retry handling. It does not resend as text if Meta rejects a button. WhatsApp
synthesis is constrained to 1,024 characters before commit (including the existing bounded repair);
web synthesis retains 4,000. Oversized button bodies are rejected, not truncated. No new queue,
migration, URL shortener, extra inference-on-delivery or frontend contract was introduced.

Booking queries already sort by start time then ID. Updated shared answer instructions include
explicit calendar dates, start times and timezone in compact chronological lists. First/earliest
booking requests use a bounded one-result search instead of carrying all list actions forward.
Prompt/config revision is now `tessa-booking-presentation-v2`.

Verified: focused transport/renderer tests; race-enabled isolated WhatsApp/auth and appdata/Tessa
regressions; Go vet and API/sender builds; synthetic local-Gemma conformance for one-result follow-up
planning and a three-booking dated/timed ordered list. The first model check omitted the explicit
date; the tightened prompt passed the rerun. The broader WhatsApp unit package still has the
unrelated existing `TestClassifyInboundControlIsExactAndBounded` assertion for `STOP now` versus
onboarding; no control-classification production logic was changed in this update.

The temporary runtime helper now uses `/tmp/tessa-booking-button-api` and
`/tmp/tessa-booking-button-sender`; API, AI role and restricted sender were restarted only after
checking their queues were empty. Prior accepted/delivered messages were not reset or replayed.
Native-button acceptance/click-through on the configured Meta sender still needs a fresh real
question; plain-text delivery evidence above is not claimed as button-delivery evidence.

### First genuine AI answer and observed typing — 2026-09-07

The user reported seeing WhatsApp's typing indicator and receiving “You have 3 bookings today,
September 7, 2026” with three booking-detail links on the HTTPS preview origin.

Verified against committed evidence for run `2a6e9225-5302-42fe-819c-b22e73cef478`:

- Planning and synthesis both succeeded with the configured local Gemma model; `fallback_used=false`.
  The run completed from 00:30:09 to 00:30:22 Africa/Lagos (about 13.5 seconds).
- `search_bookings` returned three items for 2026-09-07 through 2026-09-07 in `Africa/Lagos`,
  with `has_more=false`. All three returned booking IDs match the user's received links.
- Answer outbox `8b48bfec-0ed8-45e2-87fc-50f96e97a29e` has one attempt, accepted at 00:30:24,
  genuine Meta `sent` at 00:30:25 and `delivered` at 00:30:26. Both callback receipts completed
  under `processing_owner=tessa`. No read callback was recorded or claimed.
- Typing visibility is direct user observation, not inferred from a queued presence request.
  Authenticated opening of the three links has not yet been verified in this live check.

This closes the first local-model question/answer, observed typing and real answer-delivery gates.
Multi-turn context, answer restart deduplication, WhatsApp-first enrollment and disconnect/replacement
remain open; it is not a claim of complete Phase B or staging load certification.

### Authorized HTTPS preview and chat activation — 2026-09-07

After explicit user approval to expose the local provider webapp, created the new DNS route
`tellbook-dev.iycodes.com` on the existing tunnel (without overwriting an existing record).
The local temporary tunnel configuration serves the built adapter-node app on loopback 5278 and
same-origin `/v1` API calls on 8200. Webhooks on the preview hostname are blocked; the separate
`tellbook-webhooks.iycodes.com` host still exposes only the Meta webhook path. No Vite development
server, model endpoint, database, worker health endpoint or metrics endpoint was added to the tunnel.

The existing SvelteKit production build passed. `/tmp/tessa-client-preview.mjs` serves its handler
with `ORIGIN=https://tellbook-dev.iycodes.com`, the loopback private API origin, and an
`X-Robots-Tag: noindex, nofollow, noarchive` header. No frontend source/config changes were needed.
The runtime helper now supplies the matching HTTPS provider origin, secure host-only auth cookies,
the exact additional CORS origin and loopback-only trusted proxies.

At approximately 00:20–00:21 Africa/Lagos, restarted the API and recipient-restricted sender with
`TESSA_WHATSAPP_CONVERSATIONS_ENABLED=true`, retaining only the selected provider's allowlist.
The temporary sender now wires the existing assistant-answer authorization/renderer and shared
ingress adapter; transport restrictions remain unchanged. Its new 45-minute deadline is about
01:05 Africa/Lagos. Built it as `/tmp/tessa-live-sender-next`; build and Go vet passed.
The existing `PROCESS_ROLE=ai-worker` is running with inbox AI disabled. It uses loopback 8202
for its health listener (not the API's 8200); `/v1/readyz` reports ready and workers initialized.
The local model's health check passes. Existing configured local-first/hosted-fallback selection
is preserved. Core/payment/booking-notification/maintenance roles remain stopped.

Verified publicly: HTTPS root 200, anonymous `/clients` redirects to the existing responsive
auth modal, anonymous Tessa API 401, exact Meta verification challenge 200, and unrelated paths
on the webhook-only hostname 404. Playwright loaded the real email/password-fallback modal;
no sign-in or OTP was submitted by the agent. This does not certify a new authenticated HTTPS
session. The previously linked WhatsApp grant remains active, revision 1, with no queued AI runs
at activation. Genuine AI question/follow-up, answer delivery and typing observation are next.

### Authorized linking-only activation — 2026-09-07

The user selected the verified provider `fanoroiyanu@gmail.com` (ID
`5b273294-6ee1-404b-af67-d6d79c3c3e5c`) and the previously authorized WhatsApp ending 1683.
This provider already acknowledged the current `2026-08-30` core notice; no acknowledgement,
phone ownership or WhatsApp consent was fabricated.

- Backed up local `tellbook_db` to `/tmp/tellbook-tessa-live-20260906-before.dump` (custom archive,
  mode 0600, 34 MB; archive inventory readable). This is not a full restore rehearsal.
- Applied all nine reviewed Phase B migrations: 82 applied through `20260906018000`.
- Started the updated API on `127.0.0.1:8200`, `PROCESS_ROLE=api`, linking enabled for only the
  selected provider. Chat remains disabled. Existing `.env` and `server.env` were not edited;
  all activation/isolation settings are temporary process overrides.
- Booking email/WhatsApp, welcome delivery and inbox AI are disabled in these processes.
  The ordinary core/maintenance roles were **not** started: the audit found 41 pending payment
  webhook events and one eligible allocation. Those are outside this test.
- Temporary ignored launcher `bin/tessa-live-check/main.go` runs only the existing Tessa control
  and authentication-email workers, for at most 45 minutes from 00:02 Africa/Lagos. Transport
  wrappers refuse every email/WhatsApp destination except the authorized pair. It starts no
  payment, booking-notification, AI or maintenance workers. Build and Go vet passed.
- Runtime helper `/tmp/tessa-live-dev.mjs` supplies the shared scoped configuration. API binary
  `/tmp/tessa-live-api`; sender binary `/tmp/tessa-live-sender`. These are local test helpers,
  not new supported production process roles. Recheck state before restarting them.
- Restarted the existing webhook-only tunnel using `/tmp/tellbook-notification-webhooks.yml`.
  Public verification returned 200 with the exact challenge; unrelated paths return 404.
  Provider webapp on 5275 returned 200. Read-only Graph checks confirmed **Tessa by TellBook**,
  GREEN quality and the configured app's WABA subscription.

Initial post-start check: zero connections, zero Tessa outbox rows and zero booking deliveries.
The dashboard linking/delivery gate subsequently passed, as recorded below. Chat activation
additionally needs a truthful, reachable HTTPS provider origin;
the current local HTTP origin was not relabelled as HTTPS or replaced with an unrelated site.

### Genuine dashboard linking and delivery — 2026-09-07

The provider sent the prepared message from the authorized number and reported receiving the
linking-only reply. The database records the active grant at **00:06:56 Africa/Lagos**, with the
expected destination and `tessa-whatsapp-v1` notice, expiring 2026-10-07. No synthetic receipt or
direct grant insertion was used.

- Outbox `53e37373-4201-4613-ae15-b42827f026ab`: `kind=linked`, one send attempt, accepted
  at 00:07:01, and genuine Meta **sent and delivered** timestamps at 00:07:02. Both signed-ingress
  status receipts completed under exclusive `processing_owner=tessa`; no read callback claimed.
- The connection security email reached SMTP `accepted` at 00:07:04, with one attempt.
  This does not prove inbox placement.
- Gracefully stopped and restarted only the recipient-restricted sender at 00:10. A subsequent
  database check retained one attempt for both deliveries and found zero reclaimable Tessa
  deliveries. This demonstrates normal process-restart deduplication, not arbitrary crash safety.
  The restarted sender's 45-minute deadline is approximately 00:55 Africa/Lagos.
- Booking deliveries remain zero. Payment/core/maintenance workers were not started.

Dashboard connection + real sent/delivered + normal restart deduplication are now evidenced.
WhatsApp-first email-code linking, disconnect/replacement, AI follow-up context and read/typing
acceptance remain open. The reply correctly states that WhatsApp conversations are unavailable
because `TESSA_WHATSAPP_CONVERSATIONS_ENABLED` remains false.

### Local activation preflight — 2026-09-06

A subsequent read-only check found the local model healthy on 7080, no API listener on 8200, and
the existing webhook URL returning HTTP 530. Required Meta/WABA credential fields are populated;
this presence check does not establish current token validity or app subscription health.
The local `tellbook_db` has 73 applied migrations through `20260905030000`, with all nine Phase B
migrations still pending. Both WhatsApp linking/chat flags are unset (default-off), and the current
HTTP client public origin does not meet chat's HTTPS origin requirement.

The current `.env` enables booking email and WhatsApp notifications, unlike the earlier Phase A
email hold. Delivery and welcome queues were empty, and all 4,594 event jobs were completed; this
does not remove the future-planning exposure documented in Phase A. Do not restart unrestricted
core workers merely to test Tessa. No environment values or database rows were changed.

The previously authorized test email is not a provider account in this development database.
Confirm the provider account/test database before changing the allowlist, applying migrations or
enabling live conversation processing. Do not fabricate account consent or verification to close
the enrollment gate. An actual provider must accept the notices and send the prepared linking
message from the chosen WhatsApp number.

Remaining authorized B1/B2/B4/B5 acceptance requires WhatsApp-first enrollment, follow-up conversation,
assistant-answer sent/delivered callbacks, typing/read behavior, answer restart deduplication and disconnect. Keep chat
default-off until the target's migrations, role configuration and controlled rollout are explicitly
authorized. Local fixture consent and fake deliveries are not substitutes for those live proofs.
