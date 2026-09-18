# Additional transactional email wiring

Implemented behind `ADDITIONAL_EMAILS_ENABLED=false`. No production deployment,
live enabling, historical event backfill, or real email sends are part of this change.
The existing authentication and notification email switches remain required for
those respective families. API and core worker processes must use the same flags.

## Delivery paths

| Family | Atomic capture | Existing delivery path |
| --- | --- | --- |
| Password / linked identity | Account repository transaction → `account_security_events` | Encrypted authentication email jobs; login codes retain priority; 24-hour deadline |
| Payout account added / default changed / removed | Payout destination transaction → security event | Same authentication worker and security renderer |
| Payout status | Payout transition → `financial_jobs`, kind `financial_email` | Small worker in the current core process, shared notification SMTP sender |
| Refund progress | Refund request transition → audience-specific financial email jobs | Same financial email worker; successful refund emails stay in booking notifications |
| Outstanding step | New enabled booking creation → one `booking_step_reminder` delivery | Existing booking planner and email worker |
| Appointment completed | New enabled completion event → one customer `booking_completed` delivery | Existing booking planner and email worker; review flags remain false |

Transaction-local PostgreSQL settings enable capture triggers; they do not depend
on connection session state and work with transaction pooling. Disabled processes
create no new notices. Old bookings and events keep false creation markers.
Queued additional notices are not claimed while their delivery switch is off.

Security recipients are captured before a change. Only the first verified email
may supply the fallback when no verified email existed. Repeated identity links
and unchanged payout destination saves do not create security events. Account
numbers and phone numbers reach renderers only as last-four suffixes.

Financial deduplication uses entity, transition revision and audience. Refund
revisions advance only on status changes; payout notification revisions retain
the payout version of the last status transition, because ordinary payout
reconciliation also advances the main version. Same-status reconciliation cannot
suppress or duplicate notices. Payout amounts and destinations use the committed
snapshot. Refund amounts use the original request and successful attempts linked
to that request. Recipient verification, booking preferences, consent and contact
suppression are checked again before financial dispatch.

Processing/request-recorded notices wait five minutes. Uncertain outcomes wait
30 minutes. Terminal outcomes are immediately eligible. Superseded jobs are
cancelled before SMTP. Financial email jobs persist `dispatching` before calling
SMTP; expired dispatch leases and ambiguous results become `unknown`. Those jobs
are never automatically resent. Only definite transient SMTP failures retry,
using the existing booking email backoff and eight-attempt limit. Investigate an
unknown send using provider/SMTP evidence; do not reset it to pending blindly.

The outstanding reminder has a fixed booking-level idempotency key. Scheduling
uses the earlier of appointment minus 24 hours and actual unpaid reservation
expiry minus one hour, with a creation-plus-one-hour floor. If that leaves no
window, it is skipped. Replanning/rescheduling updates the same unsent job. At
dispatch, the first applicable step is deposit, agreement, then remaining balance.
Consent, preferences, recipient, booking status, payments and agreement state are
rechecked. Accepted or ambiguous delivery consumes the allowance. Reservation
expiry is shown only for the payment to which it applies; agreement reminders
and balances following a paid deposit have no invented deadlines. Routine queue cleanup retains the
reminder and completion delivery rows for the lifetime of the booking so their
idempotency records cannot expire. Content and dispatch eligibility are read in
one consistent database snapshot. A change discovered before SMTP cancels the
known-unsent notice without consuming its allowance; a raced reschedule replans
the same reminder job inside that cancellation transaction.

## Validation

New captured-sender integration tests cover transactional rollback, recipient
selection, password creation/change, repeated identity links, login priority,
payout snapshot changes, same-status payout/refund reconciliation, partial
refunds, obsolete delayed jobs, SMTP retry/ambiguity/interruption, reminder
schedule and priority, rescheduling, consent and recipient changes, cancellation,
expiry, completed steps, one-reminder allowance, completion deduplication and
historical-event exclusion. They use `TEST_DATABASE_URL` and never instantiate
an SMTP sender. Existing renderer and worker tests remain applicable.
The queue tests share the test database: run their packages serially (`go test
-p 1 ...`) so independently running authentication workers cannot claim another
package's login-code fixtures. Regression checks also cover retention and
consent/rescheduling changes between authorization and message preparation.

Migrations `20260914010000` through `20260914040000` were applied to an isolated
pre-change database containing pending authentication, booking and financial
jobs. Their status and payloads remained intact, and the historical booking
retained its disabled reminder marker. Downgrades intentionally refuse to delete
notification history; disable the feature and use a forward migration if needed.

Focused tests and database lifecycle checks passed. The full `go test ./...`
suite also passes after updating the stale WhatsApp test to expect ordinary
text to route to Tessa onboarding while preserving exact control-command checks.
The demo seed command currently references a nonexistent
`marketplace_customer_identities.updated_at` column; isolated fixtures avoid it.

## Separate rollout

Apply migrations first, deploy API and core workers with the flag false, then
review enabling the flag alongside the existing email switches. Use the existing
SMTP configuration, trusted public app origins, encryption keys and destination
HMAC key. No new API or preference screen is needed. Completion emails stay
review-free until review submission is separately implemented and approved.
