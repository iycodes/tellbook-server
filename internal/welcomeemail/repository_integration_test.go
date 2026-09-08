package welcomeemail

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"booking/go-server/internal/mailer"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationSender struct{ message mailer.Message }

func (sender *integrationSender) Enabled() bool { return true }
func (sender *integrationSender) Send(_ context.Context, message mailer.Message) error {
	sender.message = message
	return nil
}

func TestProviderAssignmentAndDeliveryLifecycle(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	providerID := uuid.New()
	email := "welcome-" + uuid.NewString() + "@example.com"
	if _, err := pool.Exec(ctx, `
		INSERT INTO clients (id,full_name,email,password_hash,email_verified_at)
		VALUES ($1,$2,$3,$4,NOW())
	`, providerID, `Sam & Sons`, email, "test-hash"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM clients WHERE id=$1`, providerID) })

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assignment := Assignment{Audience: AudienceProvider, AccountID: providerID, Email: email, Name: `Sam & Sons`}
	if err := AssignTx(ctx, tx, assignment); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := AssignTx(ctx, tx, assignment); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var count, version int
	var htmlBody string
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*),MAX(template_version),MAX(html_body)
		FROM welcome_email_jobs WHERE provider_client_id=$1
	`, providerID).Scan(&count, &version, &htmlBody); err != nil {
		t.Fatal(err)
	}
	if count != 1 || version != 1 {
		t.Fatalf("assigned jobs = %d at version %d", count, version)
	}
	if !strings.Contains(htmlBody, "Sam &amp; Sons") {
		t.Fatalf("assigned HTML did not escape recipient name: %q", htmlBody)
	}

	repository := NewRepository(pool)
	jobs, err := repository.ClaimJobs(ctx, "integration-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].AttemptCount != 1 {
		t.Fatalf("claimed jobs = %+v", jobs)
	}
	if err := repository.RecordFailure(ctx, jobs[0], mailer.DispositionRetryable, "smtp_transient"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE welcome_email_jobs SET next_attempt_at=NOW() WHERE id=$1`, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	jobs, err = repository.ClaimJobs(ctx, "integration-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].AttemptCount != 2 {
		t.Fatalf("reclaimed jobs = %+v", jobs)
	}
	sender := &integrationSender{}
	worker := NewWorker(repository, sender, nil, nil, nil, 1, time.Second)
	worker.processOne(ctx, jobs[0])
	if sender.message.ToEmail != email || sender.message.Subject != jobs[0].Subject ||
		sender.message.HTML != jobs[0].HTMLBody || sender.message.Text != jobs[0].TextBody {
		t.Fatalf("worker did not send the assigned snapshot: %+v", sender.message)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM welcome_email_jobs WHERE id=$1`, jobs[0].ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" {
		t.Fatalf("welcome job status = %q", status)
	}
}
