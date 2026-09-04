# Native inbox operations

## AI funnel and reliability metrics

Run the read-only durable-data report for the last 24 hours:

```sh
make inbox-ai-metrics
```

Use `WINDOW='7 days'` to select a different PostgreSQL interval. The report derives link delivery and conversion, proposal conversion, reservation success/expiry, handoff/provider takeover, inference latency, and worker retry/failure counts from committed inbox records. It does not add counters or queries to the message hot path. The report uses a 15-second statement timeout and should be scheduled on a read replica once production data volume warrants it. Process-local telemetry still reports expiry attempts that were deferred because a payment provider was uncertain; the durable report intentionally counts only committed outcomes.

## Query-plan inspection

Run the read-only plan script against a representative database, preferably staging after production-scale seed data is loaded:

```sh
make inbox-query-plans
```

`DATABASE_URL` must already be exported. The script opens a read-only transaction, caps each statement at 10 seconds, prints the relevant index definitions, and runs `EXPLAIN ANALYZE` for conversation paging, message paging, participant state, and both actor event drains. A small development database may legitimately choose sequential scans; judge production readiness using representative row counts, buffer reads, sort nodes, and execution time.

## Operational moderation

Disable or re-enable a conversation without exposing a public moderation route:

```sh
make inbox-disable CONVERSATION_ID=<uuid> REASON='abuse investigation' OPERATOR='ops@example.com'
make inbox-enable CONVERSATION_ID=<uuid> REASON='investigation complete' OPERATOR='ops@example.com'
```

Every actual state change creates a durable moderation audit row and an inbox event. Replaying the same state is a no-op and does not duplicate the audit event.

## Authenticated load measurement

Copy `scripts/inbox-load.example.json` outside source control, point it at a non-production environment, and create its NDJSON identity file. Each line represents a distinct signed-in actor:

```json
{"actor_type":"provider","headers":{"Cookie":"booking_access=REDACTED"},"conversation_id":"00000000-0000-0000-0000-000000000000"}
{"actor_type":"marketplace_customer","headers":{"Cookie":"tellbook_marketplace_session=REDACTED"},"conversation_id":"00000000-0000-0000-0000-000000000000"}
```

Never commit that identity file. Run SSE-only measurement with:

```sh
node scripts/inbox-load.mjs --config /secure/path/inbox-load.json --out /secure/path/inbox-load-report.json
```

Message load writes real messages and therefore requires both disposable staging conversations and explicit acknowledgement:

```sh
node scripts/inbox-load.mjs --config /secure/path/inbox-load.json --confirm-write --out /secure/path/inbox-load-report.json
```

The server allows at most three inbox streams per actor and, by default, forty total realtime streams per source IP. Use distinct test actors, stay within three streams per identity, and run through the same trusted proxy topology as production. For 50 messages/second, distribute writes across enough actors and conversations to exercise throughput rather than intentionally hitting actor/conversation command limits.

Set `TRUSTED_PROXY_CIDRS` to the immediate reverse-proxy/load-balancer networks that are permitted to supply forwarded client addresses. Keep the Go origin private to that proxy topology and never use a public catch-all CIDR. Requests from any other socket peer have forwarded-IP headers discarded before rate limiting, authentication auditing, and inbox stream accounting.

Set `reconnect_each_stream` to `true` to close and reopen every stream once. When the initial connection ramp is 60 seconds, those reconnects are distributed across the same 60-second shape. Set `read_probe_rate_per_second` to sample authenticated list, detail, and unread-count latency alongside the write load.

An identity may set `"streams": 1`, `2`, or `3` to override `streams_per_identity`; this is useful for a deliberately hand-authored scenario. Generated profiles do not add per-identity overrides: smoke uses 20 identities, the mixed read/write target caps execution at 1,000 identities, and `idle-sse.json` permits up to 10,000 identities with one stream each. Provider seed size controls data volume separately from concurrent sockets.

The runner reports connection status codes, reconnects, connect percentiles, unexpected stream closes, SSE event/reset counts, message and read status codes, latency percentiles, and shed requests. It does not label a run as passed: save the report alongside server telemetry and database/pool observations, then record the measured environment and acceptance decision in the implementation plan. Do not claim the 1,000-stream mixed target, 10,000-stream idle target, or 50-message/second target until that exact representative run has been completed.

`ramp_seconds` distributes stream connection attempts; `duration_seconds` is the measurement hold after that ramp completes. Message generation starts after the connection ramp and runs for the full hold duration.

## Disposable local scale fixtures

For loopback-only engineering runs, generate the representative dataset and private runner inputs with:

```sh
go run ./cmd/inbox-load-seed --confirm-local \
  --conversations=10000 \
  --messages-per-conversation=10 \
  --actors=334 \
  --output-dir=/tmp/tellbook-inbox-load
```

The command refuses production mode and non-loopback database hosts. It deletes/replaces only dedicated `inbox-load-*@example.invalid` fixtures, refreshes the relevant PostgreSQL planner statistics, creates verified password identities for two-browser checks, and writes credentials/configuration with private filesystem permissions. Do not adapt its generated credentials for shared or production environments.

Run the generated tiers against a local HTTP API with explicit acknowledgement:

```sh
node scripts/inbox-load.mjs --config /tmp/tellbook-inbox-load/smoke.json \
  --allow-http --confirm-write --out /tmp/tellbook-inbox-load/smoke-report.json
node scripts/inbox-load.mjs --config /tmp/tellbook-inbox-load/target.json \
  --allow-http --confirm-write --out /tmp/tellbook-inbox-load/target-report.json
```
