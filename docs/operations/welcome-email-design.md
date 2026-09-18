# Welcome email designs

Two version 2 designs introduce the TellBook emblem, two-tone wordmark, blush
hero, and audience-specific getting-started steps. Both keep a light layout,
600px Outlook fallback, inline styles, system fonts, and plain-text alternatives.

- Provider: “More time for your best work.” The primary action opens the provider
  workspace at `CLIENT_PUBLIC_BASE_URL/`.
- Customer: “Good plans start here.” The primary action opens service search at
  `MARKETPLACE_PUBLIC_BASE_URL/search`.

The shared authoring source is `internal/welcomeemail/templates/welcome.html`.
Fixed copy is in `internal/welcomeemail/design_test.go`. Its `[[ ]]` delimiters
compile the design; the output retains the three runtime placeholders
`{{name}}`, `{{email}}`, and `{{action_url}}` for the existing database assignment
renderer. Recipient values are HTML-escaped, and action URLs must be absolute
HTTP(S) URLs without embedded credentials. Old versions without an action URL
continue to work. Unknown names continue to use the existing “there” fallback.

The provider and customer auth repositories pass their configured URL when
assigning the welcome job, including the first email-linking path. The assigned
HTML, text, subject, recipient, and version remain immutable snapshots.

## Generate previews and migration

From the server directory:

```sh
UPDATE_WELCOME_EMAIL_MIGRATION=true \
WELCOME_EMAIL_PREVIEW_DIR="$PWD/../output/playwright/email-notifications/welcome" \
  go test ./internal/welcomeemail -count=1
```

The migration is generated at
`db/migrations/20260913010000_welcome_email_design_v2.sql`. A test verifies it
matches the authored design and copy. Preview generation creates both audiences,
long-content cases, plain text, and an interactive gallery. It does not send mail
or connect to the database. Preview links use `example.com`.

Use the booking email preview server and open `/welcome/index.html`, or serve
`output/playwright/email-notifications` on a local port. The booking gallery links
to the welcome gallery. Set `GOCACHE=/tmp/tellbook-email-go-cache` if needed in a
restricted workspace.

## Rollout state

The migration inserts **drafts**, not active replacements. It does not edit any
existing active version or any queued job. A conflicting version 2 row is left
untouched and must be reviewed before rollout. No migration is applied by the
preview command.

Deploy the URL-aware application code before activating these templates. After
design and real-inbox review, activate the matching draft for each audience in a
transaction that first archives that audience’s current active row, then sets
the reviewed draft to `active` with `activated_at=NOW()`. Confirm the exact row
and its name/content before doing so. Delivery still depends on
`WELCOME_EMAIL_ENABLED`, worker process configuration, and SMTP.

Rollback archives these design rows instead of deleting them because queued or
sent jobs may reference them. Restore the previous active template explicitly
when rolling back an activation. Jobs already assigned keep their original
content and version.

## Checks

```sh
go test ./internal/welcomeemail ./internal/auth ./internal/marketplaceauth ./internal/notifications ./cmd/api
```

The database lifecycle test uses the currently active version and requires an
isolated `TEST_DATABASE_URL`. Browser previews verify narrow widths, long
recipient values, and removal of head styles. Actual Gmail, Outlook, and Zoho
inbox checks remain separate from browser rendering.
