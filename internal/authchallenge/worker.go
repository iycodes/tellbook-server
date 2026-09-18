package authchallenge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"

	"booking/go-server/internal/mailer"
	"booking/go-server/internal/secure"
	"booking/go-server/internal/transactionemail"
	"booking/go-server/internal/whatsapp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	defaultWorkerConcurrency = 4
	claimLease               = 90 * time.Second
	maxDeliveryAttempts      = 8
	finalizationTimeout      = 5 * time.Second
)

type DeliveryJob struct {
	ID            uuid.UUID
	Realm         string
	Channel       string
	TemplateKey   string
	ChallengeID   uuid.UUID
	Ciphertext    secure.Ciphertext
	Deadline      time.Time
	AttemptCount  int
	NextAttemptAt time.Time
	LeaseOwner    string
}

type WorkerMetrics interface {
	ObserveAuthCodeDeliveryClaim(realm, channel string, latency time.Duration)
	ObserveAuthCodeDeliveryOutcome(realm, channel, outcome string)
}

type Worker struct {
	service         *Service
	sender          mailer.Sender
	logger          *slog.Logger
	wake            <-chan struct{}
	metrics         WorkerMetrics
	workerID        string
	concurrency     int
	deliveryTimeout time.Duration
	leaseDuration   time.Duration
}

func NewWorker(service *Service, sender mailer.Sender, logger *slog.Logger, wake <-chan struct{}, metrics WorkerMetrics, concurrency int, timeout time.Duration) *Worker {
	if logger == nil {
		logger = slog.Default()
	}
	if concurrency < 1 || concurrency > 32 {
		concurrency = defaultWorkerConcurrency
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	lease := timeout + 30*time.Second
	if lease < claimLease {
		lease = claimLease
	}
	return &Worker{
		service: service, sender: sender, logger: logger, wake: wake, metrics: metrics,
		workerID: "auth-code-email-" + uuid.NewString(), concurrency: concurrency,
		deliveryTimeout: timeout, leaseDuration: lease,
	}
}

func (worker *Worker) Start(ctx context.Context) {
	if worker == nil || worker.service == nil || !worker.service.emailEnabled || worker.sender == nil || !worker.sender.Enabled() {
		return
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-worker.wake:
		case <-timer.C:
		}
		worker.drain(ctx)
		timer.Reset(10 * time.Second)
	}
}

func (worker *Worker) drain(ctx context.Context) {
	for ctx.Err() == nil {
		if err := worker.service.prepareAccountSecurityEmails(ctx, 100); err != nil {
			worker.logger.Error("prepare account security emails", "error", err)
		}
		if err := worker.service.prepareTessaSecurityEmails(ctx, 100); err != nil {
			worker.logger.Error("prepare Tessa security email jobs", "error", err)
		}
		jobs, err := worker.service.claimEmailJobs(ctx, worker.workerID, worker.concurrency, worker.leaseDuration)
		if err != nil {
			worker.logger.Error("claim auth code email jobs", "error", err)
			return
		}
		worker.processBatch(ctx, jobs)
		if len(jobs) < worker.concurrency {
			return
		}
	}
}

func (worker *Worker) processBatch(ctx context.Context, jobs []DeliveryJob) {
	for start := 0; start < len(jobs); start += worker.concurrency {
		end := min(start+worker.concurrency, len(jobs))
		var group sync.WaitGroup
		group.Add(end - start)
		for _, job := range jobs[start:end] {
			go func(job DeliveryJob) { defer group.Done(); worker.processOne(ctx, job) }(job)
		}
		group.Wait()
	}
}

func (worker *Worker) processOne(ctx context.Context, job DeliveryJob) {
	if job.TemplateKey == "account_security_email" && !worker.service.additionalEmailsEnabled {
		return
	}
	worker.observeClaim(job)
	payload, err := worker.service.decryptPayload(job)
	if err != nil {
		finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
		defer cancel()
		worker.finalizeFailure(finalizeContext, job, mailer.DispositionPermanent, "payload_invalid")
		return
	}
	deliveryContext, cancel := context.WithTimeout(ctx, worker.deliveryTimeout)
	defer cancel()
	if job.TemplateKey == tessaLinkEmailTemplate {
		valid, checkErr := worker.service.tessaLinkEmailDispatchable(deliveryContext, job)
		if checkErr != nil || !valid {
			finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
			defer finalizeCancel()
			disposition, code := mailer.DispositionPermanent, "link_request_inactive"
			if checkErr != nil {
				disposition, code = mailer.DispositionRetryable, "link_dispatch_check"
			}
			worker.finalizeFailure(finalizeContext, job, disposition, code)
			return
		}
	}
	message := mailer.Message{
		ToEmail:   payload.Destination,
		Subject:   "Your Tellbook code",
		Text:      fmt.Sprintf("Use this code to continue to Tellbook:\n\n%s\n\nThe code expires 10 minutes after delivery. If you did not request it, you can ignore this message.", payload.Code),
		MessageID: fmt.Sprintf("<auth-code-%s@mail.tellbook.app>", job.ID),
	}
	switch job.TemplateKey {
	case "account_security_email":
		message, err = transactionemail.RenderSecurity(*payload.AccountSecurity)
	case tessaSecurityEmailTemplate, tessaLinkEmailTemplate:
		message, err = renderTessaEmail(job.ID, job.TemplateKey, payload)
	case "auth_code_email":
		message, err = renderAuthCodeEmail(job.ID, payload)
	}
	if err != nil {
		finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
		defer finalizeCancel()
		worker.finalizeFailure(finalizeContext, job, mailer.DispositionPermanent, "email_render_failed")
		return
	}
	err = worker.sender.Send(deliveryContext, message)
	cancel()
	finalizeContext, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer finalizeCancel()
	if err == nil {
		outcome, finalizeErr := worker.service.markEmailAccepted(finalizeContext, job)
		if finalizeErr != nil {
			worker.logger.Error("record accepted auth code email", "job_id", job.ID, "error", finalizeErr)
			return
		}
		worker.observeOutcome(job, outcome)
		return
	}
	disposition, _ := mailer.ClassifyTransportError(err)
	code := "smtp_transient"
	if disposition == mailer.DispositionPermanent {
		code = "smtp_permanent"
	}
	if disposition == mailer.DispositionAmbiguous {
		code = "smtp_outcome_unknown"
	}
	worker.finalizeFailure(finalizeContext, job, disposition, code)
}

func (worker *Worker) finalizeFailure(ctx context.Context, job DeliveryJob, disposition mailer.TransportDisposition, code string) {
	outcome, err := worker.service.recordEmailFailure(ctx, job, disposition, code)
	if err != nil {
		worker.logger.Error("record auth code email failure", "job_id", job.ID, "error", err)
		return
	}
	worker.observeOutcome(job, outcome)
}

func (s *Service) claimEmailJobs(ctx context.Context, owner string, limit int, lease time.Duration) ([]DeliveryJob, error) {
	return s.claimDeliveryJobs(ctx, ChannelEmail, owner, limit, lease)
}

func (s *Service) claimDeliveryJobs(ctx context.Context, channel, owner string, limit int, lease time.Duration) ([]DeliveryJob, error) {
	if channel != ChannelEmail && channel != ChannelWhatsApp {
		return nil, errors.New("invalid auth delivery channel")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("auth delivery lease owner is required")
	}
	if limit < 1 || limit > 100 {
		limit = defaultWorkerConcurrency
	}
	if lease <= 0 {
		lease = claimLease
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin auth delivery claim: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `
		WITH expired AS (
			SELECT id FROM auth_code_delivery_jobs
			WHERE channel=$2 AND status IN ('pending','retry','processing','unknown')
			  AND (delivery_deadline<=$1 OR (status='processing' AND lease_expires_at<=$1))
			ORDER BY LEAST(delivery_deadline,COALESCE(lease_expires_at,delivery_deadline)),id
			LIMIT 100 FOR UPDATE SKIP LOCKED
		)
		UPDATE auth_code_delivery_jobs job
		SET status=CASE WHEN job.delivery_deadline<=$1 THEN 'expired' ELSE 'unknown' END,
			payload_ciphertext=NULL,payload_nonce=NULL,payload_key_version=NULL,
			lease_owner='',lease_expires_at=NULL,
			error_code=CASE WHEN job.delivery_deadline<=$1 THEN 'delivery_deadline_expired' ELSE 'lease_expired_outcome_unknown' END,
			completed_at=$1,updated_at=$1
		FROM expired WHERE job.id=expired.id
	`, now, channel); err != nil {
		return nil, fmt.Errorf("fence expired auth deliveries: %w", err)
	}
	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT id FROM auth_code_delivery_jobs
			WHERE channel=$6 AND status IN ('pending','retry')
			  AND next_attempt_at<=$1 AND delivery_deadline>$1 AND attempt_count<$4
 AND (template_key<>'account_security_email' OR $7)
			ORDER BY (CASE WHEN template_key IN ('tessa_security_email','account_security_email') THEN 1 ELSE 0 END),next_attempt_at,created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE auth_code_delivery_jobs job
		SET status='processing',attempt_count=attempt_count+1,lease_owner=$3,
			lease_expires_at=$1+($5*INTERVAL '1 second'),updated_at=$1
		FROM due WHERE job.id=due.id
		RETURNING job.id,job.realm,job.channel,job.template_key,
			COALESCE(job.provider_challenge_id,job.marketplace_challenge_id,job.tessa_security_event_id,job.tessa_link_challenge_id,job.account_security_event_id),
			job.payload_ciphertext,job.payload_nonce,job.payload_key_version,
			job.delivery_deadline,job.attempt_count,job.next_attempt_at,job.lease_owner
	`, now, limit, owner, maxDeliveryAttempts, lease.Seconds(), channel, s.additionalEmailsEnabled)
	if err != nil {
		return nil, fmt.Errorf("claim auth code deliveries: %w", err)
	}
	defer rows.Close()
	jobs := make([]DeliveryJob, 0, limit)
	for rows.Next() {
		var job DeliveryJob
		if err := rows.Scan(&job.ID, &job.Realm, &job.Channel, &job.TemplateKey, &job.ChallengeID, &job.Ciphertext.Data, &job.Ciphertext.Nonce, &job.Ciphertext.KeyVersion, &job.Deadline, &job.AttemptCount, &job.NextAttemptAt, &job.LeaseOwner); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit auth delivery claim: %w", err)
	}
	return jobs, nil
}

func (s *Service) decryptPayload(job DeliveryJob) (deliveryPayload, error) {
	plaintext, err := s.keyring.Decrypt(job.Ciphertext, deliveryAAD(job.ID))
	if err != nil {
		return deliveryPayload{}, err
	}
	var payload deliveryPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return deliveryPayload{}, err
	}
	if _, identifier, channel, err := NormalizeIdentifier(payload.Destination, job.Channel); err != nil || channel != job.Channel || identifier != payload.Destination {
		return deliveryPayload{}, errors.New("invalid auth delivery payload")
	}
	if job.TemplateKey == "account_security_email" {
		if payload.AccountSecurity == nil || payload.Code != "" || payload.Security != nil || payload.Link != nil || job.Channel != ChannelEmail || payload.AccountSecurity.DeliveryID != job.ID || payload.AccountSecurity.Recipient != payload.Destination {
			return deliveryPayload{}, errors.New("invalid account security payload")
		}
		if _, err := transactionemail.RenderSecurity(*payload.AccountSecurity); err != nil {
			return deliveryPayload{}, err
		}
	} else if payload.AccountSecurity != nil {
		return deliveryPayload{}, errors.New("unexpected account security payload")
	} else if job.TemplateKey == tessaSecurityEmailTemplate {
		if job.Realm != RealmProvider || job.Channel != ChannelEmail || payload.Code != "" || payload.Link != nil || !validSecurityEmailPayload(payload.Security) {
			return deliveryPayload{}, errors.New("invalid Tessa security payload")
		}
	} else if job.TemplateKey == tessaLinkEmailTemplate {
		if job.Realm != RealmProvider || job.Channel != ChannelEmail || payload.Security != nil || !validCode(payload.Code) || !validLinkEmailPayload(payload.Link) {
			return deliveryPayload{}, errors.New("invalid Tessa linking payload")
		}
	} else if payload.Security != nil || payload.Link != nil || !validCode(payload.Code) {
		return deliveryPayload{}, errors.New("invalid auth delivery payload")
	}
	return payload, nil
}

func (s *Service) markEmailAccepted(ctx context.Context, job DeliveryJob) (string, error) {
	return s.markDeliveryAccepted(ctx, job, "")
}

func (s *Service) markDeliveryAccepted(ctx context.Context, job DeliveryJob, providerMessageID string) (string, error) {
	now := s.now()
	providerMessageID = strings.TrimSpace(providerMessageID)
	if job.Channel == ChannelWhatsApp && (providerMessageID == "" || len(providerMessageID) > 512) {
		return "", errors.New("WhatsApp provider message ID is invalid")
	}
	if job.Channel == ChannelEmail && providerMessageID != "" {
		return "", errors.New("email delivery cannot have a provider message ID")
	}
	table, challengeColumn, err := challengeTable(job.Realm)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var accepted bool
	query := fmt.Sprintf(`UPDATE %s challenge SET delivery_accepted_at=$3,verify_expires_at=$3+INTERVAL '10 minutes' FROM auth_code_delivery_jobs job WHERE job.id=$1 AND job.%s=challenge.id AND job.status='processing' AND job.lease_owner=$2 AND job.delivery_deadline>$3 AND challenge.consumed_at IS NULL RETURNING TRUE`, table, challengeColumn)
	if job.TemplateKey == "account_security_email" {
		query = `UPDATE account_security_events event SET email_accepted_at=$3 FROM auth_code_delivery_jobs job WHERE job.id=$1 AND job.account_security_event_id=event.id AND job.status='processing' AND job.lease_owner=$2 AND job.delivery_deadline>$3 RETURNING TRUE`
	}
	if job.TemplateKey == tessaSecurityEmailTemplate {
		query = `UPDATE tessa_whatsapp_security_events event SET email_accepted_at=$3 FROM auth_code_delivery_jobs job WHERE job.id=$1 AND job.tessa_security_event_id=event.id AND job.status='processing' AND job.lease_owner=$2 AND job.delivery_deadline>$3 RETURNING TRUE`
	}
	if job.TemplateKey == tessaLinkEmailTemplate {
		query = `UPDATE tessa_whatsapp_email_challenges challenge SET delivery_accepted_at=$3
		 FROM auth_code_delivery_jobs job, clients c WHERE job.id=$1 AND job.tessa_link_challenge_id=challenge.id
		 AND job.status='processing' AND job.lease_owner=$2 AND job.delivery_deadline>$3 AND job.lease_expires_at>$3
		 AND challenge.consumed_at IS NULL AND challenge.expires_at>$3
		 AND c.id=challenge.client_id AND c.security_revision=challenge.security_revision AND c.email_verified_at IS NOT NULL
		 AND challenge.notice_revision='` + whatsapp.TessaWhatsAppNotice + `' RETURNING TRUE`
	}
	err = tx.QueryRow(ctx, query, job.ID, job.LeaseOwner, now).Scan(&accepted)
	status, code := "accepted", ""
	if errors.Is(err, pgx.ErrNoRows) {
		status, code, err = "expired", "delivery_deadline_expired", nil
	}
	if err != nil {
		return "", fmt.Errorf("accept auth challenge delivery: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE auth_code_delivery_jobs SET status=$3,payload_ciphertext=NULL,payload_nonce=NULL,payload_key_version=NULL,lease_owner='',lease_expires_at=NULL,error_code=$4,provider_message_id=$5,accepted_at=CASE WHEN $3='accepted' THEN $6::timestamptz ELSE NULL END,completed_at=$6::timestamptz,updated_at=$6::timestamptz WHERE id=$1 AND status='processing' AND lease_owner=$2`, job.ID, job.LeaseOwner, status, code, providerMessageID, now)
	if err != nil {
		return "", fmt.Errorf("finalize accepted auth delivery: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("auth delivery lease was lost")
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return status, nil
}

func (s *Service) recordEmailFailure(ctx context.Context, job DeliveryJob, disposition mailer.TransportDisposition, code string) (string, error) {
	return s.recordDeliveryFailure(ctx, job, disposition, code, 0)
}

func (s *Service) recordDeliveryFailure(ctx context.Context, job DeliveryJob, disposition mailer.TransportDisposition, code string, retryAfter time.Duration) (string, error) {
	now := s.now()
	status, outcome := "failed", "failed"
	next := now
	terminal := true
	if disposition == mailer.DispositionAmbiguous {
		status, outcome = "unknown", "unknown"
	}
	if disposition == mailer.DispositionRetryable && job.AttemptCount < maxDeliveryAttempts {
		next = authRetryAt(now, job.ID, job.AttemptCount)
		if providerRetryAt := now.Add(min(retryAfter, 30*time.Minute)); providerRetryAt.After(next) {
			next = providerRetryAt
		}
		if next.Before(job.Deadline) {
			status, outcome, terminal = "retry", "retry", false
		} else {
			status, outcome, code = "expired", "expired", "delivery_deadline_expired"
		}
	} else if disposition == mailer.DispositionRetryable {
		outcome, code = "retry_exhausted", "retry_exhausted"
	}
	var completed any
	if terminal {
		completed = now
	}
	tag, err := s.db.Exec(ctx, `UPDATE auth_code_delivery_jobs SET status=$3,next_attempt_at=$4,payload_ciphertext=CASE WHEN $6 THEN NULL ELSE payload_ciphertext END,payload_nonce=CASE WHEN $6 THEN NULL ELSE payload_nonce END,payload_key_version=CASE WHEN $6 THEN NULL ELSE payload_key_version END,lease_owner='',lease_expires_at=NULL,error_code=$5,completed_at=$7,updated_at=$8 WHERE id=$1 AND status='processing' AND lease_owner=$2`, job.ID, job.LeaseOwner, status, next, boundedErrorCode(code), terminal, completed, now)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", errors.New("auth delivery lease was lost")
	}
	return outcome, nil
}

func authRetryAt(now time.Time, id uuid.UUID, attempt int) time.Time {
	delay := 5 * time.Second
	for i := 1; i < attempt && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	hasher := fnv.New32a()
	_, _ = hasher.Write(id[:])
	return now.Add(delay + time.Duration(hasher.Sum32()%1000)*time.Millisecond)
}

func boundedErrorCode(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 100 {
		return value[:100]
	}
	return value
}

func (worker *Worker) observeClaim(job DeliveryJob) {
	if worker.metrics == nil {
		return
	}
	latency := worker.service.now().Sub(job.NextAttemptAt)
	if latency < 0 {
		latency = 0
	}
	worker.metrics.ObserveAuthCodeDeliveryClaim(job.Realm, job.Channel, latency)
}
func (worker *Worker) observeOutcome(job DeliveryJob, outcome string) {
	if worker.metrics != nil {
		worker.metrics.ObserveAuthCodeDeliveryOutcome(job.Realm, job.Channel, outcome)
	}
}
