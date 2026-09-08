# Data retention and maintenance

Only the `maintenance` process role runs destructive retention. It owns the singleton maintenance
leader lock, deletes in ordered `FOR UPDATE SKIP LOCKED` batches, and performs at most four batches
per table every 15 minutes. This bounds locks, WAL generation, and replica lag.

| Data | Owner | Retention | Batch | Restore / audit rule |
| --- | --- | ---: | ---: | --- |
| Resolved visitor locations | Marketplace location | Until `expires_at` | 1,000 | Ephemeral privacy-sensitive lookup; recreate from a new user request |
| Provider refresh sessions | Provider auth | Until `expires_at` | 1,000 | Re-authentication creates a new session |
| Provider and marketplace auth challenges | Authentication | 24 hours after expiry | 1,000 | Delay supports short operational diagnosis; never restore for reuse |
| Auth password-reset grants | Authentication | Until `expires_at` | 1,000 | Single-use ephemeral credentials; never restore expired grants |
| Business customer-contact verification challenges | Provider profile | Until `expires_at` | 1,000 | Hashed single-use proof; request a new challenge after expiry |
| Marketplace sessions | Marketplace auth | Until `expires_at` | 1,000 | Re-authentication creates a new session |
| Unconsumed booking and booking-change quotes | Booking | 24 hours after expiry | 1,000 | Only unreferenced quotes are removed; consumed/referenced snapshots remain |
| Inbox events | Inbox realtime | 30 days | 5,000 | Messages/conversations are canonical; reconnect outside the window performs a reset |
| Tessa events | Tessa realtime | 30 days | 5,000 | Threads/messages are canonical; reconnect outside the window performs a reset |
| Archived Tessa threads and their dependent data | Tessa | 90 days after archive | 5,000 | User-directed archive lifecycle; restore only from database backup if policy permits |
| Tessa WhatsApp delivery diagnostics | Tessa transport | 90 days after the last status change | 1,000 | Accepted/sent/delivered/read/failed/cancelled/expired/manual-review only; pending, leased, retrying and unresolved dispatches remain protected |
| Terminal Tessa WhatsApp ingress | Tessa transport | 30 days after completion | 1,000 | Keep while an admitted run is queued/processing or its answer/control outbox remains; canonical messages are not deleted |
| Tessa linking/email challenges and onboarding state | Tessa connection | 24 hours after expiry | 1,000 | Only after dependent reply, email and onboarding records are gone; hashes cannot be restored for reuse |
| Tessa connection security events | Tessa connection | 90 days after creation | 1,000 | Email deadline must have passed and dependent email jobs must be gone |
| Terminal Meta webhook receipts | WhatsApp transport | 30 days after processing | 1,000 | Referenced Tessa receipts remain until their delivery/ingress/email-challenge dependencies are removed |
| Inbox AI runs and terminal turn jobs | Inbox AI | 90 days | 1,000 | Conversation messages and committed actions remain canonical |
| Completed agreement delivery jobs | Agreements | 30 days | 1,000 | Agreement/version/document records remain canonical |
| Terminal agreement-generation jobs | Agreements AI | 90 days | 1,000 | Generated template versions remain canonical |
| Completed/cancelled financial dispatch jobs | Payments | 30 days | 1,000 | Ledger, allocations, provider events and settlement evidence are retained |
| Provider daily metrics and stale projection jobs | Analytics | 400 calendar days | 1,000 | Rebuildable from retained canonical booking/payment records |

The following records are explicitly excluded from generic deletion: bookings and booking-domain
events, inbox messages/conversations, agreements and agreement instances, payments, payouts,
payment adjustments, allocations, balance entries, provider webhook events, payment exceptions,
settlement evidence, and other financial ledger records. Their legal retention policy must be
approved separately before any archival or deletion implementation.

Expired auth cleanup does not run from login or challenge requests. This prevents every API
replica from independently issuing cleanup writes under user traffic.

Tessa transport cleanup runs child-first in the existing maintenance worker. Retaining a source
receipt must not depend on a cascading foreign key preserving its children: explicit reference
guards protect queued work and the longer delivery-diagnostic window. Removing transport diagnostics
does not remove an active thread's messages, or invent a delivered/failed state: old messages then
omit optional `whatsapp_delivery` metadata. A currently open slide may retain its last observed label
until bootstrap/reload; retention emits no fake delivery transition. Archived-thread retention still
removes dependent messages and transport records as part of the explicit archive lifecycle.

The single latest connection row remains with the provider account, including its monotonically
increasing revision, until account deletion. It is not deleted and recreated by retention, which
could otherwise reuse old connection authority. Runtime checks reject expired/revoked grants;
retention never reactivates them. Unresolved work is not silently discarded by age-based cleanup.
Disconnect does not erase messages already delivered to WhatsApp.

Autovacuum thresholds are lowered only for measured high-churn queue/event/auth/location tables.
Review dead tuples, vacuum duration, WAL volume, and table growth in staging before changing these
values. Do not add partitioning or a read replica until those measurements show that bounded
maintenance and existing indexes cannot meet the target.
