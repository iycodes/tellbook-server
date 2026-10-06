# Provider catalog integrations: private beta

The Go API serves both `/mcp/chatgpt` and `/mcp/claude` with the official Go MCP SDK. Both call the shared catalog application boundary used by the provider REST UI. Tessa’s model planner is not involved. OAuth uses opaque SHA-256-hashed tokens in PostgreSQL and public-client S256 PKCE. Exact configured CIMD identities and callbacks are required; client registration is not exposed.

## Production configuration

Apply `20261002010000_provider_integrations.sql`, `20261002011000_catalog_revision_indexes.sql`, and `20261002012000_catalog_revision_precision.sql` before enabling the feature. The usual API process constructs the integration routes in `cmd/api/main.go`.

| Variable | Default / requirement |
| --- | --- |
| INTEGRATIONS_ENABLED | false; disables OAuth discovery and MCP authorization |
| INTEGRATIONS_WRITES_ENABLED | false; reads remain available when only this gate is disabled |
| INTEGRATIONS_PUBLIC_BASE_URL | HTTPS API origin, without a path |
| CLIENT_PUBLIC_BASE_URL | HTTPS browser app origin |
| INTEGRATIONS_PROVIDER_ALLOWLIST | Explicit comma-separated provider UUIDs; required when enabled |
| INTEGRATIONS_CHATGPT_CLIENT_METADATA_URL | https://chatgpt.com/oauth/client.json |
| INTEGRATIONS_CHATGPT_REDIRECT_URI | https://chatgpt.com/connector_platform_oauth_redirect |
| INTEGRATIONS_CLAUDE_CLIENT_METADATA_URL | https://claude.ai/oauth/mcp-oauth-client-metadata, verified from Claude's hosted sign-in flow |

Claude’s callback is `https://claude.ai/api/mcp/auth_callback`. Configure the actual connector metadata identity before production startup. Both metadata and callback must match exactly. CIMD must advertise `none` as a supported token authentication method. The plural supported-methods list takes precedence over the singular preference, which permits ChatGPT’s current `none` / `private_key_jwt` document to negotiate public PKCE with this server. JWT assertions and client secrets are rejected.

Consent requests and codes expire after 10 minutes. Access tokens expire after 15 minutes or the grant expiry, whichever is sooner. Refresh grants expire after 30 days and rotate on use; refresh reuse revokes the whole grant. Provider security revision changes invalidate browser and integration authorization. Browser logout does not revoke connected grants. Disconnecting in Settings → Connected apps revokes immediately. Extra scopes require another authorization request and explicit consent.

`catalog.read` includes connected identity, services, sections, and setup choices. `catalog.write` includes ordinary edits and visibility. `catalog.publish` controls publication. `catalog.delete` controls deletions. Read access is included; every additional group is an explicit checkbox.

Initial authentication and permission expansion offer all enabled catalog permission groups together. The provider selects the intended groups in one consent screen; every optional checkbox starts unchecked, and the token includes only approved scopes. A host that explicitly requests a smaller subset continues to receive only that subset in consent; Tellbook never grants scopes outside the validated authorization request. Read-only betas advertise and request read access only. Existing grants retain their original permissions until fresh consent.

Permission expansion follows each host's flow. ChatGPT receives MCP tool-result challenge metadata. Claude receives an HTTP 403 Bearer `insufficient_scope` challenge before the SDK handles the tool call. Each challenge identifies the required permission and offers all enabled groups, including existing scopes, so users can choose the permissions they intend to keep and add without reconnecting for each category. Neither response executes the denied mutation. See [Claude's permission-expansion requirements](https://claude.com/docs/connectors/building/lazy-authentication#ask-for-more-scope-with-403).

Tool approvals are separate from Tellbook OAuth consent. ChatGPT supports global and per-app preferences to ask always, before changes, or only before important changes ([official permission controls](https://developers.openai.com/plugins/changelog#app-permission-controls-in-chatgpt)). Claude exposes category and individual tool permissions under Customize → Connectors → Tellbook → Tool permissions, subject to account/workspace restrictions ([official connector controls](https://support.claude.com/en/articles/11176164-use-connectors-to-extend-claude-s-capabilities)). Users can allow reads automatically and retain approval for consequential writes. Tellbook does not change host preferences or relabel writes as reads. Claude mutations keep its required destructive annotation; edits that can remove image assets and publication/visibility changes remain consequential in ChatGPT.

Initialization instructions and both package skills reuse previously returned profile/setup information, stable IDs and revisions, and successful mutation receipts. A uniquely identified summary can supply the revision for a simple partial edit; details are read only when the requested edit needs missing settings. Conflicts and related revision changes still require refreshing affected resources. Ordinary changes to one resource are combined in one update. Clear requests use the host's approval flow without another conversational confirmation; missing choices and explicit publication/deletion intent still matter.

## Current Mac beta

Package sources from version 0.1.1 target `https://api.tellbook.app/mcp/chatgpt`
and `https://api.tellbook.app/mcp/claude`, with `https://tellbook.app` as the app
origin. Run `python3 scripts/package-integrations.py` to rebuild those portable
archives. The production endpoints must be verified after the server deploys;
the historical host checks below used version 0.1.0 on the isolated tunnel.
To build packages for the Mac beta, pass both origins and a separate output folder:

```sh
python3 scripts/package-integrations.py \
  --api-origin https://tellbook-beta-api.iycodes.com \
  --client-origin https://tellbook-beta.iycodes.com \
  --output-dir plugins/dist/beta
```

Endpoint, author, website, and README URLs are rewritten in the archives together;
packaging does not change source files or server environment variables. A
workspace archive created with `--chatgpt-server-id` uses that existing
registration's connection. It must already point to the intended server; package
URL changes do not retarget a beta registration. Production portable archives do
not include an account-specific registration.

A dedicated tunnel was created without altering existing tunnels:

- Tunnel: `tellbook-integrations-beta`, ID `df3a295c-85d4-44d3-b7ab-2f9e17ebb2e2`.
- App: https://tellbook-beta.iycodes.com → local `127.0.0.1:15275`.
- API: https://tellbook-beta-api.iycodes.com → local `127.0.0.1:18200`.
- App `/v1/*` forwards directly to Go for browser cookies.
- Database: `tellbook_integrations_test_20261002` on local PostgreSQL. The normal Tellbook database is untouched.
- Tunnel config: `/private/tmp/tellbook-beta-cloudflared.yml`; credentials: `/private/tmp/tellbook-beta-tunnel.json` (private file).
- Reviewer credentials and session secret: `/private/tmp/tellbook-beta-reviewers.json` (0600). This contains two synthetic accounts and must never be committed, placed in a plugin package, or included in a recording.

The beta bootstrap command only accepts localhost databases named `tellbook_integrations_*`. It creates synthetic profiles, verified email identities, three services per reviewer, sections, business hours, one business location, and a published agreement template. It does not configure payment providers, delivery providers, AI planning, or background workers. Existing fixtures and edits survive restarts.

Restart from the server repository (separate terminals):

```sh
go build -o /private/tmp/tellbook-integration-beta ./cmd/integration-beta
/private/tmp/tellbook-integration-beta \
  --database-url 'postgres://iyanuoluwa@127.0.0.1:5432/tellbook_integrations_test_20261002?sslmode=disable' \
  --public-url https://tellbook-beta-api.iycodes.com \
  --client-url https://tellbook-beta.iycodes.com --claude-client-url https://claude.ai/oauth/mcp-oauth-client-metadata --writes
cloudflared tunnel --config /private/tmp/tellbook-beta-cloudflared.yml run
```

The current beta includes Claude's exact published identity. Its document declares the configured callback and public token authentication (`none`). Include the Claude flag above when restarting. The bootstrap records only official public metadata URL candidates, never codes, state, credentials, or tokens.

From the client repository:

```sh
pnpm build
tellbook_beta_release="$(python3 ../tellbook-server/scripts/stage-beta-client.py --client-dir .)"
HOST=127.0.0.1 PORT=15275 ORIGIN=https://tellbook-beta.iycodes.com \
  PRIVATE_API_BASE_URL=http://127.0.0.1:18200 node "$tellbook_beta_release/build/index.js"
```

Finish the build before staging it. Stop the previous beta client process before starting the staged release on the same port. The staging command copies the completed adapter-node build into a unique private directory and refuses a build that changes during the copy. It does not start, stop, or replace any server. Running from the project’s mutable `build/` directory can leave a cached server manifest referring to chunks removed by a later build; this caused an actual consent-page 500 during the beta. The fixed release prevents subsequent development builds from changing files used by the running server. Keep its directory until that process stops. Local dependencies remain shared through `node_modules`; stage and restart after dependency changes as well.

The currently running fixed client release is `/private/tmp/tellbook-beta-client-20261002T210905Z`. Consent resumed successfully after switching to this release without changing the existing synthetic account session or OAuth grants. Reload existing browser tabs after a release change; open a fresh tab if a previously failed dynamic import remains cached. A fresh beta tab verified Services and service details against this release.

These processes must stay running and the Mac must remain awake for HTTPS connections to work. The temporary files may be cleaned by macOS; move the tunnel credentials and reviewer file to a private durable location before relying on unattended uptime, and update the runtime paths accordingly. Do not put them in either repository. Stop the three processes to take the beta offline; no existing tunnel is affected.

## Ingress and operations

Use `deploy/cloudflared.integrations.example.yml` or the optional nginx example. Route MCP, OAuth, and discovery directly to Go. Preserve Authorization and MCP headers, disable authenticated caching and response/request buffering, and do not cache consent or connected-app responses. The MCP endpoint allows only the configured public authority and literal localhost authorities, including when Go listens on loopback behind the tunnel. This explicit check replaces the SDK’s loopback-only host restriction while retaining DNS-rebinding protection. Go uses stateless Streamable HTTP JSON responses and sets `Cache-Control: no-store`. Cloudflare must not have a Cache Everything rule for these paths; use a bypass rule if an account-level custom rule exists. Discovery checks on the current beta return `CF-Cache-Status: DYNAMIC`.

Operational counters are `tellbook_integrations_outcomes_total{platform,operation,outcome}` and `tellbook_integrations_duration_seconds{platform,operation}`. They cover tool outcomes, bounded error codes, retries, conflicts, and OAuth/authorization failures. Scrape through the existing authenticated operational metrics route. Request diagnostics use paths, never authorization query parameters or credential values. OAuth and consent use the auth rate limit; MCP uses the general rate limit with hashed bearer identity and an IP ceiling.

Receipts are the append-only mutation/audit log: actor, grant, provider, operation, canonical request hash, timestamp, resulting resource/revision, and result are committed in the same PostgreSQL transaction. An identical key within one actor/provider returns its original receipt before revision checking. Changed reuse returns `idempotency_conflict`. Browser receipts are retrieved by owned receipt ID; no other provider can retrieve them. Production retention/expiry cleanup policy should be set before broader rollout.

## Contracts and verification

`contracts/provider-integrations.openapi.yaml` is generated by `go run ./cmd/integration-contracts`. It documents revision and idempotency headers, PATCH semantics, existing PUT routes, publication, visibility, duplication, section preservation, reorder revision maps, receipts, consent, connections, discovery, and OAuth. Generate and check client types with `pnpm contracts:generate:provider-integrations` and `pnpm contracts:check`.

```sh
go test ./...
TEST_DATABASE_URL='ISOLATED_DATABASE_URL' go test ./internal/appdata ./internal/integrations \
  -run 'TestCatalog|TestMCP|TestOAuth|TestCIMD|TestToolSchemas' -count=1
RUN_MCP_INSPECTOR=1 TEST_DATABASE_URL='ISOLATED_DATABASE_URL' \
  go test ./internal/integrations -run TestMCPAllToolsAcrossPlatforms -count=1 -v
```

Inspector 2.9.0 runs all 15 tools through independent HTTP connections for both endpoints, using complete synthetic OAuth flows. Its expected `isError` exit code is handled separately. This verifies the protocol and permission prompts at the MCP layer; real ChatGPT/Claude connection acceptance and conversation recordings remain separate host checks. See `docs/review/provider-integrations.md`.

Actual hosted management checks also passed on 2026-10-02: both platforms completed linking, reads/edits, explicit four-scope consent, publish/pause/draft, section deletion preserving services, disposable service deletion, and disconnect/reconnect. Identical cleanup retries returned original receipts in both hosts. The original synthetic services and second provider's catalog were preserved. Both native ZIP installations, installed workflow skills, existing app/connector mappings, and fresh package catalog reads passed on 2026-10-03. Each package reused its existing Tellbook Beta connection without additional consent, reconnection or tool approval prompts. ChatGPT's installed cloud package ID is `Plugin_e9ae4ad8673c819198096b75fa8c8a39`, distinct from its mapped MCP server registration. Genuine recordings remain incomplete in the host report.

When routine Claude reads unexpectedly ask for approval despite valid scopes, inspect the exact tool under Customize → Connectors → Tellbook → Tool permissions. In this beta, two read defaults still showed Needs approval after choosing Always allow in individual call prompts. Setting their defaults directly, reloading, and executing fresh reads verified the correction. An action approval prompt alone does not require a new Tellbook connection; only an authentication or missing-scope challenge requires reconnecting.
