package welcomeemail

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	texttemplate "text/template"
)

// This authoring template uses [[ ]] for fixed design copy. The resulting
// {{name}}, {{email}}, and {{action_url}} placeholders are resolved by the
// production assignment renderer when the version is activated in the database.
//
//go:embed templates/welcome.html
var welcomeDesignSource string

type welcomeStep struct{ Number, Title, Body string }
type welcomeDesign struct {
	Audience, Name, Subject, Preheader, Eyebrow, HeadlineOne, HeadlineTwo string
	AccountNote, Intro, Action, StepsTitle, Closing, Footnote, Fallback   string
	Steps                                                                 []welcomeStep
}

func welcomeDesigns() []welcomeDesign {
	return []welcomeDesign{
		{
			Audience: AudienceProvider, Name: "Provider welcome — branded v2",
			Subject:   "Welcome to TellBook — make room for your best work",
			Preheader: "Your provider account is ready. Let’s make it yours.",
			Eyebrow:   "FOR THE WORK YOU LOVE", HeadlineOne: "More time for", HeadlineTwo: "your best work.",
			AccountNote: "Your provider account is ready",
			Intro:       "Welcome to TellBook. You bring the skill; we’ll help you keep the bookings, payments, and conversations in one place.",
			Action:      "Open my workspace", StepsTitle: "Make yourself at home.",
			Steps: []welcomeStep{
				{"01", "Make it yours", "Add your business details, location, and availability."},
				{"02", "Create your first service", "Set your pricing and explain what customers can expect."},
				{"03", "Share your booking page", "Give customers one place to choose a service and book with you."},
			},
			Closing:  "Start with one service. You can build the rest as you go.",
			Footnote: "Your workspace is ready for you to set up. Your services become bookable when you publish them.",
			Fallback: "Go to my workspace",
		},
		{
			Audience: AudienceMarketplaceCustomer, Name: "Customer welcome — branded v2",
			Subject:   "Welcome to TellBook — good plans start here",
			Preheader: "Find your next service, choose your time, and keep your plans together.",
			Eyebrow:   "FOR YOUR NEXT GOOD PLAN", HeadlineOne: "Good plans", HeadlineTwo: "start here.",
			AccountNote: "Your customer account is ready",
			Intro:       "Find a service you’ll love, book a time that works, and keep the details close. Welcome to TellBook.",
			Action:      "Explore services", StepsTitle: "Your next booking, made simpler.",
			Steps: []welcomeStep{
				{"01", "Find your kind of service", "Explore providers, compare services, and pick what fits."},
				{"02", "Choose a time that works", "Review the details and complete the booking steps."},
				{"03", "Keep your plans together", "Your bookings, saved favourites, and conversations stay in your account."},
			},
			Closing:  "Something for your everyday. Something for a special day. We’re glad to be part of your plans.",
			Footnote: "This is your account welcome. You’ll get a separate update when you make a booking.",
			Fallback: "Find my next service",
		},
	}
}

func buildWelcomeDesign(d welcomeDesign) (string, string, error) {
	// Inputs are fixed author-owned copy, never recipient data. Literal runtime
	// placeholders remain untouched for the production assignment renderer.
	renderer, err := texttemplate.New("welcome").Delims("[[", "]]").Parse(welcomeDesignSource)
	if err != nil {
		return "", "", err
	}
	var body bytes.Buffer
	if err := renderer.Execute(&body, d); err != nil {
		return "", "", err
	}
	var plain strings.Builder
	fmt.Fprintf(&plain, "Hi {{name}},\n\n%s\n\n%s: {{action_url}}\n\n%s\n", d.Intro, d.Action, d.StepsTitle)
	for _, step := range d.Steps {
		fmt.Fprintf(&plain, "\n%s. %s\n%s\n", step.Number, step.Title, step.Body)
	}
	fmt.Fprintf(&plain, "\n%s\n\nGlad you’re here,\nThe TellBook team\n\n%s\n\nAccount: {{email}}\n", d.Closing, d.Footnote)
	return body.String(), plain.String(), nil
}

func welcomeDesignMigration() (string, error) {
	var sql strings.Builder
	sql.WriteString("-- migrate:up\n-- Generated from internal/welcomeemail/templates/welcome.html and design_test.go.\n-- Drafts only: deploy URL-aware assignment code before activating either version.\n-- Existing active templates and queued message snapshots are unchanged.\n\n")
	for _, d := range welcomeDesigns() {
		markup, plain, err := buildWelcomeDesign(d)
		if err != nil {
			return "", err
		}
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		fmt.Fprintf(&sql, "INSERT INTO welcome_email_templates (audience,version,name,subject_template,html_template,text_template,status)\nVALUES (%s,2,%s,%s,$welcome_html$%s$welcome_html$,$welcome_text$%s$welcome_text$,'draft')\nON CONFLICT (audience,version) DO NOTHING;\n\n", quote(d.Audience), quote(d.Name), quote(d.Subject), markup, plain)
	}
	sql.WriteString("-- migrate:down\n-- Keep version rows because immutable sent/queued jobs may reference them.\n-- Restore a previous active version explicitly if rolling back an activation.\nUPDATE welcome_email_templates SET status='archived',updated_at=NOW()\nWHERE version=2 AND name IN ('Provider welcome — branded v2','Customer welcome — branded v2');\n")
	return sql.String(), nil
}

func TestWelcomeDesignMigrationMatchesSource(t *testing.T) {
	expected, err := welcomeDesignMigration()
	if err != nil {
		t.Fatal(err)
	}
	path := "../../db/migrations/20260913010000_welcome_email_design_v2.sql"
	if os.Getenv("UPDATE_WELCOME_EMAIL_MIGRATION") == "true" {
		if err := os.WriteFile(path, []byte(expected), 0644); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != expected {
		t.Fatal("welcome migration differs from the design; regenerate with UPDATE_WELCOME_EMAIL_MIGRATION=true")
	}
}

func TestWelcomeDesignRuntimeRendering(t *testing.T) {
	for _, d := range welcomeDesigns() {
		t.Run(d.Audience, func(t *testing.T) {
			markup, plain, err := buildWelcomeDesign(d)
			if err != nil {
				t.Fatal(err)
			}
			action := "https://example.com/start?source=welcome&audience=" + d.Audience
			rendered, err := renderTemplate(markup, `<Amara & Co>`, "amara@example.com", action, true)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(rendered, "{{") || strings.Contains(rendered, "<Amara") || !strings.Contains(rendered, html.EscapeString(action)) {
				t.Fatal("runtime values were not safely substituted")
			}
			if !strings.Contains(rendered, "<!--[if mso]>") {
				t.Fatal("Outlook fallback missing")
			}
			text, err := renderTemplate(plain, "Amara", "amara@example.com", action, false)
			if err != nil || !strings.Contains(text, action) {
				t.Fatal("plain-text action missing", err)
			}
		})
	}
}

func TestWriteWelcomeEmailPreviews(t *testing.T) {
	directory := os.Getenv("WELCOME_EMAIL_PREVIEW_DIR")
	if directory == "" {
		t.Skip("set WELCOME_EMAIL_PREVIEW_DIR to write the gallery")
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, d := range welcomeDesigns() {
		markup, plain, err := buildWelcomeDesign(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, edge := range []bool{false, true} {
			name, email, suffix := "Amara", "amara@example.com", ""
			if d.Audience == AudienceProvider {
				name = "Sunday Studio"
			}
			if edge {
				name = "Amara-Chinelo Nwankwo-Okafor & The Sunday Studio"
				email = strings.Repeat("a", 64) + "@example.com"
				suffix = "-long"
			}
			for ext, source := range map[string]string{".html": markup, ".txt": plain} {
				rendered, err := renderTemplate(source, name, email, "https://example.com/"+d.Audience, ext == ".html")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, d.Audience+suffix+ext), []byte(rendered), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(welcomeGallery), 0644); err != nil {
		t.Fatal(err)
	}
}

const welcomeGallery = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>TellBook · Welcome emails</title><style>*{box-sizing:border-box}body{margin:0;background:#efeee7;color:#2d2426;font:14px Arial,sans-serif}header{display:flex;gap:16px;align-items:center;flex-wrap:wrap;padding:22px 28px;background:#fff;border-bottom:1px solid #e2d8d2}h1{font-size:21px;margin:0;color:#af1f4a}small{color:#584144}button,select{font:inherit;padding:10px 12px;border:1px solid #e2d8d2;background:#fff;color:#2d2426;border-radius:5px}button{cursor:pointer}button[aria-pressed=true]{background:#af1f4a;color:#fff}a{color:#af1f4a}main{padding:24px 12px}iframe{display:block;width:720px;max-width:100%;height:1580px;border:0;outline:1px solid #e2d8d2;margin:auto;background:#faf9f4}</style></head><body><header><div><h1>Tell<span style="color:#2d2426">Book</span> · Welcome emails</h1><small>Fictional data · Version 2 drafts · No emails sent</small></div><select id="variant" aria-label="Audience"><option value="provider">Provider</option><option value="marketplace_customer">Customer</option><option value="provider-long">Provider — long content</option><option value="marketplace_customer-long">Customer — long content</option></select><div><button data-width="720" aria-pressed="true">Desktop</button> <button data-width="375" aria-pressed="false">Mobile</button> <button data-width="320" aria-pressed="false">320px</button></div><a href="../index.html">Booking & payment emails</a></header><main><iframe id="preview" title="Welcome email preview" src="provider.html"></iframe><p style="text-align:center"><a id="standalone" href="provider.html" target="_blank">Open email</a> · <a id="plain" href="provider.txt" target="_blank">Plain text</a></p></main><script>const select=document.getElementById('variant'),frame=document.getElementById('preview');select.addEventListener('change',()=>{frame.src=select.value+'.html';document.getElementById('standalone').href=select.value+'.html';document.getElementById('plain').href=select.value+'.txt'});document.querySelectorAll('[data-width]').forEach(b=>b.addEventListener('click',()=>{frame.style.width=b.dataset.width+'px';document.querySelectorAll('[data-width]').forEach(o=>o.setAttribute('aria-pressed',String(o===b)))}));</script></body></html>`
