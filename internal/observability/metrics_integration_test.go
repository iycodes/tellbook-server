package observability

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationEmailMetricsUseBoundedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveNotificationEmailClaim("provider:appointment_reminder", 3*time.Second)
	metrics.ObserveNotificationEmailOutcome("provider:appointment_reminder", "manual_review")
	metrics.ObserveNotificationEmailOutcome("untrusted:template", "untrusted-outcome")

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)
	for _, expected := range []string{
		`tellbook_notification_email_claim_latency_seconds_count{template="provider:appointment_reminder"} 1`,
		`tellbook_notification_email_outcomes_total{outcome="manual_review",template="provider:appointment_reminder"} 1`,
		`tellbook_notification_email_outcomes_total{outcome="other",template="other"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics output omitted %q:\n%s", expected, output)
		}
	}
}

func TestNotificationWhatsAppMetricsUseBoundedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveNotificationWhatsAppClaim("provider_new_booking", 2*time.Second)
	metrics.ObserveNotificationWhatsAppOutcome("provider_new_booking", "accepted")
	metrics.ObserveNotificationWhatsAppStatus("delivered", "applied", 3*time.Second)
	metrics.ObserveNotificationWhatsAppOutcome("untrusted-template", "untrusted-outcome")

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, request)
	body, err := io.ReadAll(response.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	output := string(body)
	for _, expected := range []string{
		`tellbook_notification_whatsapp_claim_latency_seconds_count{template="provider_new_booking"} 1`,
		`tellbook_notification_whatsapp_outcomes_total{outcome="accepted",template="provider_new_booking"} 1`,
		`tellbook_notification_whatsapp_status_outcomes_total{outcome="applied",status="delivered"} 1`,
		`tellbook_notification_whatsapp_status_lag_seconds_count{status="delivered"} 1`,
		`tellbook_notification_whatsapp_outcomes_total{outcome="other",template="other"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics output omitted %q:\n%s", expected, output)
		}
	}
}

func TestWelcomeEmailMetricsUseBoundedLabels(t *testing.T) {
	metrics := New()
	metrics.ObserveWelcomeEmailClaim("provider", time.Second)
	metrics.ObserveWelcomeEmailOutcome("provider", "accepted")
	metrics.ObserveWelcomeEmailOutcome("untrusted", "untrusted")

	request := httptest.NewRequest("GET", "/metrics", nil)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, request)
	output := response.Body.String()
	for _, expected := range []string{
		`tellbook_welcome_email_claim_latency_seconds_count{audience="provider"} 1`,
		`tellbook_welcome_email_outcomes_total{audience="provider",outcome="accepted"} 1`,
		`tellbook_welcome_email_outcomes_total{audience="other",outcome="other"} 1`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("metrics output omitted %q:\n%s", expected, output)
		}
	}
}

func TestQueueMetricsSQLIncludesNotificationQueues(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(context.Background(), queueMetricsSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wanted := map[string]bool{
		"welcome_email":                false,
		"notification_event_planner":   false,
		"notification_scope_planner":   false,
		"notification_in_app":          false,
		"notification_email":           false,
		"notification_whatsapp":        false,
		"notification_whatsapp_status": false,
	}
	for rows.Next() {
		var queue string
		var depth, retries, throughput, deadLetters int64
		var oldest float64
		if err := rows.Scan(&queue, &depth, &oldest, &retries, &throughput, &deadLetters); err != nil {
			t.Fatal(err)
		}
		if depth < 0 || oldest < 0 || retries < 0 || throughput < 0 || deadLetters < 0 {
			t.Fatalf("queue %q returned a negative metric", queue)
		}
		if _, ok := wanted[queue]; ok {
			wanted[queue] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for queue, found := range wanted {
		if !found {
			t.Errorf("queue metrics omitted %q", queue)
		}
	}
}
