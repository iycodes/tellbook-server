package whatsapp_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/notifications"
	"booking/go-server/internal/whatsapp"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationPhaseAWebhookBurstPersistenceAndPhoneIsolation(t *testing.T) {
	if os.Getenv("RUN_NOTIFICATION_PHASE_A_CERTIFICATION") != "true" {
		t.Skip("RUN_NOTIFICATION_PHASE_A_CERTIFICATION is not true")
	}
	if os.Getenv("NOTIFICATION_CERTIFICATION_ALLOW_WRITES") != "true" {
		t.Fatal("NOTIFICATION_CERTIFICATION_ALLOW_WRITES=true is required")
	}
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required")
	}
	requestCount := webhookCertificationRequests(t)
	const (
		statusesPerRequest = 10
		appSecret          = "notification-certification-app-secret"
		businessID         = "222"
		phoneNumberID      = "333"
		wrongPhoneID       = "999"
	)
	prefix := fmt.Sprintf("wamid.phase-a-cert.%d.", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var existingReceipts int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM meta_whatsapp_webhook_receipts`).Scan(&existingReceipts); err != nil {
		t.Fatal(err)
	}
	if existingReceipts != 0 {
		t.Fatalf("certification requires an empty WhatsApp receipt queue; found %d rows", existingReceipts)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			DELETE FROM meta_whatsapp_webhook_receipts WHERE wamid LIKE $1
		`, prefix+"%")
	})
	handler, err := whatsapp.NewWebhookHandler(
		whatsapp.WebhookConfig{
			AppSecret: appSecret, VerifyToken: "notification-certification-verify-token",
			BusinessID: businessID, PhoneNumberID: phoneNumberID, MaxEvents: 1000,
		},
		whatsapp.NewWebhookRepository(pool, nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}

	latencies := make([]time.Duration, requestCount)
	errorsByRequest := make([]error, requestCount)
	var requests sync.WaitGroup
	requests.Add(requestCount)
	for requestIndex := range requestCount {
		go func(requestIndex int) {
			defer requests.Done()
			body := webhookCertificationBody(
				prefix, requestIndex, statusesPerRequest, businessID, phoneNumberID, wrongPhoneID,
			)
			request := httptest.NewRequest(http.MethodPost, "/v1/webhooks/meta/whatsapp", bytes.NewReader(body))
			request.Header.Set("X-Hub-Signature-256", webhookCertificationSignature(body, appSecret))
			response := httptest.NewRecorder()
			started := time.Now()
			handler.ServeHTTP(response, request)
			latencies[requestIndex] = time.Since(started)
			if response.Code != http.StatusOK {
				errorsByRequest[requestIndex] = fmt.Errorf("status=%d body=%s", response.Code, response.Body.String())
			}
		}(requestIndex)
	}
	requests.Wait()
	for requestIndex, requestErr := range errorsByRequest {
		if requestErr != nil {
			t.Fatalf("webhook request %d failed: %v", requestIndex, requestErr)
		}
	}

	wantReceipts := requestCount * statusesPerRequest
	var correctPhone, wrongPhone, pending int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FILTER (WHERE phone_number_id=$2),
		       COUNT(*) FILTER (WHERE phone_number_id=$3),
		       COUNT(*) FILTER (WHERE processing_status='pending')
		FROM meta_whatsapp_webhook_receipts WHERE wamid LIKE $1
	`, prefix+"%", phoneNumberID, wrongPhoneID).Scan(&correctPhone, &wrongPhone, &pending); err != nil {
		t.Fatal(err)
	}
	if correctPhone != wantReceipts || wrongPhone != 0 || pending != wantReceipts {
		t.Fatalf(
			"persisted receipts correct=%d wrong_phone=%d pending=%d, want %d/0/%d",
			correctPhone, wrongPhone, pending, wantReceipts, wantReceipts,
		)
	}

	repository, err := notifications.NewRepository(
		pool, "notification-webhook-certification-key-32-bytes", nil, false, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	workerContext, stopWorker := context.WithCancel(ctx)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		notifications.NewWhatsAppWorker(
			repository, nil, nil, nil, nil, 16, 10*time.Second, false,
		).Start(workerContext)
	}()
	deadline := time.NewTimer(30 * time.Second)
	poll := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	defer poll.Stop()
	for {
		var completed int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM meta_whatsapp_webhook_receipts
			WHERE wamid LIKE $1 AND processing_status='completed'
		`, prefix+"%").Scan(&completed); err != nil {
			stopWorker()
			t.Fatal(err)
		}
		if completed == wantReceipts {
			break
		}
		select {
		case <-deadline.C:
			stopWorker()
			t.Fatalf("webhook status worker completed %d/%d receipts", completed, wantReceipts)
		case <-poll.C:
		case <-ctx.Done():
			stopWorker()
			t.Fatal(ctx.Err())
		}
	}
	stopWorker()
	<-drained
	sort.Slice(latencies, func(left, right int) bool { return latencies[left] < latencies[right] })
	p95 := latencies[(len(latencies)*95-1)/100]
	maxP95 := webhookCertificationP95(t)
	if p95 > maxP95 {
		t.Fatalf("webhook persistence p95 %s exceeds %s", p95, maxP95)
	}
	t.Logf(
		"notification Phase A webhook certification passed: requests=%d receipts=%d persistence_p95=%s",
		requestCount, wantReceipts, p95,
	)
}

func webhookCertificationRequests(t *testing.T) int {
	t.Helper()
	const defaultRequests = 50
	raw := os.Getenv("NOTIFICATION_CERTIFICATION_WEBHOOK_REQUESTS")
	if raw == "" {
		return defaultRequests
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 10 || value > 1000 {
		t.Fatalf("NOTIFICATION_CERTIFICATION_WEBHOOK_REQUESTS must be an integer between 10 and 1000")
	}
	return value
}

func webhookCertificationP95(t *testing.T) time.Duration {
	t.Helper()
	const defaultP95 = 500 * time.Millisecond
	raw := os.Getenv("NOTIFICATION_CERTIFICATION_WEBHOOK_P95")
	if raw == "" {
		return defaultP95
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 10*time.Millisecond || value > 10*time.Second {
		t.Fatalf("NOTIFICATION_CERTIFICATION_WEBHOOK_P95 must be between 10ms and 10s")
	}
	return value
}

func webhookCertificationBody(
	prefix string,
	requestIndex int,
	statusesPerRequest int,
	businessID string,
	phoneNumberID string,
	wrongPhoneID string,
) []byte {
	correct := make([]byte, 0, statusesPerRequest*160)
	wrong := make([]byte, 0, statusesPerRequest*160)
	for statusIndex := range statusesPerRequest {
		if statusIndex > 0 {
			correct = append(correct, ',')
			wrong = append(wrong, ',')
		}
		correct = fmt.Appendf(
			correct,
			`{"id":"%s%d.correct.%d","status":"sent","timestamp":"1788523200"}`,
			prefix, requestIndex, statusIndex,
		)
		wrong = fmt.Appendf(
			wrong,
			`{"id":"%s%d.wrong.%d","status":"sent","timestamp":"1788523200"}`,
			prefix, requestIndex, statusIndex,
		)
	}
	return fmt.Appendf(
		nil,
		`{"object":"whatsapp_business_account","entry":[{"id":"%s","changes":[`+
			`{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"%s"},"statuses":[%s]}},`+
			`{"field":"messages","value":{"messaging_product":"whatsapp","metadata":{"phone_number_id":"%s"},"statuses":[%s]}}]}]}`,
		businessID, phoneNumberID, correct, wrongPhoneID, wrong,
	)
}

func webhookCertificationSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
