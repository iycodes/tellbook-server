package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/markets"
	"booking/go-server/internal/transactionemail"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FinancialEmailWorker struct {
	repository                          *LedgerRepository
	sender                              mailer.Sender
	logger                              *slog.Logger
	workerID, clientURL, marketplaceURL string
	destinationKey                      []byte
}
type financialEmailPayload struct {
	Family          string    `json:"family"`
	Audience        string    `json:"audience"`
	Recipient       string    `json:"recipient"`
	ClientID        uuid.UUID `json:"client_id"`
	BookingID       uuid.UUID `json:"booking_id"`
	Status          string    `json:"status"`
	Revision        int64     `json:"revision"`
	OccurredAt      time.Time `json:"occurred_at"`
	AmountMinor     int64     `json:"amount_minor"`
	CurrencyCode    string    `json:"currency_code"`
	CountryCode     string    `json:"country_code"`
	Reference       string    `json:"reference"`
	InstitutionName string    `json:"institution_name"`
	AccountLastFour string    `json:"account_last_four"`
}

func NewFinancialEmailWorker(r *LedgerRepository, sender mailer.Sender, key, clientURL, marketplaceURL string, logger *slog.Logger) *FinancialEmailWorker {
	if logger == nil {
		logger = slog.Default()
	}
	return &FinancialEmailWorker{repository: r, sender: sender, destinationKey: []byte(key), clientURL: strings.TrimRight(clientURL, "/"), marketplaceURL: strings.TrimRight(marketplaceURL, "/"), workerID: "financial-email-" + uuid.NewString(), logger: logger}
}
func (w *FinancialEmailWorker) Start(ctx context.Context, wake <-chan struct{}) {
	if !w.repository.financialEmails || w.sender == nil || !w.sender.Enabled() || len(w.destinationKey) < 32 {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-wake:
		}
		w.drain(ctx)
		timer.Reset(10 * time.Second)
	}
}
func (w *FinancialEmailWorker) drain(ctx context.Context) {
	if !w.repository.financialEmails {
		return
	}
	// Expired processing leases are safe to claim again; dispatching is not.
	_, err := w.repository.db.Exec(ctx, `UPDATE financial_jobs SET status='unknown',lease_owner='',lease_expires_at=NULL,last_error='smtp_outcome_unknown',completed_at=NOW(),updated_at=NOW() WHERE kind='financial_email' AND status='dispatching' AND lease_expires_at<=NOW()`)
	if err != nil {
		w.logger.Error("fence financial email sends", "error", err)
		return
	}
	jobs, err := w.repository.ClaimFinancialJobsByKind(ctx, w.workerID, "financial_email", 4, 90*time.Second)
	if err != nil {
		w.logger.Error("claim financial emails", "error", err)
		return
	}
	var group sync.WaitGroup
	for _, job := range jobs {
		group.Add(1)
		go func(job FinancialJob) { defer group.Done(); w.process(ctx, job) }(job)
	}
	group.Wait()
}

var errEmailSuppressed = errors.New("financial email no longer eligible")

func (w *FinancialEmailWorker) process(ctx context.Context, job FinancialJob) {
	if !w.repository.financialEmails {
		return
	}
	message, err := w.authorize(ctx, job)
	if err != nil {
		status, code := "failed", "email_prepare_failed"
		if errors.Is(err, errEmailSuppressed) {
			status, code = "cancelled", "email_no_longer_eligible"
		}
		if job.Attempts >= 8 && status == "failed" {
			status, code = "dead_letter", "retry_exhausted"
		}
		w.finish(ctx, job, "processing", status, code)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = w.sender.Send(sendCtx, message)
	cancel()
	status, code := "completed", ""
	if err != nil {
		disposition, _ := mailer.ClassifyTransportError(err)
		switch disposition {
		case mailer.DispositionAmbiguous:
			status, code = "unknown", "smtp_outcome_unknown"
		case mailer.DispositionRetryable:
			status, code = "failed", "smtp_transient"
			if job.Attempts >= 8 {
				status, code = "dead_letter", "retry_exhausted"
			}
		default:
			status, code = "dead_letter", "smtp_permanent"
		}
	}
	w.finish(ctx, job, "dispatching", status, code)
}

func (w *FinancialEmailWorker) finish(ctx context.Context, job FinancialJob, from, status, code string) {
	final, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	delay := mailer.RetryAt(now, job.ID, job.Attempts).Sub(now)
	tag, err := w.repository.db.Exec(final, `UPDATE financial_jobs SET status=$4,last_error=$5,available_at=NOW()+($6::bigint*INTERVAL '1 millisecond'),lease_owner='',lease_expires_at=NULL,completed_at=CASE WHEN $4='failed' THEN NULL ELSE NOW() END,updated_at=NOW() WHERE id=$1 AND lease_owner=$2 AND status=$3`, job.ID, w.workerID, from, status, code, delay.Milliseconds())
	if err != nil || tag.RowsAffected() != 1 {
		w.logger.Error("finalize financial email", "job_id", job.ID, "error", err)
		return
	}
	w.logger.Info("financial email outcome", "job_id", job.ID, "outcome", status, "reason", code)
}

func (w *FinancialEmailWorker) authorize(ctx context.Context, job FinancialJob) (mailer.Message, error) {
	tx, err := w.repository.db.Begin(ctx)
	if err != nil {
		return mailer.Message{}, err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT payload FROM financial_jobs WHERE id=$1 AND kind='financial_email' AND status='processing' AND lease_owner=$2 AND lease_expires_at>NOW() FOR UPDATE`, job.ID, w.workerID).Scan(&raw)
	if err != nil {
		return mailer.Message{}, err
	}
	var p financialEmailPayload
	if err = json.Unmarshal(raw, &p); err != nil {
		return mailer.Message{}, errEmailSuppressed
	}
	if p.Recipient == "" || p.Revision < 1 || p.OccurredAt.IsZero() {
		return mailer.Message{}, errEmailSuppressed
	}
	digest := hmac.New(sha256.New, w.destinationKey)
	digest.Write([]byte("email"))
	digest.Write([]byte{0})
	digest.Write([]byte(strings.ToLower(strings.TrimSpace(p.Recipient))))
	var suppressed bool
	// Same fingerprint contract as booking notifications.
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_contact_suppressions WHERE channel='email' AND destination_hmac=$1)`, digest.Sum(nil)).Scan(&suppressed)
	if err != nil {
		return mailer.Message{}, err
	}
	if suppressed {
		return mailer.Message{}, errEmailSuppressed
	}
	market, ok := markets.DefaultCatalog().Lookup(p.CountryCode)
	if !ok {
		return mailer.Message{}, errEmailSuppressed
	}
	var exponent uint8
	found := false
	for _, currency := range market.Currencies {
		if currency.Code == p.CurrencyCode {
			exponent = currency.MinorUnitExponent
			found = true
			break
		}
	}
	if !found {
		return mailer.Message{}, errEmailSuppressed
	}
	event := transactionemail.Event{DeliveryID: job.ID, Recipient: p.Recipient, OccurredAt: p.OccurredAt}
	var message mailer.Message
	if p.Family == "payout" && p.Audience == "provider" {
		var eligible bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payouts p JOIN clients c ON c.id=p.client_id WHERE p.id=$1 AND p.notification_revision=$2 AND p.status=$3 AND p.client_id=$4 AND c.email_verified_at IS NOT NULL AND lower(btrim(c.email))=$5)`, job.AggregateID, p.Revision, p.Status, p.ClientID, p.Recipient).Scan(&eligible)
		if err != nil {
			return mailer.Message{}, err
		}
		if !eligible {
			return mailer.Message{}, errEmailSuppressed
		}
		message, err = transactionemail.RenderPayout(transactionemail.PayoutInput{Event: event, Status: transactionemail.PayoutStatus(p.Status), AmountMinor: p.AmountMinor, CurrencyCode: p.CurrencyCode, CurrencyExponent: exponent, Reference: p.Reference, InstitutionName: p.InstitutionName, AccountLastFour: p.AccountLastFour, ApplicationURL: w.clientURL + "/"})
	} else if p.Family == "refund" && (p.Audience == "customer" || p.Audience == "provider") {
		var service, provider, customer, token string
		var owned bool
		var confirmed int64
		err = tx.QueryRow(ctx, `SELECT b.title,b.stylist_name,cu.full_name,b.public_token,b.marketplace_customer_id IS NOT NULL,
   COALESCE((SELECT SUM(a.amount_minor) FROM booking_refund_attempts a WHERE a.request_id=r.id AND a.status='successful'),0)
   FROM booking_refund_requests r JOIN bookings b ON b.id=r.booking_id JOIN clients c ON c.id=b.client_id JOIN customers cu ON cu.id=b.customer_id
   LEFT JOIN provider_notification_preferences pp ON pp.client_id=c.id LEFT JOIN marketplace_notification_preferences mp ON mp.marketplace_customer_id=b.marketplace_customer_id
   WHERE r.id=$1 AND r.notification_revision=$2 AND r.status=$3 AND b.id=$4 AND b.client_id=$5
   AND (($6='provider' AND c.email_verified_at IS NOT NULL AND lower(btrim(c.email))=$7 AND COALESCE(pp.booking_email,true))
   OR ($6='customer' AND b.customer_email_snapshot=$7 AND b.notification_consent_policy_revision=1 AND COALESCE(mp.booking_email,true)))`, job.AggregateID, p.Revision, p.Status, p.BookingID, p.ClientID, p.Audience, p.Recipient).Scan(&service, &provider, &customer, &token, &owned, &confirmed)
		if errors.Is(err, pgx.ErrNoRows) {
			return mailer.Message{}, errEmailSuppressed
		}
		if err != nil {
			return mailer.Message{}, err
		}
		if confirmed >= p.AmountMinor {
			return mailer.Message{}, errEmailSuppressed
		}
		action := w.marketplaceURL + "/bookings?booking=" + p.BookingID.String()
		if p.Audience == "provider" {
			action = w.clientURL + "/bookings?booking=" + p.BookingID.String()
		} else if !owned {
			action = w.marketplaceURL + "/bookings#claim=" + url.QueryEscape(token)
		}
		message, err = transactionemail.RenderRefund(transactionemail.RefundInput{Event: event, Audience: p.Audience, Status: transactionemail.RefundStatus(p.Status), Service: service, Provider: provider, CustomerName: customer, Reference: p.Reference, RequestedMinor: p.AmountMinor, CurrencyCode: p.CurrencyCode, CurrencyExponent: exponent, ConfirmedMinor: &confirmed, BookingURL: action})
	} else {
		return mailer.Message{}, errEmailSuppressed
	}
	if err != nil {
		return mailer.Message{}, fmt.Errorf("render financial email: %w", err)
	}
	// Commit the send fence before calling SMTP; never repeat an uncertain send.
	_, err = tx.Exec(ctx, `UPDATE financial_jobs SET status='dispatching',updated_at=NOW() WHERE id=$1`, job.ID)
	if err != nil {
		return mailer.Message{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return mailer.Message{}, err
	}
	return message, nil
}
