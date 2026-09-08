package appdata

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"booking/go-server/internal/whatsapp"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type tessaWorkloadGenerator struct {
	base         tessaIntegrationGenerator
	mu           sync.Mutex
	inputs       []string
	active, peak int
}

func (g *tessaWorkloadGenerator) GenerateJSON(ctx context.Context, system, user string, out any) error {
	g.mu.Lock()
	g.inputs = append(g.inputs, user)
	g.active++
	if g.active > g.peak {
		g.peak = g.active
	}
	g.mu.Unlock()
	defer func() { g.mu.Lock(); g.active--; g.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Millisecond): // Small fake inference delay, not a capacity forecast.
	}
	return g.base.GenerateJSON(ctx, system, user, out)
}

type tessaWorkloadSender struct {
	mu        sync.Mutex
	attempts  map[string]int
	ambiguous []string
}

func (s *tessaWorkloadSender) SendURLButton(ctx context.Context, to, body string, _ whatsapp.URLButton, correlation string) (whatsapp.SendResult, error) {
	return s.SendText(ctx, to, body, correlation)
}

func (s *tessaWorkloadSender) SendText(_ context.Context, _, body, correlation string) (whatsapp.SendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[correlation]++
	if !strings.Contains(body, "Tellbook uses your availability settings") {
		return whatsapp.SendResult{}, errors.New("unexpected committed answer")
	}
	if len(s.attempts)%12 == 0 {
		s.ambiguous = append(s.ambiguous, correlation)
		return whatsapp.SendResult{}, &whatsapp.TransportError{Cause: errors.New("fixture response lost"), Ambiguous: true}
	}
	return whatsapp.SendResult{MessageID: "wamid.fixture." + correlation}, nil
}

func TestTessaWhatsAppMultiProviderWorkloadIntegration(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	repo := NewRepository(pool)
	const providers, waves, turnsPerWave, concurrency = 24, 2, 2, 4
	const phoneID = "19990001"
	ids := make([]uuid.UUID, providers)
	allowlist := make([]string, providers)
	destinations := make([]string, providers)
	for n := range providers {
		ids[n] = insertTessaTestClient(t, ctx, pool)
		allowlist[n] = ids[n].String()
		destinations[n] = fmt.Sprintf("+23480009%05d", n)
		if err := repo.CompleteTessaIntroduction(ctx, ids[n], "test-v1", "test-v1"); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.GetTessaBootstrap(ctx, ids[n], "test-v1", 50); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO tessa_whatsapp_connections(client_id,phone_number_id,destination,status,security_revision,notice_revision,expires_at)
		SELECT id,$2,$3,'active',security_revision,$4,NOW()+INTERVAL '30 days' FROM clients WHERE id=$1`, ids[n], phoneID, destinations[n], whatsapp.TessaWhatsAppNotice); err != nil {
			t.Fatal(err)
		}
	}
	g := &tessaWorkloadGenerator{}
	base := newTessaIntegrationWorker(t, repo, g, nil)
	config := base.config
	config.MaxConcurrency = concurrency
	config.WhatsAppPhoneNumberID, config.WhatsAppProviderAllowlist = phoneID, allowlist
	limiter := NewInboxAIGenerationLimiter(concurrency)
	var generationDuration time.Duration
	for wave := range waves {
		for n, id := range ids {
			for turn := range turnsPerWave {
				question := fmt.Sprintf("Explain bookings tenant_%s wave_%d_turn_%d", id, wave+1, turn+1)
				queueTessaWhatsApp(t, ctx, repo, id, question, func(in *whatsapp.TessaInboundMessage) { in.Sender = destinations[n] })
			}
		}
		// Reconstruct both worker instances between waves. Only PostgreSQL owns
		// thread ordering/context; no per-instance conversation memory is reused.
		workers := make([]*TessaWorker, 2)
		for n := range workers {
			var err error
			workers[n], err = NewTessaWorker(repo, base.service, base.help, limiter, nil, config)
			if err != nil {
				t.Fatal(err)
			}
		}
		started := time.Now()
		group, workCtx := errgroup.WithContext(ctx)
		for n := range concurrency {
			group.Go(func() error {
				for {
					worked, err := workers[n%len(workers)].ProcessOne(workCtx, fmt.Sprintf("b5-%d-%d", wave, n))
					if err != nil {
						return err
					}
					if !worked {
						return nil
					}
				}
			})
		}
		if err := group.Wait(); err != nil {
			t.Fatal(err)
		}
		generationDuration += time.Since(started)
		var completed int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_runs WHERE client_id=ANY($1) AND status='completed'`, ids).Scan(&completed); err != nil || completed != providers*turnsPerWave*(wave+1) {
			t.Fatal("workload did not drain exactly once", completed, err)
		}
	}
	markers := regexp.MustCompile(`tenant_[a-f0-9-]{36}`)
	for _, input := range g.inputs {
		seen := map[string]bool{}
		for _, marker := range markers.FindAllString(input, -1) {
			seen[marker] = true
		}
		if len(seen) != 1 {
			t.Fatal("model context omitted or mixed provider markers", len(seen))
		}
		if strings.HasPrefix(input, "Decide how Tessa") && strings.Contains(input, "wave_2_turn_2") && !strings.Contains(input, "wave_1_turn_1") {
			t.Fatal("worker reconstruction lost follow-up context")
		}
		if strings.HasPrefix(input, "Answer the provider") && strings.Contains(input, "wave_2_turn_2") && strings.Contains(input, "wave_1_turn_1") {
			t.Fatal("synthesis received historical messages instead of fresh evidence")
		}
	}
	if g.peak > concurrency || g.peak < 2 {
		t.Fatal("workload did not exercise bounded concurrency", g.peak)
	}
	const turns = providers * waves * turnsPerWave
	if len(g.inputs) != turns*2 {
		t.Fatal("duplicate/missing inference", len(g.inputs))
	}
	for _, id := range ids {
		bootstrap, err := repo.GetTessaBootstrap(ctx, id, "test-v1", 50)
		if err != nil || len(bootstrap.Messages) != waves*turnsPerWave*2 {
			t.Fatal("incorrect provider history", err)
		}
		for n, message := range bootstrap.Messages {
			if message.Sequence != int64(n+1) || message.SourceChannel != "whatsapp" {
				t.Fatal("unordered or misattributed history")
			}
		}
	}
	links := whatsapp.NewTessaLinkRepository(pool, phoneID, "+2348000000000", true, allowlist)
	if err := links.WithAssistantReplies("https://provider.example.invalid", "test-v1"); err != nil {
		t.Fatal(err)
	}
	sender := &tessaWorkloadSender{attempts: map[string]int{}}
	drain := func() {
		t.Helper()
		workerCtx, stop := context.WithCancel(ctx)
		wake := make(chan struct{}, 1)
		done := make(chan struct{})
		go func() { defer close(done); whatsapp.NewTessaControlWorker(links, sender, nil).Start(workerCtx, wake) }()
		defer func() { stop(); <-done }()
		// A bounded test driver supplies wake hints, not a new application poller.
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				t.Fatal("transport workload deadline", ctx.Err())
			case <-ticker.C:
				select {
				case wake <- struct{}{}:
				default:
				}
				var pending int
				if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM tessa_whatsapp_outbox WHERE client_id=ANY($1) AND status IN ('pending','retry','processing','dispatching')`, ids).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if pending == 0 {
					return
				}
			}
		}
	}
	deliveryStarted := time.Now()
	drain()
	deliveryDuration := time.Since(deliveryStarted)
	drain() // Worker reconstruction must not resend accepted or ambiguous attempts.
	if len(sender.attempts) != turns || len(sender.ambiguous) != turns/12 {
		t.Fatal("unexpected transport outcome counts", len(sender.attempts), len(sender.ambiguous))
	}
	for _, attempts := range sender.attempts {
		if attempts != 1 {
			t.Fatal("automatic duplicate external attempt", attempts)
		}
	}
	var accepted, unknown int
	var p95MS float64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE status='accepted'),COUNT(*) FILTER(WHERE status='unknown') FROM tessa_whatsapp_outbox WHERE client_id=ANY($1)`, ids).Scan(&accepted, &unknown); err != nil || accepted != turns-turns/12 || unknown != turns/12 {
		t.Fatal("ambiguous send misreported", accepted, unknown, err)
	}
	if err := pool.QueryRow(ctx, `SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (r.completed_at-i.created_at))*1000) FROM tessa_whatsapp_ingress i JOIN tessa_runs r ON r.id=i.run_id WHERE i.client_id=ANY($1)`, ids).Scan(&p95MS); err != nil {
		t.Fatal(err)
	}
	t.Logf("LOCAL FAKE-MODEL/TRANSPORT: providers=%d turns=%d worker_instances=2 concurrent_calls=%d peak_model_calls=%d generation=%s ingress_to_completion_p95_ms=%.1f delivery=%s accepted=%d ambiguous=%d duplicate_attempts=0 pool_max_conns=%d", providers, turns, concurrency, g.peak, generationDuration, p95MS, deliveryDuration, accepted, unknown, pool.Config().MaxConns)
}
