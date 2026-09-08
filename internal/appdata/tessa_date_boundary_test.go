package appdata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"booking/go-server/internal/tessa"

	"github.com/google/uuid"
)

func TestTessaWeekQueryExcludesNextMondayMidnightIntegration(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	customerID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name,email,phone)
	 VALUES($1,$2,'Boundary customer','boundary@example.com','+2348000000000')`, customerID, clientID); err != nil {
		t.Fatal(err)
	}
	from, to, err := tessaDateRange("2026-09-07", "2026-09-13", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []time.Time{from, to.Add(-time.Minute), to} {
		_, err = pool.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code)
		 VALUES($1,$2,$3,'Boundary appointment','booked','paid_in_full','not_required',$4,$5,$4,$5,'NGN','NG')`, uuid.New(), clientID, customerID, start, start.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewRepository(pool).SearchTessaBookings(ctx, clientID, from, to, TessaBookingFilter{}, 8, false)
	if err != nil || len(result.Items) != 2 || result.HasMore {
		t.Fatal("next week leaked into current week", result, err)
	}
	for _, item := range result.Items {
		start, err := time.Parse(time.RFC3339, item.StartsAt)
		if err != nil || !start.Before(to) {
			t.Fatal(item, err)
		}
	}
}

type tessaCalendarGenerator struct{ calls int }

func (g *tessaCalendarGenerator) GenerateJSON(_ context.Context, _, _ string, destination any) error {
	plan, ok := destination.(*tessa.Plan)
	if !ok {
		return fmt.Errorf("unexpected destination %T", destination)
	}
	g.calls++
	*plan = tessa.Plan{
		Scope: "in_scope", Intent: "weekly_schedule", AnswerMode: "tools",
		Tools: []tessa.ToolRequest{{Name: "get_schedule", Period: "this_week", TimeScope: "period"}},
	}
	return nil
}

func TestTessaNamedPeriodReplayKeepsCommittedDatesIntegration(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	threadID, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	generator := &tessaCalendarGenerator{}
	worker := newTessaIntegrationWorker(t, repo, generator, nil)
	run := tessaClaimedRun{ID: runID, ClientID: clientID, ThreadID: threadID, SourceChannel: "web"}
	input := tessa.PlanInput{
		Question: "What bookings do I have this week?", QuestionAt: "2026-09-06T23:30:00Z",
		CurrentDate: "2026-09-08", CurrentTime: "2026-09-08T12:00:00+01:00", Timezone: "Africa/Lagos",
	}
	first, err := worker.loadOrCreatePlanningDecision(ctx, run, input)
	if err != nil {
		t.Fatal(err)
	}
	input.CurrentDate, input.CurrentTime = "2026-09-21", "2026-09-21T12:00:00+01:00"
	replay, err := worker.loadOrCreatePlanningDecision(ctx, run, input)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 || len(first.Plan.Tools) != 1 || len(replay.Plan.Tools) != 1 {
		t.Fatalf("calls=%d first=%+v replay=%+v", generator.calls, first, replay)
	}
	for _, decision := range []tessaPlanningDecision{first, replay} {
		tool := decision.Plan.Tools[0]
		if tool.Period != "this_week" || tool.From != "2026-09-07" || tool.To != "2026-09-13" || decision.CurrentDate != "2026-09-08" {
			t.Fatalf("committed calendar context changed: %+v", decision)
		}
	}
}
