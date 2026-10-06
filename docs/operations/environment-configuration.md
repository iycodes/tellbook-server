# Environment configuration

Keep `.env` and deployment environment files focused on credentials, addresses,
feature rollout switches, and deliberate overrides. `.env.example` is the starting
point; it does not need to repeat every supported default. Existing override names
remain supported. The defaults and validation in
[`internal/config/config.go`](../../internal/config/config.go) are authoritative.

The server loads `.env` without replacing variables already exported by its process.
`server.env` is an operator-managed deployment file, not automatically loaded by the
application. Do not commit either file or copy their credentials into documentation.

Keep authentication, booking notification, welcome, and additional-email gates
separate: they control different delivery paths. Keep payment capability selections, AI processing consent, and encryption/HMAC
keys explicit and independent.
Removing duplicated tuning must not change these settings or enable delivery.

`INBOX_AI_AUTOMATION_PROVIDER_ALLOWLIST` accepts `all` to make Semi-pilot and
Autopilot available to every authenticated provider, or a comma-separated list
of provider account UUIDs to restrict access. `all` is case-insensitive and must
be used by itself. `INBOX_AI_AUTOMATION_ENABLED=true` still requires one of these
explicit selections; when the switch is false, neither selection enables automation.
Provider policies, enabled services, pauses, and conversation controls continue
to determine whether a particular conversation uses an automated mode.

The anonymous support form has its own `SUPPORT_EMAIL` destination and sends on
the API role using the existing SMTP settings. A blank destination leaves the
page readable and the form unavailable. See [public support configuration](public-support.md)
for SMTP, origin, trusted proxy, and delivery requirements.

## Optional tuning

Set these only when intentionally overriding the defaults. Defaults below describe
the current implementation; review changes to config.go when upgrading. Deployment
addresses, credentials, model selection, and non-default tuning stay in environment
configuration even when a code default exists.

| Variable | Default |
| --- | --- |
| `AI_RATE_LIMIT_BURST` | `4` |
| `AI_RATE_LIMIT_PER_MINUTE` | `12` |
| `AUTH_ACCESS_COOKIE_NAME` | `booking_access` |
| `AUTH_ACCESS_TOKEN_TTL` | `15m` |
| `AUTH_BCRYPT_COST` | `12` |
| `AUTH_DELIVERY_CONCURRENCY` | `4` |
| `AUTH_DELIVERY_TIMEOUT` | `30s` |
| `AUTH_ISSUER` | `booking-api` |
| `AUTH_REFRESH_COOKIE_NAME` | `booking_refresh` |
| `AUTH_REFRESH_TOKEN_TTL` | `720h` |
| `DATABASE_CONNECT_TIMEOUT` | `5s` |
| `DATABASE_DIRECT_MAX_CONNECTIONS` | `role-dependent; see below` |
| `DATABASE_HEALTH_CHECK_PERIOD` | `30s` |
| `DATABASE_IDLE_TRANSACTION_TIMEOUT` | `30s` |
| `DATABASE_LOCK_TIMEOUT` | `5s` |
| `DATABASE_MAX_CONNECTIONS` | `14` |
| `DATABASE_MAX_CONNECTION_IDLE_TIME` | `5m` |
| `DATABASE_MAX_CONNECTION_LIFETIME` | `30m` |
| `DATABASE_MAX_CONNECTION_LIFETIME_JITTER` | `5m` |
| `DATABASE_MIN_CONNECTIONS` | `2` |
| `DATABASE_STATEMENT_TIMEOUT` | `30s` |
| `HOSTED_AI_PROVIDER` | `openai` |
| `HTTP_IDLE_TIMEOUT` | `60s` |
| `HTTP_RATE_LIMIT_BURST` | `100` |
| `HTTP_RATE_LIMIT_PER_MINUTE` | `300` |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` |
| `HTTP_READ_TIMEOUT` | `15s` |
| `HTTP_SHUTDOWN_TIMEOUT` | `10s` |
| `HTTP_SLOW_REQUEST_THRESHOLD` | `750ms` |
| `HTTP_SUCCESS_LOG_SAMPLE_RATE` | `0.1` |
| `HTTP_WRITE_TIMEOUT` | `15s` |
| `INBOX_AI_AUTOPILOT_PAYMENT_WINDOW` | `30m` |
| `INBOX_AI_MAX_CONCURRENCY` | `2` |
| `INBOX_AI_SEMI_PILOT_REPLY_DELAY` | `800ms` |
| `LLM_CHAT_COMPLETIONS_PATH` | `/v1/chat/completions` |
| `LLM_MAX_OUTPUT_TOKENS` | `1200` |
| `LLM_TEMPERATURE` | `0.2` |
| `LLM_TIMEOUT` | `30s` |
| `LOCATION_RATE_LIMIT_BURST` | `5` |
| `LOCATION_RATE_LIMIT_PER_MINUTE` | `20` |
| `MARKETPLACE_AUTH_RATE_LIMIT_BURST` | `6` |
| `MARKETPLACE_AUTH_RATE_LIMIT_PER_MINUTE` | `20` |
| `MIN_P` | `0.1` |
| `NOTIFICATION_EMAIL_CONCURRENCY` | `4` |
| `NOTIFICATION_EMAIL_TIMEOUT` | `30s` |
| `NOTIFICATION_PLANNER_CONCURRENCY` | `4` |
| `OPENAI_COMPAT_CHAT_COMPLETIONS_PATH` | `/v1/chat/completions` |
| `OPENAI_COMPAT_MAX_OUTPUT_TOKENS` | `1600` |
| `OPENAI_COMPAT_TEMPERATURE` | unset |
| `OPENAI_COMPAT_TIMEOUT` | `60s` |
| `OPENAI_COMPAT_TOKEN_LIMIT_FIELD` | `max_tokens` |
| `OPENAI_COMPAT_TOP_P` | unset |
| `OPENAI_MAX_OUTPUT_TOKENS` | `16000` |
| `OPENAI_TIMEOUT` | `120s` |
| `PAYMENT_SSE_MAX_CONNECTIONS_PER_TOKEN` | `6` |
| `PRESENCE_PENALTY` | `0` |
| `RATE_LIMIT_IP_CEILING_MULTIPLIER` | `8` |
| `REDIS_DIAL_TIMEOUT` | `750ms` |
| `REDIS_FALLBACK_MAX_CONCURRENCY` | `32` |
| `REDIS_KEY_PREFIX` | `tellbook:<APP_ENV>:v1` |
| `REDIS_MAX_PAYLOAD_BYTES` | `65536` |
| `REDIS_MIN_IDLE_CONNECTIONS` | `4` |
| `REDIS_POOL_SIZE` | `32` |
| `REDIS_POOL_TIMEOUT` | `500ms` |
| `REDIS_READ_TIMEOUT` | `250ms` |
| `REDIS_WRITE_TIMEOUT` | `250ms` |
| `REPETITION_PENALTY` | `1.0` |
| `SELF_HOSTED_THINKING` | `false` |
| `SMTP_CONNECT_TIMEOUT` | `10s` |
| `SMTP_INSECURE_SKIP_VERIFY` | `false` |
| `SSE_MAX_CONNECTIONS` | `10000` |
| `SSE_MAX_CONNECTIONS_PER_IP` | `40` |
| `TESSA_AI_FALLBACK_REQUEST_TIMEOUT` | `20s` |
| `TESSA_AI_MAX_INPUT_TOKENS` | `12000` |
| `TESSA_AI_MAX_OUTPUT_TOKENS` | `1600` |
| `TESSA_AI_PRIMARY_REQUEST_TIMEOUT` | `30s` |
| `TESSA_AI_TURN_TIMEOUT` | `75s` |
| `TESSA_AI_WORKER_CONCURRENCY` | `2` |
| `TOP_K` | `40` |
| `TOP_P` | `0.9` |
| `WELCOME_EMAIL_CONCURRENCY` | `2` |
| `WELCOME_EMAIL_TIMEOUT` | `30s` |
| `WHATSAPP_GRAPH_BASE_URL` | `https://graph.facebook.com` |
| `WHATSAPP_HTTP_TIMEOUT` | `15s` |
| `WHATSAPP_WORKER_CONCURRENCY` | `4` |

`DATABASE_DIRECT_MAX_CONNECTIONS` is derived from the process role: API uses 3
connections (4 with Tessa), worker/AI worker/maintenance use 1, and local `all`
uses 6 (7 with Tessa). Omit it unless deliberately allocating more capacity.
`DATABASE_DIRECT_URL` falls back to `DATABASE_URL` locally but is required in
production; it must connect directly to PostgreSQL, not a transaction-mode pooler.

`REDIS_KEY_PREFIX` defaults to `tellbook:<APP_ENV>:v1`. Keep an explicit override
when multiple deployments in the same environment share Redis.

SSE limits are per API instance; provision file descriptors and memory for the
connection budget plus headroom. `INBOX_AI_MAX_CONCURRENCY` is the per-process
AI-worker budget shared by Tessa and inbox model jobs.

For an OpenAI-compatible endpoint, `OPENAI_COMPAT_TOKEN_LIMIT_FIELD` accepts
`max_tokens` or `max_completion_tokens`. Leave both sampling overrides unset to
omit them; configure at most one of temperature and top-p.

## Test-only inputs

Supply these through the environment of the specific test invocation, rather than
storing them in API/worker deployment files. No certification run or real send is
part of environment cleanup.

| Variable | Use |
| --- | --- |
| `PAYMENT_CERTIFICATION_NGN_ACCOUNT_NUMBER` | Operator-controlled 10-digit account for explicit payment certification. |
| `PAYMENT_CERTIFICATION_MAX_AMOUNT_MINOR` | Required explicit value `10000` for financial certification; this is a guard, not a runtime default. |
| `TESSA_LOCAL_CAPACITY_CONCURRENCY` | Defaults to the configured Tessa worker concurrency. |
| `TESSA_LOCAL_CAPACITY_REQUESTS` | Defaults to three times the capacity-test concurrency. |
| `TESSA_LOCAL_CAPACITY_MAX_P95` | Defaults to `45s`. |

Payment certification also requires the suite's existing opt-in and run-ID controls;
setting these inputs alone does not authorize or start a test. See the provider
integration tests in `internal/payments/payaza` and `internal/payments/paystack`.
For model capacity checks, see [Tessa rollout](../tessa-rollout.md).

## Retired entries

`PAYSTACK_PUBLIC_KEY` and `PAYSTACK_PUBLIC_KEY_TEST` have no backend consumer.
`TESSA_AI_PROVIDER_ALLOWLIST` is retired and ignored. Remove these from backend
configuration; do not confuse them with the active inbox AI allowlists.

## Payment capabilities

Use one list per provider, scoped to the deployment's `PAYMENTS_ENVIRONMENT`:

```dotenv
PAYMENTS_ENVIRONMENT=test
# Leave blank until selecting the operations this deployment should offer.
PAYSTACK_ENABLED_CAPABILITIES=
PAYAZA_ENABLED_CAPABILITIES=
```

Supported entries are `card`, `bank_transfer`, `destination`, and `payout`.
For example, `card,destination` enables card collection and bank-account lookup
when their prerequisites are configured. It does not enable bank-transfer collection
or payouts. Whitespace and case are normalized, duplicate entries are collapsed,
and unknown entries fail startup. Credentials alone do not enable a capability.

To migrate an existing deployment:

1. Read its `PAYMENTS_ENVIRONMENT` (`test` is the default).
2. For each provider, collect the capabilities whose `*_SANDBOX_VERIFIED` flags
   are true for `test`, or whose `*_PRODUCTION_ENABLED` flags are true for `live`.
3. Put those names in that provider's `*_ENABLED_CAPABILITIES` list. Do not merge
   the test and live flags: doing so could enable operations in the wrong environment.
4. Remove all sixteen old flags from the deployment environment, including false
   values. The application rejects non-empty retired flags with a migration message.
5. Apply the code and environment update together during deployment.

Lists apply to whichever payment environment the deployment selects. Review them
whenever switching between test and live; retain separate deployment configurations
if those environments enable different operations. The local `.env` and `server.env`
were migrated preserving their current environment's enabled capabilities; remote
deployments still require the steps above.

Financial encryption remains mandatory for enabled capabilities. Payaza DVA bank
codes, payout PIN/source account/sender details, and Paystack's payout OTP requirement
remain independent prerequisites. Existing provider priority, historical reconciliation,
and the rule against retrying ambiguous payments through another provider are unchanged.
