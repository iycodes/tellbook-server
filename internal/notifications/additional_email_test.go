package notifications

import (
	"booking/go-server/internal/emailtest"
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/securityemail"
	"booking/go-server/internal/transactionemail"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestStepReminderScheduleAndPriority(t *testing.T) {
	created := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                  string
		start, deadline, want time.Duration
		ok                    bool
	}{{"day before", 72 * time.Hour, 0, 48 * time.Hour, true}, {"reservation earlier", 72 * time.Hour, 6 * time.Hour, 5 * time.Hour, true}, {"creation floor", 2 * time.Hour, 0, time.Hour, true}, {"too soon", 30 * time.Minute, 0, time.Hour, false}, {"reservation too soon", 48 * time.Hour, 30 * time.Minute, time.Hour, false}} {
		t.Run(tc.name, func(t *testing.T) {
			var deadline *time.Time
			if tc.deadline != 0 {
				d := created.Add(tc.deadline)
				deadline = &d
			}
			got, ok := stepReminderSchedule(created, created.Add(tc.start), deadline)
			if ok != tc.ok || !got.Equal(created.Add(tc.want)) {
				t.Fatalf("got %v %v", got, ok)
			}
		})
	}
	s := bookingState{Status: "booked", PaymentStatus: "deposit_pending", AgreementRequired: true, StartAt: created.Add(48 * time.Hour)}
	deadline := created.Add(5 * time.Hour)
	b := additionalBooking{deposit: 3000, total: 10000, reservationDeadline: &deadline}
	kind, due, d := outstandingStep(s, b, created)
	if kind != transactionemail.DepositReminder || due != 3000 || d == nil {
		t.Fatal("deposit first")
	}
	b.paid = 3000
	s.PaymentStatus = "deposit_paid_balance_due"
	kind, _, d = outstandingStep(s, b, created)
	if kind != transactionemail.AgreementReminder || d != nil {
		t.Fatal("agreement second, no deadline")
	}
	s.AgreementStatus = "signed"
	kind, due, d = outstandingStep(s, b, created)
	if kind != transactionemail.BalanceReminder || due != 7000 || d != nil {
		t.Fatal("balance third, no reservation deadline")
	}
	b.paid = 10000
	kind, _, _ = outstandingStep(s, b, created)
	if kind != "" {
		t.Fatal("paid booking reminded")
	}
	b.paid = 0
	s.Status = "cancelled"
	kind, _, _ = outstandingStep(s, b, created)
	if kind != "" {
		t.Fatal("cancelled booking reminded")
	}
	s.Status = "booked"
	s.PaymentStatus = "deposit_pending"
	kind, _, _ = outstandingStep(s, b, deadline)
	if kind != "" {
		t.Fatal("expired reservation reminded")
	}
}
func TestAdditionalBookingLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, true)
	r, err := NewRepository(f.DB, strings.Repeat("k", 32), nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	r.WithAdditionalEmails(true)
	plan := func() {
		t.Helper()
		tx, e := f.DB.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		var ev bookingEvent
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT id,sequence,event_type,payload FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1`, f.Booking).Scan(&ev.ID, &ev.Sequence, &ev.Type, &raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(raw, &ev.Payload); e != nil {
			t.Fatal(e)
		}
		s, e := loadBookingStateTx(ctx, tx, ev.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = r.reconcileAdditionalTx(ctx, tx, s, &ev, nil); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	plan()
	plan()
	var id uuid.UUID
	var at time.Time
	if err = f.DB.QueryRow(ctx, `SELECT id,scheduled_for FROM notification_deliveries WHERE booking_id=$1 AND notification_type='booking_step_reminder'`, f.Booking).Scan(&id, &at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(f.Start.Add(-24 * time.Hour)) {
		t.Fatal("wrong initial schedule")
	}
	f.Exec(t, `UPDATE notification_deliveries SET status='processing',lease_owner='old-reminder-worker',lease_expires_at=NOW()+INTERVAL '1 minute' WHERE id=$1`, id)
	f.Exec(t, `UPDATE bookings SET start_at=start_at+INTERVAL '1 day',end_at=end_at+INTERVAL '1 day',occupied_start_at=occupied_start_at+INTERVAL '1 day',occupied_end_at=occupied_end_at+INTERVAL '1 day' WHERE id=$1`, f.Booking)
	plan()
	var moved time.Time
	var replannedStatus, owner string
	if err = f.DB.QueryRow(ctx, `SELECT status,lease_owner FROM notification_deliveries WHERE id=$1`, id).Scan(&replannedStatus, &owner); err != nil || replannedStatus != "pending" || owner != "" {
		t.Fatal("reschedule retained stale processing lease", err)
	}
	if err = f.DB.QueryRow(ctx, `SELECT scheduled_for FROM notification_deliveries WHERE id=$1`, id).Scan(&moved); err != nil || !moved.Equal(at.Add(24*time.Hour)) {
		t.Fatalf("reschedule %v %v", moved, err)
	}
	r.now = func() time.Time { return moved.Add(time.Minute) }
	f.Exec(t, `UPDATE notification_deliveries SET next_attempt_at=NOW()-INTERVAL '1 minute',scheduled_for=NOW()-INTERVAL '1 minute' WHERE id=$1`, id)
	sender := &emailtest.Sender{Err: &mailer.TransportError{Disposition: mailer.DispositionAmbiguous, Cause: errors.New("ack lost")}}
	w := NewEmailWorker(r, sender, nil, nil, nil, 1, time.Second, "https://provider.example", "https://customer.example")
	jobs, err := r.ClaimDeliveries(ctx, "email", w.workerID, 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.ID == id {
			w.processOne(ctx, job)
		}
	}
	if len(sender.Messages) != 1 || !strings.Contains(sender.Messages[0].Text, "DEPOSIT OUTSTANDING") {
		t.Fatalf("reminder not sent: %+v", sender.Messages)
	}
	plan()
	var count int
	var status string
	if err = f.DB.QueryRow(ctx, `SELECT count(*),min(status) FROM notification_deliveries WHERE booking_id=$1 AND notification_type='booking_step_reminder'`, f.Booking).Scan(&count, &status); err != nil || count != 1 || status != "manual_review" {
		t.Fatalf("allowance reused: %d %s %v", count, status, err)
	}
	tx, err := f.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = securityemail.EnableTx(ctx, tx, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE bookings SET status='completed' WHERE id=$1`, f.Booking); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r.now = func() time.Time { return time.Now().UTC() }
	plan()
	plan()
	sender.Err = nil
	jobs, err = r.ClaimDeliveries(ctx, "email", w.workerID, 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.BookingID == f.Booking {
			w.processOne(ctx, job)
		}
	}
	if len(sender.Messages) != 2 || !strings.Contains(sender.Messages[1].Text, "APPOINTMENT COMPLETED") || strings.Contains(sender.Messages[1].Text, "Share your experience") {
		t.Fatalf("completion content/count: %+v", sender.Messages)
	}
	plan()
	jobs, err = r.ClaimDeliveries(ctx, "email", w.workerID, 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.BookingID == f.Booking {
			t.Fatal("completion sent twice")
		}
	}
}

func TestAdditionalReminderDispatchRechecksIntegration(t *testing.T) {
	for _, tc := range []struct{ name, update string }{{"cancelled", `UPDATE bookings SET status='cancelled' WHERE id=$1`}, {"expired", `UPDATE bookings SET reservation_expires_at=NOW()-INTERVAL '1 minute' WHERE id=$1`}, {"consent withdrawn", `UPDATE bookings SET email_reminder_consent=false,email_reminder_consent_at=NULL,email_reminder_consent_source='' WHERE id=$1`}, {"recipient changed", `UPDATE bookings SET customer_email_snapshot='changed@example.invalid' WHERE id=$1`}, {"all steps complete", `UPDATE bookings SET payment_status='paid_in_full' WHERE id=$1`}, {"preferences disabled", `UPDATE marketplace_notification_preferences SET booking_email=false,updated_at=NOW() WHERE marketplace_customer_id=(SELECT marketplace_customer_id FROM bookings WHERE id=$1)`}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := emailtest.New(t, true)
			r, err := NewRepository(f.DB, strings.Repeat("k", 32), nil, true, false)
			if err != nil {
				t.Fatal(err)
			}
			r.WithAdditionalEmails(true)
			if tc.name == "preferences disabled" {
				f.Exec(t, `INSERT INTO marketplace_customers(id,full_name,email,email_verified_at) VALUES($1,'Reminder preferences',$2,NOW())`, f.Customer, "preferences-"+f.Email)
				t.Cleanup(func() { f.DB.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, f.Customer) })
				f.Exec(t, `UPDATE bookings SET marketplace_customer_id=$2 WHERE id=$1`, f.Booking, f.Customer)
				f.Exec(t, `INSERT INTO marketplace_notification_preferences(marketplace_customer_id) VALUES($1)`, f.Customer)
			}
			r.now = func() time.Time { return f.Start.Add(-23 * time.Hour) }
			var id uuid.UUID
			if err = f.DB.QueryRow(ctx, `SELECT id FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence LIMIT 1`, f.Booking).Scan(&id); err != nil {
				t.Fatal(err)
			}
			tx, err := f.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			s, err := loadBookingStateTx(ctx, tx, id)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.reconcileAdditionalTx(ctx, tx, s, &bookingEvent{ID: id, Type: "booking_created"}, nil); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			f.Exec(t, tc.update, f.Booking)
			f.Exec(t, `UPDATE notification_deliveries SET scheduled_for=NOW()-INTERVAL '1 minute',next_attempt_at=NOW()-INTERVAL '1 minute' WHERE booking_id=$1`, f.Booking)
			sender := &emailtest.Sender{}
			w := NewEmailWorker(r, sender, nil, nil, nil, 1, time.Second, "https://provider.example", "https://customer.example")
			jobs, err := r.ClaimDeliveries(ctx, "email", w.workerID, 100, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			for _, job := range jobs {
				if job.BookingID == f.Booking {
					w.processOne(ctx, job)
				}
			}
			if len(sender.Messages) != 0 {
				t.Fatal("ineligible reminder sent")
			}
		})
	}
}
func TestAdditionalEmailsNoBackfillIntegration(t *testing.T) {
	ctx := context.Background()
	f := emailtest.New(t, false)
	r, err := NewRepository(f.DB, strings.Repeat("k", 32), nil, true, false)
	if err != nil {
		t.Fatal(err)
	}
	r.WithAdditionalEmails(true)
	f.Exec(t, `UPDATE bookings SET status='completed' WHERE id=$1`, f.Booking)
	tx, err := f.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var event bookingEvent
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT id,sequence,event_type,payload FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1`, f.Booking).Scan(&event.ID, &event.Sequence, &event.Type, &raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &event.Payload); err != nil {
		t.Fatal(err)
	}
	s, err := loadBookingStateTx(ctx, tx, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.reconcileAdditionalTx(ctx, tx, s, &event, nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE booking_id=$1 AND notification_type IN ('booking_completed','booking_step_reminder')`, f.Booking).Scan(&count); err != nil || count != 0 {
		t.Fatal("historical event backfilled", err)
	}
}

func TestAdditionalEmailChangedAfterAuthorizationIntegration(t *testing.T) {
	for _, scenario := range []string{"booking_step_reminder", "booking_completed", "reminder_rescheduled"} {
		t.Run(scenario, func(t *testing.T) {
			kind := scenario
			if scenario == "reminder_rescheduled" {
				kind = "booking_step_reminder"
			}
			ctx := context.Background()
			f := emailtest.New(t, true)
			r, err := NewRepository(f.DB, strings.Repeat("k", 32), nil, true, false)
			if err != nil {
				t.Fatal(err)
			}
			r.WithAdditionalEmails(true)
			r.now = func() time.Time { return f.Start.Add(-23 * time.Hour) }
			if kind == "booking_completed" {
				f.Exec(t, `UPDATE bookings SET status='completed' WHERE id=$1`, f.Booking)
			}
			var eventID uuid.UUID
			if err = f.DB.QueryRow(ctx, `SELECT id FROM booking_domain_events WHERE booking_id=$1 ORDER BY sequence DESC LIMIT 1`, f.Booking).Scan(&eventID); err != nil {
				t.Fatal(err)
			}
			tx, err := f.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			s, err := loadBookingStateTx(ctx, tx, eventID)
			if err != nil {
				t.Fatal(err)
			}
			candidate := deliveryCandidate{audience: "customer", channel: "email", notificationType: kind, templateKey: kind, scheduledFor: time.Now().Add(-time.Minute), destination: f.Email, idempotencyKey: kind + ":" + f.Booking.String() + ":customer:email"}
			if err = r.insertDeliveryTx(ctx, tx, s, &bookingEvent{ID: eventID}, candidate, nil); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			jobs, err := r.ClaimDeliveries(ctx, "email", "preflight-test", 100, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			var d Delivery
			for _, job := range jobs {
				if job.BookingID == f.Booking {
					d, err = r.AuthorizeDispatch(ctx, job)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if d.ID == uuid.Nil {
				t.Fatal("no claimed notice")
			}
			// Change consent after the durable fence, before preparing any SMTP message.
			if scenario == "reminder_rescheduled" {
				f.Exec(t, `UPDATE bookings SET start_at=start_at+INTERVAL '1 day',end_at=end_at+INTERVAL '1 day',occupied_start_at=occupied_start_at+INTERVAL '1 day',occupied_end_at=occupied_end_at+INTERVAL '1 day' WHERE id=$1`, f.Booking)
			} else {
				f.Exec(t, `UPDATE bookings SET notification_consent_policy_revision=0 WHERE id=$1`, f.Booking)
			}
			_, err = r.BuildEmailMessage(ctx, d, "https://provider.example", "https://customer.example")
			if !errors.Is(err, ErrEmailEligibilityChanged) {
				t.Fatalf("stale eligibility rendered: %v", err)
			}
			if err = r.CancelAuthorizedDelivery(ctx, d.ID, "eligibility_changed_before_smtp"); err != nil {
				t.Fatal(err)
			}
			if scenario == "reminder_rescheduled" {
				var status string
				var at time.Time
				if err = f.DB.QueryRow(ctx, `SELECT status,scheduled_for FROM notification_deliveries WHERE id=$1`, d.ID).Scan(&status, &at); err != nil || status != "pending" || !at.Equal(f.Start) {
					t.Fatalf("rescheduled unsent job lost: %s %s %v", status, at, err)
				}
				return
			}
			var status string
			var fenced bool
			if err = f.DB.QueryRow(ctx, `SELECT status,dispatch_authorized_at IS NOT NULL FROM notification_deliveries WHERE id=$1`, d.ID).Scan(&status, &fenced); err != nil || status != "cancelled" || fenced {
				t.Fatalf("known unsent job consumed allowance: %s %v %v", status, fenced, err)
			}
			// A later valid replan may reuse the same unsent job, never create a second one.
			f.Exec(t, `UPDATE bookings SET notification_consent_policy_revision=1 WHERE id=$1`, f.Booking)
			tx, err = f.DB.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if err = r.insertDeliveryTx(ctx, tx, s, &bookingEvent{ID: eventID}, candidate, nil); err != nil {
				t.Fatal(err)
			}
			if err = tx.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE id=$1`, d.ID).Scan(&status); err != nil || status != "pending" {
				t.Fatal("unsent job not reusable", status, err)
			}
		})
	}
}
