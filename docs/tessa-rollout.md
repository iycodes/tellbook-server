# Tessa rollout gates

Tessa remains disabled unless `TESSA_AI_ENABLED=true` and the authenticated provider UUID is in
`TESSA_AI_PROVIDER_ALLOWLIST`. Start with a small allowlist and expand it only after the gates below
pass against the exact model and worker concurrency intended for that environment.

## Automated gates

Run the deterministic suite first:

```sh
go test ./...
go test -race ./internal/tessa ./internal/appdata ./cmd/api
go vet ./...
go build ./...
make tessa-query-plans
```

Export the deployment configuration without printing it. Then run the local model fixtures:

```sh
make tessa-local-evals
make tessa-local-capacity
```

The conformance suite covers canonical tools, Nigerian English and Pidgin, date handling,
clarification, out-of-scope prompts, prompt exfiltration, untrusted tool evidence, and empty results.
The capacity gate defaults to six complete factual turns at concurrency two. Every measured turn
includes planning and synthesis and uses the running API's model, sampling, input/output, request
timeout, and total-turn settings. It fails above a 45-second p95. Set
`TESSA_LOCAL_CAPACITY_CONCURRENCY` to `TESSA_AI_WORKER_CONCURRENCY`; increase the turn count for a
longer soak. Record the model, hardware, turn count, concurrency, turn throughput, p50, p95, and
failures before changing the provider allowlist. Tool reads are covered separately by the database
plans and PostgreSQL integration suite.

Latest local baseline (2026-08-31): `gemma-4-E4B-it-UD-Q5_K_XL.gguf`, 6/6 complete turns,
concurrency 2, 69.816 seconds elapsed, 0.09 turns/second, p50 25.383 seconds, p95 30.846 seconds.
Re-run this gate on deployment hardware; this development result is not a substitute for that
environment's own measurement.

When an external fallback is selected, its credentials, approved-processing flag, notice revision,
and allowlist must match the intended deployment configuration. Run both suites below against every
model/version selected for fallback:

```sh
make tessa-external-evals
RUN_OPENAI_COMPAT_EVALS=true go test ./internal/ai -run TestExternalOpenAICompatibleConformance -count=1 -v
```

The Tessa-specific suite exercises its strict routing schema. The generic suite covers the other AI
workloads that share an OpenAI-compatible adapter. The hosted Tessa adapter is regression-tested to
ignore `OPENAI_RESPONSE_LOG_FILE`, so full assistant output is not written to that diagnostic file.

## Rollout decision

Do not expand the allowlist when any conformance request fails, the configured concurrency exceeds
measured full-turn capacity, database or SSE reset tests fail, or external-processing
approval/notice data is not current. Tessa requires an explicitly configured notice revision, a
self-hosted primary, and explicit approval before an external fallback can be enabled. A local-model
outage must degrade only Tessa: normal provider dashboard routes and non-AI booking operations must
remain available.
