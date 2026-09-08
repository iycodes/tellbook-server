# Tessa WhatsApp B1 — dashboard linking

Implemented and locally verified: 2026-09-06. This is the first Phase B slice, not the complete
WhatsApp assistant. Ordinary inbound questions still do not create Tessa runs or receive AI replies.

Cross-check fixes (2026-09-06): STOP/disconnect now cancel pending links and queued replies for the
actual sender even before the first grant exists, without revoking an unrelated number's grant.
Callback consumption locks and rechecks all matching deliveries, quarantining conflicting mappings
that appear after initial routing. Recovered leases share the five-attempt dispatch budget. The
provider modal clears replaced/expired tokens with a single visible deadline timer and no polling.
These changes have regression coverage; live activation remains separate.

## Included

- Authenticated connection status, dashboard link creation and disconnect APIs at
  `/v1/app/tessa/whatsapp` (GET/DELETE) and `/v1/app/tessa/whatsapp/link` (POST).
- Explicit WhatsApp consent; masked status; a hashed, one-use 10-minute link token bound to the
  provider, account security revision, chosen sender and configured Meta receiving number.
  One request per minute, at most five wrong-number attempts, one active connection per sender.
- Receipt deduplication, link transition and control-response outbox insertion in one transaction.
  Neither the raw token nor control message enters a Tessa transcript. A `wa.me` click alone
  never links the account; Meta's signed inbound message must prove the chosen sender.
- Dashboard disconnect and inbound `TESSA DISCONNECT`/`STOP` revoke the grant and cancel unsent
  control work. GET/DELETE remain authenticated but do not require continued AI availability.
  Login identities, reminder preferences and public business contacts are separate.
- A bounded core Go worker sends deterministic control responses, with a final grant/security/
  request/window check. Safe pre-dispatch or explicit transient rejections have bounded retries;
  ambiguous sends are not resent and become manual review after 15 minutes without a callback.
  Already dispatched network requests cannot be recalled by a later disconnect.
- Shared callback classification gives authentication, notifications and Tessa exclusive ownership.
  Early WAMID-only callbacks wait for mapping; conflicts are quarantined, and unmatched callbacks
  stop reconciliation after 15 minutes. Expired leases from pre-routing workers are recovered.
- Provider Tessa's existing fullscreen slide contains a WhatsApp button using the existing responsive
  modal. Initial status comes from bootstrap; mutations and explicit refresh use real APIs. No polling,
  extra mount-time fetch, optimistic connected state or marketplace UI was added.

## Local verification

The complete migration chain, including `20260906010000_tessa_whatsapp_linking.sql` and
`20260906011000_tessa_whatsapp_sender_cancellation.sql` (two indexed sender lookups), was applied
to the isolated local database `tellbook_tessa_b1_certification_v2`. Its earlier B1 test database
`tellbook_tessa_b1_certification` also remains local. Neither database is a live sending environment.

Passed:

- `TEST_DATABASE_URL=<isolated DB> go test ./internal/whatsapp -run TestTessa -race -count=1`:
  number binding, wrong sender, expiry, security revision, replay, concurrent completion, atomic
  rollback, early callbacks, ownership conflict/unmatched routing, expired leases, window closure,
  superseded requests and ambiguous-send no-resend behavior. Text transport uses a local fake server.
- Focused database regressions across WhatsApp, auth challenge and notification packages:
  `-run 'Test(Tessa|WhatsApp|Auth|CustomerReminder|Email|ProviderWhatsApp|Webhook|Meta|Contact)'`.
- Non-database tests in those packages plus appdata, config and the API command.
- Client Tessa API, assistant-screen and connection-modal suites: 18 tests, no unhandled errors.
- `pnpm check`: zero errors/warnings. `pnpm contracts:check:tessa`: generated types match the API.

This is not a claim that the entire seeded database suite or live Meta conformance has passed.
An attempted broader fixture seed exposed a pre-existing reference to the missing
`marketplace_customer_identities.updated_at` column; the seed transaction rolled back. B1 tests
use self-contained fixtures, and no unrelated seed rewrite was included.

## Meta contract check

Checked against official documentation on 2026-09-06:

- Text uses `/{phone-number-id}/messages`, `type=text`, a body and optional `preview_url`;
  the body is limited to 4,096 characters. B1 disables previews.
  [Meta text messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/text-messages).
- Free-form replies require an open 24-hour customer-service window following the user's inbound
  interaction. B1 conservatively derives each control reply's deadline from its triggering message
  and never revives an expired reply.
  [Meta sending messages](https://developers.facebook.com/documentation/business-messaging/whatsapp/messages/send-messages).
- The v24.0 message specification includes text and read/typing request examples. The configured
  integration version was not upgraded. Typing is planned for B4, not enabled in B1.
  [Meta v24 message contract](https://developers.facebook.com/documentation/business-messaging/whatsapp/reference/whatsapp-business-phone-number/message-api/v24.0.openapi.yaml/).

## Activation is separate

This implementation did **not** migrate the main database, restart the API, change live env files
or send real messages. `TESSA_WHATSAPP_LINKING_ENABLED` defaults to `false`.

Before an authorized live check:

1. Apply both B1 migrations to the intended database before starting the new API/worker binary.
2. Keep unrelated delivery channels isolated. In particular, review current email/notification flags
   and queued recipients before restarting a process that runs core workers; enabling B1 must not
   accidentally send seeded booking emails.
3. Configure the existing Meta webhook/send credentials, business WhatsApp number, Tessa availability
   and provider allowlist. Enable `TESSA_WHATSAPP_LINKING_ENABLED=true` only for the intended test.
4. From an authorized provider's Tessa slide, consent to the chosen number, generate the link and
   send its prepared message from that exact number. Refresh to verify the server's connected state.
5. Verify the generic control reply and real signed sent/delivered callbacks, then test disconnect.
   Opening WhatsApp is not evidence of connection, and HTTP send acceptance is not delivery evidence.

Next: B2's WhatsApp-first onboarding, dedicated-purpose email linking OTP and durable security notices.
Shared Tessa conversation admission follows in B3; assistant replies and typing follow in B4;
full lifecycle/retention and live end-to-end certification follow in B5.
