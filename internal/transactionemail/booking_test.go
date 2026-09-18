package transactionemail

import (
	"bytes"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/money"
)

func bookingFixture() BookingDetails {
	return BookingDetails{Event: fixtureEvent(), Service: "Signature cut & finish", Provider: "The Sunday Studio", StartsAt: time.Date(2026, 9, 16, 13, 0, 0, 0, time.UTC), Timezone: "Africa/Lagos", BookingURL: "https://example.com/bookings?booking=aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
}
func reminderFixture(kind ReminderKind) ReminderInput {
	b := bookingFixture()
	due := b.OccurredAt.Add(24 * time.Hour)
	return ReminderInput{BookingDetails: b, Kind: kind, BookingActive: true, StepOutstanding: true, CheckedAt: b.OccurredAt, DueMinor: 2500000, Currency: money.FormatSpec{CurrencyCode: "NGN", Exponent: 2, DecimalSeparator: ".", GroupingSeparator: ",", SymbolPosition: money.SymbolBefore, SpaceBetweenSymbol: true}, AgreementMethod: "signature", Deadline: &due}
}
func completionFixture(review bool) CompletionInput {
	b := bookingFixture()
	b.StartsAt = b.StartsAt.AddDate(0, 0, -4)
	return CompletionInput{BookingDetails: b, BookingCompleted: true, ReviewEnabled: review, ReviewEligible: true, ReviewURL: "https://example.com/booking/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/review"}
}

func TestReminderContentAndEligibility(t *testing.T) {
	for _, kind := range []ReminderKind{DepositReminder, BalanceReminder, AgreementReminder} {
		in := reminderFixture(kind)
		m, err := RenderReminder(in)
		if err != nil {
			t.Fatal(err)
		}
		if m.MessageID != "<booking-step-"+in.DeliveryID.String()+"@mail.tellbook.app>" || m.ToEmail != in.Recipient {
			t.Fatal("delivery identity changed")
		}
		for _, body := range []string{m.HTML, m.Text} {
			body = html.UnescapeString(body)
			for _, expected := range []string{"16 September 2026", "2:00 PM WAT (UTC+01:00)", "Complete by", "15 September 2026"} {
				if !strings.Contains(body, expected) {
					t.Fatalf("missing %q", expected)
				}
			}
			if kind == AgreementReminder {
				if strings.Contains(body, "25,000.00") || !strings.Contains(body, "needs your signature") {
					t.Fatal("agreement contains unrelated payment or wrong method")
				}
			} else if !strings.Contains(body, "25,000.00") || !strings.Contains(body, "before making another payment") {
				t.Fatal("payment amount or duplicate-payment guidance missing")
			}
		}
		in.Deadline = nil
		m, err = RenderReminder(in)
		if err != nil || strings.Contains(m.HTML, "Complete by") || strings.Contains(m.Text, "Complete by") {
			t.Fatal("invented deadline", err)
		}
		in.StepOutstanding = false
		if _, err = RenderReminder(in); err == nil {
			t.Fatal("completed step remained eligible")
		}
		in.StepOutstanding = true
		in.BookingActive = false
		if _, err = RenderReminder(in); err == nil {
			t.Fatal("inactive booking remained eligible")
		}
	}
	in := reminderFixture(AgreementReminder)
	in.AgreementMethod = "confirmation"
	m, err := RenderReminder(in)
	if err != nil || !strings.Contains(m.Text, "needs your confirmation") || strings.Contains(m.Text, "signature") {
		t.Fatal("confirmation described as signature", err)
	}
	for _, mutate := range []func(*ReminderInput){
		func(p *ReminderInput) { p.DueMinor = 0 },
		func(p *ReminderInput) { p.CheckedAt = p.Deadline.Add(time.Minute) },
		func(p *ReminderInput) { p.CheckedAt = time.Time{} },
		func(p *ReminderInput) { p.Kind = "unknown" },
		func(p *ReminderInput) { p.Timezone = "Wrong/Timezone" },
		func(p *ReminderInput) { p.BookingURL = "javascript:alert(1)" },
	} {
		p := reminderFixture(DepositReminder)
		mutate(&p)
		if _, err := RenderReminder(p); err == nil {
			t.Fatal("accepted invalid reminder")
		}
	}
}

func TestCompletionReviewGates(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, eligible := range []bool{false, true} {
			for _, submitted := range []bool{false, true} {
				in := completionFixture(enabled)
				in.ReviewEligible = eligible
				in.ReviewAlreadySubmitted = submitted
				m, err := RenderCompletion(in)
				if err != nil {
					t.Fatal(err)
				}
				wantReview := enabled && eligible && !submitted
				if strings.Contains(m.HTML, `href="`+in.ReviewURL+`"`) != wantReview || strings.Contains(m.Text, "Share your experience:") != wantReview {
					t.Fatal("review CTA does not respect eligibility")
				}
				if !wantReview && (!strings.Contains(m.Text, "View booking:") || strings.Contains(m.Text, "Leaving a review")) {
					t.Fatal("completion fallback is not neutral")
				}
				if !strings.Contains(m.Text, "For payment and refund updates") {
					t.Fatal("completion implies financial settlement")
				}
				if m.MessageID != "<booking-completed-"+in.DeliveryID.String()+"@mail.tellbook.app>" {
					t.Fatal("completion message ID changed")
				}
			}
		}
	}
	in := completionFixture(true)
	in.BookingCompleted = false
	if _, err := RenderCompletion(in); err == nil {
		t.Fatal("incomplete booking accepted")
	}
	in = completionFixture(true)
	in.ReviewURL = "javascript:alert(1)"
	if _, err := RenderCompletion(in); err == nil {
		t.Fatal("unsafe review URL accepted")
	}
	in.ReviewEnabled = false
	if _, err := RenderCompletion(in); err != nil {
		t.Fatal("unused review URL prevented completion fallback")
	}
	in = completionFixture(true)
	in.Provider = `<script>alert(1)</script>`
	in.Service = `Cut & <img src=x onerror=alert(1)>`
	m, err := RenderCompletion(in)
	if err != nil || strings.Contains(m.HTML, "<script>alert") || strings.Contains(m.HTML, "<img src=x") || !strings.Contains(m.HTML, "&lt;script&gt;") {
		t.Fatal("booking content not escaped", err)
	}
	if !strings.Contains(m.HTML, "<!--[if mso]>") {
		t.Fatal("Outlook fallback stripped")
	}
}

func TestWriteBookingFollowupPreviews(t *testing.T) {
	root := os.Getenv("BOOKING_FOLLOWUP_EMAIL_PREVIEW_DIR")
	if root == "" {
		t.Skip("set BOOKING_FOLLOWUP_EMAIL_PREVIEW_DIR to generate designs")
	}
	type item struct{ File, Label string }
	type gallery struct {
		Title, First string
		Items        []item
	}
	galleries := map[string]*gallery{"reminders": {Title: "Outstanding booking steps", First: "deposit"}, "completion": {Title: "Completion & reviews", First: "completed"}}
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
	for _, kind := range []ReminderKind{DepositReminder, BalanceReminder, AgreementReminder} {
		m, err := RenderReminder(reminderFixture(kind))
		write("reminders", string(kind), string(kind)+" reminder", m, err)
	}
	in := reminderFixture(AgreementReminder)
	in.AgreementMethod = "confirmation"
	m, err := RenderReminder(in)
	write("reminders", "agreement-confirmation", "Agreement — confirmation", m, err)
	in = reminderFixture(BalanceReminder)
	in.Deadline = nil
	m, err = RenderReminder(in)
	write("reminders", "no-deadline", "Balance — no deadline", m, err)
	in = reminderFixture(DepositReminder)
	in.Service = "Full bridal consultation, styling trial and personalised event preparation"
	in.Provider = "The Sunday Studio for Hair, Beauty and Special Occasions"
	in.Recipient = strings.Repeat("a", 64) + "@example.com"
	in.DueMinor = 987654321012
	m, err = RenderReminder(in)
	write("reminders", "long-details", "Long details & large amount", m, err)
	c := completionFixture(false)
	m, err = RenderCompletion(c)
	write("completion", "completed", "Completed — no review invitation", m, err)
	c = completionFixture(true)
	m, err = RenderCompletion(c)
	write("completion", "review", "Completed + review invitation (future)", m, err)
	c.ReviewAlreadySubmitted = true
	m, err = RenderCompletion(c)
	write("completion", "already-reviewed", "Already reviewed — invitation suppressed", m, err)
	c = completionFixture(true)
	c.Service = in.Service
	c.Provider = in.Provider
	c.Recipient = in.Recipient
	m, err = RenderCompletion(c)
	write("completion", "long-details", "Review invitation — long details (future)", m, err)
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
