# Tellbook plugins for ChatGPT and Claude

Research date: 2 October 2026. This document preserves the research and design recommendations made before implementation. The private beta is now implemented; see [operation and verification notes](../operations/provider-integrations.md) and [current review materials](../review/provider-integrations.md) for the delivered behavior and host acceptance results. Statements about existing code and proposed changes below describe the research baseline.

## Recommended approach

Build a shared Tellbook MCP integration with delegated provider authorization, then package it separately for ChatGPT and Claude. A provider connects their Tellbook account, grants permissions, and asks the host to perform bounded operations such as listing services, creating a draft service, or changing a service price. Tellbook remains responsible for identity, permissions, validation, and the actual database change.

The shared backend can reuse Tellbook's Go application code. New OAuth endpoints and grants are needed; service CRUD already exists. A safe partial service update is also needed because the current update replaces the whole record. Separate REST endpoints are necessary only where they serve the chosen architecture; MCP does not require duplicating every existing API.

The platform facts below come from current official documentation. The Tellbook findings come from the local server source. Tool names, scopes, routes, and rollout choices marked as proposed are design recommendations, not established platform requirements.

## What the plugins contain

A plugin is the installable package. MCP supplies the live tools; skills can explain workflows. Neither package contains provider credentials or the backend itself. OpenAI currently documents plugins containing skills, MCP, or both, with optional UI. This is the current plugin system rather than a design based on the historical ChatGPT plugin manifest. [OpenAI plugin architecture](https://developers.openai.com/plugins/concepts/plugins)

| Component | ChatGPT | Claude chat |
| --- | --- | --- |
| Manifest | Portable root `plugin.json`, using the Agent Plugins schema | `.claude-plugin/plugin.json` |
| Remote server declaration | Root `mcp.json`; portable transport type `streamable-http` | Root `.mcp.json`; transport type `http` |
| Workflow instructions | Optional `skills/<name>/SKILL.md` | `skills/<name>/SKILL.md` |
| Account access | Provider grants access through OAuth | Provider connects the connector and signs in through OAuth |
| Runtime | Hosted HTTPS MCP server | Hosted HTTPS MCP server |

OpenAI presentation and registered server mappings belong under `extensions.com.openai`. The older `.codex-plugin/plugin.json` layout remains supported, but renaming its `.mcp.json` is insufficient to produce a portable package. We should author one format deliberately and validate it. [OpenAI packaging](https://developers.openai.com/plugins/build/plugins)

Claude's directory package needs a README of at least 40 words and a license declaration or file. A remote server appears in the plugin's Connectors tab; installation alone does not connect the account. Claude chat ignores local servers and hooks, and a top-level `bin/` directory prevents installation in chat and Cowork. Tellbook should therefore use remote tools and portable workflow instructions. [Claude plugin structure](https://claude.com/docs/plugins/build), [Claude platform support](https://claude.com/docs/plugins/platform-support)

The proposed workflow skill would resolve the correct service, collect required business details, explain the intended change, invoke the relevant tool, and report its receipt. Authorization and business rules must still be enforced by the server.

## How a provider connects

The common authorization flow is:

1. The host discovers the MCP resource and its authorization server.
2. The provider signs in to Tellbook in a browser and approves the requested permissions.
3. The host exchanges a short-lived authorization code using PKCE with `S256`.
4. The host sends a delegated access token with subsequent MCP requests.
5. Tellbook validates the token and resolves the provider before executing a tool.

MCP authorization uses protected resource metadata, authorization server discovery, resource indicators, and scoped tokens. Current guidance prefers Client ID Metadata Documents (CIMD), where a client's HTTPS metadata URL identifies it; dynamic client registration remains a compatibility option. [MCP authorization specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)

### ChatGPT details

Publish resource and authorization server discovery, advertise PKCE `S256`, and validate issuer, audience, expiry, and scopes on each request. Carry `resource` through authorization and token exchange. Copy the exact redirect URI and client metadata URL from the server management page; ChatGPT's callback mode depends on issuer identification. Support RFC 9207 `iss` consistently if advertising it. CIMD supports public-client `none` or `private_key_jwt`; the proposed common starting point is public-client PKCE. Tool security schemes declare required scopes, and tool-level relinking uses `_meta["mcp/www_authenticate"]`. Custom API keys and client-credentials grants do not provide this provider-linking flow. OIDC UserInfo is an additional requirement if we support Enterprise workspace email-domain restrictions. [ChatGPT authentication](https://developers.openai.com/plugins/build/auth)

### Claude details

Unauthenticated requests must return HTTP 401 with a discovery challenge; Claude does not treat a challenge on HTTP 200 as an authentication failure. The metadata resource must match the entered server URL, including `/mcp`, and the first advertised authorization server must work. CIMD requires both `client_id_metadata_document_supported: true` and token authentication method `none`; otherwise registration follows the supported fallback. Advertise PKCE `S256`. The hosted callback is `https://claude.ai/api/mcp/auth_callback`. Token and refresh requests use form encoding. Claude Code's loopback callbacks are a separate optional target. Keep discovery and code exchange within Claude's documented 10-second timeout and refresh within 30 seconds. [Claude authentication](https://claude.com/docs/connectors/building/authentication)

### Proposed Tellbook authorization boundary

Reuse existing browser login to identify the provider on the consent page, then issue separate integration grants. Do not pass Tellbook browser cookies to either host or convert an account-wide browser token into an unrestricted integration token.

Each grant should record provider ID, validated OAuth client identity, resource audience, granted scopes, creation time, revocation status, and refresh-token state. Enforce provider isolation from this grant, never from a model-supplied provider ID or MCP `clientInfo`. Use short-lived access tokens, one-use authorization codes, refresh rotation with replay handling, and a provider-visible disconnect control.

Proposed permission groups are `provider.profile.read`, `services.read`, and `services.write`, with publication and deletion separable if exposed later. Consent should describe these in plain language. The final scope vocabulary must match discovery, tool metadata, and enforcement.

## What Tellbook already has

The server mounts versioned routes under `/v1`; authenticated application routes are under `/app`. The following operations already exist:

| Existing API | Use in the integration |
| --- | --- |
| `GET /v1/app/services` and `GET /v1/app/services/{serviceID}` | List and inspect provider services |
| `POST /v1/app/services` | Create a service using current validation |
| `PUT /v1/app/services/{serviceID}` | Full service replacement; unsuitable for a price-only change |
| `PATCH /v1/app/services/{serviceID}/visibility` | Change hidden visibility; distinct from publishing |
| `POST /v1/app/services/{serviceID}/duplicate` | Existing duplication capability, optional later tool |
| `DELETE /v1/app/services/{serviceID}` | Existing deletion capability, optional later tool |
| Service sections, business locations, and business hours routes | Resolve creation prerequisites and valid choices |

Local evidence:

- [Route registration](../../internal/appdata/handler.go) and [versioned server mounting](../../internal/server/server.go).
- [Service handlers](../../internal/appdata/service_management_handler.go) derive identity from `auth.UserFromContext`, require the configured market for creation and update, and check public image ownership. Update also handles replaced-image cleanup.
- [Current authentication](../../internal/auth/handler.go) extracts access tokens from the browser cookie. [Access claims](../../internal/auth/models.go) have no delegated grant or scope model. No MCP or OAuth authorization server was found in the reviewed server implementation.
- [Service input](../../internal/appdata/service_management_models.go) includes pricing, duration, fulfillment, availability, policies, agreements, publication status, images, and child rules.
- [Service persistence](../../internal/appdata/service_management_repository.go) validates ownership of sections, locations, and agreement templates. `saveManagedService` replaces fields and child settings on update. Unrecognized or omitted publication status normalizes to draft.
- [Tessa execution](../../internal/appdata/tessa_worker.go) already has provider-scoped service queries and a replay cache scoped to a Tessa run. Those queries are reusable; that cache does not provide general integration mutation idempotency.

Tellbook represents money as exact integer minor units and serializes amounts as quoted integers. The integration should use the business's configured currency and exact decimal conversion, preserving existing money conventions. It must not assume every provider uses NGN or calculate prices through floating-point arithmetic.

## Proposed first tool set

Start with service management so the first implementation covers a complete read and write workflow.

| Proposed tool | Behavior | Backend work |
| --- | --- | --- |
| `get_connected_profile` | Return the linked provider identity and essential business settings | Small authenticated projection |
| `list_services` | Return bounded results with stable IDs and publication state | Reuse scoped query; define filters and pagination |
| `get_service` | Inspect one owned service | Reuse detail query |
| `get_service_setup_options` | Return valid sections, locations, currency, and required creation choices | Compose existing provider-scoped reads |
| `create_service` | Create a validated draft and return its ID and receipt | Reuse domain validation; add mutation idempotency |
| `update_service_price` | Change price while preserving other service settings | New transactional partial update with conflict checking |
| `set_service_visibility` | Set hidden visibility explicitly | Reuse operation; add receipt and retry handling |

Creation should collect required values rather than guess fulfillment, location, duration, or business policies. Draft creation is the proposed default; publishing would be a separate deliberate capability. An ambiguous service name should produce candidates before a mutation. Tool output should report the actual committed result and current state, with actionable validation errors.

Do not expose a generic tool that accepts arbitrary endpoints, SQL, provider IDs, or HTTP methods. OpenAI recommends purpose-focused tools. Claude explicitly rejects tools combining read and write HTTP operations behind a method parameter. [OpenAI tool design](https://developers.openai.com/plugins/plan/tools), [Claude connector review criteria](https://claude.com/docs/connectors/building/review-criteria)

### Permission metadata needs host testing

Both catalogs must accurately distinguish reads and mutations. OpenAI defines `destructiveHint` around irreversible or difficult-to-reverse effects. Claude's current review checklist requires it for tools modifying or deleting data. This difference must be addressed during host validation; a shared business implementation does not imply identical permission metadata. Platform-specific metadata projections may be needed. Annotations never replace scope and ownership checks. Claude's directory also excludes financial asset transfers, so the proposed initial catalog omits payout or money-transfer execution. [OpenAI tool design](https://developers.openai.com/plugins/plan/tools), [Claude connector review criteria](https://claude.com/docs/connectors/building/review-criteria)

## Proposed backend changes

Use the official Go MCP SDK within the existing Go server if compatibility tests support it. Its MCP, auth, and OAuth extension packages provide protocol support and primitives; they do not supply Tellbook's consent UI or grant lifecycle. [Official MCP Go SDK](https://go.sdk.modelcontextprotocol.io/)

Keep MCP adapters thin. Extract shared application operations where necessary so both REST and MCP retain handler checks, repository validation, and cleanup effects. Calling repository methods directly without carrying over the handler behavior would be incomplete. External calls should execute these operations directly; they do not need another Tessa language-model planning pass.

Proposed new surfaces:

- Hosted `/mcp`, with platform-specific paths only if interoperability or metadata differences justify them.
- Protected resource and authorization server discovery documents, using paths appropriate to the final issuer and resource URLs.
- `/oauth/authorize`, browser consent, and `/oauth/token` for code exchange and refresh.
- Grant storage, token verification, revocation, and provider connection-management operations.
- `/oauth/register` only if dynamic registration is needed; `/oauth/userinfo` if we implement the relevant OIDC features.
- A partial service-update application operation. Add a REST `PATCH` endpoint if other clients or a separate MCP process need it; an in-process adapter can call the operation directly.
- Transactional mutation idempotency, version conflict detection, and audit receipts recording provider, grant, action, and result without token contents.

A repeated mutation with the same idempotency key and input must return its original receipt. Reusing a key for different input must fail. Concurrent provider edits must not silently disappear. The server must support determining whether a mutation committed after a lost response.

Use HTTPS Streamable HTTP and a pinned SDK version tested against both hosts. The July 2026 MCP transport introduces stateless requests and new discovery behavior; hosts may support different protocol revisions. Do not hand-code assumptions about sessions or require every latest extension before testing compatibility. [MCP Streamable HTTP specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http), [MCP July 2026 release](https://blog.modelcontextprotocol.io/posts/2026-07-28/)

UI is optional for the first tool set. A later service card or edit preview can use MCP Apps resources, keeping text results usable independently and feature-detecting OpenAI-specific extensions. [OpenAI MCP UI](https://developers.openai.com/plugins/build/chatgpt-ui)

## Testing and distribution

Test the server in MCP Inspector, then in actual ChatGPT developer mode and as a Claude custom connector. Claude has no separate connector staging runtime, so connect its real host to our isolated test environment. Local Claude Code success does not establish that Claude chat works. [ChatGPT connection testing](https://developers.openai.com/plugins/deploy/connect-chatgpt), [Claude connector testing](https://claude.com/docs/connectors/building/testing)

The verification suite should cover:

- Connecting, consent denial, expired tokens, refresh, disconnect, and insufficient scopes.
- Cross-provider IDs, invalid audience, redirect mismatch, code replay, and revoked grants.
- Listing, selecting an ambiguous service, creating a draft, and changing only the intended price.
- Currency accuracy and preservation of availability, policies, images, and child rules.
- Retried calls, lost responses, conflicting edits, and receipts reflecting committed state.
- Accurate host permissions, invalid-input explanations, and unsupported requests without invented success.

Use populated synthetic provider accounts. Reviewer access must work reliably through the documented sign-in flow.

| Public distribution | Required path |
| --- | --- |
| ChatGPT | Submit the plugin ZIP with its MCP integration, configure OAuth, verify domain ownership, pass scans and review, then publish |
| Claude connector | Submit the hosted MCP URL through the developer portal |
| Claude plugin | Submit the distributable plugin from a public GitHub repository; an owned remote connector needs its own submission too |

OpenAI currently supports one connected MCP server per plugin, despite packages being able to declare multiple servers. Include MCP in the initial submission; it cannot currently be added later to a skills-only plugin. Review materials include a populated test account, five positive and three negative test cases, a recording, and product, support, privacy, and terms information. [OpenAI submission](https://developers.openai.com/plugins/deploy/submission)

Claude's connector and plugin submissions are separate. Pair them under the same organization and reuse the connector URL to avoid duplicate tool sets. A public plugin repository can contain only distribution files; this does not require publishing Tellbook's backend source. Connector listings ordinarily enter as Community listings, while plugin submissions undergo their documented review path. [Claude directory publication](https://claude.com/docs/directory/publish)

## Implementation order and remaining decisions

1. Finalize provider scopes, consent behavior, tool contracts, and partial-update semantics.
2. Implement and test the authorization boundary and a read-only vertical slice in both hosts.
3. Add draft creation and transactional price updates with receipts, isolation, and retry tests.
4. Validate permission metadata and protocol compatibility before fixing shared versus separate MCP paths.
5. Package each plugin, test actual provider workflows, and prepare review materials before submission.

This research did not connect a live server to either host, inspect our developer portal entitlements, select an SDK version, or validate a production callback configuration. Those are implementation checks. Publication URLs, exact ChatGPT callback values, final scope names, and any OIDC requirements remain configuration decisions; they should be verified when configuring the integrations.
