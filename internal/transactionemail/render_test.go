package transactionemail

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSecurityVariants(t *testing.T) {
	cases := map[SecurityKind]string{PasswordChanged: "Password changed", PasswordReset: "Password reset completed", PasswordSet: "Password added", EmailLinked: "Email sign-in linked", PhoneLinked: "Phone sign-in linked", PayoutAccountChanged: "Payout account updated"}
	for kind, status := range cases {
		t.Run(string(kind), func(t *testing.T) {
			in := securityFixture(kind)
			m, err := RenderSecurity(in)
			if err != nil {
				t.Fatal(err)
			}
			if m.ToEmail != in.Recipient || m.MessageID != "<account-security-"+in.DeliveryID.String()+"@mail.tellbook.app>" {
				t.Fatal("incorrect delivery identity")
			}
			for _, body := range []string{m.HTML, m.Text} {
				for _, want := range []string{status, "14 September 2026 at 1:42 PM UTC", "Don’t recognise this change?", "Open TellBook directly"} {
					if !strings.Contains(body, want) {
						t.Fatalf("missing %q", want)
					}
				}
				if kind != PhoneLinked && strings.Contains(body, "1683") {
					t.Fatal("unrelated phone data leaked")
				}
				if kind != PayoutAccountChanged && strings.Contains(body, "4821") {
					t.Fatal("unrelated bank data leaked")
				}
			}
			if strings.Contains(m.HTML, "href=") {
				t.Fatal("security notices must direct users to open TellBook themselves")
			}
			if !strings.Contains(m.HTML, "<!--[if mso]>") {
				t.Fatal("Outlook wrappers removed")
			}
		})
	}
}

func TestPayoutStatusesAndPrecision(t *testing.T) {
	cases := map[PayoutStatus]string{PayoutStatusPending: "PROCESSING", PayoutStatusSuccessful: "SUCCESSFUL", PayoutStatusFailed: "FAILED", PayoutStatusReversed: "REVERSED", PayoutStatusRequiresAction: "ACTION REQUIRED", PayoutStatusUnknown: "AWAITING CONFIRMATION", PayoutStatusCancelled: "CANCELLED"}
	for status, label := range cases {
		t.Run(string(status), func(t *testing.T) {
			in := payoutFixture(status)
			m, err := RenderPayout(in)
			if err != nil {
				t.Fatal(err)
			}
			if m.MessageID != "<provider-payout-"+in.DeliveryID.String()+"@mail.tellbook.app>" || m.ToEmail != in.Recipient {
				t.Fatal("incorrect delivery identity")
			}
			for _, body := range []string{m.HTML, m.Text} {
				for _, want := range []string{label, "125,000.00", "NGN", "ending 4821", in.Reference, "14 September 2026 at 1:42 PM UTC"} {
					if !strings.Contains(body, want) {
						t.Fatalf("missing %q", want)
					}
				}
			}
			if status == PayoutStatusUnknown && !strings.Contains(m.Text, "not yet marked successful or failed") {
				t.Fatal("uncertain status misrepresented")
			}
			if status == PayoutStatusReversed && !strings.Contains(m.Text, "does not confirm that funds are available") {
				t.Fatal("reversal misrepresents available balance")
			}
		})
	}
	for _, tc := range []struct {
		amount         int64
		exponent       uint8
		currency, want string
	}{{123456, 0, "JPY", "123,456"}, {123456, 3, "KWD", "123.456"}, {9007199254740993, 2, "NGN", "90,071,992,547,409.93"}} {
		in := payoutFixture(PayoutStatusSuccessful)
		in.AmountMinor, in.CurrencyExponent, in.CurrencyCode = tc.amount, tc.exponent, tc.currency
		m, err := RenderPayout(in)
		if err != nil || !strings.Contains(m.Text, tc.want) {
			t.Fatal("currency precision lost", err, m.Text)
		}
	}
}

func TestInvalidAndEscapedData(t *testing.T) {
	in := payoutFixture(PayoutStatusPending)
	in.InstitutionName = `<img src=x onerror="alert(1)"> & Bank`
	in.Reference = `<script>alert(1)</script>`
	in.ApplicationURL = "https://example.com/?a=1&b=2"
	m, err := RenderPayout(in)
	if err != nil || strings.Contains(m.HTML, "<script>alert") || strings.Contains(m.HTML, "<img src=x") || !strings.Contains(m.HTML, "&lt;script&gt;") || !strings.Contains(m.HTML, "a=1&amp;b=2") {
		t.Fatal("dynamic content not escaped", err)
	}
	for _, mutate := range []func(*PayoutInput){
		func(p *PayoutInput) { p.Status = PayoutStatusCreated },
		func(p *PayoutInput) { p.Status = "invented" },
		func(p *PayoutInput) { p.AmountMinor = 0 },
		func(p *PayoutInput) { p.AmountMinor = -1 },
		func(p *PayoutInput) { p.CurrencyCode = "BADCODE" },
		func(p *PayoutInput) { p.CurrencyExponent = 19 },
		func(p *PayoutInput) { p.AccountLastFour = "0123456789" },
		func(p *PayoutInput) { p.ApplicationURL = "javascript:alert(1)" },
		func(p *PayoutInput) { p.ApplicationURL = "https://user:password@example.com" },
		func(p *PayoutInput) { p.OccurredAt = time.Time{} },
		func(p *PayoutInput) { p.DeliveryID = uuid.Nil },
		func(p *PayoutInput) { p.Recipient = "a@example.com\r\nBcc: b@example.com" },
	} {
		p := payoutFixture(PayoutStatusPending)
		mutate(&p)
		if _, err := RenderPayout(p); err == nil {
			t.Fatal("accepted invalid payout event")
		}
	}
	for _, kind := range []SecurityKind{PhoneLinked, PayoutAccountChanged, "unknown"} {
		p := securityFixture(kind)
		p.PhoneLastFour = "1234567890"
		p.AccountLastFour = "1234567890"
		if _, err := RenderSecurity(p); err == nil {
			t.Fatal("accepted unsupported event or unmasked identifier")
		}
	}
	in = payoutFixture(PayoutStatusPending)
	in.ApplicationURL = ""
	m, err = RenderPayout(in)
	if err != nil || strings.Contains(m.HTML, "href=") {
		t.Fatal("optional action link not omitted", err)
	}
}
