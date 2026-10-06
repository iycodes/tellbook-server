# Tessa rollout gates

## Conversational evaluation and grounding (2026-09-07)

Current protocol: `tessa-conversation-grounding-v7`. API and AI workers must use the
same revision. This update adds no migration, UI change, model switch or retry layer.

- Planning retains conversation history and references and writes a self-contained
  `resolved_question` before choosing tools. Synthesis receives that request, the original
  question and current tool evidence, excluding all historical messages and prose summaries.
  The active `current_question` is serialized last, after context, in both prompts.
  A bounded, tenant/thread-scoped read of the last completed lookup supplies `previous_tools`
  for continuation filters; it cannot read messages after the current trigger. Booking reference
  labels include customer/date instead of only the service title. Neither is current fact evidence.
- Booking search and exact counts share an independent `payment_state` filter (`any`,
  `unpaid`, `balance_due`, `paid_in_full`). Booking status is not payment status. `unpaid`
  means the initial obligation is unsatisfied; `balance_due` means the deposit is paid.
  Status filters remain independent, including whether terminal bookings are included.
  `excluded_statuses` expresses exclusions directly, without guessing that everything remaining
  is confirmed. Both cancellation spellings have the same matching semantics in lists/counts.
- Safe evidence adds catalog-formatted monetary `_display` values, preserving exact integer
  `_minor` values for audit. Unknown currency formatting remains unavailable, never guessed.
  Formatting occurs before the existing 4 KB evidence bound and is committed for replay.
- Availability evidence explicitly reports returned slot counts; service duration is not a slot.
- The OpenAI Responses adapter preserves application schema property order on the wire,
  including nested tool discriminators; generic map decoding previously reordered it.
- The static planning minimum is 3,300 input tokens (normal configured budget is unchanged).

Run `node scripts/tessa-conversation-eval.mjs` from the server directory for development
scenarios, then `node scripts/tessa-conversation-eval.mjs 'holdout|breadth'` for separate
scenarios. `transfer` contains six additional questions. Append `external` to compare the
configured approved external model without changing application routing. It reads `.env` privately but uses only the local
`tellbook_tessa_b2_certification` database, synthetic accounts and the configured models.
The database must already be migrated and idle. Run serially with other database/AI tests.
No API, WhatsApp/email sender or real account is involved; the repository/worker/tool/history
path is real. Fixtures are deleted after the test. Transcripts contain synthetic data only.
Passing technical/factual gates still requires human review of the generated prose.

## Exact booking totals (2026-09-07)

Protocol `tessa-exact-booking-counts-v6` introduced exact counts. Planning declares `booking_count_only`
before selecting tools. Count-only plans require `get_booking_metrics` with `metric=count`;
booking lists cannot satisfy them. The existing bounded repair attempt handles inconsistent
plans. Requests for a total plus details may use count and a bounded search together.

The metrics tool's count mode runs a database `COUNT(*)` with the same shared tenant, calendar
overlap, status, literal name/title search, and upcoming predicates as `search_bookings`.
An empty status filter includes all statuses, as booking search does; `get_schedule` remains
the non-terminal schedule view. Count mode has no row limit, profile/currency requirement,
or financial query, and returns no individual booking rows to the model. `metric=summary` preserves existing performance
statistics and comparisons, including their financial configuration requirements.

The exact total (including zero) and resolved range are committed as one small evidence object.
Synthesis checks for matching exact-count evidence before any model call. Tessa writes its own
answer; count-only responses have no required appointment list or truncation notice. Navigation
uses “View bookings,” never a guessed individual booking ID. Local conformance covers the
list-to-count follow-up, English/Pidgin, filtered totals, and natural responses to 23 and zero.
Database regressions cover more than eight rows, mixed currencies without profile setup, zero,
literal search escaping, status/upcoming filters, month boundaries, tenant isolation, and replay.
Aggregate/empty-result schemas require an empty entity ID when no entity evidence exists;
optional examples remain supported and answer text is never restricted to preset wording.

Verification: focused race tests, isolated Tessa database and WhatsApp fake-transport regressions,
static analysis, and API build passed. Final local count/calendar conformance and aggregate/empty
answer regressions passed after correcting the calendar-scope and aggregate-metadata issues
found during the broader run. External live conformance and repository-wide certification were
not rerun for this update.

No new API endpoint, frontend change, database migration, timeout change, or retry layer.
Restart API and AI worker together to deploy the new configuration revision; this implementation
does not itself restart services or send live messages.

## Server-resolved calendar periods (2026-09-07)

Protocol `tessa-calendar-periods-v5` introduced calendar periods. For all eight date-range tools, the model
selects today/yesterday/tomorrow, this/last/next week, or this/last/next month. It leaves
`from` and `to` empty for these named periods; Go resolves them using the original
question timestamp in the provider's timezone. Explicit dates and other clearly specified
ranges use `period=custom` with inclusive dates. Missing periods and mixed named-period/date
arguments are rejected through the existing correction attempt, not silently defaulted.

Calendar weeks run Monday–Sunday. Thus “this week” sent on September 7, 2026 resolves to
September 7–13 inclusive, and SQL reads up to (but excluding) September 14 midnight.
Calendar-day addition preserves local midnight across daylight-saving transitions. Month
boundaries account for leap years. The resolved plan is committed before tool execution;
retries replay those dates without recomputing them using a later clock.

Synthesis receives the named period and resolved range so it can describe the whole requested
period even when all returned bookings fall on one day. Tessa still writes its own answer:
no response templates, additional model calls, retry layers, or frontend/database changes.
The strict schema and Go request contract have been updated together.

Verification covers all eight dated tools, question-time timezone conversion, calendar/DST
boundaries, exclusion of next Monday's midnight booking, and committed-plan replay. The opt-in
local conformance suite includes named/custom periods after a prior “today” conversation and
weekly answer framing. Run `make tessa-local-evals` against the deployment model.
Focused race tests, isolated Tessa database/WhatsApp regressions with fake transports, static
analysis, API build, and the full local-model conformance suite passed. The final calendar
suite also passed with whole-period filtering enforced in its assertions for future ranges.

Deploy by restarting the API and AI worker together: the existing configuration fence rejects
queued plans from older protocol revisions. This code update does not itself restart services
or send live messages. External live conformance and load certification are separate gates.

## Grounded, model-written answers (2026-09-07)

Protocol `tessa-grounded-answers-v4` uses tool-specific argument schemas. Search selection is
explicit (`list`, `first`, `top`); list page size is server-owned and capped at eight. An
intentional first/top selection is distinct from a truncated list.
Requests for up to 25 selected results retain their requested count; reads remain capped at
eight, so a bounded preview cannot silently masquerade as a fulfilled larger request.
Upcoming appointment
filtering uses the committed planning clock before SQL applies its limit. The original
WhatsApp send timestamp is supplied separately for resolving relative dates in delayed messages.

Tessa still writes the entire visible answer, including any partial-result explanation.
Each passage is written once, with an optional evidence entity ID. The app joins these passages
and displays the model-written notice without templating or rewriting the prose. Validation rejects
unknown references, empty passages, undisclosed partial collections, raw URLs/UUIDs,
and overlong output through the existing single correction attempt. These checks establish
reference membership and declared coverage, not a mathematical proof of every natural-language
claim. Do not describe them as a complete hallucination detector or add a critic-model call.

Navigation and bounded follow-up references derive from the same current evidence. No new
frontend payload or database migration is required. The changed protocol revision invalidates
old queued model plans through the existing configuration fence; restart API and AI worker
together when deploying. Existing committed messages remain history, not regenerated answers.

All three model adapters use the application-defined schema. The hosted adapter follows the
[official Structured Outputs contract](https://developers.openai.com/api/docs/guides/structured-outputs),
including strict objects and nested tool variants, instead of reflecting a different Tessa shape.
The local adapter preserves schema key order, explicit zero temperature, and rejects missing/unsupported finish
reasons, truncation, and filtering. No new retry or provider-fallback layer was introduced.
Existing input/output token settings are unchanged.

Answer schemas have a small fixed set of reusable variants; entity membership is validated in Go,
not encoded as per-conversation schema enums that would grow the compiled-schema cache.

Cross-check fixes: payment and availability entities now participate in grounding; customer
and attention summaries distinguish required entities from optional booking examples. Omitting
an optional example does not falsely mark an aggregate answer incomplete. Navigation is built
from mentioned evidence before action/reference caps, so unmentioned items do not consume
the navigation slots of described items. Planning and synthesis both resolve relative
dates from the original question timestamp in the provider timezone. Upcoming SQL filtering
still uses the committed processing clock, which is no longer exposed as a competing date in
the model's planning input. No additional model call or retry was introduced.

Verification for this update: focused Go race tests, static analysis, and API build passed;
Tessa database/WhatsApp regressions passed against the isolated certification database with
fake transports. Local Gemma synthetic conformance covered the full tool suite, list/first
selection resets across phrasings, reference follow-ups, delayed relative dates, upcoming
filters, answer coverage, and booking presentation. These are regression results, not a new
concurrency certification. Hosted schema behavior was tested with fake HTTP responses; the
external live suite was not rerun. No live messages were sent or services restarted.
The repository-wide suite is not certified by this update: the existing unrelated
`TestClassifyInboundControlIsExactAndBounded` expectation for `STOP now` remains unresolved.

Tessa is available to every authenticated provider when `TESSA_AI_ENABLED=true`; this is the
global on/off switch. `TESSA_AI_PROVIDER_ALLOWLIST` has been removed and any stale value is ignored.
WhatsApp still requires its channel switches, verified account linking, current consent and an
unexpired connection. Tenant isolation, rate limits and delivery authority checks are unchanged.
Test recipient restrictions belong only in opt-in live-test harnesses, not application access.
Run the gates below against the exact model and worker concurrency intended for the environment.
Deploy this change to the API, core worker and AI worker together, then restart those processes;
changing or deleting the old environment variable alone does not update an older binary.

Account-access update verification (12 September 2026): focused race tests, isolated PostgreSQL
Tessa integration tests in appdata/authchallenge/whatsapp, static analysis and the API build passed.
Coverage includes multiple providers without account configuration, anonymous rejection, global
disable, verified linking, revocation and tenant isolation. No live accounts or delivery workers
were changed. The broader WhatsApp package still has the unrelated `STOP now` classification
test failure noted above.

## Automated gates

Run the deterministic suite first:

```sh
go test ./...
go test -race ./internal/tessa ./internal/appdata ./cmd/api
go vet ./...
go build ./...
make tessa-query-plans
```

Export the deployment configuration without printing it. Then run the local model fixtures:

```sh
make tessa-local-evals
make tessa-local-capacity
```

The conformance suite covers canonical tools, Nigerian English and Pidgin, date handling,
clarification, out-of-scope prompts, prompt exfiltration, untrusted tool evidence, and empty results.
The capacity gate defaults to the configured Tessa worker concurrency and three factual turns
per concurrent worker (six turns at the default concurrency of two). Every measured turn
includes planning and synthesis and uses the running API's model, sampling, input/output, request
timeout, and total-turn settings. It fails above a 45-second p95. Set
`TESSA_LOCAL_CAPACITY_CONCURRENCY` to `TESSA_AI_WORKER_CONCURRENCY`; increase the turn count for a
longer soak. Record the model, hardware, turn count, concurrency, turn throughput, p50, p95, and
failures before increasing traffic or worker concurrency. Tool reads are covered separately by the database
plans and PostgreSQL integration suite.

Latest local baseline (2026-08-31): `gemma-4-E4B-it-UD-Q5_K_XL.gguf`, 6/6 complete turns,
concurrency 2, 69.816 seconds elapsed, 0.09 turns/second, p50 25.383 seconds, p95 30.846 seconds.
Re-run this gate on deployment hardware; this development result is not a substitute for that
environment's own measurement.

When an external fallback is selected, its credentials, approved-processing flag and notice revision
must match the intended deployment configuration. Run both suites below against every
model/version selected for fallback:

```sh
make tessa-external-evals
RUN_OPENAI_COMPAT_EVALS=true go test ./internal/ai -run TestExternalOpenAICompatibleConformance -count=1 -v
```

The Tessa-specific suite exercises its strict routing schema. The generic suite covers the other AI
workloads that share an OpenAI-compatible adapter. The hosted Tessa adapter is regression-tested to
ignore `OPENAI_RESPONSE_LOG_FILE`, so full assistant output is not written to that diagnostic file.

## Rollout decision

Do not enable Tessa when any conformance request fails, the configured concurrency exceeds
measured full-turn capacity, database or SSE reset tests fail, or external-processing
approval/notice data is not current. Tessa requires an explicitly configured notice revision, a
self-hosted primary, and explicit approval before an external fallback can be enabled. A local-model
outage must degrade only Tessa: normal provider dashboard routes and non-AI booking operations must
remain available.
