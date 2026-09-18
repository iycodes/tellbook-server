# Account security and provider payout designs

The reusable HTML/plain-text renderers are now connected to existing workers
behind a disabled-by-default switch. See [additional email wiring](additional-email-wiring.md)
for capture, recipient, retry, tests and rollout details.

## Account security

`RenderSecurity` covers password changed, password reset completed, password
added, email identity linked, phone identity linked, and payout account updated.
The password-added variant avoids saying a prior password changed when the user
is setting their first password. Email and phone identity notices concern
account sign-in; Tessa connection notices remain a separate family.

The layout uses an ivory change record with a burgundy edge, event time in UTC,
and instructions for an unrecognised change. Security notices have no action
links. No password, code, full phone number, bank account number, IP address, or
inferred location is included. A phone or bank account is identified by its last
four digits only; the renderer rejects longer identifiers. Email-link notices
direct the recipient to review linked identities in TellBook without disclosing
the new address in the body.

## Provider payouts

`RenderPayout` accepts the existing `payments.PayoutStatus` values pending,
successful, failed, reversed, requires_action, unknown, and cancelled. Created
and unsupported states are rejected. The receipt layout highlights the payout
amount and currency, followed by the destination, reference, and recorded time.
Long amounts receive smaller type so they remain legible on narrow screens.

Amounts use integer minor units and an explicit currency exponent from trusted
currency metadata. No float conversion, assumed fee, computed net amount,
arrival estimate, or invented failure reason is included. A success reflects
provider confirmation. Unknown is explicitly unresolved; reversal is not a
promise that funds have become available for another payout.

An optional HTTPS application link is accepted from trusted configuration. The
preview link points to `https://example.com/tellbook`, deliberately marked as a
sample in the gallery. There is no fabricated payout-detail route. Omitting the
URL removes the button; the text still directs the user to TellBook.

## Integration

Implemented in [additional email wiring](additional-email-wiring.md).

## Preview and validation

Set `TRANSACTION_EMAIL_PREVIEW_DIR` to the email preview root and run:

```sh
go test ./internal/transactionemail -count=1
```

The preview test generates `security/index.html` and `payouts/index.html`, along
with HTML/plain-text fixtures. The six security and seven payout variants have
three additional edge cases: long security details, large payout/long reference,
and a payout without a button. All preview data is fictional.

The tests cover event metadata, status-specific copy, UTC conversion, integer
currency precision (including zero- and three-decimal currencies), HTML escaping,
masked identifiers, unsafe URLs, invalid events, and Outlook wrappers.

Essential styles are inline, with fluid tables and conditional Outlook wrappers.
Light colour-scheme metadata requests the intended palette without claiming that
inbox apps cannot recolour it. Browser checks cover 320px, 375px, and 760px, plus
320px with style elements removed. Actual Gmail, Outlook, and Zoho inbox checks
and sample sends are still outstanding.
