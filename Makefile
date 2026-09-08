APP_NAME := tellbook-api
BUILD_DIR := bin

.PHONY: build run test db-up db-down db-status seed-demo import-regions marketplace-discovery-status marketplace-discovery-rebuild marketplace-discovery-repair marketplace-discovery-drain marketplace-discovery-verify marketplace-discovery-query-plans inbox-disable inbox-enable inbox-query-plans inbox-ai-metrics tessa-query-plans tessa-local-evals tessa-local-capacity tessa-external-evals whatsapp-template-conformance auth-whatsapp-template-conformance auth-whatsapp-live-conformance auth-email-live-conformance notification-phase-a-health notification-phase-a-certify scale-seed-smoke scale-seed-baseline scale-seed-large scale-query-plans scale-scenarios scale-certify

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(APP_NAME) ./cmd/api

run:
	go run ./cmd/api

test:
	go test ./...

db-up:
	dbmate --migrations-dir db/migrations up

db-down:
	dbmate --migrations-dir db/migrations down

db-status:
	dbmate --migrations-dir db/migrations status

seed-demo:
	go run ./cmd/seed

scale-seed-smoke:
	go run ./cmd/inbox-load-seed --confirm-local --actors 100 --conversations 500 --messages-per-conversation 4 --output-dir /tmp/tellbook-scale-smoke

scale-seed-baseline:
	go run ./cmd/inbox-load-seed --confirm-local --actors 5000 --conversations 10000 --messages-per-conversation 8 --output-dir /tmp/tellbook-scale-baseline

scale-seed-large:
	go run ./cmd/inbox-load-seed --confirm-local --actors 25000 --conversations 50000 --messages-per-conversation 12 --output-dir /tmp/tellbook-scale-large

scale-query-plans:
	bash scripts/capture-scale-baseline.sh

scale-scenarios:
	bash scripts/capture-scale-scenarios.sh "$(or $(SCALE_DIR),/tmp/tellbook-scale-baseline)"

scale-certify:
	@test -n "$(CERTIFICATION_REPORT)" || (echo "CERTIFICATION_REPORT is required" >&2; exit 1)
	node scripts/check-scale-certification.mjs "$(CERTIFICATION_REPORT)"

import-regions:
	go run ./cmd/import-regions

marketplace-discovery-status:
	go run ./cmd/marketplace-discovery --action status

marketplace-discovery-rebuild:
	go run ./cmd/marketplace-discovery --action rebuild

marketplace-discovery-repair:
	go run ./cmd/marketplace-discovery --action repair --provider-id "$(PROVIDER_ID)"

marketplace-discovery-drain:
	go run ./cmd/marketplace-discovery --action drain

marketplace-discovery-verify:
	go run ./cmd/marketplace-discovery --action verify

marketplace-discovery-query-plans:
	@test -n "$$DATABASE_URL" || (echo "DATABASE_URL is required" >&2; exit 1)
	psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f scripts/marketplace-discovery-query-plans.sql

inbox-disable:
	go run ./cmd/inbox-admin --action disable --conversation-id "$(CONVERSATION_ID)" --reason "$(REASON)" --operator "$(OPERATOR)"

inbox-enable:
	go run ./cmd/inbox-admin --action enable --conversation-id "$(CONVERSATION_ID)" --reason "$(REASON)" --operator "$(OPERATOR)"

inbox-query-plans:
	@test -n "$$DATABASE_URL" || (echo "DATABASE_URL is required" >&2; exit 1)
	@psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f scripts/inbox-query-plans.sql

inbox-ai-metrics:
	@test -n "$$DATABASE_URL" || (echo "DATABASE_URL is required" >&2; exit 1)
	@psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -v window="$(or $(WINDOW),24 hours)" -f scripts/inbox-ai-metrics.sql

tessa-query-plans:
	@test -n "$$DATABASE_URL" || (echo "DATABASE_URL is required" >&2; exit 1)
	@psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f scripts/tessa-query-plans.sql

tessa-local-evals:
	@test -n "$$LLM_BASE_URL" || (echo "LLM_BASE_URL is required" >&2; exit 1)
	@test -n "$$LLM_MODEL" || (echo "LLM_MODEL is required" >&2; exit 1)
	RUN_TESSA_LOCAL_EVALS=true go test ./internal/tessa -run 'TestTessa(LocalConformance|BookingPresentationLocalConformance|GroundingConversationLocalConformance|OperationalGroundingLocalConformance|CalendarPeriodLocalConformance|BookingCountLocalConformance)' -count=1 -v

tessa-local-capacity:
	@test -n "$$LLM_BASE_URL" || (echo "LLM_BASE_URL is required" >&2; exit 1)
	@test -n "$$LLM_MODEL" || (echo "LLM_MODEL is required" >&2; exit 1)
	RUN_TESSA_LOCAL_CAPACITY=true go test ./internal/tessa -run TestTessaLocalCapacity -count=1 -v

tessa-external-evals:
	RUN_TESSA_EXTERNAL_EVALS=true go test ./cmd/api -run TestTessaExternalFallbackConformance -count=1 -v

whatsapp-template-conformance:
	go run ./cmd/whatsapp-template-conformance

auth-whatsapp-template-conformance:
	go run ./cmd/whatsapp-template-conformance --scope auth

auth-whatsapp-live-conformance:
	RUN_AUTH_WHATSAPP_LIVE_CONFORMANCE=true go test ./internal/authchallenge -run '(TestWhatsAppAuthLiveConformance|TestMarketplaceWhatsAppAuthLiveConformance)$$' -count=1 -v

auth-email-live-conformance:
	RUN_AUTH_EMAIL_LIVE_CONFORMANCE=true go test ./internal/authchallenge -run '^TestEmailAuthLiveConformance$$' -count=1 -v

notification-phase-a-health:
	@test -n "$$DATABASE_URL" || (echo "DATABASE_URL is required" >&2; exit 1)
	@psql "$$DATABASE_URL" -v ON_ERROR_STOP=1 -f scripts/notification-phase-a-health.sql

notification-phase-a-certify:
	@test -n "$$TEST_DATABASE_URL" || (echo "TEST_DATABASE_URL is required" >&2; exit 1)
	@test "$$NOTIFICATION_CERTIFICATION_ALLOW_WRITES" = "true" || (echo "NOTIFICATION_CERTIFICATION_ALLOW_WRITES=true is required; use a disposable local/staging database" >&2; exit 1)
	RUN_NOTIFICATION_PHASE_A_CERTIFICATION=true go test -race -p=1 ./internal/notifications ./internal/whatsapp -run 'TestNotificationPhaseA' -count=1 -timeout=5m -v
