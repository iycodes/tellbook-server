# Tessa WhatsApp B2 — dedicated email-linking codes

Implemented and locally verified on 2026-09-06. The internal service is now wired to
[signed WhatsApp onboarding](tessa-whatsapp-b2-onboarding.md) and shared grant completion.
Live activation/acceptance remains pending. No new public auth endpoint, model invocation or
fallback was added.

## Implemented

- Separate `tessa_whatsapp_email_challenges` records, purpose `tessa_whatsapp_link`, tied to the
  inbound receipt, verified provider email/account, receiving phone ID, signed WhatsApp sender,
  account security revision and current Tessa channel notice.
- Random six-digit code with a purpose/request-bound HMAC using the existing server key. The
  outbound destination/code are encrypted with the existing delivery keyring. No code is returned
  to the caller, logged, or written to Tessa transcripts/model input by this service.
- Ten-minute validity from issuance, 60-second resend cooldown, three issuances per provider and
  per receiving-phone/sender pair per rolling hour, and 60 globally per minute. Indexed counters
  and a short transaction-level issuance lock make limits atomic. Five wrong attempts per sender
  per rolling hour survive resend; resending invalidates the previous request. Issuance returns
  no challenge for unknown/unverified/mismatched accounts or an exhausted issuance budget.
- The existing authentication email worker sends `tessa_link_email`; no new SMTP infrastructure
  or booking-notification dependency. `AUTH_EMAIL_ENABLED` controls issuance/sending. Queue wakeup,
  encryption cleanup, bounded concurrency, and safe retry/unknown handling are reused. Code emails
  retain the same queue priority as authentication codes, ahead of security notices.
- SMTP acceptance is required before code verification. It is not proof of inbox delivery.
  Ambiguous acceptance stays `unknown`, without a blind resend or an enabled code.
- The worker checks request expiry, consumption and current account security revision before
  sending, and rechecks validity when recording acceptance. Verification independently checks
  the current account. A revocation racing SMTP cannot retract an already transmitted message,
  but the inactive code cannot subsequently authorize linking.
- Dashboard replacement, account disconnect and sender-scoped STOP/TESSA DISCONNECT invalidate
  pending email challenges, including when new linking is disabled. Unrelated senders cannot
  cancel a challenge. The worker suppresses stale queued mail when it claims/checks it.
- Database references/template constraints and strict payload validation isolate these jobs from
  security notices and provider/customer sign-in, password-reset and identity-link challenges.

## Transaction contract

`IssueTessaEmailLinkTx` and `VerifyTessaEmailLinkTx` accept the ingress transaction; they never
commit it. The onboarding caller validates the configured connector, feature/allowlist
eligibility and explicit notice acceptance, and use the sender from signature-verified ingress,
not a user-claimed number. It must persist the receipt and deterministic control response atomically
with code issuance. Public replies must be generic regardless of account existence or issuance
limits; the internal nil challenge result must not become an account-existence oracle.

Verification returns `Verified=false, nil` for an invalid code, so the caller must **commit** that
receipt and failed-attempt mutation. Duplicate inbound receipts must bypass verification entirely.
Database failures roll back. On success, consume the proof, enforce the shared active-grant
constraints, replace/revoke through the shared connection service, record the security notice and
enqueue the linked response in **one transaction**. Never commit successful proof consumption by
itself, transfer another account's active sender grant, or turn the proof into a login credential.

The service assumes its caller has performed ingress/rollout checks. The signed webhook integration
now performs those checks and has local receipt/response/grant rollback and replay evidence.
Authorized live linking remains an acceptance gate.

## Verification and activation

Migration `20260906013000_tessa_email_link_challenges.sql` was applied only to isolated local
database `tellbook_tessa_b2_certification`, on top of its fully migrated B1/B2 chain. Schema snapshot
updated without replacing unrelated schema changes.

Race-enabled tests cover concurrent single consumption, sender/receiver/notice mismatch, SMTP
acceptance and ambiguous outcomes, encrypted payload/purpose isolation, stale account revision,
expiry, resend cooldown and carried attempts, rollback, duplicate issuance, provider eligibility,
disabled issuance, dashboard replacement, disconnect and sender-scoped STOP. Focused existing
authentication/WhatsApp/notification regressions, Go vet and API compilation are also checked.
All sends use in-process fake transports; no real recipients are contacted.

The main database, API process and live env files are unchanged. Apply the migration before
restarting updated code against any intended deployment database. Review queued recipient scope
before any live restart. Phase B metadata retention and broader lifecycle/load certification remain
in B5, not claimed complete here.
