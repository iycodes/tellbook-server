package appdata

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/money"
)

func TestMarketplaceReceiptTemplateRendersLedgerAmountsAndEscapesText(t *testing.T) {
	receipt := MarketplaceBookingReceipt{
		ReceiptNumber:       "TB-TEST",
		ProviderName:        `<script>alert("provider")</script>`,
		CustomerName:        "Customer",
		ServiceTitle:        "Hair service",
		StartsAt:            time.Date(2026, time.August, 24, 9, 0, 0, 0, time.UTC),
		Timezone:            "Africa/Lagos",
		CurrencyCode:        "NGN",
		TotalAmountMinor:    money.Minor(100000),
		NetPaidAmountMinor:  money.Minor(80000),
		RefundedAmountMinor: money.Minor(20000),
		Payments: []MarketplaceBookingReceiptPayment{{
			Reference: "TB-PAY-1", Purpose: "initial", Method: "card",
			AmountMinor: money.Minor(100000), PaidAt: time.Now(),
		}},
	}
	var output bytes.Buffer
	if err := marketplaceReceiptTemplate.Execute(&output, receipt); err != nil {
		t.Fatal(err)
	}
	html := output.String()
	for _, expected := range []string{"₦1000.00", "₦800.00", "₦200.00", "TB-PAY-1"} {
		if !strings.Contains(html, expected) {
			t.Fatalf("receipt omitted %q", expected)
		}
	}
	if strings.Contains(html, `<script>alert`) || !strings.Contains(html, `&lt;script&gt;`) {
		t.Fatal("receipt provider text was not HTML escaped")
	}
}
