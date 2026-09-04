# AI provider operations

Tellbook supports three explicit server-side AI adapters:

- `self_hosted` for the existing llama-server contract;
- `hosted` for the official OpenAI Responses API contract;
- `openai_compatible` for one external OpenAI-compatible Chat Completions endpoint.

`DEFAULT_AI_PROVIDER` controls inbox drafts, semi-pilot, autopilot, service and section
descriptions, preparation/aftercare instructions, and public-page content.
`AGREEMENT_AI_PROVIDER` independently controls agreement generation.

## External endpoint contract

The external endpoint must accept `POST /v1/chat/completions` (or the explicitly configured
path), Bearer authentication, non-streaming system/user messages, and strict nested
`response_format.type=json_schema` output. It must return one assistant choice with string
content, a `stop` finish reason, and JSON matching the supplied schema.

Tellbook sends exactly one configured token limit field: `max_tokens` or
`max_completion_tokens`. Temperature and `top_p` are both optional and cannot be configured
together. Llama-specific fields, Responses API fields, arbitrary request fields, and arbitrary
headers are never sent by this adapter.

## Configuration

```env
DEFAULT_AI_PROVIDER=openai_compatible
# AGREEMENT_AI_PROVIDER=openai_compatible

OPENAI_COMPAT_BASE_URL=https://models.example.com
OPENAI_COMPAT_CHAT_COMPLETIONS_PATH=/v1/chat/completions
OPENAI_COMPAT_MODEL=model-name
OPENAI_COMPAT_API_KEY=replace-with-provider-key
OPENAI_COMPAT_TIMEOUT=60s
OPENAI_COMPAT_MAX_OUTPUT_TOKENS=1600
OPENAI_COMPAT_TOKEN_LIMIT_FIELD=max_tokens
OPENAI_COMPAT_TEMPERATURE=
OPENAI_COMPAT_TOP_P=
```

The API key is required whenever either route selects the external adapter. Non-loopback
endpoints must use HTTPS. Credentials must be injected as server environment configuration and
must not be written to source control.

## Failure and retry behavior

The adapter makes one HTTP request per inference attempt and has no internal retry loop.
Tellbook validates returned JSON against the exact sent schema locally, without remote schema
retrieval, before strict Go decoding and task-specific semantic validation.

- malformed, schema-invalid, or protocol-invalid completed model output may receive one model
  repair request from the semi-pilot/autopilot decision service;
- the durable automation worker retries typed transport failures, provider timeouts, `429`, and
  transient `5xx` responses within its existing attempt limit and backoff;
- authentication, permission, configuration, refusal/filtering, truncation, and output that
  remains invalid are terminal and produce the existing safe handoff;
- synchronous routes return their existing unavailable response and do not retry automatically.

Normal logs contain only the adapter, configured model, duration, safe outcome, retryability,
and token counts when supplied. Provider response bodies, prompts, customer data, and API keys
must never be logged.

## Conformance and rollout

Run unit and integration coverage first:

```sh
go test ./internal/config ./internal/llm ./internal/ai ./internal/appdata
```

The live conformance suite is opt-in and performs real, billable inference. Export the external
configuration above, then run:

```sh
RUN_OPENAI_COMPAT_EVALS=true go test ./internal/ai -run TestExternalOpenAICompatibleConformance -count=1 -v
```

The suite exercises inbox drafts, both automation decision protocols, every current content
generation task, and agreement generation. Do not select the endpoint for production until all
results pass both local JSON Schema validation and existing application semantic validation.

Baseline verification: all cases passed against OpenAI Chat Completions with `gpt-5.6-luna` on
2026-08-29. Run the suite again for each external endpoint and model selected for deployment.

After conformance, select the external default route with inbox automation still disabled.
Verify synchronous generation, then enable inbox drafts for an explicit provider allowlist.
Enable semi-pilot next, and enable autopilot only after the existing booking safety and worker
integration suites pass for the same model and configuration hash.
