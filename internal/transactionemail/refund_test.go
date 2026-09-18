package transactionemail

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func refundFixture(status RefundStatus, audience string) RefundInput {
	return RefundInput{CustomerName: "Amara Okafor", Event: fixtureEvent(), Status: status, Audience: audience, Service: "Signature cut & finish", Provider: "The Sunday Studio", Reference: "8d923ac0-7dc4-4d86-9afc-b0853912f009", RequestedMinor: 2500000, CurrencyCode: "NGN", CurrencyExponent: 2, BookingURL: "https://example.com/bookings?booking=aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
}

func TestRefundProgressStatesAndAmounts(t *testing.T) {
	for _, audience := range []string{"customer", "provider"} {
		for _, status := range []RefundStatus{RefundQueued, RefundProcessing, RefundFailed, RefundManualReview} {
			in := refundFixture(status, audience)
			m, err := RenderRefund(in)
			if err != nil {
				t.Fatal(err)
			}
			if m.ToEmail != in.Recipient || m.MessageID != "<refund-progress-"+in.DeliveryID.String()+"@mail.tellbook.app>" {
				t.Fatal("incorrect delivery identity")
			}
			for _, body := range []string{m.HTML, m.Text} {
				for _, want := range []string{"Requested refund", "25,000.00", "NGN", in.Reference, "14 September 2026 at 1:42 PM UTC"} {
					if !strings.Contains(body, want) {
						t.Fatalf("missing %q", want)
					}
				}
				for _, unwanted := range []string{"Payout amount", "CONFIRMED REFUNDED SO FAR", "business days", "original payment method"} {
					if strings.Contains(body, unwanted) {
						t.Fatalf("invented or unrelated refund detail %q", unwanted)
					}
				}
			}
			if status == RefundManualReview && !strings.Contains(m.Text, "unconfirmed") {
				t.Fatal("uncertainty lost")
			}
			if status == RefundFailed && !strings.Contains(m.Text, "could not be completed as requested") {
				t.Fatal("failed request not explained")
			}
			if audience == "provider" && status == RefundFailed && !strings.Contains(m.Text, "before requesting another refund") {
				t.Fatal("provider retry guidance missing")
			}
			if !strings.Contains(m.HTML, "<!--[if mso]>") {
				t.Fatal("Outlook wrapper lost")
			}
		}
	}
	in := refundFixture(RefundFailed, "customer")
	confirmed := int64(1000000)
	in.ConfirmedMinor = &confirmed
	m, err := RenderRefund(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.HTML, m.Text} {
		for _, want := range []string{"25,000.00", "10,000.00", "CONFIRMED REFUNDED SO FAR", "part of the original request"} {
			if !strings.Contains(body, want) {
				t.Fatal("partial amount missing")
			}
		}
	}
	confirmed = 0
	m, err = RenderRefund(in)
	if err != nil || !strings.Contains(m.Text, "NGN 0.00") || strings.Contains(m.Text, "A portion has been confirmed") {
		t.Fatal("zero confused with unknown", err)
	}
}

func TestRefundInvalidAndEscapedContent(t *testing.T) {
	for _, mutate := range []func(*RefundInput){
		func(p *RefundInput) { p.Status = "successful" },
		func(p *RefundInput) { p.Status = "unknown" },
		func(p *RefundInput) { p.Audience = "other" },
		func(p *RefundInput) { p.RequestedMinor = 0 },
		func(p *RefundInput) { p.Reference = "" },
		func(p *RefundInput) { p.CurrencyCode = "bad" },
		func(p *RefundInput) { p.BookingURL = "javascript:alert(1)" },
		func(p *RefundInput) { n := int64(-1); p.ConfirmedMinor = &n },
		func(p *RefundInput) { n := p.RequestedMinor; p.ConfirmedMinor = &n },
		func(p *RefundInput) { n := p.RequestedMinor + 1; p.ConfirmedMinor = &n },
		func(p *RefundInput) { p.Status = RefundQueued; n := int64(1); p.ConfirmedMinor = &n },
	} {
		in := refundFixture(RefundProcessing, "customer")
		mutate(&in)
		if _, err := RenderRefund(in); err == nil {
			t.Fatal("accepted invalid refund")
		}
	}
	in := refundFixture(RefundProcessing, "customer")
	in.Provider = `<script>alert(1)</script>`
	in.Reference = `<img src=x onerror=alert(1)>`
	m, err := RenderRefund(in)
	if err != nil || strings.Contains(m.HTML, "<script>alert") || strings.Contains(m.HTML, "<img src=x") || !strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Fatal("refund content not escaped", err)
	}
}

func TestWriteRefundPreviews(t *testing.T) {
	root := os.Getenv("REFUND_EMAIL_PREVIEW_DIR")
	if root == "" {
		t.Skip("set REFUND_EMAIL_PREVIEW_DIR to generate designs")
	}
	dir := filepath.Join(root, "refunds")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	type item struct{ File, Label string }
	data := struct {
		Title, First string
		Items        []item
	}{Title: "Refund progress", First: "customer-processing"}
	write := func(file, label string, in RefundInput) {
		t.Helper()
		m, err := RenderRefund(in)
		if err != nil {
			t.Fatal(err)
		}
		for ext, body := range map[string]string{".html": m.HTML, ".txt": m.Text} {
			if err := os.WriteFile(filepath.Join(dir, file+ext), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		data.Items = append(data.Items, item{file, label})
	}
	for _, audience := range []string{"customer", "provider"} {
		for _, status := range []RefundStatus{RefundProcessing, RefundQueued, RefundFailed, RefundManualReview} {
			write(audience+"-"+string(status), audience+" — "+string(status), refundFixture(status, audience))
		}
	}
	partial := int64(1000000)
	in := refundFixture(RefundProcessing, "customer")
	in.ConfirmedMinor = &partial
	write("customer-partial", "Customer — partially completed request", in)
	in = refundFixture(RefundFailed, "provider")
	in.ConfirmedMinor = &partial
	write("provider-partial-failed", "Provider — failed with a completed portion", in)
	in = refundFixture(RefundManualReview, "customer")
	in.RequestedMinor = 987654321012
	in.Provider = "The Sunday Studio for Hair, Beauty and Special Occasions"
	in.Service = "Full bridal consultation, styling trial and personalised event preparation"
	in.Recipient = strings.Repeat("a", 64) + "@example.com"
	write("long-details", "Long details & large amount", in)
	var body bytes.Buffer
	if err := template.Must(template.New("gallery").Parse(transactionGallery)).Execute(&body, data); err != nil {
		t.Fatal(err)
	}
	// Match the initially selected option to the initial iframe.
	if err := os.WriteFile(filepath.Join(dir, "index.html"), body.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
}
