package authchallenge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestAuthCodeEmailPurposesAndLegacyPayload(t *testing.T) {
	cases := []struct{ purpose, subject string }{
		{PurposeSignIn, "Your TellBook sign-in code"},
		{PurposePasswordReset, "Reset your TellBook password"},
		{PurposeLinkIdentity, "Verify your email for TellBook"},
		{"", "Your TellBook verification code"},
		{"future_purpose", "Your TellBook verification code"},
	}
	for _, tc := range cases {
		t.Run(tc.purpose, func(t *testing.T) {
			payload := deliveryPayload{Destination: "amara@example.com", Code: "028461", Purpose: tc.purpose}
			id := uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
			message, err := renderAuthCodeEmail(id, payload)
			if err != nil {
				t.Fatal(err)
			}
			if message.Subject != tc.subject || message.ToEmail != payload.Destination || message.MessageID != "<auth-code-"+id.String()+"@mail.tellbook.app>" {
				t.Fatal("delivery metadata changed")
			}
			if !strings.Contains(message.HTML, ">028461</p>") || !strings.Contains(message.Text, "\n\n028461\n\n") {
				t.Fatal("code must be contiguous selectable text with its leading zero")
			}
			if strings.Contains(message.Subject, payload.Code) || strings.Contains(message.HTML, "href=") {
				t.Fatal("code appeared in subject or email contains an action link")
			}
			for _, body := range []string{message.HTML, message.Text} {
				if !strings.Contains(body, "10 minutes after delivery") || !strings.Contains(body, "ignore this email") {
					t.Fatal("expiry or unrequested-code guidance missing")
				}
			}
			if !strings.Contains(message.HTML, "<!--[if mso]>") {
				t.Fatal("Outlook fallback stripped")
			}
		})
	}
	var old deliveryPayload
	if err := json.Unmarshal([]byte(`{"destination":"amara@example.com","code":"028461"}`), &old); err != nil {
		t.Fatal(err)
	}
	message, err := renderAuthCodeEmail(uuid.New(), old)
	if err != nil || message.Subject != "Your TellBook verification code" {
		t.Fatal("legacy encrypted payload is not compatible", err)
	}
}

func TestAuthCodeEmailRejectsInvalidOrDifferentPurposePayload(t *testing.T) {
	for _, payload := range []deliveryPayload{
		{Code: "12345"}, {Code: "1234567"}, {Code: "<script>"},
		{Code: "028461", Link: &linkEmailPayload{PhoneSuffix: "1683"}},
		{Code: "028461", Security: &securityEmailPayload{}},
	} {
		if _, err := renderAuthCodeEmail(uuid.New(), payload); err == nil {
			t.Fatal("accepted invalid code or Tessa payload")
		}
	}
	message, err := renderAuthCodeEmail(uuid.New(), deliveryPayload{Code: "028461", Destination: `<img src=x onerror="alert(1)">`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.HTML, "<img src=x") || !strings.Contains(message.HTML, "&lt;img") {
		t.Fatal("recipient content was not escaped")
	}
}

func TestWriteAuthCodeEmailPreviews(t *testing.T) {
	directory := os.Getenv("AUTH_CODE_EMAIL_PREVIEW_DIR")
	if directory == "" {
		t.Skip("set AUTH_CODE_EMAIL_PREVIEW_DIR to generate offline previews")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{PurposeSignIn, PurposePasswordReset, PurposeLinkIdentity, ""} {
		file := purpose
		if file == "" {
			file = "verification"
		}
		for _, long := range []bool{false, true} {
			email, suffix := "amara@example.com", ""
			if long {
				email = strings.Repeat("a", 64) + "@example.com"
				suffix = "-long"
			}
			message, err := renderAuthCodeEmail(uuid.MustParse("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), deliveryPayload{Destination: email, Code: "028461", Purpose: purpose})
			if err != nil {
				t.Fatal(err)
			}
			for extension, body := range map[string]string{".html": message.HTML, ".txt": message.Text} {
				if err := os.WriteFile(filepath.Join(directory, file+suffix+extension), []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(authCodeGallery), 0644); err != nil {
		t.Fatal(err)
	}
}

const authCodeGallery = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · Verification emails</title><style>*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2}h1{font-size:21px;margin:0;color:#af1f4a}small{color:#584144}button,select{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;background:#fff;color:#2d2426;border-radius:5px}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff}a{color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1150px;border:0;outline:1px solid #e2d8d2;margin:auto;background:#faf9f4}</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> · Verification emails</h1><small>Fictional data · Fictional code · Production renderer · No emails sent</small></div><select id="variant" aria-label="Verification type"><option value="sign_in">Sign-in code</option><option value="password_reset">Password reset</option><option value="link_identity">Link email address</option><option value="verification">Generic verification</option><option value="sign_in-long">Sign-in — long email</option><option value="password_reset-long">Password reset — long email</option><option value="link_identity-long">Link email — long email</option><option value="verification-long">Verification — long email</option></select><div><button data-width="720" aria-pressed="true">Desktop</button> <button data-width="375" aria-pressed="false">Mobile</button> <button data-width="320" aria-pressed="false">320px</button></div><a href="../index.html">Booking & payment emails</a></header><main><iframe id="preview" title="Verification email preview" src="sign_in.html"></iframe><p style="text-align:center"><a id="standalone" href="sign_in.html" target="_blank">Open email</a> · <a id="plain" href="sign_in.txt" target="_blank">Plain text</a></p></main><script>const select=document.getElementById('variant'),frame=document.getElementById('preview');select.addEventListener('change',()=>{frame.src=select.value+'.html';document.getElementById('standalone').href=select.value+'.html';document.getElementById('plain').href=select.value+'.txt'});document.querySelectorAll('[data-width]').forEach(b=>b.addEventListener('click',()=>{frame.style.width=b.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(o=>o.setAttribute('aria-pressed',String(o===b)))}));</script></body></html>`
