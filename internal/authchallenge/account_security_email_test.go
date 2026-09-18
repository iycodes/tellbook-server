package authchallenge

import (
	"booking/go-server/internal/securityemail"
	"strings"
	"testing"
	"time"
)

func TestAccountSecurityQueueIntegration(t *testing.T) {
	ctx, s, client, _ := tessaSecurityEmailFixture(t)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = securityemail.RecordTx(ctx, tx, "provider", client, client.String()+"@example.invalid", "password_set", nil); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(ctx)
	var count int
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE provider_client_id=$1`, client).Scan(&count); err != nil || count != 0 {
		t.Fatal("rollback leaked event", err)
	}
	tx, err = s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = securityemail.RecordTx(ctx, tx, "provider", client, client.String()+"@example.invalid", "password_set", nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.prepareAccountSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM auth_code_delivery_jobs WHERE account_security_event_id IN(SELECT id FROM account_security_events WHERE provider_client_id=$1)`, client).Scan(&count); err != nil || count != 0 {
		t.Fatal("disabled queue created job", err)
	}
	s.additionalEmailsEnabled = true
	for range 2 {
		if err = s.prepareAccountSecurityEmails(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.claimEmailJobs(ctx, "account-security-test", 100, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sender := &securityCaptureSender{}
	w := NewWorker(s, sender, nil, nil, nil, 1, time.Second)
	for _, job := range jobs {
		if job.TemplateKey == "account_security_email" {
			payload, e := s.decryptPayload(job)
			if e != nil || payload.AccountSecurity == nil || payload.Code != "" {
				t.Fatal("encrypted security payload", e)
			}
			if payload.Destination != client.String()+"@example.invalid" {
				continue
			}
			if _, e = s.db.Exec(ctx, `UPDATE clients SET email=$2 WHERE id=$1`, client, "changed-"+client.String()+"@example.invalid"); e != nil {
				t.Fatal(e)
			}
			w.processOne(ctx, job)
		}
	}
	if len(sender.messages) != 1 || sender.messages[0].ToEmail != client.String()+"@example.invalid" || !strings.Contains(sender.messages[0].Text, "password is set") {
		t.Fatal("wrong committed recipient or template")
	}
	if err = s.db.QueryRow(ctx, `SELECT count(*) FROM account_security_events WHERE provider_client_id=$1 AND email_accepted_at IS NOT NULL`, client).Scan(&count); err != nil || count != 1 {
		t.Fatal("acceptance missing", err)
	}
}

func TestAccountSecurityLoginPriorityIntegration(t *testing.T) {
	ctx, s, client, _ := tessaSecurityEmailFixture(t)
	s.additionalEmailsEnabled = true
	tx, err := s.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = securityemail.RecordTx(ctx, tx, "provider", client, client.String()+"@example.invalid", "password_changed", nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.prepareAccountSecurityEmails(ctx, 100); err != nil {
		t.Fatal(err)
	}
	challenge, err := s.Start(ctx, StartRequest{Realm: RealmProvider, RawIdentifier: client.String() + "@example.invalid", Channel: ChannelEmail, Purpose: PurposeSignIn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Exec(ctx, `DELETE FROM provider_auth_challenges WHERE id=$1`, challenge.ChallengeID) })
	jobs, err := s.claimEmailJobs(ctx, "account-priority", 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].ChallengeID != challenge.ChallengeID {
		t.Fatal("login code lost priority", err)
	}
}
