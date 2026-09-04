# Performance baseline operations

This document is the O1 runbook for measuring Tellbook before changing its runtime topology or
adding Redis. It applies to the current single-process API/worker deployment.

## PostgreSQL query statistics

The `20260831020000_scale_o1_customer_data.sql` migration creates `pg_stat_statements`. PostgreSQL
must also load it at process startup:

```conf
shared_preload_libraries = 'pg_stat_statements'
```

Preserve any libraries already in that setting, restart PostgreSQL, apply migrations, and verify:

```sql
SHOW shared_preload_libraries;
SELECT COUNT(*) FROM pg_stat_statements;
```

Postgres.app can add its own command-line preload library, which overrides `ALTER SYSTEM`. Add
`pg_stat_statements` alongside that existing value in the server's startup options rather than
replacing it. Managed PostgreSQL must expose the extension and preload it through the provider's
parameter-group mechanism.

## Health, readiness, and metrics

- `GET /v1/healthz` is process liveness and never queries the database.
- `GET /v1/readyz` checks the database with a two-second deadline. The current monolith also
  reports that configuration and workers finished initialization before the listener was created,
  and reports maintenance ownership as `embedded_monolith`. Runtime-role isolation and elected
  maintenance ownership belong to O2. Redis is reported as `degraded` without failing readiness,
  because bounded PostgreSQL/local fallbacks keep non-sensitive traffic available.
- `GET /internal/metrics` exposes OpenMetrics only when `METRICS_AUTH_TOKEN` is configured and the
  request supplies the same value as a Bearer token. With no token configured, the route returns
  404. Do not expose this endpoint through the public marketplace proxy.

Relevant settings:

```dotenv
METRICS_AUTH_TOKEN=<long-random-internal-token>
HTTP_SUCCESS_LOG_SAMPLE_RATE=0.1
HTTP_SLOW_REQUEST_THRESHOLD=750ms
```

The metrics use bounded labels only. They cover normalized HTTP routes, response size, database
query operation and pool wait/saturation, external HTTP service/outcome, SSE connection lifetime
and event lag, inbox stream counters, and the fixed set of durable queues. Raw URLs, customer IDs,
provider IDs, booking IDs, and public tokens are not labels. Successful ordinary requests are
sampled; errors and slow ordinary requests are always logged. Successful SSE lifetimes are kept in
the dedicated stream metrics instead of producing one slow-request log per connection.

## Deterministic local scale data

Every profile refuses non-loopback databases, requires `--confirm-local`, and refuses
`APP_ENV=production`. Rerunning a profile replaces only the dedicated `inbox-load-*` synthetic
records and rebuilds planner statistics.

```bash
make scale-seed-smoke
make scale-seed-baseline
make scale-seed-large
```

The profiles progressively create 100, 5,000, and 25,000 marketplace-visible providers, plus
services, locations, availability, customers, bookings, approved reviews, saved providers,
notifications, conversations, messages, and durable events. Login/session artifacts are written
under `/tmp/tellbook-scale-*`; they are development credentials and must not be committed. Data
volume and concurrent sockets are deliberately separate: the generated mixed target uses at most
1,000 identities, while `idle-sse.json` uses up to 10,000 identities with one stream each for the
O2 connection-capacity measurement.

## Query-plan and top-SQL capture

With the API and embedded workers running locally, execute the bounded API/write/SSE scenario and
capture readiness, queue/worker metrics, production query plans, and top SQL in one command:

```bash
make scale-scenarios
```

Set `SCALE_DIR=/tmp/tellbook-scale-large` to use another generated profile and
`SCALE_API_ORIGIN` when the local API does not use `http://127.0.0.1:8200`. `DATABASE_URL` and
`METRICS_AUTH_TOKEN` are required. The command writes `readiness.json`, before/after OpenMetrics,
the load report, query plans, top SQL, and environment metadata into one timestamped artifact
directory. The queue metrics capture the real embedded workers; the scenario does not create fake
worker-only records.

To capture database plans without running the HTTP scenario, run:

```bash
make scale-query-plans
```

This writes a timestamped, ignored directory under `artifacts/performance/` containing:

- `query-plans.txt`: `EXPLAIN (ANALYZE, BUFFERS, WAL, SETTINGS)` for the production repository
  query shapes used by discovery, Saved, Notifications, customer bookings, provider dashboard,
  and durable event drain;
- `top-sql.txt`: the top statements by total execution time with calls, mean time, and rows;
- `environment.txt`: capture time, Go/PostgreSQL versions, and database size.

Capture one directory before and one after every optimization slice. Compare plan shape, actual
rows versus estimates, buffer reads, execution time, calls, and total execution time. Do not reset
`pg_stat_statements` unless both comparison runs use the same reset procedure and workload window.

## SvelteKit bundle budgets

Each application builds production output, resolves the dependency graph for its critical initial
routes from the SvelteKit and Vite manifests, writes an ignored JSON report, and fails when a
checked-in route-specific JavaScript, CSS, or manifest-linked image budget is exceeded:

```bash
cd ../tellbook-marketplace && pnpm bundle:check
cd ../tellbook-client && pnpm bundle:check
```

Reports are written to `artifacts/bundle-report.json`. The global largest-JavaScript-chunk and
total-generated-CSS ceilings remain as secondary guards. Files under `static/` and API/CDN image
responses are not present in Vite's route graph, so their transfer sizes remain browser-test gates;
manifest-linked images are enforced separately from compressed JS and CSS. Both `bundle:check`
commands must be required pull-request checks in the deployment CI rather than optional reports.

The final staging procedure and the report format that prevents an unmeasured capacity claim are
defined in `docs/operations/scale-certification.md`.
