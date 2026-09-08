# Business customer contact

Server/API and client UI implemented 2026-09-05.

The contact belongs to `client_profiles`. Account identities and notification destinations stay
independent. No existing number or sharing permission is copied automatically.

Authenticated provider endpoints (all beneath `/v1/app/profile/customer-contact`):

| Method | Suffix | Behavior |
| --- | --- | --- |
| GET | none | Contact, verification, sharing flags, revision, and eligible verified numbers |
| PATCH | none | Save either sharing flag with the current `revision` |
| POST | `/reuse-verified` | Copy the explicitly selected `phone` using current `revision` |
| POST | `/verification` | Normalize and stage a different `phone` using current `revision`; return a prepared WhatsApp verification link |
| DELETE | none | Remove the contact using current `revision` |

`reuse-verified` accepts only the exact E.164 number currently verified in this provider's
notification preferences or phone identity. Reuse copies verification evidence; subsequent
account/notification number edits do not silently redirect customer contact.
Selecting the same already-verified contact is a no-op and preserves its sharing choices.

For a different number, the provider sends the prepared `VERIFY` message from that number to
Tellbook. The existing signed webhook checks the actual sender. Challenges expire after 15
minutes, permit five wrong-sender attempts, and are single-use. Replacement invalidates previous
challenges. Only token/destination hashes are retained in the challenge table. Bounded maintenance
removes expired records.

Both `allow_booking_contact` and `show_on_public_profile` default to false. Either requires a
verified number. Replacing/removing a number clears both flags. Verification alone does not
enable sharing. A stale `revision` receives 409; refresh GET before saving again. Start verification
and successful verification each advance the revision, so refresh after both operations.

Public `/v1/public/clients/{slug}` returns `profile.customer_contact_phone` only for explicit
public visibility. The public profile cache revision changes with the contact; previously cached
responses retain the existing 15-second browser / 30-second CDN freshness periods, each with a
60-second stale-revalidation window.
Booking contact is returned as `provider_contact_phone` only when booking sharing is enabled, in
the capability-token-protected public summary and the customer-owned marketplace booking detail.
These two choices are independent; a public-only contact does not opt into booking reminders.
Customer booking emails include the same shared contact when available; otherwise the contact
line is omitted without blocking email delivery.

`user_reminder` body parameter 6 is now defined as `provider_contact_phone`: the verified business
contact shared with booked customers. Planning, dispatch authorization, and final rendering check
this permission. Changes affecting booking contact enqueue the existing bounded scope replan;
public-only changes do not scan or replan bookings. Already accepted messages cannot be recalled,
and a sharing change after the final send check can race with a send already in progress.

Guest reminder buttons use `/bookings#claim=<opaque token>`; owned bookings use `?booking=<UUID>`.
The template registry preserves Meta's existing body and button URL. The contract hold is removed,
but deployment notification/template flags still require explicit rollout and recipient checks.
No flags are enabled by this migration.

Provider APIs are documented in `contracts/provider-notifications.openapi.yaml`. Public and
marketplace booking contracts include the gated response fields.

## Provider and customer UI

Provider **Settings → Business Profile → Customer contact** offers verified-number reuse,
WhatsApp verification, independent sharing switches, removal, and an explicit refresh/check button.
The settings server load reads contact and notification preferences in parallel with independent
errors. The profile slide remains lazy-loaded and restores after reload; verification links stay
in memory rather than browser persistence. After sending the prepared message, use **Check
verification**. After a reload, the latest verification state is loaded from the server; start a
new verification only if another link is needed.

Contact operations are single-flight, abort on unmount, and use the current revision. Sharing
changes appear only after server acknowledgement. Conflicts reload state without replaying the
mutation. No polling or additional public-page requests were added. Contact saves are independent
of the existing Save Profile button.

The provider's public profile and marketplace profile render a call link only when the public
API supplies `customer_contact_phone`. Guest booking confirmations in both apps and customer-owned
marketplace booking details use only `provider_contact_phone`. They never fall back to account
or notification numbers.
