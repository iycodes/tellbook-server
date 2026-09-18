# Refund progress emails

`internal/transactionemail.RenderRefund` provides customer and provider designs
for the existing booking-refund request states `queued`, `processing`, `failed`,
and `manual_review`. They are connected to committed refund transitions behind
a disabled switch; see [additional email wiring](additional-email-wiring.md).

## Content and layout

The requested refund amount leads the receipt in a blush panel. Details show
the service, provider (customer emails) or optional customer name (provider
emails), request reference, and recorded time in explicit UTC. The CTA opens
the audience-appropriate booking. Failed requests are labelled Needs attention;
manual-review requests are labelled Awaiting confirmation. A queued request is
recorded, not approved or completed.

One booking refund request may span several payments. The overall request can
fail after some attempts succeed, so failure copy says the request could not
be completed as requested rather than claiming no money was refunded.
`ConfirmedMinor` optionally adds a separate Confirmed refunded so far row. Nil
means no total was supplied; zero explicitly means no portion is confirmed.
Negative, excessive, fully completed, and queued-with-partial-success inputs
are rejected as inconsistent with these progress designs. The requested amount
always remains the original request amount, never an inferred remaining amount.

Amounts use integer minor units and configured currency exponents. Long amounts
use smaller type. No bank/card data, raw processor error, invented arrival
estimate, refund destination, or promise of retry is included. Provider notices
advise checking completed portions or uncertain outcomes before requesting
another refund. Customer notices direct questions to the service provider.

The existing `payment_refunded` booking notification remains the completed-refund
design; this renderer rejects `successful` to avoid introducing a second
completion notice. If a later integration needs request-level completion events
for partial booking refunds, define their relationship to `payment_refunded`
explicitly rather than treating a partially refunded booking as fully refunded.

The shared receipt template now accepts amount and action labels. Existing
security and payout renderers retain their default labels and actions. Currency
formatting and narrow-screen amount sizing are shared with payout emails.

## Integration

Committed transitions, request-scoped confirmed amounts, delayed/superseded
notices, preferences and SMTP dispatch fencing are implemented in
[additional email wiring](additional-email-wiring.md). The existing completed
refund notification remains the only successful-refund email.

## Preview and verification

Set `REFUND_EMAIL_PREVIEW_DIR` to the email preview root and run:

```sh
go test ./internal/transactionemail -count=1
```

The gallery is written to `refunds/index.html`, with eight audience/state variants
and three additional partial-success/long-content examples. All preview data and
links are fictional. HTML and plain text come from the same renderer.

Tests cover state/audience copy, partial-success accounting, zero versus missing
totals, invalid inputs, escaped content, delivery identity, and Outlook wrappers.
The existing payout/security/booking renderer tests also pass. Browser checks
cover eleven fixtures at 320px, 375px and 760px, plus 320px with style elements
removed (44 checks). Desktop and mobile screenshots were visually reviewed.
No test emails were sent; actual Gmail, Outlook and Zoho verification is pending.
