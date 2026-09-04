# Database runtime and connection budget

Tellbook uses two PostgreSQL pools per process:

- `query` serves ordinary transactions and repository queries;
- `direct` is a small, zero-minimum pool reserved for session-bound `LISTEN` connections and the
  maintenance leader lock.

Set `DATABASE_URL` for the query pool. Set `DATABASE_DIRECT_URL` to a direct PostgreSQL endpoint.
It may be left blank in local development, where it resolves to `DATABASE_URL`. Never point the
direct URL at PgBouncer transaction mode.

## Per-process direct connection budget

| Process role | Direct connections |
| --- | ---: |
| `api` | 3, or 4 when Tessa is enabled |
| `worker` | 1 |
| `ai-worker` | 1 |
| `maintenance` | 1 |
| local `all` | 6, or 7 when Tessa is enabled |

The API connections are the payment, inbox, booking, and optional Tessa event listeners. Worker
roles each own one coalesced wake listener. Maintenance owns one leader-lock connection. Startup
rejects a `DATABASE_DIRECT_MAX_CONNECTIONS` value below the role's exact requirement.

The deployment budget is:

```text
(API replicas × API query max)
+ (worker replicas × worker query max)
+ (AI-worker replicas × AI-worker query max)
+ (maintenance replicas × maintenance query max)
+ all role-specific direct maxima
+ migration/admin reserve
< PostgreSQL max_connections
```

Keep the calculated peak below 80% of PostgreSQL `max_connections`; the remaining 20% is the
failure, migration, and administrative reserve. Do not choose replica counts and pool maxima
independently.

## Pool lifecycle

Both pools set an application name containing environment, process role, and pool role. They also
enforce bounded connect, statement, lock, and idle-transaction timeouts. Connection lifetime uses
jitter so replicas do not reconnect together. The query and direct pools are separately labelled
in `tellbook_db_pool_*` metrics; queue metrics run only from the query pool.

The query pool no longer performs `LISTEN` or session-level advisory locking. Payment
reconciliation and payout initiation use durable expiring leases, so provider HTTP calls do not
hold a PostgreSQL connection. Transaction-scoped advisory locks remain valid with transaction
pooling.

## PgBouncer gate

PgBouncer transaction mode is optional and must not be enabled until its staging integration
suite passes using the intended deployment URLs. When enabled:

1. point only `DATABASE_URL` at the transaction-pool endpoint;
2. keep `DATABASE_DIRECT_URL` on direct PostgreSQL or a session-pool endpoint;
3. verify every event and worker-wake listener reconnects after a forced disconnect;
4. verify payment, booking, inbox, Tessa, and maintenance integration suites;
5. confirm peak query-pool utilization stays below 70% and acquire-wait p95 stays below 10 ms.

The load-test and administrative CLI programs open their own short-lived direct connections and
are outside the long-running application replica budget.
