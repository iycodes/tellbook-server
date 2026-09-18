// Package emailtest supplies isolated database records and captured mail for lifecycle tests.
package emailtest

import (
	"booking/go-server/internal/mailer"
	"booking/go-server/internal/securityemail"
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

type Fixture struct {
	DB                        *pgxpool.Pool
	Client, Customer, Booking uuid.UUID
	Email                     string
	Start                     time.Time
}

func New(t *testing.T, enabled bool) *Fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	f := &Fixture{DB: db, Client: uuid.New(), Customer: uuid.New(), Booking: uuid.New(), Start: time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)}
	f.Email = f.Client.String() + "@example.invalid"
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = securityemail.EnableTx(ctx, tx, enabled, enabled); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO clients(id,full_name,email,email_verified_at) VALUES($1,'Email test provider',$2,NOW())`, f.Client, f.Email); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO customers(id,client_id,full_name,email) VALUES($1,$2,'Email test customer',$3)`, f.Customer, f.Client, f.Email); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,title,stylist_name,source,status,payment_status,agreement_status,start_at,end_at,base_service_amount_minor,total_amount_minor,deposit_amount_minor,currency_code,duration_minutes,location_label,original_amount_minor,discounted_service_amount_minor,country_code,occupied_start_at,occupied_end_at,timezone,customer_email_snapshot,notification_consent_policy_revision,email_reminder_consent,email_reminder_consent_at,email_reminder_consent_source,created_at)
 VALUES($1,$2,$3,'Email lifecycle appointment','Email test provider','direct_public_page','booked','deposit_pending','not_required',$4::timestamptz,$4::timestamptz+INTERVAL '1 hour',10000,10000,3000,'NGN',60,'Studio',10000,10000,'NG',$4::timestamptz,$4::timestamptz+INTERVAL '1 hour','Africa/Lagos',$5,1,true,NOW(),'public_checkout',NOW()-INTERVAL '2 hours')`, f.Booking, f.Client, f.Customer, f.Start, f.Email); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO booking_domain_events(id,client_id,booking_id,event_type,dedupe_key,payload) VALUES($1,$2,$3,'booking_created',$1::uuid::text,'{"status":"booked","payment_status":"deposit_pending"}')`, uuid.New(), f.Client, f.Booking); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Only this fixture's records; never truncate shared queues.
		db.Exec(ctx, `DELETE FROM financial_jobs WHERE payload->>'client_id'=$1`, f.Client.String())
		db.Exec(ctx, `DELETE FROM booking_refund_requests WHERE booking_id=$1`, f.Booking)
		db.Exec(ctx, `DELETE FROM booking_change_commands WHERE booking_id=$1`, f.Booking)
		db.Exec(ctx, `DELETE FROM payouts WHERE client_id=$1`, f.Client)
		db.Exec(ctx, `DELETE FROM payment_allocations WHERE client_id=$1`, f.Client)
		db.Exec(ctx, `DELETE FROM payout_destinations WHERE client_id=$1`, f.Client)
		db.Exec(ctx, `DELETE FROM payments WHERE client_id=$1`, f.Client)
		db.Exec(ctx, `DELETE FROM bookings WHERE id=$1`, f.Booking)
		db.Exec(ctx, `DELETE FROM customers WHERE id=$1`, f.Customer)
		db.Exec(ctx, `DELETE FROM clients WHERE id=$1`, f.Client)
	})
	return f
}
func (f *Fixture) Exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.DB.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *Fixture) Payment(t *testing.T, amount int64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	f.Exec(t, `INSERT INTO payments(id,public_token,booking_id,client_id,customer_id,purpose,provider,method,country_code,currency_code,amount_minor,price_snapshot,reference,idempotency_key,request_fingerprint,status,paid_at) VALUES($1,$1::uuid::text,$2,$3,$4,'deposit','paystack','card','NG','NGN',$5,'{}',$1::uuid::text,$1::uuid::text,repeat('a',64),'paid',NOW())`, id, f.Booking, f.Client, f.Customer, amount)
	return id
}

type Sender struct {
	Messages   []mailer.Message
	Err        error
	BeforeSend func()
}

func (*Sender) Enabled() bool { return true }
func (s *Sender) Send(_ context.Context, m mailer.Message) error {
	if s.BeforeSend != nil {
		s.BeforeSend()
	}
	s.Messages = append(s.Messages, m)
	return s.Err
}
