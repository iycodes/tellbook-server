# Outstanding steps and appointment follow-ups

These are reusable HTML/plain-text designs in `internal/transactionemail`, with
offline preview galleries. Scheduling and completion delivery are now wired
behind a disabled switch; see [additional email wiring](additional-email-wiring.md).

## Outstanding steps

`RenderReminder` covers deposit, remaining balance, and agreement reminders.
Agreement copy distinguishes signature from confirmation. An appointment card
with a calendar date sits above the outstanding step. Long service names use a
full-width row; large monetary values receive smaller type.

The caller supplies current eligibility (`BookingActive`, `StepOutstanding`),
the time eligibility was checked (`CheckedAt`), a trusted booking URL, and an
optional actual step deadline. Inactive bookings, completed steps, and deadlines
at or before `CheckedAt` are rejected. No deadline is invented when absent.
Deposit amounts must be the outstanding deposit obligation, not the total
remaining booking balance; balance reminders use the remaining balance. Amounts
use integer minor units and configured currency formatting. Agreement reminders
never show unrelated payment data.

Appointment and deadline times use the booking's IANA timezone with an explicit
UTC offset. Payment guidance asks recipients to check the latest payment state
before paying again. The footer directs them to remaining requirements and
provider confirmation; completing one step is not presented as securing the
whole booking. CTAs lead to the customer's booking, where the current action can
be reviewed, rather than constructing a new payment request from an email.

## Completion and reviews

`RenderCompletion` requires a completed booking. The default is a completion
summary with a View booking action. The review variant offers an optional,
neutral invitation that welcomes both positive and negative feedback; it never
preselects a rating. Payment and refund information remains on the booking page.

The review invitation appears only when `ReviewEnabled && ReviewEligible &&
!ReviewAlreadySubmitted`, and requires a valid HTTPS review URL. Every other
combination produces the completion-only email, even if an unused review URL is
missing or invalid.

**The review submission flow is not live in the inspected client.**
`tellbook-marketplace/src/routes/booking/[id]/review/+page.ts` loads sample booking
data; the page's publish handler changes local state without a backend request.
The backend public-review handler provides read access. Keep `ReviewEnabled`
false until authenticated booking ownership, submission, persistence, and
review eligibility are implemented. The gallery labels the review variant as
future functionality. Its `example.com` links are explicitly sample links.

## Delivery integration

Implemented with one outstanding-step reminder per booking and completion-only
follow-ups. See [additional email wiring](additional-email-wiring.md). Review
submission and review invitations remain separate future work.

## Preview and validation

Set `BOOKING_FOLLOWUP_EMAIL_PREVIEW_DIR` to the preview root and run:

```sh
go test ./internal/transactionemail -count=1
```

This writes `reminders/index.html` and `completion/index.html`, plus six reminder
and four completion HTML/plain-text fixtures. The examples include signature
and confirmation, missing deadlines, long details, a large amount, and the
already-reviewed fallback. All data and links are fictional.

Tests cover eligibility suppression, deadline cutoffs, timezone conversion,
payment/confirmation-specific copy, review gating across every flag combination,
HTML escaping, unsafe URLs, delivery identities and Outlook fallback markup.
The layouts retain inline essential styles and fluid tables with conditional
600px Outlook wrappers, matching the other TellBook emails. Local browser
inspection covers 320px, 375px and 760px plus 320px with style elements removed.
Actual Gmail, Outlook and Zoho rendering and test sends remain outstanding.
