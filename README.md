# go-server

Initial Go backend for the booking app.

## Stack

- Go 1.26
- `chi` for routing
- `pgx/v5` for PostgreSQL access
- `dbmate` for migrations
- JWT access tokens
- long-lived `HttpOnly` refresh tokens stored hashed in Postgres
- optional Cloudflare R2-backed cover image upload during registration

## Run

1. Copy `.env.example` to `.env`
2. Set `DATABASE_URL`
3. Apply migrations:

```bash
dbmate --migrations-dir db/migrations up
```

4. Start the server:

```bash
go run ./cmd/api
```

The server auto-loads `.env` from either the current working directory or `go-server/.env` before reading config.

## Seed demo data

After applying migrations, seed a demo provider workspace with:

```bash
go run ./cmd/seed --client-id <uuid>
```

Or via make:

```bash
make seed-demo
```

Optional flags:

- `--client-id`
- `--email`
- `--password`
- `--full-name`
- `--reset=true|false`

The seed command creates a demo provider user plus related profile, services, clients, bookings, inbox data, notifications, automation settings, agreement templates, portfolio items, and reviews.

## Routes

- `GET /v1/healthz`
- `GET /v1/meta/markets`
- `POST /v1/auth/register`
- `POST /v1/auth/register/verify`
- `POST /v1/auth/register/resend`
- `POST /v1/auth/login`
- `POST /v1/auth/session`
- `POST /v1/auth/password/forgot`
- `POST /v1/auth/password/reset`
- `POST /v1/auth/logout`
- `GET /v1/app/profile`
- `PUT /v1/app/profile`
- `PATCH /v1/app/profile/market`

## Market and money foundations

`GET /v1/meta/markets` is public and returns the versioned, UI-safe market catalog. The response includes each catalogued country's currency metadata, locale, timezone, dialing code, distance unit, market status, and currently enabled payment and payout capabilities. It supports `ETag` revalidation and cache reuse.

Application money uses signed 64-bit integer minor units. JSON contracts must encode minor amounts as quoted base-10 integers so JavaScript clients do not lose precision. `internal/money` provides exact decimal parsing and formatting without floating-point arithmetic.

Profile market settings are stored as one all-or-nothing tuple: country, currency, timezone, locale, and configuration timestamp. Content updates cannot overwrite this tuple. Country and currency changes are rejected after incompatible financial configuration or booking/payment history exists.

## Auth behavior

- register stores a short-lived pending registration and sends an email verification code
- successful registration verification and login set short-lived access and long-lived refresh cookies; both are `HttpOnly`
- register accepts optional `bio`, `cover_image_data_url`, and `cover_image_content_type`
- pending registration password hashes and verification token hashes are stored in `auth_pending_registrations`
- `POST /v1/auth/session` validates the access cookie and rotates the refresh token only when the access cookie has expired
- refresh tokens are stored hashed in `auth_refresh_sessions`
- password reset tokens are stored hashed in `auth_password_reset_tokens`
- `POST /v1/auth/logout` revokes the current refresh token cookie

## Optional R2 setup

If you want registration cover images stored in Cloudflare R2, set:

- `R2_PRIVATE_BUCKET_NAME`
- `R2_PUBLIC_BUCKET_NAME` if you want public assets separated
- `R2_ACCOUNT_ID` or `R2_ENDPOINT`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_PUBLIC_BUCKET_BASE_URL`

## Optional SMTP setup

If you want verification and password reset codes sent by email, set:

- `SMTP_USERNAME`
- `SMTP_PASSWORD`

Optional overrides, if you do not want the defaults:

- `SMTP_HOST` defaults to `smtp.zoho.com`
- `SMTP_PORT` defaults to `465`
- `SMTP_FROM_EMAIL` defaults to `SMTP_USERNAME`
- `SMTP_FROM_NAME` defaults to `Booking`
- `SMTP_SECURITY` defaults to `tls`
- `SMTP_INSECURE_SKIP_VERIFY` defaults to `false`
- `SMTP_CONNECT_TIMEOUT` defaults to `10s`

## Optional Paystack setup

Hosted collection, bank listing, payout account-name resolution, and transfers use Paystack where their capabilities are enabled. Set:

- `PAYSTACK_SECRET_KEY` for live mode
- `PAYSTACK_SECRET_KEY_TEST` for test mode
- `PAYSTACK_BASE_URL` defaults to `https://api.paystack.co`

When the Paystack secret key is missing, Paystack capabilities stay disabled.

## Generic financial ledger security

The provider-neutral payment ledger is created by migration `20260731130000_add_generic_financial_foundation.sql`. Secure webhook payloads and retained payout identifiers require all of:

- `PAYMENTS_ENVIRONMENT` set to `test` or `live`
- `FINANCIAL_DATA_ENCRYPTION_KEYS` as a JSON object of version names to base64-encoded 32-byte AES keys
- `FINANCIAL_DATA_ACTIVE_KEY_VERSION` matching one keyring entry
- `FINANCIAL_DATA_FINGERPRINT_KEY` as a separate base64-encoded 32-byte HMAC key

The keyring, active version, and fingerprint key must be configured together. Keep old encryption-key versions available until stored values have been re-encrypted. Never reuse provider API keys as financial-data keys.

## Provider-neutral payment adapters

Payaza and Paystack implement the same collection, reconciliation, webhook, destination, and payout contracts. Payaza is preferred only after its specific capability has been marked sandbox-verified or production-enabled; provider selection never retries an ambiguous operation through another provider.

Set `CLIENT_PUBLIC_BASE_URL` to the SvelteKit application's absolute public origin. The server uses this trusted value for hosted-checkout return URLs and customer agreement links; request `Origin` and `Referer` headers are never used to construct those URLs.

Payaza configuration:

- `PAYAZA_PUBLIC_KEY` and `PAYAZA_SECRET_KEY` are the live credential pair
- `PAYAZA_PUBLIC_KEY_TEST` and `PAYAZA_SECRET_KEY_TEST` are the test credential pair
- `PAYAZA_BASE_URL` defaults to `https://api.payaza.africa/live`
- Payaza's tenant header is always derived from `PAYMENTS_ENVIRONMENT`
- `PAYAZA_CARD_SANDBOX_VERIFIED` and `PAYAZA_CARD_PRODUCTION_ENABLED` control Payaza card checkout readiness
- `PAYAZA_BANK_TRANSFER_SANDBOX_VERIFIED` and `PAYAZA_BANK_TRANSFER_PRODUCTION_ENABLED` control Payaza dynamic virtual-account readiness
- `PAYAZA_NGN_DVA_BANK_CODE` and `PAYAZA_NGN_DVA_ENQUIRY_BANK_CODE` select the certified NGN DVA bank and its account-enquiry code
- `PAYAZA_TRANSFER_PIN` and `PAYAZA_SOURCE_ACCOUNTS` are the server-only live payout authorization and currency-to-source-account map
- `PAYAZA_TRANSFER_PIN_TEST` and `PAYAZA_SOURCE_ACCOUNTS_TEST` are their isolated test-mode equivalents
- `PAYAZA_PAYOUT_SENDER_NAME`, `PAYAZA_PAYOUT_SENDER_PHONE`, and `PAYAZA_PAYOUT_SENDER_ADDRESS` identify TellBook as the regulated sender on Payaza payout requests

`PAYMENTS_ENVIRONMENT` deterministically selects the credential pair for collections, reconciliation, account-name verification, webhooks, and payouts. Payaza's test tenant does not currently expose the NGN bank-list endpoint, so when the active environment is `test`, TellBook uses the separately configured live client only for that read-only institution directory. It never retries or sends a test financial operation through live credentials. Without the live pair, the Payaza destination capability remains unavailable in test mode.

Paystack uses `PAYSTACK_SECRET_KEY` for live mode, `PAYSTACK_SECRET_KEY_TEST` for test mode, and `PAYSTACK_BASE_URL` for both. `PAYSTACK_CARD_*` and `PAYSTACK_BANK_TRANSFER_*` flags independently control the two collection rails. Card checkout is restricted to the card channel, while bank transfer uses Pay with Transfer through the Charge API. Collection amounts are sent as quoted minor units. Paystack payouts remain unavailable unless transfer OTP has been disabled on the Paystack account and `PAYSTACK_PAYOUT_OTP_DISABLED=true`; provider-side third-party payout permission is also required. TellBook does not store, request, or automatically submit a human OTP.

Paystack settlement evidence is synchronized only after a Paystack collection rail is certified for the active environment. Successful settlement records and their exact transaction membership are persisted before an allocation becomes payout-eligible. Settlement matching requires the provider, TellBook payment reference, amount, and currency to agree; collection success alone never releases funds. Payout initiation also checks the provider balance and is serialized by provider and currency across server instances.

Each provider has separate sandbox-verification and production-enable flags for card collection, bank-transfer collection, destination lookup, and payouts. A capability is unavailable until its exact flag and required credentials are configured; do not enable a flag before completing that provider flow end to end.

## Migrations

Migrations live in `db/migrations` and are intended to be run with `dbmate`.
