package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/config"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Live evidence stays in a dedicated database across separate go test processes.
// Neither a failed/ambiguous delivery nor an accepted delivery is automatically resubmitted.
func TestNotificationLiveConformance(t *testing.T) {
	phase := os.Getenv("RUN_NOTIFICATION_LIVE_CONFORMANCE")
	if phase == "" {
		t.Skip("opt-in live notification certification is disabled")
	}
	if phase != "email" && phase != "whatsapp" && phase != "verify" {
		t.Fatal("phase must be email, whatsapp or verify")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	u, err := url.Parse(databaseURL)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
		!strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "tellbook_notification_live_") {
		t.Fatal("a dedicated local tellbook_notification_live_* database is required")
	}
	email := strings.TrimSpace(os.Getenv("NOTIFICATION_LIVE_EMAIL"))
	phone := strings.TrimSpace(os.Getenv("NOTIFICATION_LIVE_WHATSAPP"))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || !strings.HasPrefix(phone, "+") {
		t.Fatal("explicit authorized email and E.164 WhatsApp destinations are required")
	}
	t.Chdir("../..")
	if err := config.LoadDotEnv(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	keys := []string{string(whatsapp.TemplateProviderNewBooking), string(whatsapp.TemplateProviderBookingReminder)}
	repo, err := NewRepository(pool, cfg.NotificationDestinationHMACKey, keys, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var foreign int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_deliveries WHERE idempotency_key NOT LIKE 'live-cert:%'`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Fatal("refusing a database containing unrelated notification deliveries")
	}
	if phase == "verify" {
		worker := NewWhatsAppWorker(repo, nil, nil, nil, nil, 1, 15*time.Second, false)
		worker.processStatusCycle(ctx)
		for _, channel := range []string{"email", "whatsapp"} {
			jobs, err := repo.ClaimDeliveries(ctx, channel, "live-cert-restarted-"+uuid.NewString(), 100, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 0 {
				t.Fatalf("restart found %d sendable %s deliveries; no network send was attempted", len(jobs), channel)
			}
		}
		var emails, whatsapps, unaccepted, repeated int
		err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE channel='email'),COUNT(*) FILTER(WHERE channel='whatsapp'),
			COUNT(*) FILTER(WHERE status NOT IN ('accepted','sent','delivered','read')),COUNT(*) FILTER(WHERE attempt_count<>1)
			FROM notification_deliveries`).Scan(&emails, &whatsapps, &unaccepted, &repeated)
		if err != nil {
			t.Fatal(err)
		}
		if emails != len(emailTemplateRegistry) || whatsapps != 2 || unaccepted != 0 || repeated != 0 {
			t.Fatalf("incomplete evidence: email=%d/%d whatsapp=%d/2 unaccepted=%d repeated=%d", emails, len(emailTemplateRegistry), whatsapps, unaccepted, repeated)
		}
		var callbackEvidence int
		err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM notification_deliveries d WHERE channel='whatsapp'
			AND status IN ('delivered','read') AND EXISTS(SELECT 1 FROM meta_whatsapp_webhook_receipts r
			WHERE r.wamid=d.provider_message_id AND r.message_status='sent')
			AND EXISTS(SELECT 1 FROM meta_whatsapp_webhook_receipts r WHERE r.wamid=d.provider_message_id AND r.message_status='delivered')`).Scan(&callbackEvidence)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("separate-process restart: %d email and %d WhatsApp rows retained; attempts=1; zero reclaimable sends", emails, whatsapps)
		if callbackEvidence != 2 {
			t.Fatalf("Meta sent+delivered callback evidence exists for %d/2 templates; do not resend", callbackEvidence)
		}
		t.Log("both approved provider templates have real sent and delivered receipts")
		return
	}
	clientID := liveNotificationID("provider")
	customerID := liveNotificationID("customer")
	_, err = pool.Exec(ctx, `INSERT INTO clients(id,full_name,email,password_hash,email_verified_at)
		VALUES($1,'TellBook certification provider',$2,'not-a-login-hash',NOW()) ON CONFLICT(id) DO NOTHING`, clientID, email)
	if err != nil {
		t.Fatal(err)
	}
	var storedEmail string
	if err := pool.QueryRow(ctx, `SELECT email FROM clients WHERE id=$1`, clientID).Scan(&storedEmail); err != nil || storedEmail != email {
		t.Fatal("fixture destination differs from authorized recipient")
	}
	_, err = pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name,email,phone)
		VALUES($1,$2,'TellBook certification customer',$3,$4) ON CONFLICT(id) DO NOTHING`, customerID, clientID, email, phone)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO provider_notification_preferences(client_id,booking_email,booking_whatsapp,whatsapp_e164,
		whatsapp_verified_at,whatsapp_verification_method,whatsapp_verification_revision)
		VALUES($1,TRUE,TRUE,$2,NOW(),'inbound_challenge',1) ON CONFLICT(client_id) DO NOTHING`, clientID, phone)
	if err != nil {
		t.Fatal(err)
	}
	// Verification/consent above is synthetic fixture evidence, not production enrollment.
	var emailSender mailer.Sender
	var waSender *whatsapp.Client
	if phase == "email" {
		emailSender, err = mailer.NewSMTPMailer(mailer.Config{Host: cfg.SMTPHost, Port: cfg.SMTPPort,
			Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, FromEmail: cfg.SMTPFromEmail, FromName: cfg.SMTPFromName,
			Security: cfg.SMTPSecurity, InsecureSkipVerify: cfg.SMTPInsecureSkipVerify, ConnectTimeout: cfg.SMTPConnectTimeout,
			SendTimeout: cfg.NotificationEmailTimeout, MaxConnections: 1})
		if err != nil || emailSender == nil || !emailSender.Enabled() {
			t.Fatal("configured SMTP sender is required")
		}
	} else {
		waSender, err = whatsapp.NewClient(whatsapp.ClientConfig{BaseURL: cfg.WhatsAppGraphBaseURL, GraphVersion: cfg.WhatsAppGraphVersion,
			PhoneNumberID: cfg.WABAPhoneNumberID, BusinessAccountID: cfg.WhatsAppBusinessAccountID, AccessToken: cfg.WABAToken,
			Timeout: cfg.WhatsAppHTTPTimeout, EnabledTemplateKeys: keys})
		if err != nil {
			t.Fatal(err)
		}
		definitions := make([]whatsapp.TemplateDefinition, 0, len(keys))
		for _, key := range keys {
			d, _ := whatsapp.LookupTemplate(whatsapp.TemplateKey(key))
			definitions = append(definitions, d)
		}
		report, err := waSender.ConformTemplateDefinitions(ctx, definitions)
		if err != nil {
			t.Fatal(err)
		}
		if !report.Valid() {
			t.Fatalf("approved template contract gate: %v", report.Errors)
		}
	}
	cases := []string{"provider:provider_new_booking", "provider:appointment_reminder"}
	if phase == "email" {
		cases = nil
		for key := range emailTemplateRegistry {
			cases = append(cases, key)
		}
		sort.Strings(cases)
	}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			deliveryID := liveNotificationID(phase + ":" + key)
			var status string
			err := pool.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE id=$1`, deliveryID).Scan(&status)
			if err == nil {
				if status == "accepted" || status == "sent" || status == "delivered" || status == "read" {
					t.Logf("retained %s %s; no resend", deliveryID, status)
					return
				}
				t.Fatalf("existing delivery %s is %s; operator review required, not automatically resent", deliveryID, status)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			liveNotificationFixture(t, ctx, pool, repo, clientID, customerID, deliveryID, phase, key, email)
			jobs, err := repo.ClaimDeliveries(ctx, phase, "live-cert-"+deliveryID.String(), 1, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) != 1 || jobs[0].ID != deliveryID {
				t.Fatal("claimed delivery does not match the isolated test; refusing send")
			}
			if phase == "email" {
				worker := NewEmailWorker(repo, liveRestrictedEmail{emailSender, email}, nil, nil, nil, 1, cfg.NotificationEmailTimeout, cfg.ClientPublicBaseURL, cfg.MarketplacePublicBaseURL)
				worker.processOne(ctx, jobs[0])
			} else {
				worker := NewWhatsAppWorker(repo, liveRestrictedWhatsApp{waSender, phone}, nil, nil, nil, 1, cfg.WhatsAppHTTPTimeout, true)
				worker.processDelivery(ctx, jobs[0])
			}
			var code string
			var attempts int
			if err := pool.QueryRow(ctx, `SELECT status,attempt_count,last_error_code FROM notification_deliveries WHERE id=$1`, deliveryID).Scan(&status, &attempts, &code); err != nil {
				t.Fatal(err)
			}
			if status != "accepted" || attempts != 1 {
				t.Fatalf("delivery=%s status=%s attempts=%d code=%s; no automatic repeat", deliveryID, status, attempts, code)
			}
			t.Logf("delivery=%s template=%s accepted attempts=1", deliveryID, key)
		})
		if t.Failed() {
			return
		}
	}
}

func liveNotificationID(key string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("tellbook-notification-live:"+key))
}

type liveRestrictedEmail struct {
	mailer.Sender
	destination string
}

func (s liveRestrictedEmail) Send(ctx context.Context, m mailer.Message) error {
	if m.ToEmail != s.destination {
		return errors.New("live certification blocked unapproved recipient")
	}
	return s.Sender.Send(ctx, m)
}

type liveRestrictedWhatsApp struct {
	sender      *whatsapp.Client
	destination string
}

func (s liveRestrictedWhatsApp) SendTemplate(ctx context.Context, m whatsapp.TemplateMessage) (whatsapp.SendResult, error) {
	if m.To != s.destination {
		return whatsapp.SendResult{}, errors.New("live certification blocked unapproved recipient")
	}
	return s.sender.SendTemplate(ctx, m)
}

func liveNotificationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, repo *Repository, clientID, customerID, deliveryID uuid.UUID, channel, key, email string) {
	t.Helper()
	parts := strings.Split(key, ":")
	audience, kind := parts[0], parts[1]
	bookingID, eventID := liveNotificationID("booking:"+channel+key), liveNotificationID("event:"+channel+key)
	status, payment := "confirmed", "paid_in_full"
	switch kind {
	case "booking_cancelled":
		status = "cancelled"
	case "booking_expired":
		status = "expired"
	case "payment_failed":
		payment = "payment_failed"
	case "payment_refunded":
		payment = "refunded"
	case "payment_action_required":
		payment = "disputed"
	}
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	_, err := pool.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,title,stylist_name,source,status,payment_status,agreement_status,
		start_at,end_at,base_service_amount_minor,total_amount_minor,currency_code,duration_minutes,location_label,
		original_amount_minor,discounted_service_amount_minor,country_code,occupied_start_at,occupied_end_at,timezone,
		customer_email_snapshot,notification_consent_policy_revision,email_reminder_consent,email_reminder_consent_at,email_reminder_consent_source)
		VALUES($1,$2,$3,'TEST ONLY — notification certification','TellBook certification provider','direct_public_page',$4,$5,'not_required',
		$6::timestamptz,$6::timestamptz+INTERVAL '1 hour',0,0,'NGN',60,'Test appointment — no attendance required',0,0,'NG',$6::timestamptz,$6::timestamptz+INTERVAL '1 hour','Africa/Lagos',
		$7,1,TRUE,NOW(),'public_checkout')`, bookingID, clientID, customerID, status, payment, start, email)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(eventPayload{Status: status, PreviousStatus: "confirmed", PaymentStatus: payment, PreviousPaymentStatus: "unpaid", AgreementStatus: "not_required", StartsAt: start, PreviousStartsAt: start.Add(-time.Hour)})
	var sequence int64
	err = pool.QueryRow(ctx, `INSERT INTO booking_domain_events(id,client_id,booking_id,event_type,dedupe_key,payload)
		VALUES($1,$2,$3,'booking_updated',$4,$5) RETURNING sequence`, eventID, clientID, bookingID, "live-cert:"+eventID.String(), payload).Scan(&sequence)
	if err != nil {
		t.Fatal(err)
	}
	template, destination := "", email
	if channel == "whatsapp" {
		destination = os.Getenv("NOTIFICATION_LIVE_WHATSAPP")
		template = string(whatsapp.TemplateProviderNewBooking)
		if kind == "appointment_reminder" {
			template = string(whatsapp.TemplateProviderBookingReminder)
		}
	}
	var occurrence *time.Time
	var minutes *int
	if kind == "appointment_reminder" {
		occurrence = &start
		n := 1440
		minutes = &n
	}
	_, err = pool.Exec(ctx, `INSERT INTO notification_deliveries(id,idempotency_key,client_id,booking_id,booking_event_id,booking_event_sequence,
		audience_type,channel,notification_type,template_key,reminder_occurrence_at,reminder_offset_minutes,scheduled_for,next_attempt_at,destination_hmac,preference_revision)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW(),NOW(),$13,1)`, deliveryID, "live-cert:"+channel+":"+key, clientID, bookingID, eventID, sequence,
		audience, channel, kind, template, occurrence, minutes, repo.destinationFingerprint(channel, destination))
	if err != nil {
		t.Fatal(fmt.Errorf("insert live delivery: %w", err))
	}
}
