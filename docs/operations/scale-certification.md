# Scale certification

Tellbook is not capacity-certified merely because the O1–O8 code is present. Certification is a
measurement against one named build and one documented staging topology. Repeat it whenever the
API/worker topology, database tier, Redis tier, ingress/CDN, model runtime, or critical query shape
changes materially.

## Required evidence

Start from `scripts/scale-certification.template.json`. Keep raw reports, before/after metrics,
query plans, browser traces, and failure-injection notes in one immutable artifact directory. Every
scenario in the template must name its evidence file or dashboard URL; the checker rejects blank
evidence and missing scenarios.

The minimum scenario set is:

| Scenario ID | Required coverage |
| --- | --- |
| `anonymous_marketplace` | Home, category, region/LGA, text, distance, and provider-profile reads |
| `marketplace_discovery_database` | Uncached discovery repository latency before CDN caching |
| `edge_cached_marketplace` | Warm anonymous responses from the target user region |
| `auth_and_account` | Session/account reads plus distributed identifier, actor, and IP limits |
| `provider_application` | Dashboard, bookings, customers, notifications, and stats |
| `booking_contention` | Quote/create/change/payment status, retries, idempotency, and capacity contention |
| `inbox_and_tessa` | Lists, large histories, sends, AI queue/worker completion, event delivery, and reconnect |
| `idle_sse_10000` | 10,000 established idle streams on the intended ingress/API topology |
| `controlled_event_burst` | Bounded event fan-out after streams are established |
| `worker_backlog_and_projection` | Queue age, drain rate, projection rebuild, and recovery |
| `cold_cache_and_stampede` | Empty caches and concurrent misses for the same hot keys |
| `redis_outage` | Bounded fallback behavior, recovery, and database protection |
| `listener_reconnect` | Direct PostgreSQL listener interruption and catch-up from durable cursors |
| `failed_replica` | One API or worker replica terminated during traffic |
| `soak` | Sustained target traffic with memory, pools, queues, and event lag observed |

Use the seeded scale profiles and capture utilities for the database, inbox, and SSE portions:

```bash
make scale-seed-baseline
make scale-scenarios SCALE_DIR=/tmp/tellbook-scale-baseline

make scale-seed-large
node scripts/inbox-load.mjs \
  --config /tmp/tellbook-scale-large/idle-sse.json \
  --allow-http \
  --out artifacts/performance/idle-sse.json
```

Run both production frontend gates against the exact build being certified:

```bash
cd ../tellbook-marketplace && pnpm check && pnpm test && pnpm bundle:check
cd ../tellbook-client && pnpm check && pnpm test && pnpm bundle:check
```

For each critical browser route, capture a production-mode navigation on a mobile and desktop
profile. Verify there is no hydration error, duplicate SSR/browser data request, unexpected layout
shift, or main-thread long task introduced by application code. Open, close, reload, and navigate
back through persisted conversations, categories, notifications, and Tessa. A closed or hidden
slide must have no timer, polling request, or live event connection in the network trace.

## Pass/fail gate

Copy the template into the artifact directory, replace all placeholders with measured values, and
run:

```bash
make scale-certify CERTIFICATION_REPORT=artifacts/performance/<run>/certification.json
```

The command fails unless every scenario and assertion is present and all initial gates pass:

- unexpected error rate below 0.1%, after explicitly identified intentional rejections;
- simple-read p95 below 300 ms and write p95 below 500 ms, excluding third-party payment/model
  latency;
- discovery database p95 below 150 ms and warm edge-cache p95 below 100 ms;
- database acquire-wait p95 below 10 ms and peak pool utilization below 70%;
- no duplicate side effects, database heartbeat reads from idle streams, sustained saturation,
  growing queue/event lag, or unbounded memory growth;
- both frontend bundle checks pass.

`requests_per_second` and `concurrent_active_users` in a passing report are the only capacity numbers
that may be published for that topology. Row counts, configured connection limits, and code review
are not capacity measurements.
