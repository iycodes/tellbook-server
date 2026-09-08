package tessa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const groundingFirst = "10000000-0000-4000-8000-000000000001"
const groundingSecond = "10000000-0000-4000-8000-000000000002"

func TestComposedBookingListHasBlankLinesWithinCharacterBudget(t *testing.T) {
	answer := Answer{Parts: []AnswerPart{
		{Text: "Your bookings:\n"},
		{EntityID: groundingFirst, Text: "\n1. Ada — 7 September 2026, 9:00 AM\n"},
		{EntityID: groundingSecond, Text: "2. Bisi — 7 September 2026, 11:00 AM"},
	}, PartialNotice: " More bookings are available. "}
	if err := composeAnswer(&answer); err != nil {
		t.Fatal(err)
	}
	want := "Your bookings:\n\n1. Ada — 7 September 2026, 9:00 AM\n\n2. Bisi — 7 September 2026, 11:00 AM\n\nMore bookings are available."
	if answer.Content != want || len(answer.Mentions) != 2 || answer.Mentions[0].Quote != strings.TrimSpace(answer.Parts[1].Text) {
		t.Fatalf("unexpected formatted answer: %+v", answer)
	}
	input := groundingInput("list", 8, true)
	input.MaxAnswerCharacters = len([]rune(want))
	if err := validateAnswer(answer, input); err != nil {
		t.Fatal(err)
	}
	input.MaxAnswerCharacters--
	if err := validateAnswer(answer, input); err == nil {
		t.Fatal("paragraph separators must count toward the delivery character limit")
	}
}

func groundingInput(selection string, limit int, hasMore bool) SynthesisInput {
	payload, _ := json.Marshal(map[string]any{"items": []map[string]string{{"booking_id": groundingFirst}, {"booking_id": groundingSecond}}, "has_more": hasMore})
	return SynthesisInput{MaxAnswerCharacters: 1024, Tools: []ToolRequest{{Name: "search_bookings", Selection: selection, Limit: limit}}, Evidence: []Evidence{{Tool: "search_bookings", Result: payload}}}
}

func TestSearchSelectionDoesNotCarryOverPageSize(t *testing.T) {
	for _, name := range []string{"search_bookings", "search_customers", "search_services"} {
		tool := ToolRequest{Name: name, Selection: "first", Limit: 1}
		if name == "search_bookings" {
			tool.Period = "custom"
			tool.From, tool.To = "2026-09-07", "2026-09-07"
			tool.TimeScope = "period"
		}
		plan := Plan{Scope: "in_scope", Intent: "lookup", AnswerMode: "tools", Tools: []ToolRequest{tool}}
		first, err := NormalizePlan(plan)
		if err != nil || first.Tools[0].Limit != 1 {
			t.Fatal(first, err)
		}
		plan.Tools[0].Selection = "list"
		list, err := NormalizePlan(plan)
		if err != nil || list.Tools[0].Limit != MaxSearchResults {
			t.Fatal(list, err)
		}
		plan.Tools[0].Selection = ""
		if _, err = NormalizePlan(plan); err == nil {
			t.Fatal("missing selection accepted")
		}
		plan.Tools[0].Selection, plan.Tools[0].Limit = "top", 10
		requested, err := NormalizePlan(plan)
		if err != nil || requested.Tools[0].Limit != 10 {
			t.Fatal("requested count was silently reduced", requested, err)
		}
	}
}

func TestPlanningRepairExplainsTheInconsistentMode(t *testing.T) {
	_, err := NormalizePlan(Plan{Scope: "in_scope", Intent: "help", AnswerMode: "direct", Tools: []ToolRequest{{Name: "search_tellbook_help", Query: "availability"}}})
	if err == nil || !strings.Contains(err.Error(), "answer_mode tools") {
		t.Fatal("repair cannot identify the inconsistent field", err)
	}
}

func TestAnswerGroundingChecksCoverageWithoutPrescribingWording(t *testing.T) {
	input := groundingInput("list", 8, false)
	for _, text := range []string{
		"You've got Ada at 9:10, then Bisi at 10:40. A little breathing room between the two!",
		"Ada — 9:10\nBisi — 10:40",
	} {
		answer := Answer{Content: text, Mentions: []AnswerMention{{groundingFirst, "Ada"}, {groundingSecond, "Bisi"}}}
		if err := validateAnswer(answer, input); err != nil {
			t.Fatal(err)
		}
	}
	answer := Answer{Content: "Ada is first.", Mentions: []AnswerMention{{groundingFirst, "Ada"}}}
	if err := validateAnswer(answer, input); err == nil {
		t.Fatal("omitted row presented as complete")
	}
	answer.PartialNotice = "There's more on your schedule; this is just the first appointment."
	answer.Content += " " + answer.PartialNotice
	if err := validateAnswer(answer, input); err != nil {
		t.Fatal(err)
	}
	answer.PartialNotice = "A notice that is not visible."
	if err := validateAnswer(answer, input); err == nil {
		t.Fatal("invisible notice accepted")
	}
	answer.PartialNotice = ""
	answer.Mentions = []AnswerMention{{"20000000-0000-4000-8000-000000000001", "Ada"}}
	if err := validateAnswer(answer, input); err == nil {
		t.Fatal("invented entity accepted")
	}
}

func TestFulfilledTopSelectionIsNotAnIncompleteList(t *testing.T) {
	answer := Answer{Content: "Ada, followed by Bisi.", Mentions: []AnswerMention{{groundingFirst, "Ada"}, {groundingSecond, "Bisi"}}}
	for _, tc := range []struct {
		selection string
		limit     int
		valid     bool
	}{
		{"top", 2, true}, {"top", 3, false}, {"list", 8, false},
	} {
		if err := validateAnswer(answer, groundingInput(tc.selection, tc.limit, true)); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestGroundingRepairUsesExistingSecondAttempt(t *testing.T) {
	generator := &groundingRepairGenerator{}
	service, err := NewService(Provider{Name: "local", Model: "test", Generator: generator, Timeout: time.Second}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.GenerateAnswer(context.Background(), service.Primary(), groundingInput("list", 8, true))
	if err != nil || generator.calls != 2 || !strings.Contains(generator.feedback, "results are partial") {
		t.Fatal(err, generator)
	}
}

type groundingRepairGenerator struct {
	calls    int
	feedback string
}

func (g *groundingRepairGenerator) GenerateJSON(_ context.Context, _, prompt string, destination any) error {
	g.calls++
	g.feedback = prompt
	answer := destination.(*Answer)
	*answer = Answer{Parts: []AnswerPart{{EntityID: groundingFirst, Text: "Ada is first."}}}
	if g.calls == 2 {
		answer.PartialNotice = "This is only part of your schedule."
	}
	return nil
}

func TestOperationalEvidenceEntitiesAreValidWithoutExhaustingExamples(t *testing.T) {
	for _, tc := range []struct {
		tool, payload      string
		required, optional []string
	}{
		{"get_booking_payment_status", `{"found":true,"payment":{"booking_id":"` + groundingFirst + `"}}`, []string{groundingFirst}, nil},
		{"get_availability", `{"services":[{"service_id":"` + groundingFirst + `"}],"has_more":false}`, []string{groundingFirst}, nil},
		{"get_customer_booking_summary", `{"found":true,"customer":{"customer":{"customer_id":"` + groundingFirst + `"},"total_bookings":20,"recent_bookings":[{"booking_id":"` + groundingSecond + `"}]}}`, []string{groundingFirst}, []string{groundingSecond}},
		{"get_booking_attention_summary", `{"total":20,"examples":[{"booking_id":"` + groundingSecond + `"}]}`, nil, []string{groundingSecond}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			input := SynthesisInput{MaxAnswerCharacters: 1024, Evidence: []Evidence{{Tool: tc.tool, Result: json.RawMessage(tc.payload)}}}
			coverage, err := ResultCoverage(input)
			if err != nil || len(coverage) != 1 || strings.Join(coverage[0].EntityIDs, ",") != strings.Join(tc.required, ",") || strings.Join(coverage[0].OptionalEntityIDs, ",") != strings.Join(tc.optional, ",") {
				t.Fatal(coverage, err)
			}
			answer := Answer{Parts: []AnswerPart{{Text: "Here is the summary."}}}
			for _, id := range tc.required {
				answer.Parts = append(answer.Parts, AnswerPart{EntityID: id, Text: "The requested detail."})
			}
			if err := composeAnswer(&answer); err != nil {
				t.Fatal(err)
			}
			if err := validateAnswer(answer, input); err != nil {
				t.Fatal("summary required unrelated examples", err)
			}
			for _, id := range tc.optional {
				answer.Parts = append(answer.Parts, AnswerPart{EntityID: id, Text: "One supporting example."})
			}
			if err := composeAnswer(&answer); err != nil {
				t.Fatal(err)
			}
			if err := validateAnswer(answer, input); err != nil {
				t.Fatal("valid optional reference rejected", err)
			}
			answer.Parts = append(answer.Parts, AnswerPart{EntityID: "30000000-0000-4000-8000-000000000001", Text: "Invented."})
			if err := composeAnswer(&answer); err != nil {
				t.Fatal(err)
			}
			if err := validateAnswer(answer, input); err == nil {
				t.Fatal("unknown entity accepted")
			}
		})
	}
}

type datedAnswerGenerator struct {
	input  SynthesisInput
	plan   PlanInput
	prompt string
}

func (g *datedAnswerGenerator) GenerateJSON(_ context.Context, _, prompt string, out any) error {
	g.prompt = prompt
	if plan, ok := out.(*Plan); ok {
		if err := json.Unmarshal([]byte(strings.TrimPrefix(prompt, planningPromptPrefix)), &g.plan); err != nil {
			return err
		}
		*plan = Plan{Scope: "in_scope", Intent: "help", AnswerMode: "tools", Tools: []ToolRequest{{Name: "search_tellbook_help", Query: "booking"}}}
		return nil
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(prompt, synthesisPromptPrefix)), &g.input); err != nil {
		return err
	}
	out.(*Answer).Parts = []AnswerPart{{Text: "No bookings in that period."}}
	return nil
}

func TestAnswerUsesTheSameQuestionDateAsPlanning(t *testing.T) {
	g := &datedAnswerGenerator{}
	s, err := NewService(Provider{Name: "local", Model: "test", Generator: g, Timeout: time.Second}, nil, 12000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.GenerateAnswer(context.Background(), s.Primary(), SynthesisInput{CurrentDate: "2026-09-09", QuestionAt: "2026-09-07T23:30:00Z", Timezone: "Africa/Lagos"})
	if err != nil || g.input.CurrentDate != "2026-09-08" {
		t.Fatal("synthesis used processing date or UTC date", g.input, err)
	}
	_, err = s.GeneratePlan(context.Background(), s.Primary(), PlanInput{Question: "Explain bookings", CurrentDate: "2026-09-09", CurrentTime: "10:00:00", QuestionAt: "2026-09-07T23:30:00Z", Timezone: "Africa/Lagos"})
	if err != nil || g.plan.ReferenceDate != "2026-09-08" || strings.Contains(g.prompt, "2026-09-09") || strings.Contains(g.prompt, "checked_date") {
		t.Fatal("planner received conflicting date anchors", g.plan, err)
	}
}
