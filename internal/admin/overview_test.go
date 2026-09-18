package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOverviewCanonicalCountsWindowsAndQueues(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	business, booking, _, _ := seedFinanceInvestigation(t, s, 0)
	_, second, _, _ := seedFinanceInvestigation(t, s, 0)
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	start := time.Date(2035, 4, 1, 0, 0, 0, 0, time.UTC)
	run(`UPDATE bookings SET start_at=$2,end_at=$2::timestamptz+interval '1 hour',occupied_start_at=$2,occupied_end_at=$2::timestamptz+interval '1 hour' WHERE id=$1`, booking, start)
	run(`UPDATE bookings SET start_at=$2,end_at=$2::timestamptz+interval '1 hour',occupied_start_at=$2,occupied_end_at=$2::timestamptz+interval '1 hour',currency_code='USD' WHERE id=$1`, second, start.AddDate(0, 0, 2))
	run(`UPDATE client_profiles SET verified=true,platform_restricted=true WHERE client_id=$1`, business)
	statuses := []string{"booked", "confirmed", "completed", "cancelled", "no_show"}
	for i := range 62 {
		// 60 in-window rows plus one immediately before and one at the exclusive end.
		at := start.AddDate(0, 0, i%3)
		if i == 60 {
			at = start.Add(-time.Nanosecond * 1000)
		}
		if i == 61 {
			at = start.AddDate(0, 0, 3)
		}
		run(`INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code) SELECT $1,client_id,customer_id,'PRIVATE_REPORT_BOOKING',$3,'unpaid','not_required',$4,$4::timestamptz+interval '1 hour',$4,$4::timestamptz+interval '1 hour','NGN','NG' FROM bookings WHERE id=$2`, uuid.New(), booking, statuses[i%len(statuses)], at)
	}
	for i := range 4 {
		in := supportInput()
		in.FollowUp = s.now().UTC().Format(time.DateOnly)
		if i == 1 {
			in.AssigneeID = &full.Staff.ID
		}
		if i == 2 {
			in.FollowUp = s.now().UTC().AddDate(0, 0, 1).Format(time.DateOnly)
		}
		r, e := s.SaveSupportCase(ctx, full, uuid.Nil, in)
		if e != nil {
			t.Fatal(e)
		}
		if i == 1 || i == 3 {
			in.ExpectedRevision = 1
			in.RequestKey = uuid.New()
			in.Status = "waiting"
			if i == 3 {
				in.Status = "resolved"
			}
			if _, e = s.SaveSupportCase(ctx, full, r.ID, in); e != nil {
				t.Fatal(e)
			}
		}
	}
	out, e := s.Overview(ctx, full, "2035-04-01", "2035-04-04")
	if e != nil {
		t.Fatal(e)
	}
	if out.Bookings.Total != 62 || len(out.Bookings.Days) != 3 || out.Bookings.Days[0].Count != 21 || out.Bookings.Days[1].Count != 20 || out.Bookings.Days[2].Count != 21 {
		t.Fatalf("window: %+v", out.Bookings)
	}
	if out.Support == nil || *out.Support != (OverviewSupport{Active: 3, Unassigned: 2, Due: 2, Waiting: 1}) {
		t.Fatal("support counts", out.Support)
	}
	for _, filter := range []SupportFilter{{Status: "active"}, {Status: "active", Owner: "unassigned"}, {Status: "active", Due: "due"}, {Status: "waiting"}} {
		p, e := s.SupportCases(ctx, full.Staff.ID, filter)
		if e != nil {
			t.Fatal(e)
		}
		want := out.Support.Active
		if filter.Owner != "" {
			want = out.Support.Unassigned
		}
		if filter.Due != "" {
			want = out.Support.Due
		}
		if filter.Status == "waiting" {
			want = out.Support.Waiting
		}
		if int64(len(p.Items)) != want {
			t.Fatal("queue mismatch", filter, p, want)
		}
	}
	// Verify the report against canonical pagination, not one page's length.
	var count int64
	cursor := ""
	for {
		p, e := s.Bookings(ctx, BookingFilter{From: out.From, To: out.To, Cursor: cursor})
		if e != nil {
			t.Fatal(e)
		}
		count += int64(len(p.Items))
		cursor = p.NextCursor
		if cursor == "" {
			break
		}
	}
	if count != out.Bookings.Total {
		t.Fatal("register mismatch", count, out.Bookings.Total)
	}
	for _, status := range out.Bookings.Statuses {
		p, e := s.Bookings(ctx, BookingFilter{From: out.From, To: out.To, Status: status.Status})
		if e != nil || int64(len(p.Items)) != status.Count {
			t.Fatal("status mismatch", status, e)
		}
	}
	for _, day := range out.Bookings.Days {
		p, e := s.Bookings(ctx, BookingFilter{From: day.Date, To: day.To})
		if e != nil || int64(len(p.Items)) != day.Count {
			t.Fatal("day mismatch", day, e)
		}
	}
	var businesses, verified int64
	for _, f := range []string{"", "verified"} {
		var after *uuid.UUID
		var n int64
		for {
			p, e := s.Businesses(ctx, "", f, after)
			if e != nil {
				t.Fatal(e)
			}
			n += int64(len(p.Items))
			if p.NextCursor == "" {
				break
			}
			id := uuid.MustParse(p.NextCursor)
			after = &id
		}
		if f == "" {
			businesses = n
		} else {
			verified = n
		}
	}
	if out.Businesses.Total != businesses || out.Businesses.Verified != verified || out.Businesses.Unverified != businesses-verified {
		t.Fatal("business mismatch", out.Businesses)
	}
	empty, e := s.Overview(ctx, full, "2098-01-01", "2098-02-01")
	if e != nil || empty.Bookings.Total != 0 || len(empty.Bookings.Days) != 31 || len(empty.Bookings.Statuses) != 0 {
		t.Fatal("empty window", empty, e)
	}
	if empty.Businesses != out.Businesses || *empty.Support != *out.Support {
		t.Fatal("current snapshots used date filter")
	}
	defaults, e := s.Overview(ctx, full, "", "")
	if e != nil || defaults.From != s.now().UTC().Format(time.DateOnly) || len(defaults.Bookings.Days) != 7 {
		t.Fatal("defaults", defaults, e)
	}
	for _, v := range [][2]string{{"invalid", ""}, {"2035-04-01", "2035-05-03"}, {"2035-04-04", "2035-04-01"}, {"2035-04-01", "2035-04-01"}, {"2035-02-30", "2035-03-01"}} {
		if _, e = s.Overview(ctx, full, v[0], v[1]); e == nil {
			t.Fatal("invalid window accepted", v)
		}
	}
}

func TestOverviewStaffBoundaryAndAggregatePrivacy(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	_, _, _, at := seedFinanceInvestigation(t, s, 1)
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/reports/overview?from="+at.Format(time.DateOnly)+"&to="+at.AddDate(0, 0, 1).Format(time.DateOnly), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Cookie", "provider_session=anything")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, role := range []string{"super_admin", "operations", "support", "finance", "analyst"} {
		if _, e := s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, full.Staff.ID, role); e != nil {
			t.Fatal(e)
		}
		w := call(full.Token)
		if w.Code != 200 {
			t.Fatal(role, w.Code, w.Body.String())
		}
		var out Overview
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		if (out.Support != nil) != allowed(role, "support.read") {
			t.Fatal("private queues leaked", role)
		}
		for _, private := range []string{"PRIVATE_", "Finance QA", "email", "customer_id", "business_id", "assignee", "request_key", "amount_minor", "reference", "session_token"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("private fields in aggregate", role, private)
			}
		}
		var raw map[string]json.RawMessage
		json.Unmarshal(w.Body.Bytes(), &raw)
		if role == "analyst" && len(raw) != 6 {
			t.Fatal("unexpected Analyst fields", string(w.Body.Bytes()))
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("report can be cached")
		}
	}
	for _, token := range []string{"", "provider.jwt.token"} {
		if w := call(token); w.Code != 401 {
			t.Fatal("nonstaff accepted", w.Code)
		}
	}
	if _, e := s.db.Exec(ctx, `UPDATE admin_sessions SET stage='login' WHERE id=$1`, full.ID); e != nil {
		t.Fatal(e)
	}
	if w := call(full.Token); w.Code != 401 {
		t.Fatal("partial MFA accepted", w.Code)
	}
	if _, e := s.db.Exec(ctx, `UPDATE admin_sessions SET stage='full' WHERE id=$1`, full.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.Logout(ctx, full); e != nil {
		t.Fatal(e)
	}
	if w := call(full.Token); w.Code != 401 {
		t.Fatal("revoked session accepted", w.Code)
	}
}
