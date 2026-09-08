# Pending WhatsApp template contracts

Last verified against the configured Meta WABA on 5 September 2026. These contracts are registered
in `internal/whatsapp/templates.go`, but must not be added to
`WHATSAPP_ENABLED_TEMPLATE_KEYS` until Meta reports `APPROVED` and
`make whatsapp-template-conformance` passes.

If Meta rejects one of them, recreate it exactly from the corresponding request below. Template
IDs identify the current submissions only and must not be reused when recreating a template.

## `booking_status_update`

- Current Meta template ID: `1090994120552820`
- Current status: `PENDING`
- Current rejected reason: `NONE`
- Category: `UTILITY`
- Language: `en`
- Header: none
- Buttons: none
- Body variables, in order: recipient name, booking reference, update summary, service title,
  appointment date and time

```json
{
  "name": "booking_status_update",
  "category": "UTILITY",
  "language": "en",
  "components": [
    {
      "type": "BODY",
      "text": "Hello {{1}}, booking {{2}} has been updated.\n\nUpdate: {{3}}\nService: {{4}}\nDate & time: {{5}}\n\nPlease review the booking in Tellbook if action is required.",
      "example": {
        "body_text": [["Ada", "TB-20481", "Appointment rescheduled", "Bridal makeup", "12 September 2026 at 2:00 PM"]]
      }
    },
    {
      "type": "FOOTER",
      "text": "Tellbook booking notification"
    }
  ]
}
```

## `provider_account_created`

- Current Meta template ID: `1056193580488807`
- Current status: `PENDING`
- Current rejected reason: `NONE`
- Category: `UTILITY`
- Language: `en`
- Header: none
- Buttons: none
- Body variable: provider name

```json
{
  "name": "provider_account_created",
  "category": "UTILITY",
  "language": "en",
  "components": [
    {
      "type": "BODY",
      "text": "Hello {{1}}, this confirms that your Tellbook provider account was created successfully. If you did not create this account, please contact Tellbook support.",
      "example": {
        "body_text": [["Amina"]]
      }
    },
    {
      "type": "FOOTER",
      "text": "Tellbook account notification"
    }
  ]
}
```

## `user_account_created`

- Current Meta template ID: `1381380196901236`
- Current status: `PENDING`
- Current rejected reason: `NONE`
- Category: `UTILITY`
- Language: `en`
- Header: none
- Buttons: none
- Body variable: user name

```json
{
  "name": "user_account_created",
  "category": "UTILITY",
  "language": "en",
  "components": [
    {
      "type": "BODY",
      "text": "Hello {{1}}, this confirms that your Tellbook account was created successfully. If you did not create this account, please contact Tellbook support.",
      "example": {
        "body_text": [["Ada"]]
      }
    },
    {
      "type": "FOOTER",
      "text": "Tellbook account notification"
    }
  ]
}
```

## Approval and rollout check

1. Run `make whatsapp-template-conformance` with the configured WABA credentials.
2. Confirm the command reports no status or contract errors for the template.
3. Add only the approved key to `WHATSAPP_ENABLED_TEMPLATE_KEYS`.
4. Restart workers and perform one authorized real-recipient send with callback evidence.

`booking_status_update` is consumed by the booking notification planner once its key is enabled.
The two account-created contracts are registered and sender-encodable, but this change does not
pretend they are being delivered: the separate durable WhatsApp welcome producer named in the auth
plan has not been implemented yet. That producer must be added before phone/WhatsApp account
creation advertises welcome-message delivery for either auth realm.
