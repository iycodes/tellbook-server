package tessa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type synthesisContextProbe struct{ t *testing.T }

func (p synthesisContextProbe) GenerateJSON(_ context.Context, _, prompt string, destination any) error {
	if strings.Contains(prompt, "STALE_FACT") {
		p.t.Fatal("prior assistant facts or summary reached synthesis")
	}
	if strings.Contains(prompt, "Only confirmed appointments") {
		p.t.Fatal("historical provider filters reached synthesis")
	}
	if !strings.Contains(prompt, "Show the first two confirmed appointments in September 2026") {
		p.t.Fatal("resolved intent context was lost")
	}
	destination.(*Answer).Parts = []AnswerPart{{Text: "There are no matching confirmed appointments."}}
	return nil
}

func TestSynthesisKeepsIntentButExcludesPriorAssistantFacts(t *testing.T) {
	p := Provider{Name: "local", Model: "test", Timeout: time.Second, Generator: synthesisContextProbe{t}}
	s, err := NewService(p, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	recent := []ContextMessage{{Role: "assistant", Content: "STALE_FACT", References: []ContextReference{{ID: "STALE_FACT"}}}, {Role: "provider", Content: "Only confirmed appointments", References: []ContextReference{{ID: "STALE_FACT"}}}}
	_, err = s.GenerateAnswer(context.Background(), p, SynthesisInput{Question: "Show the first two", ResolvedQuestion: "Show the first two confirmed appointments in September 2026", CurrentDate: "2026-09-07", Timezone: "Africa/Lagos", ConversationSummary: "STALE_FACT", RecentMessages: recent,
		Tools: []ToolRequest{{Name: "search_bookings", Selection: "top", Limit: 2}}, Evidence: []Evidence{{Tool: "search_bookings", Result: json.RawMessage(`{"items":[],"has_more":false}`)}}})
	if err != nil {
		t.Fatal(err)
	}
	if recent[0].Content != "STALE_FACT" || len(recent[1].References) != 1 {
		t.Fatal("input history was mutated")
	}
}

func TestActiveQuestionIsSerializedAfterHistoricalContext(t *testing.T) {
	for _, input := range []any{
		PlanInput{Question: "Current revenue question", RecentMessages: []ContextMessage{{Role: "provider", Content: "Old service question"}}},
		SynthesisInput{Question: "Current revenue question", ResolvedQuestion: "Resolved request", Evidence: []Evidence{{Tool: "get_payment_summary", Result: json.RawMessage(`{"payment_count":0}`)}}},
	} {
		payload, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(payload), `"current_question":"Current revenue question"}`) {
			t.Fatal("current question must be the final field", string(payload))
		}
	}
}

func TestPaymentFiltersAreIndependentAndToolScoped(t *testing.T) {
	for _, name := range []string{"search_bookings", "get_booking_metrics"} {
		for _, state := range []string{"any", "unpaid", "balance_due", "paid_in_full"} {
			tool := ToolRequest{Name: name, PaymentState: state, Statuses: []string{"confirmed"}, Period: "custom", From: "2026-09-01", To: "2026-09-30", TimeScope: "period"}
			if name == "search_bookings" {
				tool.Selection = "list"
			} else {
				tool.Metric = "count"
			}
			p, err := NormalizePlan(Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}})
			if err != nil || p.Tools[0].PaymentState != state || p.Tools[0].Statuses[0] != "confirmed" {
				t.Fatal(p, err)
			}
			tool.PaymentState = "pending"
			if _, err = NormalizePlan(Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}}); err == nil {
				t.Fatal("booking status accepted as payment state")
			}
		}
	}
	for _, tool := range []ToolRequest{{Name: "get_inbox_summary", PaymentState: "unpaid"}, {Name: "get_booking_metrics", Metric: "summary", PaymentState: "unpaid", Period: "custom", From: "2026-09-01", To: "2026-09-30", TimeScope: "period"}} {
		if _, err := NormalizePlan(Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}}); err == nil {
			t.Fatal("unsupported payment filter accepted", tool)
		}
	}
}
