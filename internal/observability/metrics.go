package observability

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry                         *prometheus.Registry
	httpRequests                     *prometheus.CounterVec
	httpDuration                     *prometheus.HistogramVec
	httpResponseBytes                *prometheus.HistogramVec
	dbQueries                        *prometheus.CounterVec
	dbQueryDuration                  *prometheus.HistogramVec
	dbQueryErrors                    *prometheus.CounterVec
	externalRequests                 *prometheus.CounterVec
	externalDuration                 *prometheus.HistogramVec
	notificationEmailOutcomes        *prometheus.CounterVec
	notificationEmailClaimLatency    *prometheus.HistogramVec
	welcomeEmailOutcomes             *prometheus.CounterVec
	welcomeEmailClaimLatency         *prometheus.HistogramVec
	authCodeDeliveryOutcomes         *prometheus.CounterVec
	authCodeDeliveryClaimLatency     *prometheus.HistogramVec
	notificationWhatsAppOutcomes     *prometheus.CounterVec
	notificationWhatsAppClaimLatency *prometheus.HistogramVec
	notificationWhatsAppStatuses     *prometheus.CounterVec
	notificationWhatsAppStatusLag    *prometheus.HistogramVec
	redisOperations                  *prometheus.CounterVec
	redisDuration                    *prometheus.HistogramVec
	cacheRequests                    *prometheus.CounterVec
	sseConnections                   *prometheus.GaugeVec
	sseOpened                        *prometheus.CounterVec
	sseDuration                      *prometheus.HistogramVec
	sseEventLag                      *prometheus.HistogramVec
}

func New() *Metrics {
	metrics := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook",
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Completed HTTP requests by normalized route, method, and status.",
		}, []string{"route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook",
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration by normalized route and method.",
			Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 15, 30, 60},
		}, []string{"route", "method"}),
		httpResponseBytes: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook",
			Subsystem: "http",
			Name:      "response_size_bytes",
			Help:      "HTTP response body size by normalized route and method.",
			Buckets:   prometheus.ExponentialBuckets(256, 4, 9),
		}, []string{"route", "method"}),
		dbQueries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook",
			Subsystem: "db",
			Name:      "queries_total",
			Help:      "Database queries by bounded SQL operation.",
		}, []string{"operation"}),
		dbQueryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook",
			Subsystem: "db",
			Name:      "query_duration_seconds",
			Help:      "Database query duration by bounded SQL operation.",
			Buckets:   []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"operation"}),
		dbQueryErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook",
			Subsystem: "db",
			Name:      "query_errors_total",
			Help:      "Database query errors by bounded SQL operation.",
		}, []string{"operation"}),
		externalRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "external", Name: "requests_total",
			Help: "External HTTP requests by bounded service and outcome.",
		}, []string{"service", "outcome"}),
		externalDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "external", Name: "request_duration_seconds",
			Help:    "External HTTP request duration by bounded service and outcome.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"service", "outcome"}),
		notificationEmailOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "notification_email", Name: "outcomes_total",
			Help: "Durable booking-email outcomes by bounded template and outcome.",
		}, []string{"template", "outcome"}),
		notificationEmailClaimLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "notification_email", Name: "claim_latency_seconds",
			Help:    "Delay between a booking email becoming due and a worker claiming it.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 15, 30, 60, 300, 900, 3600},
		}, []string{"template"}),
		welcomeEmailOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "welcome_email", Name: "outcomes_total",
			Help: "Durable welcome-email outcomes by bounded audience and outcome.",
		}, []string{"audience", "outcome"}),
		welcomeEmailClaimLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "welcome_email", Name: "claim_latency_seconds",
			Help:    "Delay between a welcome email becoming due and a worker claiming it.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 15, 30, 60, 300, 900, 3600},
		}, []string{"audience"}),
		authCodeDeliveryOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "auth_code_delivery", Name: "outcomes_total",
			Help: "Durable authentication-code delivery outcomes by bounded realm, channel, and outcome.",
		}, []string{"realm", "channel", "outcome"}),
		authCodeDeliveryClaimLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "auth_code_delivery", Name: "claim_latency_seconds",
			Help:    "Delay between an authentication code becoming due and a worker claiming it.",
			Buckets: []float64{0.05, 0.1, 0.5, 1, 2.5, 5, 15, 30, 60, 90},
		}, []string{"realm", "channel"}),
		notificationWhatsAppOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "notification_whatsapp", Name: "outcomes_total",
			Help: "Durable booking WhatsApp send outcomes by bounded template and outcome.",
		}, []string{"template", "outcome"}),
		notificationWhatsAppClaimLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "notification_whatsapp", Name: "claim_latency_seconds",
			Help:    "Delay between a booking WhatsApp notification becoming due and a worker claiming it.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 15, 30, 60, 300, 900, 3600},
		}, []string{"template"}),
		notificationWhatsAppStatuses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "notification_whatsapp", Name: "status_outcomes_total",
			Help: "Verified Meta status callback outcomes by bounded status and processing result.",
		}, []string{"status", "outcome"}),
		notificationWhatsAppStatusLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "notification_whatsapp", Name: "status_lag_seconds",
			Help:    "Delay between Meta's status timestamp and durable callback processing.",
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 15, 30, 60, 300, 900, 3600},
		}, []string{"status"}),
		redisOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "redis", Name: "operations_total",
			Help: "Redis operations by bounded command class and outcome.",
		}, []string{"operation", "outcome"}),
		redisDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "redis", Name: "operation_duration_seconds",
			Help:    "Redis operation duration by bounded command class and outcome.",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}, []string{"operation", "outcome"}),
		cacheRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "cache", Name: "requests_total",
			Help: "Application cache requests by bounded cache and outcome.",
		}, []string{"cache", "outcome"}),
		sseConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "tellbook", Subsystem: "sse", Name: "connections",
			Help: "Current SSE connections by bounded stream type.",
		}, []string{"stream"}),
		sseOpened: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "tellbook", Subsystem: "sse", Name: "opened_total",
			Help: "SSE connections opened by bounded stream type.",
		}, []string{"stream"}),
		sseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "sse", Name: "connection_duration_seconds",
			Help:    "SSE connection lifetime by bounded stream type.",
			Buckets: []float64{1, 5, 15, 30, 60, 300, 900, 1800, 3600},
		}, []string{"stream"}),
		sseEventLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "tellbook", Subsystem: "sse", Name: "event_lag_seconds",
			Help:    "Age of durable events when delivered over SSE.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 15, 30, 60, 300},
		}, []string{"stream"}),
	}
	metrics.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		metrics.httpRequests,
		metrics.httpDuration,
		metrics.httpResponseBytes,
		metrics.dbQueries,
		metrics.dbQueryDuration,
		metrics.dbQueryErrors,
		metrics.externalRequests,
		metrics.externalDuration,
		metrics.notificationEmailOutcomes,
		metrics.notificationEmailClaimLatency,
		metrics.welcomeEmailOutcomes,
		metrics.welcomeEmailClaimLatency,
		metrics.authCodeDeliveryOutcomes,
		metrics.authCodeDeliveryClaimLatency,
		metrics.notificationWhatsAppOutcomes,
		metrics.notificationWhatsAppClaimLatency,
		metrics.notificationWhatsAppStatuses,
		metrics.notificationWhatsAppStatusLag,
		metrics.redisOperations,
		metrics.redisDuration,
		metrics.cacheRequests,
		metrics.sseConnections,
		metrics.sseOpened,
		metrics.sseDuration,
		metrics.sseEventLag,
	)
	return metrics
}

func (m *Metrics) ObserveRedisOperation(operation, outcome string, duration time.Duration) {
	if m == nil {
		return
	}
	m.redisOperations.WithLabelValues(operation, outcome).Inc()
	m.redisDuration.WithLabelValues(operation, outcome).Observe(duration.Seconds())
}

func (m *Metrics) ObserveCacheRequest(cache, outcome string) {
	if m == nil {
		return
	}
	m.cacheRequests.WithLabelValues(cache, outcome).Inc()
}

func (m *Metrics) ObserveNotificationEmailClaim(template string, latency time.Duration) {
	if m == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	m.notificationEmailClaimLatency.WithLabelValues(boundedNotificationEmailTemplate(template)).Observe(latency.Seconds())
}

func (m *Metrics) ObserveNotificationEmailOutcome(template, outcome string) {
	if m == nil {
		return
	}
	m.notificationEmailOutcomes.WithLabelValues(
		boundedNotificationEmailTemplate(template), boundedNotificationEmailOutcome(outcome),
	).Inc()
}

func (m *Metrics) ObserveWelcomeEmailClaim(audience string, latency time.Duration) {
	if m == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	m.welcomeEmailClaimLatency.WithLabelValues(boundedWelcomeEmailAudience(audience)).Observe(latency.Seconds())
}

func (m *Metrics) ObserveWelcomeEmailOutcome(audience, outcome string) {
	if m == nil {
		return
	}
	m.welcomeEmailOutcomes.WithLabelValues(
		boundedWelcomeEmailAudience(audience), boundedWelcomeEmailOutcome(outcome),
	).Inc()
}

func (m *Metrics) ObserveAuthCodeDeliveryClaim(realm, channel string, latency time.Duration) {
	if m == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	m.authCodeDeliveryClaimLatency.WithLabelValues(
		boundedAuthRealm(realm), boundedAuthChannel(channel),
	).Observe(latency.Seconds())
}

func (m *Metrics) ObserveAuthCodeDeliveryOutcome(realm, channel, outcome string) {
	if m == nil {
		return
	}
	m.authCodeDeliveryOutcomes.WithLabelValues(
		boundedAuthRealm(realm), boundedAuthChannel(channel), boundedAuthOutcome(outcome),
	).Inc()
}

func boundedAuthRealm(value string) string {
	switch strings.TrimSpace(value) {
	case "provider", "marketplace_customer":
		return strings.TrimSpace(value)
	default:
		return "other"
	}
}

func boundedAuthChannel(value string) string {
	switch strings.TrimSpace(value) {
	case "email", "whatsapp":
		return strings.TrimSpace(value)
	default:
		return "other"
	}
}

func boundedAuthOutcome(value string) string {
	switch strings.TrimSpace(value) {
	case "accepted", "retry", "retry_exhausted", "failed", "unknown", "expired":
		return strings.TrimSpace(value)
	default:
		return "other"
	}
}

func boundedWelcomeEmailAudience(audience string) string {
	switch strings.TrimSpace(audience) {
	case "provider", "marketplace_customer":
		return strings.TrimSpace(audience)
	default:
		return "other"
	}
}

func boundedWelcomeEmailOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "accepted", "retry", "retry_exhausted", "failed", "manual_review":
		return strings.TrimSpace(outcome)
	default:
		return "other"
	}
}

func boundedNotificationEmailTemplate(template string) string {
	switch strings.TrimSpace(template) {
	case "customer:customer_booking_received", "customer:customer_booking_secured",
		"provider:provider_new_booking", "provider:appointment_reminder", "customer:appointment_reminder",
		"provider:booking_rescheduled", "customer:booking_rescheduled",
		"provider:booking_cancelled", "customer:booking_cancelled",
		"provider:booking_expired", "customer:booking_expired",
		"provider:payment_satisfied", "customer:payment_satisfied",
		"provider:payment_failed", "customer:payment_failed",
		"provider:payment_refunded", "customer:payment_refunded",
		"provider:payment_action_required", "customer:payment_action_required":
		return strings.TrimSpace(template)
	default:
		return "other"
	}
}

func boundedNotificationEmailOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "accepted", "retry", "retry_exhausted", "failed", "manual_review", "cancelled":
		return strings.TrimSpace(outcome)
	default:
		return "other"
	}
}

func (m *Metrics) ObserveNotificationWhatsAppClaim(template string, latency time.Duration) {
	if m == nil {
		return
	}
	if latency < 0 {
		latency = 0
	}
	m.notificationWhatsAppClaimLatency.WithLabelValues(boundedNotificationWhatsAppTemplate(template)).Observe(latency.Seconds())
}

func (m *Metrics) ObserveNotificationWhatsAppOutcome(template, outcome string) {
	if m == nil {
		return
	}
	m.notificationWhatsAppOutcomes.WithLabelValues(
		boundedNotificationWhatsAppTemplate(template), boundedNotificationWhatsAppOutcome(outcome),
	).Inc()
}

func (m *Metrics) ObserveNotificationWhatsAppStatus(status, outcome string, lag time.Duration) {
	if m == nil {
		return
	}
	if lag < 0 {
		lag = 0
	}
	status = boundedNotificationWhatsAppStatus(status)
	m.notificationWhatsAppStatuses.WithLabelValues(status, boundedNotificationWhatsAppStatusOutcome(outcome)).Inc()
	m.notificationWhatsAppStatusLag.WithLabelValues(status).Observe(lag.Seconds())
}

func boundedNotificationWhatsAppTemplate(template string) string {
	switch strings.TrimSpace(template) {
	case "provider_new_booking", "provider_booking_reminder", "user_reminder":
		return strings.TrimSpace(template)
	default:
		return "other"
	}
}

func boundedNotificationWhatsAppOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "accepted", "retry", "retry_exhausted", "failed", "unknown", "cancelled":
		return strings.TrimSpace(outcome)
	default:
		return "other"
	}
}

func boundedNotificationWhatsAppStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "sent", "delivered", "read", "failed", "deleted":
		return strings.TrimSpace(status)
	default:
		return "other"
	}
}

func boundedNotificationWhatsAppStatusOutcome(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "applied", "stale", "ignored", "retry", "dead_letter":
		return strings.TrimSpace(outcome)
	default:
		return "other"
	}
}

type RedisPoolSnapshot struct {
	Hits             uint32
	Misses           uint32
	Timeouts         uint32
	WaitCount        uint32
	Unusable         uint32
	WaitDuration     time.Duration
	TotalConnections uint32
	IdleConnections  uint32
	StaleConnections uint32
	PendingRequests  uint32
}

func (m *Metrics) RegisterRedisPool(snapshot func() RedisPoolSnapshot) {
	if m != nil && snapshot != nil {
		m.registry.MustRegister(newRedisPoolCollector(snapshot))
	}
}

type redisPoolCollector struct {
	snapshot func() RedisPoolSnapshot
	desc     map[string]*prometheus.Desc
}

func newRedisPoolCollector(snapshot func() RedisPoolSnapshot) *redisPoolCollector {
	desc := make(map[string]*prometheus.Desc, 10)
	for name, help := range map[string]string{
		"hits_total":                  "Redis connection-pool hits.",
		"misses_total":                "Redis connection-pool misses.",
		"timeouts_total":              "Redis connection-pool acquisition timeouts.",
		"waits_total":                 "Redis connection-pool acquisition waits.",
		"unusable_total":              "Redis connections rejected as unusable.",
		"wait_duration_seconds_total": "Cumulative Redis connection-pool wait duration.",
		"connections":                 "Current Redis connections.",
		"idle_connections":            "Current idle Redis connections.",
		"stale_connections_total":     "Redis stale connections removed from the pool.",
		"pending_requests":            "Current requests waiting for a Redis connection.",
	} {
		desc[name] = prometheus.NewDesc("tellbook_redis_pool_"+name, help, nil, nil)
	}
	return &redisPoolCollector{snapshot: snapshot, desc: desc}
}

func (collector *redisPoolCollector) Describe(channel chan<- *prometheus.Desc) {
	for _, descriptor := range collector.desc {
		channel <- descriptor
	}
}

func (collector *redisPoolCollector) Collect(channel chan<- prometheus.Metric) {
	snapshot := collector.snapshot()
	counters := map[string]float64{
		"hits_total":                  float64(snapshot.Hits),
		"misses_total":                float64(snapshot.Misses),
		"timeouts_total":              float64(snapshot.Timeouts),
		"waits_total":                 float64(snapshot.WaitCount),
		"unusable_total":              float64(snapshot.Unusable),
		"wait_duration_seconds_total": snapshot.WaitDuration.Seconds(),
		"stale_connections_total":     float64(snapshot.StaleConnections),
	}
	for name, value := range counters {
		channel <- prometheus.MustNewConstMetric(collector.desc[name], prometheus.CounterValue, value)
	}
	for name, value := range map[string]float64{
		"connections":      float64(snapshot.TotalConnections),
		"idle_connections": float64(snapshot.IdleConnections),
		"pending_requests": float64(snapshot.PendingRequests),
	} {
		channel <- prometheus.MustNewConstMetric(collector.desc[name], prometheus.GaugeValue, value)
	}
}

func (m *Metrics) InstrumentHTTPClient(client *http.Client, service string) *http.Client {
	if client == nil {
		client = &http.Client{}
	}
	if m == nil {
		return client
	}
	clone := *client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = externalRoundTripper{base: transport, metrics: m, service: service}
	return &clone
}

type externalRoundTripper struct {
	base    http.RoundTripper
	metrics *Metrics
	service string
}

func (t externalRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	startedAt := time.Now()
	response, err := t.base.RoundTrip(request)
	outcome := "network_error"
	if response != nil {
		outcome = strconv.Itoa(response.StatusCode/100) + "xx"
	}
	t.metrics.externalRequests.WithLabelValues(t.service, outcome).Inc()
	t.metrics.externalDuration.WithLabelValues(t.service, outcome).Observe(time.Since(startedAt).Seconds())
	return response, err
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
}

func (m *Metrics) ObserveHTTPRequest(route, method string, status, responseBytes int, duration time.Duration) {
	m.httpRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.httpDuration.WithLabelValues(route, method).Observe(duration.Seconds())
	m.httpResponseBytes.WithLabelValues(route, method).Observe(float64(responseBytes))
}

func (m *Metrics) RegisterDatabasePool(pool *pgxpool.Pool) {
	m.registry.MustRegister(newDatabasePoolCollector(pool, "query"), newQueueCollector(pool))
}

func (m *Metrics) RegisterDirectDatabasePool(pool *pgxpool.Pool) {
	m.registry.MustRegister(newDatabasePoolCollector(pool, "direct"))
}

func (m *Metrics) SSEConnectionOpened(stream string) func() {
	startedAt := time.Now()
	m.sseConnections.WithLabelValues(stream).Inc()
	m.sseOpened.WithLabelValues(stream).Inc()
	return func() {
		m.sseConnections.WithLabelValues(stream).Dec()
		m.sseDuration.WithLabelValues(stream).Observe(time.Since(startedAt).Seconds())
	}
}

func (m *Metrics) ObserveSSEEventLag(stream string, createdAt time.Time) {
	lag := time.Since(createdAt)
	if lag < 0 {
		lag = 0
	}
	m.sseEventLag.WithLabelValues(stream).Observe(lag.Seconds())
}

type queryTrace struct {
	startedAt time.Time
	operation string
}

type queryTraceContextKey struct{}

func (m *Metrics) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTraceContextKey{}, queryTrace{
		startedAt: time.Now(),
		operation: sqlOperation(data.SQL),
	})
}

func (m *Metrics) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(queryTraceContextKey{}).(queryTrace)
	if !ok {
		return
	}
	m.dbQueries.WithLabelValues(trace.operation).Inc()
	m.dbQueryDuration.WithLabelValues(trace.operation).Observe(time.Since(trace.startedAt).Seconds())
	if data.Err != nil {
		m.dbQueryErrors.WithLabelValues(trace.operation).Inc()
	}
}

func sqlOperation(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "OTHER"
	}
	operation := strings.ToUpper(fields[0])
	if operation == "WITH" {
		return "SELECT"
	}
	switch operation {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "COPY", "BEGIN", "COMMIT", "ROLLBACK":
		return operation
	default:
		return "OTHER"
	}
}

type InboxSnapshot struct {
	StreamsCurrent  int64
	StreamsOpened   uint64
	StreamsRejected uint64
	StreamFailures  uint64
	StreamResets    uint64
	EventsDelivered uint64
	EventLagTotal   time.Duration
	EventLagMaximum time.Duration
}

type inboxCollector struct {
	snapshot func() InboxSnapshot
	desc     map[string]*prometheus.Desc
}

func (m *Metrics) RegisterInbox(snapshot func() InboxSnapshot) {
	if snapshot != nil {
		m.registry.MustRegister(newInboxCollector(snapshot))
	}
}

func newInboxCollector(snapshot func() InboxSnapshot) *inboxCollector {
	desc := make(map[string]*prometheus.Desc, 8)
	for name, help := range map[string]string{
		"streams_current":         "Current authenticated inbox SSE streams.",
		"streams_opened_total":    "Authenticated inbox SSE streams opened.",
		"streams_rejected_total":  "Inbox SSE streams rejected by connection limits.",
		"stream_failures_total":   "Inbox SSE stream failures.",
		"stream_resets_total":     "Inbox SSE cursor resets.",
		"events_delivered_total":  "Durable inbox events delivered over SSE.",
		"event_lag_seconds_total": "Cumulative age of delivered inbox events.",
		"event_lag_seconds_max":   "Maximum observed age of a delivered inbox event.",
	} {
		desc[name] = prometheus.NewDesc("tellbook_inbox_"+name, help, nil, nil)
	}
	return &inboxCollector{snapshot: snapshot, desc: desc}
}

func (c *inboxCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.desc {
		ch <- desc
	}
}

func (c *inboxCollector) Collect(ch chan<- prometheus.Metric) {
	snapshot := c.snapshot()
	ch <- prometheus.MustNewConstMetric(c.desc["streams_current"], prometheus.GaugeValue, float64(snapshot.StreamsCurrent))
	ch <- prometheus.MustNewConstMetric(c.desc["streams_opened_total"], prometheus.CounterValue, float64(snapshot.StreamsOpened))
	ch <- prometheus.MustNewConstMetric(c.desc["streams_rejected_total"], prometheus.CounterValue, float64(snapshot.StreamsRejected))
	ch <- prometheus.MustNewConstMetric(c.desc["stream_failures_total"], prometheus.CounterValue, float64(snapshot.StreamFailures))
	ch <- prometheus.MustNewConstMetric(c.desc["stream_resets_total"], prometheus.CounterValue, float64(snapshot.StreamResets))
	ch <- prometheus.MustNewConstMetric(c.desc["events_delivered_total"], prometheus.CounterValue, float64(snapshot.EventsDelivered))
	ch <- prometheus.MustNewConstMetric(c.desc["event_lag_seconds_total"], prometheus.CounterValue, snapshot.EventLagTotal.Seconds())
	ch <- prometheus.MustNewConstMetric(c.desc["event_lag_seconds_max"], prometheus.GaugeValue, snapshot.EventLagMaximum.Seconds())
}

type queueCollector struct {
	pool *pgxpool.Pool
	desc map[string]*prometheus.Desc
}

func newQueueCollector(pool *pgxpool.Pool) *queueCollector {
	labels := []string{"queue"}
	return &queueCollector{pool: pool, desc: map[string]*prometheus.Desc{
		"depth":          prometheus.NewDesc("tellbook_queue_depth", "Runnable or processing jobs currently retained by queue.", labels, nil),
		"oldest":         prometheus.NewDesc("tellbook_queue_oldest_age_seconds", "Age of the oldest runnable job.", labels, nil),
		"retries":        prometheus.NewDesc("tellbook_queue_retry_attempts", "Retry attempts on active queue records.", labels, nil),
		"throughput":     prometheus.NewDesc("tellbook_queue_completed_last_5m", "Jobs completed in the last five minutes.", labels, nil),
		"dead_letters":   prometheus.NewDesc("tellbook_queue_dead_letter_depth", "Jobs retained for operational review after exhausting retries.", labels, nil),
		"scrape_success": prometheus.NewDesc("tellbook_queue_scrape_success", "Whether the latest queue metric collection succeeded.", nil, nil),
	}}
}

func (c *queueCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.desc {
		ch <- desc
	}
}

func (c *queueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := c.pool.Query(ctx, queueMetricsSQL)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.desc["scrape_success"], prometheus.GaugeValue, 0)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var queue string
		var depth, retries, throughput, deadLetters int64
		var oldest float64
		if rows.Scan(&queue, &depth, &oldest, &retries, &throughput, &deadLetters) != nil {
			ch <- prometheus.MustNewConstMetric(c.desc["scrape_success"], prometheus.GaugeValue, 0)
			return
		}
		ch <- prometheus.MustNewConstMetric(c.desc["depth"], prometheus.GaugeValue, float64(depth), queue)
		ch <- prometheus.MustNewConstMetric(c.desc["oldest"], prometheus.GaugeValue, oldest, queue)
		ch <- prometheus.MustNewConstMetric(c.desc["retries"], prometheus.GaugeValue, float64(retries), queue)
		ch <- prometheus.MustNewConstMetric(c.desc["throughput"], prometheus.GaugeValue, float64(throughput), queue)
		ch <- prometheus.MustNewConstMetric(c.desc["dead_letters"], prometheus.GaugeValue, float64(deadLetters), queue)
	}
	if rows.Err() != nil {
		ch <- prometheus.MustNewConstMetric(c.desc["scrape_success"], prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.desc["scrape_success"], prometheus.GaugeValue, 1)
}

const queueMetricsSQL = `
	SELECT 'agreement_lifecycle', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM agreement_jobs WHERE completed_at >= NOW()-INTERVAL '5 minutes'),
		0::bigint
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at) FILTER (WHERE status='queued'))),0)::double precision AS oldest,
			COALESCE(SUM(attempt_count),0)::bigint AS retries
		FROM agreement_jobs WHERE status IN ('queued','processing')
	) active
	UNION ALL
	SELECT 'agreement_generation', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM agreement_template_generation_jobs WHERE completed_at >= NOW()-INTERVAL '5 minutes'),
		0::bigint
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at) FILTER (WHERE status='queued'))),0)::double precision AS oldest,
			COALESCE(SUM(attempt_count),0)::bigint AS retries
		FROM agreement_template_generation_jobs WHERE status IN ('queued','processing')
	) active
	UNION ALL
	SELECT 'financial', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM financial_jobs WHERE completed_at >= NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM financial_jobs WHERE status='dead_letter')
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at) FILTER (WHERE status IN ('pending','failed')))),0)::double precision AS oldest,
			COALESCE(SUM(attempts),0)::bigint AS retries
		FROM financial_jobs WHERE status IN ('pending','failed','processing')
	) active
	UNION ALL
	SELECT 'inbox_ai', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM inbox_ai_turn_jobs WHERE completed_at >= NOW()-INTERVAL '5 minutes'),
		0::bigint
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at) FILTER (WHERE status='queued'))),0)::double precision AS oldest,
			COALESCE(SUM(attempt_count),0)::bigint AS retries
		FROM inbox_ai_turn_jobs WHERE status IN ('queued','processing')
	) active
	UNION ALL
	SELECT 'tessa', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM tessa_runs WHERE completed_at >= NOW()-INTERVAL '5 minutes'),
		0::bigint
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at) FILTER (WHERE status='queued'))),0)::double precision AS oldest,
			COALESCE(SUM(attempt_count),0)::bigint AS retries
		FROM tessa_runs WHERE status IN ('queued','processing')
	) active
	UNION ALL
	SELECT 'provider_daily_metrics', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM provider_daily_metrics WHERE projected_at >= NOW()-INTERVAL '5 minutes'),
		0::bigint
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(enqueued_at))),0)::double precision AS oldest,
			COALESCE(SUM(revision-1),0)::bigint AS retries
		FROM provider_daily_metric_jobs
	) active
	UNION ALL
	SELECT 'notification_event_planner', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM notification_event_jobs
		 WHERE status='completed' AND completed_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM notification_event_jobs WHERE status='dead_letter')
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(
				CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)
				FILTER (WHERE (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM notification_event_jobs WHERE status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'notification_scope_planner', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM notification_scope_replan_jobs
		 WHERE status='completed' AND completed_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM notification_scope_replan_jobs WHERE status='dead_letter')
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(
				CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)
				FILTER (WHERE (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM notification_scope_replan_jobs WHERE status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'notification_in_app', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM notification_in_app_jobs
		 WHERE status='completed' AND completed_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM notification_in_app_jobs WHERE status='dead_letter')
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(scheduled_for)
				FILTER (WHERE scheduled_for<=NOW() AND
					(CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM notification_in_app_jobs WHERE status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'notification_email', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM notification_deliveries
		 WHERE channel='email' AND status='accepted' AND accepted_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM notification_deliveries
		 WHERE channel='email' AND status IN ('failed','manual_review'))
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(scheduled_for)
				FILTER (WHERE scheduled_for<=NOW() AND
					(CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM notification_deliveries
		WHERE channel='email' AND status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'welcome_email', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM welcome_email_jobs
		 WHERE status='accepted' AND accepted_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM welcome_email_jobs WHERE status IN ('failed','manual_review'))
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(
				CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)
				FILTER (WHERE (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM welcome_email_jobs WHERE status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'auth_code_email', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM auth_code_delivery_jobs
		 WHERE channel='email' AND status='accepted' AND accepted_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM auth_code_delivery_jobs
		 WHERE channel='email' AND status IN ('failed','unknown'))
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(
				CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)
				FILTER (WHERE (CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM auth_code_delivery_jobs
		WHERE channel='email' AND status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'notification_whatsapp', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM notification_deliveries
		 WHERE channel='whatsapp' AND provider_status IN ('accepted','sent','delivered','read')
		   AND provider_status_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM notification_deliveries
		 WHERE channel='whatsapp' AND status IN ('failed','manual_review'))
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(scheduled_for)
				FILTER (WHERE scheduled_for<=NOW() AND
					(CASE WHEN status='processing' THEN lease_expires_at ELSE next_attempt_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM notification_deliveries
		WHERE channel='whatsapp' AND status IN ('pending','retry','processing')
	) active
	UNION ALL
	SELECT 'notification_whatsapp_status', active.depth, active.oldest, active.retries,
		(SELECT COUNT(*)::bigint FROM meta_whatsapp_webhook_receipts
		 WHERE event_kind='status' AND processing_status='completed'
		   AND processed_at>=NOW()-INTERVAL '5 minutes'),
		(SELECT COUNT(*)::bigint FROM meta_whatsapp_webhook_receipts
		 WHERE event_kind='status' AND processing_status='dead_letter')
	FROM (
		SELECT COUNT(*)::bigint AS depth,
			COALESCE(EXTRACT(EPOCH FROM (NOW()-MIN(created_at)
				FILTER (WHERE (CASE WHEN processing_status='processing' THEN lease_expires_at ELSE available_at END)<=NOW()))),0)::double precision AS oldest,
			COALESCE(SUM(GREATEST(attempt_count-1,0)),0)::bigint AS retries
		FROM meta_whatsapp_webhook_receipts
		WHERE event_kind='status' AND processing_status IN ('pending','retry','processing')
	) active
`

type databasePoolCollector struct {
	pool *pgxpool.Pool
	desc map[string]*prometheus.Desc
}

func newDatabasePoolCollector(pool *pgxpool.Pool, poolName string) *databasePoolCollector {
	desc := make(map[string]*prometheus.Desc, 12)
	for name, help := range map[string]string{
		"connections":                      "Current database pool connections by state.",
		"connection_limit":                 "Configured maximum database pool connections.",
		"acquires_total":                   "Total successful database pool acquisitions.",
		"acquire_duration_seconds_total":   "Cumulative time spent acquiring database connections.",
		"canceled_acquires_total":          "Total canceled database pool acquisitions.",
		"empty_acquires_total":             "Total acquisitions that waited for a connection.",
		"empty_acquire_wait_seconds_total": "Cumulative time spent waiting when the pool was empty.",
	} {
		labels := []string(nil)
		if name == "connections" {
			labels = []string{"state"}
		}
		desc[name] = prometheus.NewDesc(
			"tellbook_db_pool_"+name,
			help,
			labels,
			prometheus.Labels{"pool": poolName},
		)
	}
	return &databasePoolCollector{pool: pool, desc: desc}
}

func (c *databasePoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.desc {
		ch <- desc
	}
}

func (c *databasePoolCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.pool.Stat()
	for state, value := range map[string]int32{
		"acquired":     stats.AcquiredConns(),
		"idle":         stats.IdleConns(),
		"constructing": stats.ConstructingConns(),
		"total":        stats.TotalConns(),
	} {
		ch <- prometheus.MustNewConstMetric(c.desc["connections"], prometheus.GaugeValue, float64(value), state)
	}
	ch <- prometheus.MustNewConstMetric(c.desc["connection_limit"], prometheus.GaugeValue, float64(stats.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.desc["acquires_total"], prometheus.CounterValue, float64(stats.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.desc["acquire_duration_seconds_total"], prometheus.CounterValue, stats.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.desc["canceled_acquires_total"], prometheus.CounterValue, float64(stats.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.desc["empty_acquires_total"], prometheus.CounterValue, float64(stats.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.desc["empty_acquire_wait_seconds_total"], prometheus.CounterValue, stats.EmptyAcquireWaitTime().Seconds())
}
