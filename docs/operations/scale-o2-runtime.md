# O2 realtime and process runtime contract

This contract is part of the deployed capacity, not a best-effort tuning note.

## Process ownership

Run separate deployments with `PROCESS_ROLE=api`, `worker`, `ai-worker`, and
`maintenance`. `PROCESS_ROLE=all` is for local development and is rejected when
`APP_ENV=production`.

- `api`: HTTP routes plus payment, inbox, booking, and enabled Tessa LISTEN loops.
- `worker`: durable agreement, payment, webhook, allocation, and refund work.
- `ai-worker`: Tessa, provider-requested inbox drafts, and inbox automation model work only.
- `maintenance`: one replica. It owns the PostgreSQL advisory leader lock and runs
  template synchronization, retention, expiry, telemetry, and repair/reconciliation sweeps.

Every role exposes health, readiness, and metrics HTTP endpoints. Non-API roles do
not mount customer/application routes. A maintenance process that cannot own or
retain its dedicated leader-lock session is not ready and exits.

## Realtime capacity

The default per-API-instance admission budget is 10,000 concurrent SSE streams,
40 streams per remote IP, and 6 public streams for one payment token. Change these
only with matching infrastructure capacity:

- `SSE_MAX_CONNECTIONS=10000`
- `SSE_MAX_CONNECTIONS_PER_IP=40`
- `PAYMENT_SSE_MAX_CONNECTIONS_PER_TOKEN=6`

For a 10,000-stream API instance, set the process open-file limit to at least
25,000 (client sockets, listener/database/outbound sockets, logs, and headroom).
Set the pod/container file limit to the same or higher value. Establish the memory
request from the 10,000-idle-stream load run plus at least 30% headroom; do not
copy an unmeasured per-connection estimate into production.

Ingress/proxy requirements:

- disable response buffering for `text/event-stream` (`X-Accel-Buffering: no` is
  also emitted by the API);
- keep the upstream read/idle timeout above 75 seconds (heartbeats are every 25 seconds);
- preserve `Last-Event-ID`, cookies, and streaming flushes;
- allow at least the declared stream count and matching upstream connections;
- stop new connections before terminating an API pod and allow at least 15 seconds
  for drain. Server shutdown cancels every request base context, so streams release
  their admission slots immediately and browsers reconnect to another replica.

Idle SSE heartbeats are comments and perform no database read. Durable cursor
drains happen on connection, database notification, and listener reconnect. Every
stream has a 30-minute maximum age so stale infrastructure state is periodically
re-established without synchronized browser polling.

The generated large local/staging fixture includes `idle-sse.json`, a no-write,
no-read-probe scenario for up to 10,000 distinct authenticated streams. Run it only
on an environment whose file-descriptor, memory, ingress, and database budgets were
provisioned for that target; store the report and matching metrics before claiming
the 10,000-stream acceptance result.

## Worker wake and correctness

PostgreSQL queue triggers notify the `core` or `ai` worker group after commit.
Each local subscriber has a one-item wake buffer, so bursts coalesce. Workers use
15–30 second safety polling for a lost notification; row leases, fencing,
idempotency, and `FOR UPDATE SKIP LOCKED` remain the correctness mechanism.

Payment reconciliation is a single durable `financial_jobs` row per payment.
Webhooks remain primary. Worker claims are fair across providers; a short row-locked
reservation persists both the next-call time and a crash-expiring provider lease.
This enforces cluster-wide provider rate and concurrency admission without holding
a database connection during the provider request.
Claimed jobs run in a bounded batch. Jobs use jittered age-based backoff and become
`dead_letter` after the bounded attempt/age policy instead of retrying forever.

Provider-requested inbox drafts are accepted as durable `inbox_ai_runs` jobs and
return `202` immediately. The AI worker owns inference; API processes only build
the bounded snapshot and enqueue it. The browser checks the authoritative run
status with visibility-aware exponential backoff. Each provider has a bounded
manual-draft backlog, and only one manual draft per provider is processed at a
time. Inference capacity is process-local and shared by Tessa and inbox work; no
database connection is held while a model is running.
