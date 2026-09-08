package tessa

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"booking/go-server/internal/aierror"
	"booking/go-server/internal/tessaconfig"
)

type scriptedGenerator struct {
	calls int
	fill  func(int, any) error
}

func TestWhatsAppAnswerLimitIsEnforcedBeforeCommit(t *testing.T) {
	for _, test := range []struct {
		channel string
		length  int
		valid   bool
	}{
		{"whatsapp", 1024, true}, {"whatsapp", 1025, false}, {"web", 4000, true},
	} {
		generator := &scriptedGenerator{fill: func(_ int, out any) error {
			out.(*Answer).Parts = []AnswerPart{{Text: strings.Repeat("😀", test.length)}}
			return nil
		}}
		service, err := NewService(Provider{Name: "local", Model: "test", Generator: generator, Timeout: time.Second}, nil, 12000)
		if err != nil {
			t.Fatal(err)
		}
		answer, err := service.GenerateAnswer(context.Background(), service.Primary(), SynthesisInput{SourceChannel: test.channel, MaxAnswerCharacters: 9000})
		if (err == nil) != test.valid {
			t.Fatalf("channel=%s length=%d error=%v", test.channel, test.length, err)
		}
		if test.valid && utf8.RuneCountInString(answer.Content) != test.length {
			t.Fatal("answer was truncated")
		}
		if !test.valid && generator.calls != 2 {
			t.Fatalf("repair attempts=%d", generator.calls)
		}
	}
}

func (generator *scriptedGenerator) GenerateJSON(_ context.Context, _, _ string, destination any) error {
	generator.calls++
	return generator.fill(generator.calls, destination)
}

type timeoutGenerator struct{}

func (timeoutGenerator) GenerateJSON(ctx context.Context, _, _ string, _ any) error {
	<-ctx.Done()
	return ctx.Err()
}

type schemaScriptedGenerator struct {
	jsonCalls   int
	schemaCalls int
	schemaName  string
	schema      json.RawMessage
	response    json.RawMessage
}

func (generator *schemaScriptedGenerator) GenerateJSON(context.Context, string, string, any) error {
	generator.jsonCalls++
	return errors.New("non-schema generation was used")
}

func (generator *schemaScriptedGenerator) GenerateJSONSchema(
	_ context.Context,
	_, _, schemaName string,
	schema json.RawMessage,
) (json.RawMessage, error) {
	generator.schemaCalls++
	generator.schemaName = schemaName
	generator.schema = append(json.RawMessage(nil), schema...)
	return append(json.RawMessage(nil), generator.response...), nil
}

func TestGeneratePlanRepairsOneInvalidSemanticResponse(t *testing.T) {
	generator := &scriptedGenerator{fill: func(call int, destination any) error {
		plan := destination.(*Plan)
		if call == 1 {
			*plan = Plan{Scope: "in_scope", Intent: "booking", AnswerMode: "direct", DirectResponse: "invented"}
			return nil
		}
		*plan = Plan{
			Scope: "in_scope", Intent: "booking", AnswerMode: "tools",
			Tools: []ToolRequest{{Name: "search_tellbook_help", Query: "booking status"}},
		}
		return nil
	}}
	service, err := NewService(Provider{Name: "local", Model: "test", Generator: generator, Timeout: time.Second}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.GeneratePlan(context.Background(), service.Primary(), PlanInput{Question: "How do bookings work?"})
	if err != nil {
		t.Fatalf("GeneratePlan() error = %v", err)
	}
	if generator.calls != 2 || plan.AnswerMode != "tools" {
		t.Fatalf("repair calls=%d plan=%+v", generator.calls, plan)
	}
}

func TestGeneratePlanClassifiesProviderDeadlineForFallback(t *testing.T) {
	service, err := NewService(Provider{
		Name: "local", Model: "test", Generator: timeoutGenerator{}, Timeout: 5 * time.Millisecond,
	}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GeneratePlan(context.Background(), service.Primary(), PlanInput{Question: "Hello"})
	if err == nil || !aierror.IsRetryable(err) {
		t.Fatalf("GeneratePlan() error = %v, want retryable timeout", err)
	}
	var providerError *aierror.Error
	if !errors.As(err, &providerError) || providerError.Kind != aierror.KindTimeout {
		t.Fatalf("GeneratePlan() error = %v, want provider timeout", err)
	}
}

func TestGeneratePlanClarifiesAmbiguousBareWeekdayWithoutCallingProvider(t *testing.T) {
	generator := &scriptedGenerator{fill: func(_ int, _ any) error {
		return errors.New("provider should not be called")
	}}
	service, err := NewService(Provider{Name: "local", Model: "test", Generator: generator, Timeout: time.Second}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.GeneratePlan(context.Background(), service.Primary(), PlanInput{Question: "Do I have bookings that Friday?"})
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 0 || plan.Scope != "needs_clarification" || plan.AnswerMode != "direct" ||
		!strings.Contains(plan.DirectResponse, "Friday") {
		t.Fatalf("calls=%d plan=%+v", generator.calls, plan)
	}
}

func TestAmbiguousWeekdayReferenceAllowsQualifiedOrReferredDates(t *testing.T) {
	for _, input := range []PlanInput{
		{Question: "Show my bookings this Friday"},
		{Question: "Show my bookings Friday next week"},
		{Question: "Show my bookings Friday 4 September"},
		{Question: "What about the bookings that Friday?", RecentMessages: []ContextMessage{{Role: "assistant", Content: "The date is 2026-09-04."}}},
	} {
		if got := ambiguousWeekdayReference(input); got != "" {
			t.Fatalf("ambiguousWeekdayReference(%+v) = %q", input, got)
		}
	}
	if got := ambiguousWeekdayReference(PlanInput{
		Question:       "What about the bookings that Friday?",
		RecentMessages: []ContextMessage{{Role: "assistant", Content: "Either 2026-09-04 or 2026-09-11."}},
	}); got != "Friday" {
		t.Fatalf("multiple referents resolved as %q", got)
	}
	if got := ambiguousWeekdayReference(PlanInput{Question: "Write a poem about Friday"}); got != "" {
		t.Fatalf("out-of-scope weekday prompt was intercepted as %q", got)
	}
	for _, question := range []string{
		"Write a free verse poem about Friday",
		"Review my essay about Friday",
		"Explain the word prepaid on Friday",
	} {
		if got := ambiguousWeekdayReference(PlanInput{Question: question}); got != "" {
			t.Fatalf("out-of-scope weekday prompt %q was intercepted as %q", question, got)
		}
	}
	if got := ambiguousWeekdayReference(PlanInput{Question: "Am I free Friday?"}); got != "Friday" {
		t.Fatalf("provider availability prompt was not clarified, got %q", got)
	}
}

func TestGenerateAnswerRepairsEmptyContentOnce(t *testing.T) {
	generator := &scriptedGenerator{fill: func(call int, destination any) error {
		answer := destination.(*Answer)
		if call == 2 {
			answer.Parts = []AnswerPart{{Text: "You can review it from Bookings."}}
		}
		return nil
	}}
	service, err := NewService(Provider{Name: "local", Model: "test", Generator: generator, Timeout: time.Second}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := service.GenerateAnswer(context.Background(), service.Primary(), SynthesisInput{Question: "Where?"})
	if err != nil {
		t.Fatalf("GenerateAnswer() error = %v", err)
	}
	if generator.calls != 2 || answer.Content == "" {
		t.Fatalf("repair calls=%d answer=%+v", generator.calls, answer)
	}
}

func TestValidatePlanRejectsUnknownOrDependentTools(t *testing.T) {
	for _, plan := range []Plan{
		{Scope: "in_scope", Intent: "booking", AnswerMode: "tools", Tools: []ToolRequest{{Name: "cancel_booking"}}},
		{Scope: "in_scope", Intent: "booking", AnswerMode: "tools", Tools: []ToolRequest{
			{Name: "search_tellbook_help", Query: "booking"},
			{Name: "search_tellbook_help", Query: "booking"},
		}},
	} {
		if err := ValidatePlan(plan); err == nil {
			t.Fatalf("ValidatePlan() accepted %+v", plan)
		}
	}
}

func TestGeneratePlanUsesStrictSchemaAndReturnsNormalizedPlan(t *testing.T) {
	generator := &schemaScriptedGenerator{response: json.RawMessage(`{
		"scope":" in_scope ","intent":" booking_help ","answer_mode":" tools ",
		"tools":[{"name":" search_tellbook_help ","query":" booking status "}],
		"direct_response":""
	}`)}
	service, err := NewService(Provider{
		Name: "local", Model: "test", Generator: generator, Timeout: time.Second,
	}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.GeneratePlan(context.Background(), service.Primary(), PlanInput{Question: "How do bookings work?"})
	if err != nil {
		t.Fatalf("GeneratePlan() error = %v", err)
	}
	if generator.schemaCalls != 1 || generator.jsonCalls != 0 || generator.schemaName != "tessa_plan" {
		t.Fatalf("schema/json calls = %d/%d, schema name = %q", generator.schemaCalls, generator.jsonCalls, generator.schemaName)
	}
	if !strings.Contains(string(generator.schema), `"additionalProperties":false`) {
		t.Fatalf("schema is not strict: %s", generator.schema)
	}
	if plan.Scope != "in_scope" || plan.AnswerMode != "tools" ||
		plan.Tools[0].Name != "search_tellbook_help" || plan.Tools[0].Query != "booking status" {
		t.Fatalf("plan was not normalized: %+v", plan)
	}
}

func TestPlanInputDropsOldestMessagesToHonorConfiguredLimit(t *testing.T) {
	const budget = tessaconfig.MinimumInputTokens + 100
	service, err := NewService(Provider{
		Name: "local", Model: "test", Generator: &scriptedGenerator{}, Timeout: time.Second,
	}, nil, budget)
	if err != nil {
		t.Fatal(err)
	}
	recent := make([]ContextMessage, 12)
	for index := range recent {
		recent[index] = ContextMessage{Role: "provider", Content: strings.Repeat("context ", 120)}
	}
	payload, err := service.fitPlanInput(PlanInput{
		Question: "How do bookings work?", RecentMessages: recent,
		CurrentDate: "2026-08-30", Timezone: "Africa/Lagos",
	})
	if err != nil {
		t.Fatalf("fitPlanInput() error = %v", err)
	}
	var fitted PlanInput
	if err := json.Unmarshal(payload, &fitted); err != nil {
		t.Fatal(err)
	}
	if len(fitted.RecentMessages) >= len(recent) {
		t.Fatalf("recent messages were not bounded: %d", len(fitted.RecentMessages))
	}
	if got := estimateInputTokens(planningSystemPrompt+planningPromptPrefix+planningRepairSuffix, string(payload)); got > budget {
		t.Fatalf("estimated input tokens = %d, want <= %d", got, budget)
	}
}

func TestPlanInputDropsSummaryBeforeRecentMessages(t *testing.T) {
	service, err := NewService(Provider{
		Name: "local", Model: "test", Generator: &scriptedGenerator{}, Timeout: time.Second,
	}, nil, 4000)
	if err != nil {
		t.Fatal(err)
	}
	recent := []ContextMessage{{Role: "provider", Content: "What about the same service tomorrow?"}}
	payload, err := service.fitPlanInput(PlanInput{
		Question: "Is it available?", ConversationSummary: strings.Repeat("older context ", 1500),
		RecentMessages: recent, CurrentDate: "2026-08-30", Timezone: "Africa/Lagos",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fitted PlanInput
	if err := json.Unmarshal(payload, &fitted); err != nil {
		t.Fatal(err)
	}
	if len(fitted.RecentMessages) != 1 || len(fitted.ConversationSummary) >= 1500*len("older context ") {
		t.Fatalf("fitted context = summary bytes %d, recent %+v", len(fitted.ConversationSummary), fitted.RecentMessages)
	}
}

func TestTrailingUTF8PreservesValidMultibyteText(t *testing.T) {
	value := strings.Repeat("🙂", 20)
	trimmed := trailingUTF8(value, 17)
	if !utf8.ValidString(trimmed) || len(trimmed) > 17 || trimmed == "" {
		t.Fatalf("trailingUTF8() = %q (%d bytes)", trimmed, len(trimmed))
	}
}

func TestMinimumInputTokensAlwaysFitsTheStaticPlanningRequest(t *testing.T) {
	minimum := tessaconfig.MinimumInputTokens
	if actual := minimumPlanningInputTokens(); actual > minimum {
		t.Fatalf("static planning request needs %d tokens, above configured floor %d", actual, minimum)
	}
	provider := Provider{Name: "local", Model: "test", Generator: &scriptedGenerator{}, Timeout: time.Second}
	if _, err := NewService(provider, nil, minimum-1); err == nil {
		t.Fatalf("NewService() accepted %d tokens below the derived minimum", minimum-1)
	}
	service, err := NewService(provider, nil, minimum)
	if err != nil {
		t.Fatalf("NewService() rejected the derived minimum: %v", err)
	}
	if _, err := service.fitPlanInput(PlanInput{RecentMessages: []ContextMessage{}}); err != nil {
		t.Fatalf("fitPlanInput() did not fit at the derived minimum: %v", err)
	}
}

func TestValidatePlanAcceptsBoundedBookingTools(t *testing.T) {
	plan := Plan{
		Scope: "in_scope", Intent: "schedule", AnswerMode: "tools",
		Tools: []ToolRequest{{Name: "get_schedule", Period: "custom", TimeScope: "period", From: "2026-08-30", To: "2026-09-05"}},
	}
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan() error = %v", err)
	}
}

func TestValidatePlanRejectsUnsafeBookingArguments(t *testing.T) {
	tests := []ToolRequest{
		{Name: "get_booking", BookingID: "not-a-booking"},
		{Name: "search_bookings", From: "2026-01-01", To: "2027-01-02", Limit: 25},
		{Name: "search_bookings", From: "2026-08-30", To: "2026-08-31", Limit: 25, Statuses: []string{"refunded"}},
		{Name: "get_availability", From: "2026-08-30", To: "2026-10-01"},
	}
	for _, request := range tests {
		if dateRangeTool(request.Name) {
			request.Period = "custom"
		}
		plan := Plan{Scope: "in_scope", Intent: "booking_search", AnswerMode: "tools", Tools: []ToolRequest{request}}
		if err := ValidatePlan(plan); err == nil {
			t.Fatalf("ValidatePlan() accepted %+v", request)
		}
	}
}

func TestValidatePlanAcceptsBoundedOperationalTools(t *testing.T) {
	requests := []ToolRequest{
		{Name: "search_services", Selection: "list", Query: "consultation", Status: "published", Limit: 8},
		{Name: "get_service", ServiceID: "10000000-0000-4000-8000-000000000001"},
		{Name: "search_customers", Selection: "top", Query: "Ada", Limit: 8},
		{Name: "get_customer_booking_summary", CustomerID: "20000000-0000-4000-8000-000000000001"},
		{Name: "get_payment_summary", From: "2026-08-01", To: "2026-08-30"},
		{Name: "get_booking_payment_status", BookingID: "30000000-0000-4000-8000-000000000001"},
		{Name: "get_payout_summary", From: "2026-08-01", To: "2026-08-30"},
		{Name: "get_booking_metrics", Metric: "summary", TimeScope: "period", From: "2026-08-01", To: "2026-08-30", ComparePrevious: true},
		{Name: "get_inbox_summary"},
		{Name: "get_review_summary", From: "2026-08-01", To: "2026-08-30"},
		{Name: "get_public_profile_status"},
	}
	for _, request := range requests {
		if dateRangeTool(request.Name) {
			request.Period = "custom"
		}
		plan := Plan{Scope: "in_scope", Intent: "operations", AnswerMode: "tools", Tools: []ToolRequest{request}}
		if err := ValidatePlan(plan); err != nil {
			t.Fatalf("ValidatePlan() rejected %+v: %v", request, err)
		}
	}
}

func TestValidatePlanRejectsUnsafeOperationalArguments(t *testing.T) {
	requests := []ToolRequest{
		{Name: "search_services", Selection: "list", Status: "deleted", Limit: 8},
		{Name: "search_customers", Selection: "top", Query: "Ada", Limit: 26},
		{Name: "get_customer_booking_summary", CustomerID: "not-a-customer"},
		{Name: "get_payout_summary", Query: "all accounts"},
		{Name: "get_payout_summary", From: "2025-01-01", To: "2026-08-30"},
		{Name: "get_booking_metrics", Metric: "summary", TimeScope: "period", From: "2025-01-01", To: "2026-08-30"},
		{Name: "get_review_summary", From: "2026-08-30", To: "2026-08-01"},
	}
	for _, request := range requests {
		if dateRangeTool(request.Name) {
			request.Period = "custom"
		}
		plan := Plan{Scope: "in_scope", Intent: "operations", AnswerMode: "tools", Tools: []ToolRequest{request}}
		if err := ValidatePlan(plan); err == nil {
			t.Fatalf("ValidatePlan() accepted %+v", request)
		}
	}
}
