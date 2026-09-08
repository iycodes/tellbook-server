# Tessa WhatsApp B2 — security-email foundation

Implemented locally on 2026-09-06. The dedicated linking-code service and WhatsApp-first onboarding
integration are now locally verified; authorized B2 live acceptance remains pending. Ordinary
WhatsApp messages still do not enter Tessa conversations. No UI/API capability is falsely enabled.

## Implemented

- Link, replacement and actual disconnect transitions persist a deduplicated security event in the
  same transaction. Receipt replay and repeated disconnect do not create duplicate notices; failed
  linking transactions roll the event back too. Replacement produces one replacement notice, not
  an intermediate disconnect notice.
- Each event snapshots the provider's verified account email. Later identity changes cannot redirect
  the notice. Accounts without a verified email record `no_verified_email`; disconnect still succeeds.
- Existing `authchallenge.Worker` prepares encrypted `tessa_security_email` jobs and sends them through
  the existing SMTP transport, bounded concurrency, retry classification and stable message IDs.
  No separate SMTP sender, worker process, new secret, booking-email dependency or welcome-email
  behavior was added. Security messages contain the final four phone digits and event time, no OTP.
- `AUTH_EMAIL_ENABLED` controls sending. An SMTP outage or disabled email does not block recording
  connection changes. Unqueued events have a 24-hour deadline and expire without a late burst of mail.
- Login-code deliveries have queue priority over security notices. Preparation failures are logged
  without stopping already queued authentication mail. Indexed, bounded, skip-locked preparation
  prevents multiple workers from creating the same event's job.
- Security events have their own delivery reference, mutually exclusive with provider/customer auth
  challenge references. They cannot be interpreted as login, password-reset or identity-link challenges.
- `email_queued_at` persists independently of terminal delivery-job retention. Deleting an old job
  does not re-enqueue its event. SMTP acceptance updates the event and job atomically; ambiguous SMTP
  outcomes remain `unknown`, not blindly resent. Acceptance is not proof of inbox delivery.
- Account deletion cascades through the event and encrypted job. Scheduled Phase B metadata retention
  remains part of B5; this slice does not claim production lifecycle certification.

## Verification and activation

Migration `20260906012000_tessa_security_email_intents.sql` was verified through the complete chain
on isolated local database `tellbook_tessa_b2_certification`. The earlier B1 test database also has
an intermediate copy of this development migration; use the B2 database for current certification.

Passed race-enabled Tessa/WhatsApp and authentication-email tests for atomic rollback, duplicate
events, link/replacement/disconnect behavior, unverified email, encrypted payload validation,
original-recipient preservation, SMTP acceptance, ambiguous no-resend behavior, disabled sending,
job-retention deduplication and login-code priority. Tests use in-process fake mail senders, not
real recipients. Focused existing auth/notification regressions and Go vet are also run.

The main database, running API, live env files and live sending settings were not changed. Apply
the new migration before starting the updated worker against any intended deployment database.
Review existing auth and notification queues before a live restart, and preserve the recipient
restrictions in the Phase A/B1 activation notes.

The [dedicated-purpose email OTP service](tessa-whatsapp-b2-email-link.md) is now implemented and
locally verified, together with [deterministic WhatsApp onboarding](tessa-whatsapp-b2-onboarding.md)
using the same atomic receipt/linking boundary. Never reuse an ordinary sign-in code for Tessa linking
or put onboarding codes into model input or conversation history.
