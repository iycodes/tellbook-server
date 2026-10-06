# Tellbook private beta review materials

Status: implementation, local/Inspector verification, core management acceptance, and native package installation checks passed. Both signed-in hosts completed real OAuth linking, conversational reads and edits, explicit four-scope consent, publish/pause/draft transitions, disposable section/service deletion, and disconnect/reconnect checks for Synthetic Studio 1 on 2026-10-02. ChatGPT also duplicated, renamed, moved, and hid a draft, then replayed both deletion requests with the original receipts. Claude created a hidden draft at exactly NGN 2500.50, renamed its section, and replayed its service deletion with the original receipt. All three original services and their section remain intact. Both native ZIP installations, installed skill discovery, and fresh package catalog reads passed on 2026-10-03, reusing their existing connections without additional approvals or reconnections. Genuine recordings remain incomplete in [the host validation report](host-validation-20261002.md). Public submission has not been attempted. Claude's exact hosted CIMD identity (`https://claude.ai/oauth/mcp-oauth-client-metadata`) is configured and its public document verified.

## Product listing draft

**Name:** Tellbook. **Category:** Productivity. **Short description:** Manage your Tellbook services. **Developer:** Tellbook.

**Description:** Connect your Tellbook provider account to manage your service catalog from a conversation. Find services and sections, create and edit drafts, duplicate services, organize sections, and explicitly publish, pause, hide, or delete services. Choose read, edit, publication, and deletion permissions separately. Changes respect your Tellbook business settings and booking history. The private beta uses synthetic reviewer accounts.

**Prompts:** “List my Tellbook services”; “Create a draft 45-minute virtual consultation for NGN 2500”; “Pause Business consultation”; “Move Draft consultation into a new section called Coaching.”

**Endpoints:** ChatGPT `https://tellbook-beta-api.iycodes.com/mcp/chatgpt`; Claude `https://tellbook-beta-api.iycodes.com/mcp/claude`. No embedded MCP UI is required or bundled.

**Packages:** `plugins/chatgpt` contains the portable Agent Plugins manifest, MCP configuration, workflow skill, logo, README, and private-beta license. `plugins/claude` contains the Claude manifest, remote HTTP configuration, the same workflow skill, logo, README, and license. Archives are in `plugins/dist`. Rebuild with `python3 scripts/package-integrations.py`; `--api-origin` replaces endpoints for another HTTPS deployment. The archives explicitly include only reviewed public package files.

Use `--chatgpt-server-id plugin_asdk_app_6abfe12dacf88191902d53090710d42b` to also build the current private workspace archive. It references the registered ChatGPT server using `.app.json`, rather than declaring a second remote server. This registration belongs to the test account/workspace; use a separately registered ID elsewhere. The portable archive remains independent of that account.

## Reviewer accounts

The isolated Mac beta creates two reviewers in `/private/tmp/tellbook-beta-reviewers.json`. Each has its own profile, published services, a draft, a section, business hours, a location, and a published agreement template. Accounts are separately allowlisted. Passwords are generated, and session secrets are separate from the normal development API. Share each account privately with the reviewer; exclude credentials and authorization URLs from recordings and archives. These accounts contain no real customers, booking payments, or production data.

Reviewer one should test management. Reviewer two should confirm that reviewer one’s known resource and receipt IDs cannot be read or modified. Automated tests additionally cover NGN (exponent 2) and XOF (exponent 0), quote dependency restrictions, retained booking history, fake media deletion, refresh reuse revocation, and simultaneous edits.

## Example conversations and expected behavior

| Case | User conversation | Expected result |
| --- | --- | --- |
| Read catalog | “What services do I offer?” → “Show the details for Business consultation.” | Identify connected business; list summaries; read the selected stable ID and revision. |
| Create draft | “Add a 45-minute virtual consultation for NGN 2500.50.” | Read setup choices, collect genuinely missing fields, create exactly one draft, return receipt and draft status. No automatic publication. |
| Partial edit | “Change Design consultation’s description to ‘A focused design review’.” | Read current detail/revision; update only description; preserve pricing, availability and policies. |
| Publication | “Publish Draft consultation.” → “Pause it again.” | Separate explicit status calls with current revisions and distinct keys; report resulting status. |
| Sections and duplication | “Make a Coaching section; move Draft consultation into it and duplicate it.” | Owned section ID; service partial edit changes section; new duplicate is draft. |
| Ambiguous names | “Delete consultation.” | Resolve among similarly named services before deleting. No guessed ID or deletion. |
| Insufficient permission | Connect with read only; ask “Create a new section.” | Structured insufficient_scope response and relink prompt; no write. Additional permissions require new consent. |
| Wrong account / stale edit | Try another provider’s ID; edit same service in the UI before an MCP save. | Foreign resource not_found; stale revision_conflict; no overwriting or foreign receipt retrieval. |
| Lost response | Retry a successful create with exactly the same key and arguments. | Same service ID and receipt, even if the resource has since changed. Changed key reuse fails. |
| Dependency | Delete a service referenced by a quote/proposal. | service_in_use; booking history preserved; suggest pause. |
| Section deletion | “Delete Coaching and move its services into Consultations.” | Validate source and destination ownership; transaction preserves services and bumps their revisions. |
| Disconnect | Settings → Connected apps → Disconnect, then ask to list services. | Grant shows disconnected and all access/refresh tokens immediately fail. |

## Host validation and recordings

Local verification on 2026-10-02 passed: all backend unit tests, isolated PostgreSQL catalog/OAuth/MCP tests, 49 relevant client tests, client contract checks, Svelte diagnostics, and the production build. MCP Inspector 2.9.0 exercised all 15 tools on each platform endpoint. ChatGPT package manifests passed the published Agent Plugins 1.0.0 schemas; the Claude package passed `claude plugin validate`. Both archive integrity checks passed.

The Cloudflare HTTPS endpoint also passed a complete synthetic OAuth exchange, an independent Inspector tool list and connected-profile read, and immediate token rejection after browser disconnect. These tests used the configured official ChatGPT public client metadata, but did not originate from a ChatGPT conversation. [Consent](evidence/consent.jpg), [connected app](evidence/connected-app.jpg), and [disconnected app](evidence/disconnected-app.jpg) screenshots document that synthetic browser flow. No credentials or tokens are included in those images.

Actual ChatGPT and Claude conversations subsequently verified the connected business, service list, prices, IDs, and description-only writes. Both hosts stopped for additional consent when a read grant attempted an edit. Claude uses an HTTP 403 insufficient-scope challenge; ChatGPT uses the structured MCP tool challenge. Claude's tool discovery showed all 15 tools and its per-tool approval controls. Claude also passed section creation, exact-price service creation, hiding, transactional section rename, and actual disconnect/reconnect. These conversational results are separate from Inspector verification and are recorded in the host report.

Use a new conversation and the synthetic account on each host. Record the following sequence, keeping passwords, OAuth codes, tokens, and authorization query strings outside the video frame:

1. Install the package privately and show the tools/skill discovery.
2. Start OAuth linking, resume through Tellbook sign-in, and show the business identity and independent permission checkboxes. Demonstrate cancellation first.
3. Connect with read access, list services, and show the missing edit permission prompt.
4. For the complete management test, choose edit, publication and deletion together in one fresh consent screen after the operator approves those permission groups. Create a draft, change one field, duplicate it, publish and pause explicitly. This single expanded consent avoids a separate reconnection for each group; host tool approvals still apply.
5. Rename a section and move a service. Demonstrate a stale edit and an identical retry using developer inspection if the conversational client does not expose keys.
6. Use the already approved delete permission to delete the disposable synthetic duplicate and preserve section contents. If the earlier consent intentionally omitted deletion, request that additional permission before continuing.
7. Show Tellbook Connected apps with platform, scopes, status and last use; disconnect and demonstrate denied access.

Save host recordings under `docs/review/recordings/` using names `chatgpt-catalog.mp4` and `claude-catalog.mp4`, with a test date and host/client version in a small companion report. This folder is reserved for genuine recordings, and currently contains no manufactured evidence. Browser snapshots of Tellbook consent and Connected apps may be saved separately as local verification artifacts.

On 3 October 2026, an operator-reported ChatGPT tab recording accompanied a fresh walkthrough with 11 verified catalog mutations and three negative cases. [The session report](chatgpt-recording-20261003.md) records its cases, receipts, versions, final cleanup and coverage limits. The exported footage has not been received or reviewed, so the recording checkbox remains incomplete. [OpenAI's MCP directory review](https://developers.openai.com/plugins/deploy/submission) requires a walkthrough recording URL; [Claude's current connector submission checklist](https://claude.com/docs/connectors/building/submission) does not list a required video. Existing private connections work without public-directory review recordings.

## Submission checklist

- [x] Shared Go MCP SDK service operations; platform audiences and annotations.
- [x] Four permission groups; public PKCE/CIMD OAuth; consent and disconnect.
- [x] Default-off gates and provider allowlist; synthetic reviewer bootstrap.
- [x] REST/OpenAPI contract and generated client types.
- [x] Local OAuth, catalog, exact-money, and independent Inspector workflows.
- [x] Claude local manifest validator.
- [x] ChatGPT manifest and MCP configuration validated against the published package schemas.
- [x] Public HTTPS Inspector read and synthetic browser disconnect/token rejection.
- [x] Registered ChatGPT technical identity (`plugin_asdk_app...`) and generated private workspace mapping.
- [x] Actual Claude connector metadata identity configured, public document verified, and sign-in reaches consent.
- [x] Both hosts’ core management acceptance and visible permission prompts, including status changes, section preservation, disposable deletion, and disconnect/reconnect.
- [x] Claude native ZIP installation, enabled workflow skill and existing connector mapping, and a fresh skill invocation.
- [x] ChatGPT native ZIP import, installed workflow skill and existing app mapping, and a fresh package catalog read.
- [x] Host-specific validation report with actual conversations, receipts and screenshots.
- [ ] Genuine host recordings.
- [ ] Durable HTTPS hosting and credential storage for reviewer access beyond this Mac session.
- [ ] Developer contact/support URL, privacy policy URL, terms URL, final distribution license, and public listing screenshots approved by the product owner.
- [ ] Public directory submission, intentionally deferred.

Do not mark these remaining boxes complete on the strength of HTTP unit tests or Inspector output. Submit only after the real-host checks and publication materials are verified.

## Sources

- [OpenAI plugin packaging](https://developers.openai.com/plugins/build/plugins)
- [OpenAI authentication](https://developers.openai.com/plugins/build/auth)
- [OpenAI tool requirements](https://developers.openai.com/plugins/plan/tools)
- [OpenAI submission](https://developers.openai.com/plugins/deploy/submission)
- [Claude plugin layout and testing](https://claude.com/docs/plugins/build)
- [Claude connector authentication](https://claude.com/docs/connectors/building/authentication)
- [Claude connector review criteria](https://claude.com/docs/connectors/building/review-criteria)
- [MCP Inspector](https://modelcontextprotocol.io/docs/tools/inspector)
