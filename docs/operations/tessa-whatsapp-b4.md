# Tessa WhatsApp B4 — answer delivery and typing

Server, delivery-state API/SSE/UI and runtime wiring implemented and locally verified on
2026-09-06. **B4 authorized live acceptance remains pending.**
Ordinary WhatsApp chat is still disabled in the running application. No environment settings
were changed, no API restart was performed and no real message was sent during this portion.

## Implemented

- `20260906016000_tessa_whatsapp_answers.sql` extends the existing B1 text outbox with an
  assistant-message reference, owning thread and core notice revision. A composite foreign key
  enforces provider/thread ownership; each assistant answer has at most one delivery intent.
- The existing Tessa completion transaction inserts the assistant answer and its delivery intent
  together. An outbox failure rolls back the answer and terminal run update. Web answers do not
  create WhatsApp deliveries. Only references are queued, not copied private answer bodies.
- The existing control sender now handles `answer` entries. Dispatch reloads the committed answer
  and checks the active grant, account/connection revision, phone/sender namespace, allowlist,
  current core notice, source ingress, completed run, active thread and original reply window.
  Account/thread locks fence revocation and reset before the dispatch intent is committed.
- Text remains within one 4,096-character message. At most three stored presentation actions
  become server-generated links on a configured HTTPS provider origin. Only known routes are
  mapped; detail IDs must be UUIDs. Screens whose inner panels use browser history state link to
  their actual parent route rather than inventing unsupported deep links. Links that exceed the
  message budget are omitted without truncating the answer or splitting it into multiple sends.
- Delivery uses the existing single-attempt Graph transport, bounded safe retries, ambiguous-send
  reconciliation, exclusive callback routing and monotonic status handling. Retrying delivery does
  not rerun inference/tools. Accepted/ambiguous/expired/cancelled answers do not enter the send
  queue again. A closed original reply window cannot be reopened by changing `available_at`.
- `SendTyping` uses the configured Graph version/receiving phone and verified inbound WAMID,
  sends `status=read` plus `typing_indicator.type=text`, and expects `{ "success": true }`.
  This intentionally marks the incoming message read; it is not an answer-delivery claim.
- Typing starts only after admission, capacity acquisition and run start. A bounded asynchronous
  task checks window/grant/lease authority immediately before dispatch. Its deadline is two
  seconds, and it is cancelled/joined when the turn exits. Failures never fail generation or
  delay it behind the presence request. Only the first execution attempts presence; retries and
  recovered executions do not replay it. No extra worker, polling timer or cosmetic retry queue.
- `20260906017000_tessa_whatsapp_delivery_state.sql` adds acceptance and callback timestamps.
  Public status changes publish durable `message.delivery_changed` events in the same transaction;
  internal claim/retry transitions do not publish noise. Delivery writers lock the provider before
  the outbox to serialize event publication and avoid revocation lock inversions.
- Bootstrap, pagination and batched SSE hydration include optional answer delivery metadata through
  an indexed message/outbox join. Internal IDs, destinations and transport errors are not exposed.
  Missing earlier callback timestamps are not fabricated; acceptance is not labelled delivery.
- The provider Tessa slide renders truthful delivery labels through its existing SSE connection.
  Events update loaded messages only, and older page snapshots cannot regress a newer delivery
  update. There is no delivery polling or extra conversation request.
- `TESSA_WHATSAPP_CONVERSATIONS_ENABLED=false` is the independent default-off chat rollout switch.
  Enabling it requires linking to be enabled and a valid HTTPS `CLIENT_PUBLIC_BASE_URL` origin.
  It wires ingress, answer rendering, AI namespace/allowlist and typing together. Connection capability
  and onboarding copy reflect this wiring; linking alone does not claim chat is available.

Meta's [typing documentation](https://developers.facebook.com/documentation/business-messaging/whatsapp/typing-indicators)
was rechecked with Firecrawl on 2026-09-06. It confirms the request/response above and that the
indicator clears on reply or after 25 seconds. The transport contract is tested on the existing
v24.0-shaped endpoint; actual support on the configured live sender remains a live acceptance gate.

## Verification

Migrations applied only to `tellbook_tessa_b2_certification`. The schema snapshot includes only
the relevant new migration changes, preserving unrelated existing schema differences.

Race-enabled PostgreSQL tests cover atomic answer/intent commit and rollback, source binding,
committed-answer reuse, dispatch expiry/revocation/security/notice/thread/namespace/disable fences,
safe retries after worker reconstruction, ambiguous non-retry and callbacks before send completion.
Typing tests cover request shape, configured version, one attempt, correct inbound ID, nonblocking
failure/cancellation, queued/web/retry/revoked/expired skips. Existing B1–B3 and auth/notification
regressions, Go vet and API compile checks pass with fake models and senders only. Further tests
cover delivery bootstrap/pagination/SSE, cancellation publication, duplicate/out-of-order callbacks,
timestamp preservation and complete capability wiring. Provider tests cover every public status,
stale snapshots and no-refetch updates; Svelte checking and generated-contract checks pass.

## Activation and remaining gates

1. Before an authorized rollout, apply all pending migrations to the target database and use matching
   chat/linking flags, provider allowlist, phone namespace and notice revisions across API/core/AI
   roles. The actual environment and running processes were not changed during this implementation.
2. Certify both linking journeys, a real follow-up conversation, genuine sent/delivered callbacks
   and read/typing behavior with an authorized recipient. Local tests do not close these live gates.
3. B5 lifecycle, retention and target-load certification are the next implementation portion and
   remain separate. No staging or production capacity claim is made here.
