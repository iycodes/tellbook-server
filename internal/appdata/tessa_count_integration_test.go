package appdata

import (
	"booking/go-server/internal/tessa"
	"encoding/json"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestTessaExactCountsShareSearchFiltersIntegration(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	clientID := insertTessaTestClient(t, ctx, pool)
	otherID := insertTessaTestClient(t, ctx, pool)
	repo := NewRepository(pool)
	from, to, err := tessaDateRange("2026-09-01", "2026-09-30", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	add := func(owner uuid.UUID, start time.Time, status, title, currency string) {
		t.Helper()
		customer := uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name) VALUES($1,$2,'Count test customer')`, customer, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code)
		 VALUES($1,$2,$3,$4,$5,'paid_in_full','not_required',$6,$7,$6,$7,$8,'NG')`, uuid.New(), owner, customer, title, status, start, start.Add(time.Hour), currency); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 12; i++ {
		title, currency := "Consultation", "NGN"
		if i == 0 {
			title, currency = "100%_match", "USD"
		}
		if i == 1 {
			title = "100abcXmatch"
		}
		add(clientID, from.Add(time.Duration(i+1)*time.Hour), "booked", title, currency)
	}
	add(clientID, from.Add(20*time.Hour), "cancelled", "Cancelled", "NGN")
	add(clientID, from.Add(-30*time.Minute), "booked", "Overnight", "NGN")
	add(clientID, to, "booked", "Outside range", "NGN")
	add(otherID, from.Add(time.Hour), "booked", "Other provider", "NGN")
	cutoff := from.Add(8 * time.Hour)
	for _, tc := range []struct {
		name, query string
		statuses    []string
		cutoff      *time.Time
		expected    int64
	}{
		{name: "all including terminal", expected: 14},
		{name: "booked", statuses: []string{"booked"}, expected: 13},
		{name: "cancelled", statuses: []string{"cancelled"}, expected: 1},
		{name: "literal wildcard characters", query: "100%_match", expected: 1},
		{name: "upcoming", statuses: []string{"booked"}, cutoff: &cutoff, expected: 5},
		{name: "empty", query: "not present", expected: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := TessaBookingFilter{Statuses: tc.statuses, Query: tc.query, StartsNotBefore: tc.cutoff}
			count, err := repo.CountTessaBookings(ctx, clientID, from, to, filter)
			if err != nil || !count.Exact || count.TotalBookings != tc.expected || count.To != "2026-09-30" {
				t.Fatal(count, err)
			}
			list, err := repo.SearchTessaBookings(ctx, clientID, from, to, filter, 8, true)
			if err != nil || len(list.Items) != int(min(tc.expected, 8)) || list.HasMore != (tc.expected > 8) {
				t.Fatal(list, err)
			}
		})
	}
	// Exercise the actual tool path without profile/currency configuration;
	// mixed booking currencies must not prevent a numerical count.
	threadID, runID := enqueueTessaIntegrationRun(t, ctx, repo, clientID)
	worker := newTessaIntegrationWorker(t, repo, &tessaIntegrationGenerator{}, nil)
	request := tessa.ToolRequest{Name: "get_booking_metrics", Metric: "count", PaymentState: "any", Period: "this_month", From: "2026-09-01", To: "2026-09-30", TimeScope: "period"}
	run := tessaClaimedRun{ID: runID, ThreadID: threadID, ClientID: clientID, SourceChannel: "web", CheckAt: from}
	payload, rows, err := worker.loadOrExecuteTool(ctx, run, 0, request, "Africa/Lagos")
	var count TessaBookingCount
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(payload, &count); err != nil || count.TotalBookings != 14 || !count.Exact || rows != 1 {
		t.Fatal(string(payload), rows, err)
	}
	// Replay keeps the committed aggregate even if the live rows have changed.
	add(clientID, from.Add(22*time.Hour), "booked", "Later insert", "NGN")
	replay, _, err := worker.loadOrExecuteTool(ctx, run, 0, request, "Africa/Lagos")
	var replayCount TessaBookingCount
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(replay, &replayCount); err != nil || replayCount != count {
		t.Fatal("count evidence was recomputed on replay", err)
	}
}
