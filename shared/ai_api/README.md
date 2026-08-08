# AI API Contracts

Shared request and response structs for the Booking main API and AI server.

Recommended usage:

- `tellbook-server/go.mod`
  - `require booking/shared/ai_api v0.0.0`
  - `replace booking/shared/ai_api => ./shared/ai_api`
- `tellbook-ai-server/go.mod`
  - `require booking/shared/ai_api v0.0.0`
  - `replace booking/shared/ai_api => ../tellbook-server/shared/ai_api`

This package is intentionally transport-focused:

- request payloads
- response payloads
- small shared enums/types

It does not contain provider-specific AI logic.

Current contract groups include:

- template variable extraction
- service optimization
- schedule optimization
- generated messages
- suggested replies
- service content generation
- section content generation
- agreement plain-English summaries
- profile and public-page content generation
