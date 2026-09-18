package admin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func supportInput() SupportInput {
	return SupportInput{Title: "Investigate booking query", Description: "Customer needs clarification about an existing booking.", Status: "open", Priority: "normal", Reason: "Received an internal escalation", RequestKey: uuid.New()}
}
func supportCode(t *testing.T, e error, code string) {
	t.Helper()
	var p *Problem
	if !errors.As(e, &p) || p.Code != code {
		t.Fatalf("want %s got %v", code, e)
	}
}
func TestSupportCaseLifecycleConcurrencyAndAudit(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	in := supportInput()
	in.AssigneeID = &full.Staff.ID
	in.FollowUp = s.now().UTC().Format(time.DateOnly)
	var wg sync.WaitGroup
	results := make(chan SupportResult, 4)
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); r, e := s.SaveSupportCase(ctx, full, uuid.Nil, in); results <- r; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id uuid.UUID
	for r := range results {
		if id != uuid.Nil && r.ID != id {
			t.Fatal("duplicate case")
		}
		id = r.ID
	}
	var count int
	if e := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE entity_id=$1`, id).Scan(&count); e != nil || count != 1 {
		t.Fatal("audit count", count, e)
	}
	detail, e := s.SupportCase(ctx, id)
	if e != nil || detail.Revision != 1 || len(detail.History) != 1 {
		t.Fatal(detail, e)
	}
	changed := in
	changed.Title = "Changed retry"
	_, e = s.SaveSupportCase(ctx, full, uuid.Nil, changed)
	supportCode(t, e, "idempotency_conflict")
	in.ExpectedRevision = 1
	in.RequestKey = uuid.New()
	in.Status = "resolved"
	in.Reason = "Confirmed the booking details with existing evidence"
	done, e := s.SaveSupportCase(ctx, full, id, in)
	if e != nil || done.Revision != 2 {
		t.Fatal(done, e)
	}
	detail, e = s.SupportCase(ctx, id)
	if e != nil || detail.Resolution != in.Reason || detail.History[0].Action != "resolved" {
		t.Fatal(detail, e)
	}
	page, e := s.SupportCases(ctx, full.Staff.ID, SupportFilter{Due: "due"})
	if e != nil || len(page.Items) != 0 {
		t.Fatal("resolved in due queue", e)
	}
	replay, e := s.SaveSupportCase(ctx, full, id, in)
	if e != nil || replay != done {
		t.Fatal("retry", replay, e)
	}
	in.RequestKey = uuid.New()
	_, e = s.SaveSupportCase(ctx, full, id, in)
	supportCode(t, e, "record_changed")
	in.ExpectedRevision = 2
	in.Status = "waiting"
	in.Reason = "New evidence needs review"
	in.RequestKey = uuid.New()
	_, e = s.SaveSupportCase(ctx, full, id, in)
	if e != nil {
		t.Fatal(e)
	}
	detail, e = s.SupportCase(ctx, id)
	if e != nil || detail.Resolution != "" || detail.History[0].Action != "reopened" {
		t.Fatal(detail, e)
	}
	page, e = s.SupportCases(ctx, full.Staff.ID, SupportFilter{Due: "due", Owner: "me"})
	if e != nil || len(page.Items) != 1 {
		t.Fatal("due/mine", e)
	}
	// Different requests based on the same revision must have a single winner.
	errs = make(chan error, 2)
	for _, priority := range []string{"high", "urgent"} {
		wg.Add(1)
		go func(priority string) {
			defer wg.Done()
			next := in
			next.Priority = priority
			next.ExpectedRevision = 3
			next.RequestKey = uuid.New()
			_, e := s.SaveSupportCase(ctx, full, id, next)
			errs <- e
		}(priority)
	}
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		} else {
			supportCode(t, e, "record_changed")
		}
	}
	if wins != 1 {
		t.Fatal("concurrent winners", wins)
	}
	key := uuid.New()
	for range 2 {
		if _, e = s.AddRecordNote(ctx, full, "support_case", id, key, "Internal case evidence"); e != nil {
			t.Fatal(e)
		}
	}
	notes, e := s.RecordNotes(ctx, "support_case", id, nil)
	if e != nil || len(notes.Items) != 1 {
		t.Fatal(notes, e)
	}
	if _, e = s.RecordNotes(ctx, "support_case", uuid.New(), nil); e == nil {
		t.Fatal("missing case notes")
	}
	// Both new-case and update transactions must fail atomically with audit.
	_, e = s.db.Exec(ctx, `ALTER TABLE admin_audit_events ADD CONSTRAINT support_test_audit CHECK(entity_type<>'support_case') NOT VALID`)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Exec(ctx, `ALTER TABLE admin_audit_events DROP CONSTRAINT IF EXISTS support_test_audit`) })
	in.ExpectedRevision = 4
	in.RequestKey = uuid.New()
	in.Title = "Should roll back"
	if _, e = s.SaveSupportCase(ctx, full, id, in); e == nil {
		t.Fatal("ignored audit failure")
	}
	if _, e = s.SaveSupportCase(ctx, full, uuid.Nil, supportInput()); e == nil {
		t.Fatal("creation ignored audit failure")
	}
	detail, e = s.SupportCase(ctx, id)
	if e != nil || detail.Revision != 4 || detail.Title == in.Title {
		t.Fatal("update survived", detail, e)
	}
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_support_cases`).Scan(&count); e != nil || count != 1 {
		t.Fatal("create survived", count, e)
	}
	if _, e = s.AddRecordNote(ctx, full, "support_case", id, uuid.New(), "Rollback note"); e == nil {
		t.Fatal("note ignored audit failure")
	}
}
func TestSupportFiltersValidationAndAssignments(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	for i := range 28 {
		in := supportInput()
		if i == 0 {
			in.Title = "Literal %_ support"
		}
		if _, e := s.SaveSupportCase(ctx, full, uuid.Nil, in); e != nil {
			t.Fatal(e)
		}
	}
	// Equal timestamps still page stably using the ID tie-breaker.
	s.db.Exec(ctx, `UPDATE admin_support_cases SET created_at='2026-09-01'`)
	p, e := s.SupportCases(ctx, full.Staff.ID, SupportFilter{})
	if e != nil || len(p.Items) != 25 || p.NextCursor == "" {
		t.Fatal(p, e)
	}
	tail, e := s.SupportCases(ctx, full.Staff.ID, SupportFilter{Cursor: p.NextCursor})
	if e != nil || len(tail.Items) != 3 {
		t.Fatal(tail, e)
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range append(p.Items, tail.Items...) {
		if seen[c.ID] {
			t.Fatal("duplicate page row")
		}
		seen[c.ID] = true
	}
	for _, f := range []SupportFilter{{Cursor: p.NextCursor, Owner: "unassigned"}, {Cursor: "bad"}, {Status: "bogus"}, {Owner: uuid.NewString()}, {Due: "tomorrow"}, {Priority: "critical"}, {Q: strings.Repeat("a", 101)}} {
		if _, e = s.SupportCases(ctx, full.Staff.ID, f); e == nil {
			t.Fatal("invalid filter accepted", f)
		}
	}
	only, e := s.SupportCases(ctx, full.Staff.ID, SupportFilter{Q: "%_"})
	if e != nil || len(only.Items) != 1 {
		t.Fatal("literal search", e)
	}
	oldNow := s.now
	s.now = func() time.Time { return oldNow().Add(24 * time.Hour) }
	_, e = s.SupportCases(ctx, full.Staff.ID, SupportFilter{Cursor: p.NextCursor})
	supportCode(t, e, "invalid_cursor")
	s.now = oldNow
	for _, mutate := range []func(*SupportInput){func(i *SupportInput) { i.Title = " " }, func(i *SupportInput) { i.Description = strings.Repeat("é", 4001) }, func(i *SupportInput) { i.Reason = "" }, func(i *SupportInput) { i.FollowUp = "2026-02-30" }, func(i *SupportInput) { i.Status = "resolved" }, func(i *SupportInput) { i.AssigneeID = newUUID() }, func(i *SupportInput) { i.TargetKind = "booking" }, func(i *SupportInput) { i.TargetKind = "support_case"; i.TargetID = newUUID() }, func(i *SupportInput) { i.TargetKind = "booking"; i.TargetID = newUUID() }} {
		in := supportInput()
		mutate(&in)
		if _, e = s.SaveSupportCase(ctx, full, uuid.Nil, in); e == nil {
			t.Fatal("invalid case accepted", in)
		}
	}
	// Explicit target identity stays separate even when UUIDs match across tables.
	account := uuid.New()
	if _, e = s.db.Exec(ctx, `INSERT INTO marketplace_customers(id,full_name,email) VALUES($1,'Case account',$2)`, account, account.String()+"@example.test"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		s.db.Exec(ctx, `DELETE FROM admin_support_changes WHERE case_id IN(SELECT id FROM admin_support_cases WHERE account_id=$1)`, account)
		s.db.Exec(ctx, `DELETE FROM admin_support_cases WHERE account_id=$1`, account)
		s.db.Exec(ctx, `DELETE FROM marketplace_customers WHERE id=$1`, account)
	})
	in := supportInput()
	in.TargetKind = "marketplace_account"
	in.TargetID = &account
	in.AssigneeID = &full.Staff.ID
	r, e := s.SaveSupportCase(ctx, full, uuid.Nil, in)
	if e != nil {
		t.Fatal(e)
	}
	in.ExpectedRevision = r.Revision
	in.RequestKey = uuid.New()
	in.TargetKind = "customer_contact"
	_, e = s.SaveSupportCase(ctx, full, r.ID, in)
	supportCode(t, e, "invalid_target")
	in.TargetKind = "marketplace_account"
	assignees, e := s.SupportAssignees(ctx)
	if e != nil || len(assignees) != 1 || assignees[0].ID != full.Staff.ID {
		t.Fatal(assignees, e)
	}
	// An active but ineligible staff member cannot receive a new assignment.
	other := uuid.New()
	if _, e = s.db.Exec(ctx, `INSERT INTO admin_staff(id,email,full_name,role,status) VALUES($1,$2,'Finance Staff','finance','active')`, other, other.String()+"@example.test"); e != nil {
		t.Fatal(e)
	}
	in.AssigneeID = &other
	_, e = s.SaveSupportCase(ctx, full, r.ID, in)
	supportCode(t, e, "invalid_assignee")
	s.db.Exec(ctx, `UPDATE admin_staff SET role='support',status='suspended' WHERE id=$1`, other)
	_, e = s.SaveSupportCase(ctx, full, r.ID, in)
	supportCode(t, e, "invalid_assignee")
	s.db.Exec(ctx, `UPDATE admin_staff SET status='active' WHERE id=$1`, other)
	r, e = s.SaveSupportCase(ctx, full, r.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	s.db.Exec(ctx, `UPDATE admin_staff SET status='suspended' WHERE id=$1`, other)
	in.ExpectedRevision = r.Revision
	in.RequestKey = uuid.New()
	if _, e = s.SaveSupportCase(ctx, full, r.ID, in); e != nil {
		t.Fatal("historical assignment cannot be retained", e)
	}
	in.AssigneeID = nil
	in.ExpectedRevision++
	in.RequestKey = uuid.New()
	if _, e = s.SaveSupportCase(ctx, full, r.ID, in); e != nil {
		t.Fatal("unassign former owner", e)
	}
}
func newUUID() *uuid.UUID { id := uuid.New(); return &id }
func TestSupportHTTPAuthorization(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	full, _, _ := fullSession(t, s)
	in := supportInput()
	r, e := s.SaveSupportCase(ctx, full, uuid.Nil, in)
	if e != nil {
		t.Fatal(e)
	}
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	call := func(method, path, token string, body any) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	for _, role := range []string{"super_admin", "support", "operations", "finance", "analyst"} {
		s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, full.Staff.ID, role)
		for _, path := range []string{"/support/cases", "/support/cases/" + r.ID.String(), "/support/cases/" + r.ID.String() + "/notes"} {
			want := 403
			if allowed(role, "support.read") {
				want = 200
			}
			if got := call("GET", path, full.Token, nil); got != want {
				t.Fatalf("%s %s got%d want%d", role, path, got, want)
			}
		}
		want := 403
		if allowed(role, "support.manage") {
			want = 200
		}
		if got := call("GET", "/staff/assignees", full.Token, nil); got != want {
			t.Fatal(role, got, want)
		}
		if got := call("POST", "/support/cases", full.Token, supportInput()); got != want {
			t.Fatal(role, got, want)
		}
		if got := call("POST", "/support/cases/"+r.ID.String()+"/notes", full.Token, map[string]any{"body": "Internal note", "request_key": uuid.New()}); got != want {
			t.Fatal(role, got, want)
		}
		update := in
		update.ExpectedRevision = 1
		update.RequestKey = uuid.New()
		if allowed(role, "support.manage") {
			current, _ := s.SupportCase(ctx, r.ID)
			update.ExpectedRevision = current.Revision
		}
		if got := call("POST", "/support/cases/"+r.ID.String(), full.Token, update); got != want {
			t.Fatal(role, got, want)
		}
	}
	for _, token := range []string{"", "provider.jwt.token"} {
		if got := call("GET", "/support/cases", token, nil); got != 401 {
			t.Fatal("wrong realm", got)
		}
	}
	s.db.Exec(ctx, `UPDATE admin_staff SET role='super_admin',revision=revision+1 WHERE id=$1`, full.Staff.ID)
	if _, e = s.SaveSupportCase(ctx, full, uuid.Nil, supportInput()); e == nil {
		t.Fatal("stale authorization")
	}
	if got := call("GET", "/support/cases", full.Token, nil); got != 401 {
		t.Fatal("revoked revision", got)
	}
}
