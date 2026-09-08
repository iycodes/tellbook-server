# Tessa conversational evaluation — 7 September 2026

## Scope and method

This is scenario-based conversational evaluation, not training on a list of answers.
The suite contains 36 distinct questions in six independent conversations. Development
questions were rerun after fixes; separate holdout/breadth questions and a fresh transfer
conversation checked whether the changes applied beyond the original failures.

The test uses real repository enqueueing, PostgreSQL-backed conversation history and query
state, the Tessa worker, actual model requests, real read tools, and committed answers.
It deliberately does not test browser rendering, HTTP authentication, or WhatsApp delivery.
No real customer message was sent and no real provider data was modified.

The approach combines task-specific factual gates with manual transcript review, following
[OpenAI's evaluation guidance](https://developers.openai.com/api/docs/guides/evaluation-best-practices).
Completion, valid JSON, or an HTTP success is not evidence that the answer was correct.
Assertions avoid matching whole sentences: wording remains model-generated. Some checks
cover tool selection and exact evidence; others reject particular unsupported claims.
Human review remains necessary, particularly for ambiguous follow-ups and conversational quality.

## Isolated ground truth

All fixtures are synthetic, in the local `tellbook_tessa_b2_certification` database. They are
removed after each evaluation. The question timestamp is anchored to 7 September 2026 in
Africa/Lagos, independently of the worker's processing clock.

- 14 September bookings; 3 on 7 September, more than a single page during the week.
- 11 `booked`, 1 `pending`, 2 `cancelled`; **zero `confirmed`**. Booked does not mean confirmed.
- 5 unpaid bookings; zero deposit-paid/balance-due bookings.
- 12 bookings needing attention: 5 awaiting payment and 7 awaiting provider confirmation.
- Two different customers named Ada. Ada Okafor has 4 bookings; Ada Bello has 3 including one cancellation.
- Strategy Consultation: 60 minutes, virtual, 2,500,000 minor NGN units = **₦25,000.00**.
- No recorded revenue transactions, available payouts, inbox conversations, or reviews.
- No configured bookable slots. A service's 60-minute duration does not mean 60 minutes are free.

## Coverage

| Conversation | What it exercises |
| --- | --- |
| `calendar` | Today → first booking's payment → week → month total → confirmed filter → first two |
| `operations` | Attention → unpaid list → ambiguous name → explicit name clarification → prices → revenue |
| `boundaries` | Bare weekday → explicit date → unsupported cancellation → empty future month → privacy → off-topic coding |
| `holdout` | Pidgin total → cancellation exclusion → earliest three → named customer's count → next month → missing customer |
| `breadth` | Inbox, marketplace readiness, reviews, payouts, booking-hours help, period comparison |
| `transfer` | Explicit monthly range → outstanding deposits → reset payment filter → ordinal calendar date/name-based availability → service details → prior-month revenue |

For a follow-up count, retaining an earlier cancellation exclusion can be a reasonable
interpretation only when the answer explicitly qualifies the smaller count. The suite accepts
that interpretation; it does not accept an invented `confirmed` restriction or a count of zero.

## General fixes made

1. **External schema transmission:** the OpenAI Responses adapter recursively decoded the
   application schema into maps, losing the intended property order. It now preserves raw
   nested JSON so tool discriminators precede arguments. A wire-level regression test verifies
   this. The external comparison changed from widespread wrong-tool/invalid-plan failures to
   23/24 passing checks immediately after this fix.
2. **Current request versus history:** the active `current_question` is last in both input
   payloads. Planning writes a self-contained `resolved_question`; synthesis no longer receives
   old messages or prose summaries that can compete with current evidence. Conflicting
   instructions about using history were clarified.
3. **Structured follow-up scope:** planning receives the last completed query's actual filters,
   through one bounded tenant/thread-scoped read. It cannot see answers after the trigger
   message. Booking references now include customer/date information, not just a repeated
   service title.
4. **Independent filters:** search and exact counts share one filter implementation with booking
   status inclusions, exclusions, payment state, literal search, range and upcoming cutoff.
   `pending` is not a proxy for unpaid; excluding cancellations is not a proxy for confirmed.
5. **Money and zero values:** evidence includes catalog-formatted monetary values, using integer
   arithmetic and the correct currency exponent. Raw minor units remain in the audit evidence.
   Unknown currencies are not guessed. The response contract distinguishes a known zero from
   unavailable data.
6. **Availability:** evidence explicitly counts returned slots, including zero. The existing
   availability tool accepts a literal service-name query as an alternative to a known ID;
   filtering happens before service/slot limits. It does not silently select an arbitrary match.

This update adds no canned answer templates, question-keyword routing rules, new retry layers,
new model calls per normal turn, write tools, public API endpoints, UI changes, or migrations.
The configured local-first routing and 30-second local / 20-second external request timeouts
remain unchanged. The minimum supported input budget is now 3,300 tokens; the configured
normal input budget was not increased.

## Verification and limitations

### Live results

| Run | Factual gates | Median turn | p95 turn | Notes |
| --- | --- | --- | --- | --- |
| Final configured local-first, 24 turns | 22/24 | 16.4 s | 39.5 s | All completed; 21 local answers and 3 external fallbacks |
| External comparison after schema-order fix, 24 turns | 23/24 | 4.5 s | 5.6 s | Remaining failure was named-service availability tool selection |
| External transfer conversation after availability fix, 6 turns | 6/6 | 4.2 s | 6.3 s | Includes the previously failing availability case |

These are serial end-to-end worker timings, including planning, tools and synthesis, not
provider API latency or load-test percentiles. The final local-first maximum was 42.5 seconds.
The external 24-turn suite was not rerun in full after the last availability change; its
six-turn transfer conversation was rerun instead. The additional boundary/breadth scenarios
were exercised in earlier passes, not in the final 24-turn comparison.

Two local-first response-quality failures remain and are deliberately retained as failing
live-evaluation gates:

- After an ambiguous payment question followed by the full customer name, the local model
  repeated a booking clarification without querying that customer's bookings. It should
  retrieve the matches and explain their payment states, or ask a useful evidence-backed
  clarification when necessary.
- A named-customer count retained the previous cancellation exclusion but called the result
  a total without disclosing that restriction. The database correctly returned 2 under the
  chosen filter; the unqualified total is 3. This is a scope/presentation failure, not a
  database counting failure. The same sequence passed with the external model, which made
  the exclusion explicit.

The local model also occasionally uses awkward ISO timestamps or speaks as the provider
("I offer") rather than as their assistant. These are not presented as factual passes for
tone. Further work should evaluate clarification usefulness and filter disclosure across
new conversations, not add phrase-specific handlers. Existing technical-failure fallback
does not detect semantically weak but valid answers. No new judge/retry/routing layer was
added to hide these failures, and production model selection was not changed.

Synthetic transcripts for this development session are in
`/tmp/tessa-conversation-local-final.log`, `/tmp/tessa-conversation-external-fixed.log`, and
`/tmp/tessa-conversation-external-transfer.log`. They are temporary local artifacts; the
checked-in harness and fixtures are the reproducible record.

### Regression checks

Passed on the final functional code:

- Race-enabled tests for `internal/tessa`, `internal/appdata`, `internal/llm`, `cmd/api`,
  and `internal/config` (database integration tests require their separate opt-in run).
- The isolated database-backed Tessa regression suite with race detection, including
  named-service availability and count/list filter reconciliation.
- `go vet` for Tessa, appdata, LLM and API packages; API binary build; `git diff --check`.

This is not a claim that the entire repository's unrelated test suites passed.

Deterministic coverage includes count/list reconciliation beyond a page, include/exclude/payment
filters, cancellation spellings, tenant isolation, query-state replay boundaries, exact currency
formatting (including zero, negative amounts, zero-decimal currency, large integers), safe result
trimming, empty availability, service-name filtering before limits, prompt ordering, and hosted
schema ordering on the actual HTTP request body.

The external benchmark uses the existing approved external model inside the test harness only.
It does not switch production routing or imply that every future question is certified.
This is a functional response evaluation, not a load test or a statistical reliability guarantee.
The local and external models are stochastic, and a single passing run is insufficient for a
broad production-readiness claim. No running API/AI worker was restarted by this task.
Activation requires rebuilding and restarting the API and AI workers together on
`tessa-conversation-grounding-v7`; do not mix their schema revisions.

## Reproduce

From `tellbook-server`, with the certification database already migrated and idle:

```sh
# Configured local-first routing, including its normal fallback
node scripts/tessa-conversation-eval.mjs 'calendar|operations|holdout|transfer'

# Same synthetic path, configured external model only for comparison
node scripts/tessa-conversation-eval.mjs 'calendar|operations|holdout|transfer' external

# Additional breadth/safety questions
node scripts/tessa-conversation-eval.mjs 'boundaries|breadth'
```

Run serially with other database/AI worker tests: they share the certification queue.
The launcher reads `.env` privately and refuses a nonlocal database. The Go test also checks
the exact database name, requires explicit opt-in, and refuses an already-active queue.
`CONVERSATION_EVAL` log entries contain synthetic questions, answers, tool plans, safe evidence,
model identity, fallback use and elapsed time. Keep future real-user examples redacted.
