# Tessa WhatsApp B3 — durable admission and shared context

Implemented locally on 2026-09-06. **Runtime chat is not activated.** B4 now wires conversation
ingress, the AI namespace, durable replies and typing behind the default-off
`TESSA_WHATSAPP_CONVERSATIONS_ENABLED` switch, with delivery state in the existing provider UI.
When the switch is off, the connection API reports `conversation_available: false`.
No main-database migration was applied and the API was not restarted during B3/B4 implementation.

## Implementation

- Signed, configured-phone ingress resolves the actual sender's active grant and provider allowlist.
  A separate adapter persists eligible text in the receipt transaction. Bare codes, bare emails,
  linking commands and unsupported media do not enter conversation history. Text is bounded to
  4,000 characters; onboarding retains its smaller control-input bound. A linked conversation does
  not depend on SMTP being enabled once conversation ingress is configured.
- Queue rows bind receipt/source ID, provider, existing active thread, receiving phone, sender,
  connection revision, account security revision and Meta timestamp. Source `wamid` is opaque text;
  a deterministic namespace-derived UUID supplies the Tessa client-message idempotency key.
- The core Tessa notice must already be acknowledged. Linking does not silently acknowledge a
  separate AI notice: accounts needing that notice receive the bounded instruction to open Tessa
  in Tellbook. The WhatsApp channel notice explicitly discloses shared web/WhatsApp context and
  Meta/configured-AI-provider processing.
- At most five messages wait per provider, with a maximum queue age of five minutes. Only pending
  items retain their text; admission moves it into the normal Tessa message and clears the queue
  copy. Rejection, expiry and cancellation clear queue text and persist a terminal reason.
- The worker admits only the oldest pending item for an eligible provider and only when the
  preceding run is terminal. Per-provider account/advisory/thread locks preserve receipt commit
  order across replicas. Admission is one DB-only transaction, so it needs no separate persistent
  lease or admission-retry framework. Existing run leases and retries govern generation.
- Web and WhatsApp use the same turn-insertion function and existing one-active-thread/run
  constraints. Web submissions cannot jump ahead of an unexpired pending WhatsApp turn; an
  idempotent web replay still returns its committed result. Thread reset cancels pending items
  instead of attaching them to the replacement thread.
- Existing context loading supplies the question, up to 11 preceding messages and bounded older
  summary. Queued follow-ups are not materialized prematurely, so they see the preceding answer.
  Existing local-first/fallback and committed planning/tool evidence paths remain shared.
- Active grant, account/connection revision, namespace, allowlist, expiry and run lease are checked
  before WhatsApp inference, before tool execution/evidence reuse and before synthesis/fallback.
  Completion takes the account lock and checks again before committing the answer. Already-started
  external inference cannot be retracted, but revoked authority cannot start subsequent operations
  or commit its answer. Normal web turns do not pay these extra WhatsApp query costs.
- Busy/expired/unavailable control notices reuse the existing WhatsApp outbox. They are limited
  to one per provider per minute and require current connection authority at dispatch. Revocation
  cancels pending ingress and unsent outbox rows. B4 owns assistant-answer delivery and its status.

## Provider client

`source_channel` now permits `web` and `whatsapp` in the database and OpenAPI contract. Generated
provider types match it. Provider questions display “From WhatsApp”; assistant answers from those
turns display “WhatsApp request”, not a delivery claim. The existing keyed message rendering,
bootstrap/load, persisted full-screen slide and realtime flow are reused. No extra fetch, polling
loop, effect or duplicated client-side conversation store was added.

## Verification / remaining gates

Migration `20260906015000_tessa_whatsapp_ingress.sql` was applied only to the isolated local
`tellbook_tessa_b2_certification` database. The schema snapshot preserves unrelated existing diffs.

Local tests cover namespace/source deduplication, linking-text exclusion, long bounded questions,
web/WhatsApp ordering and shared context, five-item overflow, expiry, reset, connection/security/
notice changes, concurrent admission, revocation during inference and retry after worker
reconstruction with committed planning/tool-evidence reuse. Existing Tessa, authentication and
WhatsApp race suites plus focused notification regressions are run with fake models/transports.
Provider component/API/realtime tests and Svelte checks cover the new source marker.

### B1–B3 cross-check fixes (2026-09-06)

- Linked chat no longer inherits the anonymous onboarding ten-minute freshness filter. Delayed
  messages within the reply window enter the bounded queue; closed-window and invalid future
  timestamps get terminal records without retaining message text. Queue timestamps and reply
  deadlines come from the durable signed receipt, read alongside grant authority in one query.
- Admission claims the provider account with `SKIP LOCKED`, so a busy account does not stall the
  admission worker. Both head selection and its locked recheck stay in the configured phone
  namespace; another connector cannot block or have its queue mutated by this worker.
- Web submission, introduction and reset take account locks before thread locks, matching
  WhatsApp operations and avoiding foreign-key lock inversions. Reset locks the active thread
  before runs; completion serializes with reset through the existing thread advisory lock.
- New PostgreSQL regressions cover signed delayed routing, timestamp authority, terminal expiry,
  future timestamps, locked-account skipping, connector isolation and deterministic account/thread
  lock ordering. Existing race, notification/auth and provider UI checks remain green. No frontend
  rewrite, new polling, runtime activation, live sending or schema migration was needed.

B4 now attaches `WithConversationIngress(NewTessaWhatsAppIngress(...))` and the matching worker
namespace/allowlist together with answer delivery under its chat switch. Before activation, apply
pending migrations and use consistent configuration across roles; see [B4 activation notes](tessa-whatsapp-b4.md).
No full live WhatsApp conversation, throughput target or production capacity is claimed. Live B1/B2
gates, B4 live delivery/typing and B5 retention/lifecycle/load gates remain.
