package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/agreements/domain"
	"github.com/google/uuid"
)

func agreementEmailFixture() lifecycleAgreement {
	accepted := time.Date(2026, 9, 14, 11, 30, 0, 0, time.UTC)
	return lifecycleAgreement{
		ID: uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), Title: "Event hairstyling service agreement",
		ConfirmationMethod: string(domain.ConfirmationMethodSignature), Status: string(domain.AgreementStatusAwaitingCustomer),
		SentToEmail: "amara@example.com", CustomerName: "Amara Okafor", BusinessName: "The Sunday Studio", AcceptedAt: &accepted,
	}
}

func TestAgreementEmailMethodAndCompletionCopy(t *testing.T) {
	for _, method := range []string{"signature", "confirmation"} {
		for _, completed := range []bool{false, true} {
			a := agreementEmailFixture()
			a.ConfirmationMethod = method
			message, err := renderAgreementEmail(a, "test-job", "https://example.com/agreement/preview-only", completed)
			if err != nil {
				t.Fatal(err)
			}
			if message.ToEmail != a.SentToEmail || message.MessageID != agreementEmailMessageID(a.ID, "test-job") {
				t.Fatal("recipient or delivery identity changed")
			}
			expected := "Review & confirm"
			if method == "signature" {
				expected = "Review & sign"
			}
			if completed {
				expected = "View agreement"
			}
			if !strings.Contains(message.Text, expected+": https://example.com/agreement/preview-only") {
				t.Fatal("incorrect agreement action")
			}
			if completed {
				status := "Confirmed & recorded"
				if method == "signature" {
					status = "Signed & recorded"
				}
				if !strings.Contains(message.Text, status) || !strings.Contains(message.HTML, "14 September 2026 at 11:30 AM UTC") {
					t.Fatal("completion record missing")
				}
			} else if strings.Contains(message.HTML, "RECORDED ON") || strings.Contains(message.HTML, "14 September 2026") {
				t.Fatal("pending email exposed acceptance time")
			}
			if !strings.Contains(message.HTML, "<!--[if mso]>") {
				t.Fatal("Outlook fallback stripped")
			}
			if method == "confirmation" && strings.Contains(message.Text, "before signing") {
				t.Fatal("confirmation was described as a signature")
			}
			if strings.Contains(message.HTML, "Download PDF") || strings.Contains(message.Text, "attached") {
				t.Fatal("email claims an unavailable attachment")
			}
		}
	}
}

func TestAgreementEmailEscapingAndMissingOptionalValues(t *testing.T) {
	a := agreementEmailFixture()
	a.Title = `Agreement <script>alert(1)</script>`
	a.BusinessName = `Studio & Co`
	a.CustomerName = ""
	a.AcceptedAt = nil
	message, err := renderAgreementEmail(a, "test-job", "https://example.com/agreement/preview?a=1&b=2", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.HTML, "<script>") || !strings.Contains(message.HTML, "&lt;script&gt;") || !strings.Contains(message.HTML, "Studio &amp; Co") {
		t.Fatal("agreement content was not escaped")
	}
	if !strings.Contains(message.Text, "Hi there,") || !strings.Contains(message.HTML, "preview?a=1&amp;b=2") || strings.Contains(message.HTML, "RECORDED ON") {
		t.Fatal("optional values or URL escaping incorrect")
	}
	message, err = renderAgreementEmail(a, "test-job", "javascript:alert(1)", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.HTML, `href="javascript:`) {
		t.Fatal("unsafe action URL became active")
	}
	a.ConfirmationMethod = "unknown"
	if _, err := renderAgreementEmail(a, "test-job", "https://example.com", false); err == nil {
		t.Fatal("unknown method should not invent agreement instructions")
	}
}

func TestWriteAgreementEmailPreviews(t *testing.T) {
	directory := os.Getenv("AGREEMENT_EMAIL_PREVIEW_DIR")
	if directory == "" {
		t.Skip("set AGREEMENT_EMAIL_PREVIEW_DIR to generate offline previews")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		file, method    string
		completed, long bool
	}{
		{"signature-request", "signature", false, false}, {"confirmation-request", "confirmation", false, false},
		{"signature-completed", "signature", true, false}, {"confirmation-completed", "confirmation", true, false},
		{"request-long", "signature", false, true}, {"completed-long", "confirmation", true, true},
	} {
		a := agreementEmailFixture()
		a.ConfirmationMethod = scenario.method
		if scenario.completed {
			a.Status = string(domain.AgreementStatusCompleted)
		}
		if scenario.long {
			a.Title = "Special occasion hairstyling, bridal party preparation & on-location service agreement"
			a.CustomerName = "Amara-Chinelo Nwankwo-Okafor"
			a.BusinessName = "The Sunday Studio & Bridal Collective — Victoria Island"
			a.SentToEmail = strings.Repeat("a", 64) + "@example.com"
		}
		message, err := renderAgreementEmail(a, "preview-only", "https://example.com/agreement/preview-only", scenario.completed)
		if err != nil {
			t.Fatal(err)
		}
		for extension, body := range map[string]string{".html": message.HTML, ".txt": message.Text} {
			if err := os.WriteFile(filepath.Join(directory, scenario.file+extension), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(agreementEmailGallery), 0644); err != nil {
		t.Fatal(err)
	}
}

const agreementEmailGallery = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · Agreement emails</title><style>*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2}h1{font-size:21px;margin:0;color:#af1f4a}small{color:#584144}button,select{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;background:#fff;color:#2d2426;border-radius:5px}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff}a{color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1580px;border:0;outline:1px solid #e2d8d2;margin:auto;background:#faf9f4}</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> · Agreement emails</h1><small>Fictional data · Production renderer · No emails sent</small></div><select id="variant" aria-label="Agreement email type"><option value="signature-request">Review & sign</option><option value="confirmation-request">Review & confirm</option><option value="signature-completed">Signature completed</option><option value="confirmation-completed">Confirmation completed</option><option value="request-long">Request — long content</option><option value="completed-long">Completed — long content</option></select><div><button data-width="720" aria-pressed="true">Desktop</button> <button data-width="375" aria-pressed="false">Mobile</button> <button data-width="320" aria-pressed="false">320px</button></div><a href="../index.html">Booking & payment emails</a></header><main><iframe id="preview" title="Agreement email preview" src="signature-request.html"></iframe><p style="text-align:center"><a id="standalone" href="signature-request.html" target="_blank">Open email</a> · <a id="plain" href="signature-request.txt" target="_blank">Plain text</a></p></main><script>const select=document.getElementById('variant'),frame=document.getElementById('preview');select.addEventListener('change',()=>{frame.src=select.value+'.html';document.getElementById('standalone').href=select.value+'.html';document.getElementById('plain').href=select.value+'.txt'});document.querySelectorAll('[data-width]').forEach(b=>b.addEventListener('click',()=>{frame.style.width=b.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(o=>o.setAttribute('aria-pressed',String(o===b)))}));</script></body></html>`
