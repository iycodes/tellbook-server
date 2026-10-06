# ChatGPT recording session — 3 October 2026

The operator reported resuming a Chrome tab recording for this walkthrough. The live workflow and receipt verification passed. No exported video has been received or inspected yet; footage, duration, redaction and submission suitability remain unverified.

## Environment

- Host: ChatGPT web, GPT-6.1 Sol Light.
- Browser: Google Chrome 154.0.8037.95 on macOS.
- Installed Tellbook package: version 0.1.0, `Plugin_e9ae4ad8673c819198096b75fa8c8a39`, using the existing Tellbook Beta connection.
- Connected business: Synthetic Studio 1, provider `cd201675-6d44-4223-a15e-5beaa7789b40`.
- [Recorded conversation: List Tellbook services](https://chatgpt.com/c/6ac125bd-2b88-83ea-b471-45e4e1ad06bf).
- API: `https://tellbook-beta-api.iycodes.com/mcp/chatgpt`; isolated Mac beta database `tellbook_integrations_test_20261002`.

The existing four-scope connection was reused. No OAuth relinking, permission expansion or persistent approval setting change was performed during this walkthrough.

## Cases demonstrated

| Case | Observed result |
| --- | --- |
| Read catalog | Returned the three original services, their prices, durations and statuses for Synthetic Studio 1. |
| Create draft | Created ChatGPT beta consultation in Consultations at exactly NGN 2500.50, for 45 minutes, with virtual fulfillment and availability matching Business consultation. |
| Partial edit | Changed only the description; receipt verification preserved price, duration and draft status. |
| Organize services | Created a disposable section, moved the draft into it, hid the service, and renamed the section. |
| Explicit publication | Published, paused and returned the service to draft in separate operations, keeping it hidden and preserving its exact price throughout. |
| Section cleanup | Deleted the test section using the explicit uncategorized choice. A subsequent host read showed the preserved service as a hidden draft at NGN 2500.50. |
| Service cleanup | Deleted the disposable service, then fetched the final catalog: three original services in Consultations, none hidden. |
| Negative: invalid price | NGN -1.00 returned `invalid_request` for `pricing.price_amount`; no price change was committed. |
| Negative: unspecified service | Asked which consultation to edit instead of selecting one. The pending change was cancelled. |
| Negative: missing service | The zero UUID lookup returned `not_found` with an account-scoped message; no mutation occurred. |

A proposed business-currency change request was blocked by automatic approval review before submission because it would request an unauthorized account-setting mutation. It was replaced with the read-only missing-service case above. No currency-change refusal by Tellbook is claimed. The business remains configured for NGN.

## Receipts and verification

Disposable service: `3f0fa4ea-2093-48cb-81fc-c59cec3b069c`. Disposable section: `67a65129-a768-4dbc-a80d-ff107ef2e20a`. Both are deleted.

| Operation | Receipt | Returned revision |
| --- | --- | --- |
| Create service | `109fcbbb-b55b-4fd8-8eab-a39f05455e31` | Service 1 |
| Description edit | `b0625b53-cdf8-4537-887b-97c17f15cc2f` | Service 2 |
| Create section | `825b58e6-69ff-407b-bafe-ae6cdd98a47e` | Section 1 |
| Move service | `6e6b3a72-a8fa-488f-bc6a-e339243c1ceb` | Service 3 |
| Hide service | `c2d6efcd-4240-4cae-a78b-34a906b51b20` | Service 4 |
| Rename section | `33f77165-c4fb-4463-a243-aaccb6bbf894` | Section 3 |
| Publish service | `033dc98b-5c32-40c5-a167-c0fba732d085` | Service 6 |
| Pause service | `ed9c4721-74b3-4a93-aca8-6f6da23215ac` | Service 7 |
| Return to draft | `d88c458b-a966-459a-9ce9-cd7c4a7518e0` | Service 8 |
| Delete section | `ce1e22c1-8c63-4263-9ebd-b982fa6f79bc` | Deleted |
| Delete service | `34fd1c98-7856-4daf-916b-c4df52e51744` | Deleted |

[Read-only database verification](evidence/chatgpt-recording-verification.txt) confirmed all 11 unique receipts belong to the correct provider and ChatGPT grant. Both providers retain their three original services at their previous revisions, prices, durations, visibility and statuses. Studio 1's Consultations section advanced from revision 8 to 10 through the test service's addition and removal; Studio 2's section remains revision 4. No test resource remains. No original service was edited or deleted.

Screenshots: [draft and receipt](evidence/chatgpt-recording-draft.jpg), [section organization](evidence/chatgpt-recording-organization.jpg), [explicit publication](evidence/chatgpt-recording-published.jpg), [invalid-price rejection](evidence/chatgpt-recording-invalid-price.jpg), and [final catalog after cleanup](evidence/chatgpt-recording-final-catalog.jpg).

## Footage follow-up

Save the actual operator recording and inspect it before marking the recording complete. The launcher and native input controls intermittently stalled; trim idle footage while retaining the submitted prompts, tool activity, results and receipts. Verify credentials, authorization query strings and unrelated account content are absent.

This walkthrough reused a connected account. OAuth consent, denial, permission expansion, disconnect, duplication, idempotent retries and stale-revision conflicts were not demonstrated again in this clip; their earlier real-host and automated evidence remains in [the host validation report](host-validation-20261002.md). Public directory submission remains deferred, and durable reviewer hosting and final listing/legal metadata are still required.
