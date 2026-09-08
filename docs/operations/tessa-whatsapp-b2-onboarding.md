# Tessa WhatsApp B2 — onboarding integration

Implemented and locally verified on 2026-09-06. **Live acceptance is pending.** This completes
the local B2 implementation, not WhatsApp-to-Tessa AI conversations (B3/B4).

## Flow and scope

An unlinked text sender receives a welcome with CONNECT and CREATE choices. CONNECT presents
the channel/privacy notice; AGREE accepts the current notice and asks for a verified provider
account email. The email step always queues the same generic code prompt. Only an eligible,
allowlisted provider with that verified email receives a dedicated linking email. CREATE supplies
the existing provider website URL (`CLIENT_PUBLIC_BASE_URL`); no account is created in WhatsApp.

The code step supports RESEND and CONNECT to restart with another email. Incorrect codes use
the same generic response, including for unknown accounts. An email, guessed provider identity,
ordinary login code or AGREE outside the consent step grants no access. STOP closes even anonymous
onboarding and cancels pending replies/challenges; TESSA DISCONNECT also revokes an existing grant.

Code verification and dashboard-token verification both call the same grant-completion function.
Number replacement requires the new signed sender and account proof. Completion does not transfer
a different provider's active sender grant, change login identities or opt in to reminders. It
records the security event and linked reply in the same transaction. Existing web connection status
reports the actual grant; `conversation_available` remains false. Already-linked senders bypass
onboarding; ordinary questions are not retained or sent to a model at this stage.

## Persistence and delivery

- `tessa_whatsapp_onboarding` stores a bounded state per receiving phone/sender, notice revision,
  optional resolved provider/challenge reference and expiry. It does not retain entered email/code
  text. Raw control input stays transient, is capped at 320 bytes and never enters transcripts.
- The signature-validated receipt, state transition, optional encrypted email job, proof
  consumption, grant/security intent and appropriate control reply commit atomically. A failed
  final reply insert rolls all of them back, returning HTTP 503 for Meta retry. A duplicate receipt
  cannot increment wrong-code attempts or complete/link/send twice.
- Generic onboarding replies use anonymous, typed rows in the existing WhatsApp outbox, not a
  second sender. Database constraints keep them separate from account-authorized private control
  replies. The worker checks the current session revision/expiry before dispatch. Advancing,
  cancelling or replacing a session cancels its obsolete unsent prompts.
- Existing callback ownership, stable correlation IDs, bounded retries and ambiguous-send handling
  apply unchanged. A request already dispatched to Meta cannot be retracted; queued stale prompts
  are not dispatched later.
- Session inactivity expiry is 10 minutes; code expiry remains fixed at 10 minutes from issuance.
  Inbound onboarding older than 10 minutes, more than one minute in the future, or older than the
  session's last inbound timestamp is ignored. Replies are capped at 30 per sender/receiving-phone
  pair per hour and 600 globally per minute. These are initial rollout guardrails, not certified
  production throughput. Indexed DB counters and short transaction locks coordinate API replicas;
  no SMTP/Meta/model call occurs while those transactions are open.
- Eligibility does not change generic prompt text or bind those outbound prompts to a claimed
  account. Email issuance/attempt limits remain the dedicated service's separate limits. A sender
  that cannot obtain a code can restart with CONNECT or use web sign-in/dashboard linking.

## Configuration and verification

The existing `TESSA_WHATSAPP_LINKING_ENABLED`, verified Meta connector configuration and Tessa
provider allowlist still govern access. `AUTH_EMAIL_ENABLED` wires email onboarding in API and
core-worker processes; when unavailable, this journey does not start. Keep deployment API/worker
configuration aligned. No live env settings were changed or new fallback flags added.

Migration `20260906014000_tessa_whatsapp_onboarding.sql` and the schema snapshot cover state and
outbox extensions. It was applied only to isolated `tellbook_tessa_b2_certification`. Apply all
pending migrations before restarting updated code against the intended database.

Local race-enabled tests cover signed ingress, explicit consent, eligible/unknown/unverified/
non-allowlisted emails, SMTP acceptance, replay-safe wrong-code attempts, concurrent completion,
email-proven replacement, rollback at email and grant completion, STOP, stale/disabled/expired
anonymous sends, ambiguous no-resend behavior and reply budgets. Focused existing auth, notification
and WhatsApp regressions, Go vet and API compilation pass. All senders are in-process test doubles.

No live messages were sent; the main database, running API and live env files are unchanged.
Authorized live dashboard/email linking, SMTP acceptance and Meta sent/delivered callbacks remain
B1/B2 acceptance gates. Review pending queues/recipient scope before activation. B3 is next for
durable WhatsApp messages and shared Tessa context; B4 adds assistant replies/typing. Lifecycle
retention and broader load certification remain in B5.
