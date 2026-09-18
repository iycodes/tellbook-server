package transactionemail

import (
	"bytes"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"github.com/google/uuid"
)

func fixtureEvent() Event {
	return Event{DeliveryID: uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), Recipient: "amara@example.com", OccurredAt: time.Date(2026, 9, 14, 14, 42, 0, 0, time.FixedZone("WAT", 3600))}
}
func securityFixture(kind SecurityKind) SecurityInput {
	return SecurityInput{Event: fixtureEvent(), Kind: kind, PhoneLastFour: "1683", InstitutionName: "Example Bank", AccountLastFour: "4821"}
}
func payoutFixture(status PayoutStatus) PayoutInput {
	return PayoutInput{Event: fixtureEvent(), Status: status, AmountMinor: 12500000, CurrencyCode: "NGN", CurrencyExponent: 2, Reference: "pout-56d2701e4c334acba5e9f2a671024938", InstitutionName: "Example Bank", AccountLastFour: "4821", ApplicationURL: "https://example.com/tellbook"}
}

func TestWriteTransactionEmailPreviews(t *testing.T) {
	root := os.Getenv("TRANSACTION_EMAIL_PREVIEW_DIR")
	if root == "" {
		t.Skip("set TRANSACTION_EMAIL_PREVIEW_DIR to generate designs")
	}
	type item struct{ File, Label string }
	type galleryData struct {
		Title, First string
		Items        []item
	}
	galleries := map[string]*galleryData{"security": {Title: "Account security", First: "password_changed"}, "payouts": {Title: "Provider payouts", First: "pending"}}
	write := func(family, file, label string, m mailer.Message, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, family)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for ext, body := range map[string]string{".html": m.HTML, ".txt": m.Text} {
			if err := os.WriteFile(filepath.Join(dir, file+ext), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		galleries[family].Items = append(galleries[family].Items, item{file, label})
	}
	for _, kind := range []SecurityKind{PasswordChanged, PasswordReset, PasswordSet, EmailLinked, PhoneLinked, PayoutAccountChanged} {
		m, err := RenderSecurity(securityFixture(kind))
		write("security", string(kind), strings.ReplaceAll(string(kind), "_", " "), m, err)
	}
	longSecurity := securityFixture(PayoutAccountChanged)
	longSecurity.InstitutionName = "Example International Community and Commercial Banking Corporation"
	longSecurity.Recipient = strings.Repeat("a", 64) + "@example.com"
	m, err := RenderSecurity(longSecurity)
	write("security", "long-details", "Long account details", m, err)
	for _, status := range []PayoutStatus{PayoutStatusPending, PayoutStatusSuccessful, PayoutStatusFailed, PayoutStatusReversed, PayoutStatusRequiresAction, PayoutStatusUnknown, PayoutStatusCancelled} {
		m, err := RenderPayout(payoutFixture(status))
		write("payouts", string(status), strings.ReplaceAll(string(status), "_", " "), m, err)
	}
	large := payoutFixture(PayoutStatusSuccessful)
	large.AmountMinor = 987654321012
	large.InstitutionName = longSecurity.InstitutionName
	large.Recipient = longSecurity.Recipient
	large.Reference = "pout-" + strings.Repeat("f", 80)
	m, err = RenderPayout(large)
	write("payouts", "long-details", "Large amount & long details", m, err)
	noLink := payoutFixture(PayoutStatusUnknown)
	noLink.ApplicationURL = ""
	m, err = RenderPayout(noLink)
	write("payouts", "no-link", "Awaiting confirmation — no action link", m, err)
	for family, data := range galleries {
		var body bytes.Buffer
		if err := template.Must(template.New("gallery").Parse(transactionGallery)).Execute(&body, data); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, family, "index.html"), body.Bytes(), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

const transactionGallery = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · {{.Title}}</title><style>*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2}h1{font-size:21px;margin:0;color:#af1f4a}small{color:#584144}button,select{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;background:#fff;color:#2d2426;border-radius:5px}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff}a{color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1350px;border:0;outline:1px solid #e2d8d2;margin:auto;background:#faf9f4}</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> · {{.Title}}</h1><small>Fictional data · Sample links · Design preview · Delivery not connected</small></div><select id="variant" aria-label="Email variant">{{range .Items}}<option value="{{.File}}">{{.Label}}</option>{{end}}</select><div><button data-width="720" aria-pressed="true">Desktop</button> <button data-width="375" aria-pressed="false">Mobile</button> <button data-width="320" aria-pressed="false">320px</button></div><a href="../index.html">Email studio</a> · <a href="../security/index.html">Account security</a> · <a href="../payouts/index.html">Provider payouts</a> · <a href="../reminders/index.html">Outstanding steps</a> · <a href="../completion/index.html">Completion & reviews</a> · <a href="../refunds/index.html">Refund progress</a></header><main><iframe id="preview" title="Email preview" src="{{.First}}.html"></iframe><p style="text-align:center"><a id="standalone" href="{{.First}}.html" target="_blank">Open email</a> · <a id="plain" href="{{.First}}.txt" target="_blank">Plain text</a></p></main><script>const select=document.getElementById('variant'),frame=document.getElementById('preview');select.addEventListener('change',()=>{frame.src=select.value+'.html';document.getElementById('standalone').href=select.value+'.html';document.getElementById('plain').href=select.value+'.txt'});document.querySelectorAll('[data-width]').forEach(b=>b.addEventListener('click',()=>{frame.style.width=b.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(o=>o.setAttribute('aria-pressed',String(o===b)))}));</script></body></html>`
