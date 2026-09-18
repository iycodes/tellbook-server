package authchallenge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func tessaDesignFixture(kind string) (string, deliveryPayload) {
	p := deliveryPayload{Destination: "amara@example.com"}
	if kind == "link" {
		p.Code, p.Link = "028461", &linkEmailPayload{PhoneSuffix: "1683"}
		return tessaLinkEmailTemplate, p
	}
	p.Security = &securityEmailPayload{Kind: kind, PhoneSuffix: "1683", OccurredAt: time.Date(2026, 9, 14, 12, 30, 0, 0, time.FixedZone("WAT", 3600))}
	return tessaSecurityEmailTemplate, p
}

func TestTessaEmailVariants(t *testing.T) {
	for _, kind := range []string{"link", "linked", "replaced", "disconnected"} {
		t.Run(kind, func(t *testing.T) {
			key, payload := tessaDesignFixture(kind)
			id := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
			message, err := renderTessaEmail(id, key, payload)
			if err != nil {
				t.Fatal(err)
			}
			prefix := "tessa-security"
			if kind == "link" {
				prefix = "tessa-link"
			}
			if message.ToEmail != payload.Destination || message.MessageID != "<"+prefix+"-"+id.String()+"@mail.tellbook.app>" {
				t.Fatal("delivery metadata changed")
			}
			if !strings.Contains(message.HTML, "<!--[if mso]>") || strings.Contains(message.HTML, "href=") {
				t.Fatal("missing Outlook fallback or unexpected action link")
			}
			if kind == "link" {
				if !strings.Contains(message.HTML, ">028461</p>") || strings.Contains(message.Subject, payload.Code) {
					t.Fatal("code must be selectable with leading zero, and absent from subject")
				}
				preheader := strings.Split(strings.Split(message.HTML, `aria-hidden="true"`)[1], "</div>")[0]
				if strings.Contains(preheader, payload.Code) {
					t.Fatal("code in preheader")
				}
				for _, body := range []string{message.HTML, message.Text} {
					for _, expected := range []string{"10 minutes after the request", "only in the Tessa WhatsApp conversation", "not a sign-in code", "ignore this email"} {
						if !strings.Contains(body, expected) {
							t.Fatalf("missing linking guidance: %s", expected)
						}
					}
					if strings.Contains(body, "after delivery") || strings.Contains(body, "RECORDED ON") {
						t.Fatal("invented event or wrong expiry")
					}
				}
			} else {
				for _, body := range []string{message.HTML, message.Text} {
					for _, expected := range []string{"14 September 2026 at 11:30 AM UTC", "not a change to your login number or booking reminder preferences", "Sign in to TellBook directly"} {
						if !strings.Contains(body, expected) {
							t.Fatalf("missing security guidance: %s", expected)
						}
					}
					if strings.Contains(body, "028461") || strings.Contains(body, "YOUR LINKING CODE") {
						t.Fatal("security event contains code")
					}
					action := map[string]string{"linked": "was connected", "replaced": "replaced the previous number", "disconnected": "was disconnected"}[kind]
					if !strings.Contains(body, action) {
						t.Fatal("incorrect event copy")
					}
				}
			}
		})
	}
}

func TestTessaEmailRejectsInvalidPayloadAndEscapesRecipient(t *testing.T) {
	for _, kind := range []string{"link", "linked"} {
		key, p := tessaDesignFixture(kind)
		p.Destination = `<img src=x onerror="alert(1)">`
		m, err := renderTessaEmail(uuid.New(), key, p)
		if err != nil || strings.Contains(m.HTML, "<img src=x") || !strings.Contains(m.HTML, "&lt;img") {
			t.Fatal("recipient is not escaped", err)
		}
		if kind == "link" {
			p.Link.PhoneSuffix = "<123"
		} else {
			p.Security.Kind = "unknown"
		}
		if _, err := renderTessaEmail(uuid.New(), key, p); err == nil {
			t.Fatal("accepted invalid payload")
		}
	}
	key, p := tessaDesignFixture("link")
	p.Code = "12345"
	if _, err := renderTessaEmail(uuid.New(), key, p); err == nil {
		t.Fatal("accepted short code")
	}
	_, p = tessaDesignFixture("linked")
	p.Code = "028461"
	if _, err := renderTessaEmail(uuid.New(), tessaSecurityEmailTemplate, p); err == nil {
		t.Fatal("accepted security event with code")
	}
	if _, err := renderTessaEmail(uuid.New(), "unknown", p); err == nil {
		t.Fatal("accepted unknown template")
	}
}

func TestWriteTessaEmailPreviews(t *testing.T) {
	directory := os.Getenv("TESSA_EMAIL_PREVIEW_DIR")
	if directory == "" {
		t.Skip("set TESSA_EMAIL_PREVIEW_DIR to generate offline previews")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"link", "linked", "replaced", "disconnected"} {
		for _, long := range []bool{false, true} {
			key, p := tessaDesignFixture(kind)
			file := kind
			if long {
				p.Destination = strings.Repeat("a", 64) + "@example.com"
				file += "-long"
			}
			m, err := renderTessaEmail(uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), key, p)
			if err != nil {
				t.Fatal(err)
			}
			for ext, body := range map[string]string{".html": m.HTML, ".txt": m.Text} {
				if err := os.WriteFile(filepath.Join(directory, file+ext), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(tessaEmailGallery), 0644); err != nil {
		t.Fatal(err)
	}
}

const tessaEmailGallery = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · Tessa emails</title><style>*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2}h1{font-size:21px;margin:0;color:#af1f4a}small{color:#584144}button,select{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;background:#fff;color:#2d2426;border-radius:5px}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff}a{color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1150px;border:0;outline:1px solid #e2d8d2;margin:auto;background:#faf9f4}</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> · Tessa emails</h1><small>Fictional data · Fictional code · Production renderer · No emails sent</small></div><select id="variant" aria-label="Tessa email type"><option value="link">Connect WhatsApp</option><option value="linked">WhatsApp connected</option><option value="replaced">Number replaced</option><option value="disconnected">Generic disconnected</option><option value="link-long">Link code — long email</option><option value="linked-long">WhatsApp connected — long email</option><option value="replaced-long">Number replaced — long email</option><option value="disconnected-long">Disconnected — long email</option></select><div><button data-width="720" aria-pressed="true">Desktop</button> <button data-width="375" aria-pressed="false">Mobile</button> <button data-width="320" aria-pressed="false">320px</button></div><a href="../index.html">Booking & payment emails</a></header><main><iframe id="preview" title="Tessa email preview" src="link.html"></iframe><p style="text-align:center"><a id="standalone" href="link.html" target="_blank">Open email</a> · <a id="plain" href="link.txt" target="_blank">Plain text</a></p></main><script>const select=document.getElementById('variant'),frame=document.getElementById('preview');select.addEventListener('change',()=>{frame.src=select.value+'.html';document.getElementById('standalone').href=select.value+'.html';document.getElementById('plain').href=select.value+'.txt'});document.querySelectorAll('[data-width]').forEach(b=>b.addEventListener('click',()=>{frame.style.width=b.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(o=>o.setAttribute('aria-pressed',String(o===b)))}));</script></body></html>`
