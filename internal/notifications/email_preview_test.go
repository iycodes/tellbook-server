package notifications

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func emailPreviewData() emailTemplateData {
	return emailTemplateData{
		Audience: "customer", Type: "customer_booking_secured", BookingStatus: "confirmed",
		RecipientEmail: "amara@example.com", RecipientName: "Amara",
		CustomerName: "Amara Okafor", ProviderName: "The Sunday Studio",
		ServiceTitle: "Signature braids & finishing",
		When:         "Friday, 18 September 2026 at 10:00 AM WAT", Location: "14 Adeola Odeku Street, Victoria Island, Lagos",
		Total: "₦45,000.00", Paid: "₦15,000.00", Due: "₦30,000.00",
		ProviderContactPhone: "+234 800 000 0000", ActionURL: "https://example.com/bookings#claim=preview-only",
	}
}

func TestNotificationLayoutRendersEveryAudienceAndEvent(t *testing.T) {
	for key := range emailTemplateRegistry {
		t.Run(key, func(t *testing.T) {
			data := emailPreviewData()
			parts := strings.SplitN(key, ":", 2)
			data.Audience, data.Type = parts[0], parts[1]
			message, err := renderEmailTemplate(uuid.New(), data)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message.HTML, data.ActionURL) || !strings.Contains(message.Text, data.ActionURL) {
				t.Fatal("booking action missing from HTML or plain text")
			}
			if !strings.Contains(message.HTML, "<!--[if mso]>") || !strings.Contains(message.HTML, `width="600"`) {
				t.Fatal("Outlook fixed-width fallback was stripped during rendering")
			}
			if strings.Contains(message.HTML, "ZgotmplZ") || len(message.HTML) > 60000 {
				t.Fatal("invalid template value or unexpectedly large email")
			}
			balance, appointment := strings.Index(message.HTML, "BOOKING BALANCE"), strings.Index(message.HTML, "THE APPOINTMENT")
			if balance < 0 || appointment < 0 || (balance < appointment) != strings.HasPrefix(data.Type, "payment_") {
				t.Fatal("balance/appointment order does not match the email purpose")
			}
		})
	}
}

func TestNotificationLayoutEscapesContentAndRejectsUnsafeLink(t *testing.T) {
	data := emailPreviewData()
	data.RecipientName = `<img src=x onerror="alert(1)">`
	data.ProviderName = `Studio </td><script>alert(1)</script>`
	data.ActionURL = `javascript:alert(1)`
	message, err := renderEmailTemplate(uuid.New(), data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.HTML, "<img src=x") || strings.Contains(message.HTML, "<script") || strings.Contains(message.HTML, `href="javascript:`) {
		t.Fatal("untrusted content became active HTML")
	}
	if !strings.Contains(message.HTML, "&lt;img") || !strings.Contains(message.HTML, "&lt;script&gt;") {
		t.Fatal("escaped content missing")
	}
}

// TestWriteEmailPreviews is an opt-in, offline design tool. It renders the same
// templates as the delivery worker with fictional data; it never sends email.
func TestWriteEmailPreviews(t *testing.T) {
	directory := os.Getenv("EMAIL_PREVIEW_DIR")
	if directory == "" {
		t.Skip("set EMAIL_PREVIEW_DIR to generate the offline email gallery")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(emailTemplateRegistry))
	for key := range emailTemplateRegistry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	type entry struct{ Name, File, Subject string }
	entries := make([]entry, 0, len(keys)+2)
	write := func(key string, data emailTemplateData) {
		t.Helper()
		message, err := renderEmailTemplate(uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), data)
		if err != nil {
			t.Fatal(err)
		}
		name := strings.ReplaceAll(key, ":", "-")
		for extension, body := range map[string]string{".html": message.HTML, ".txt": message.Text} {
			if err := os.WriteFile(filepath.Join(directory, name+extension), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		entries = append(entries, entry{key, name + ".html", message.Subject})
	}
	for _, key := range keys {
		parts := strings.SplitN(key, ":", 2)
		data := emailPreviewData()
		data.Audience, data.Type = parts[0], parts[1]
		if data.Audience == "provider" {
			data.RecipientName, data.RecipientEmail = "Sunday Studio", "studio@example.com"
			data.ActionURL = "https://example.com/bookings?booking=preview-only"
		}
		if data.Type == "payment_satisfied" {
			data.Paid, data.Due = data.Total, "₦0.00"
		}
		if data.Type == "booking_cancelled" || data.Type == "booking_expired" {
			data.BookingStatus = strings.TrimPrefix(data.Type, "booking_")
		}
		write(key, data)
	}
	pending := emailPreviewData()
	pending.Audience, pending.Type, pending.BookingStatus = "provider", "appointment_reminder", "pending"
	pending.RecipientName, pending.RecipientEmail = "Sunday Studio", "studio@example.com"
	write("edge:awaiting-confirmation", pending)
	long := emailPreviewData()
	long.RecipientName = "Amara-Chinelo Nwankwo-Okafor"
	long.ServiceTitle = "The complete occasion package: signature braids, colour consultation & finishing with accessories"
	long.ProviderName = "The Sunday Studio & Bridal Collective — Victoria Island"
	long.Location = "Suite 1204, The Waterfront Centre, 14 Adeola Odeku Street, Victoria Island, Lagos, Nigeria"
	long.Total, long.Paid, long.Due = "₦125,450,000.00", "₦45,450,000.00", "₦80,000,000.00"
	long.RecipientEmail = strings.Repeat("a", 64) + "@example.com"
	write("edge:long-content", long)
	file, err := os.Create(filepath.Join(directory, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := template.Must(template.New("gallery").Parse(emailGalleryHTML)).Execute(file, entries); err != nil {
		t.Fatal(err)
	}
	fmt.Println("Email previews: " + filepath.Join(directory, "index.html"))
}

const emailGalleryHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · Email studio</title>
<style>
*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2;display:flex;gap:24px;align-items:center;flex-wrap:wrap}h1{font-size:21px;letter-spacing:-.5px;margin:0;color:#af1f4a}small{color:#584144}select,button{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;border-radius:5px;background:#fff;color:#2d2426}select{max-width:100%}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff;border-color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1450px;margin:auto;border:1px solid #e2d8d2;background:#faf9f4}#subject{text-align:center;margin:0 0 18px;font-size:13px}a{color:#af1f4a}
</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> <span style="color:#2d2426;font-weight:400">Email studio</span></h1><small>Fictional data · Production renderer · No emails sent</small></div><select id="variant" aria-label="Email variant">{{range .}}<option value="{{.File}}" data-subject="{{.Subject}}">{{.Name}}</option>{{end}}</select><div><button type="button" data-width="720" aria-pressed="true">Desktop</button> <button type="button" data-width="375" aria-pressed="false">Mobile</button> <button type="button" data-width="320" aria-pressed="false">320px</button></div><a href="welcome/index.html">Welcome emails</a> <a href="auth/index.html">Verification emails</a> <a href="agreements/index.html">Agreement emails</a> <a href="tessa/index.html">Tessa emails</a> <a href="security/index.html">Account security</a> <a href="payouts/index.html">Provider payouts</a> <a href="reminders/index.html">Outstanding steps</a> <a href="completion/index.html">Completion & reviews</a> <a href="refunds/index.html">Refund progress</a></header><main><p id="subject"></p><iframe id="preview" title="Email preview" src="customer-customer_booking_secured.html"></iframe><p style="text-align:center"><a id="standalone" href="customer-customer_booking_secured.html" target="_blank">Open email</a> · <a id="plain" href="customer-customer_booking_secured.txt" target="_blank">Plain text</a></p></main><script>
const selector=document.getElementById('variant'),frame=document.getElementById('preview');selector.value='customer-customer_booking_secured.html';function update(){frame.src=selector.value;document.getElementById('subject').textContent='Subject: '+selector.selectedOptions[0].dataset.subject;document.getElementById('standalone').href=selector.value;document.getElementById('plain').href=selector.value.replace('.html','.txt')}selector.addEventListener('change',update);document.querySelectorAll('[data-width]').forEach(button=>button.addEventListener('click',()=>{frame.style.width=button.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(other=>other.setAttribute('aria-pressed',String(other===button)))}));update();
</script></body></html>`
