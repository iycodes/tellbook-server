# AI Contracts

Request and response structs shared by TellBook's application handlers and in-process AI generation services.

This package is intentionally transport-focused:

- request payloads
- response payloads
- small shared enums/types

Provider-specific AI logic belongs in `internal/llm`; prompt and generation logic belongs in `internal/ai`.

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
