# Data retention and maintenance

Only the `maintenance` process role runs destructive retention. It owns the singleton maintenance
leader lock, deletes in ordered `FOR UPDATE SKIP LOCKED` batches, and performs at most four batches
per table every 15 minutes. This bounds locks, WAL generation, and replica lag.

| Data | Owner | Retention | Batch | Restore / audit rule |
| --- | --- | ---: | ---: | --- |
| Resolved visitor locations | Marketplace location | Until `expires_at` | 1,000 | Ephemeral privacy-sensitive lookup; recreate from a new user request |
| Provider pending registrations and password-reset tokens | Provider auth | Until `expires_at` | 1,000 | Ephemeral credentials; never restore expired tokens |
| Provider refresh sessions | Provider auth | Until `expires_at` | 1,000 | Re-authentication creates a new session |
| Marketplace auth challenges | Marketplace auth | 24 hours after expiry | 1,000 | Delay supports short operational diagnosis; never restore for reuse |
| Marketplace sessions | Marketplace auth | Until `expires_at` | 1,000 | Re-authentication creates a new session |
| Unconsumed booking and booking-change quotes | Booking | 24 hours after expiry | 1,000 | Only unreferenced quotes are removed; consumed/referenced snapshots remain |
| Inbox events | Inbox realtime | 30 days | 5,000 | Messages/conversations are canonical; reconnect outside the window performs a reset |
| Tessa events | Tessa realtime | 30 days | 5,000 | Threads/messages are canonical; reconnect outside the window performs a reset |
| Archived Tessa threads and their dependent data | Tessa | 90 days after archive | 5,000 | User-directed archive lifecycle; restore only from database backup if policy permits |
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

Autovacuum thresholds are lowered only for measured high-churn queue/event/auth/location tables.
Review dead tuples, vacuum duration, WAL volume, and table growth in staging before changing these
values. Do not add partitioning or a read replica until those measurements show that bounded
maintenance and existing indexes cannot meet the target.
