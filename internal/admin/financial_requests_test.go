package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func seedFinancialReview(t *testing.T, s *Service) PayoutReviewInput {
	t.Helper()
	ctx := context.Background()
	business, _, payments, _ := seedFinanceInvestigation(t, s, 1)
	input := PayoutReviewInput{Kind: "payout", BusinessID: business, DestinationID: uuid.New()}
	if e := s.db.QueryRow(ctx, `SELECT id FROM payment_allocations WHERE payment_id=$1`, payments[0]).Scan(&input.AllocationID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(ctx, `INSERT INTO payout_destinations(id,client_id,provider,country_code,currency_code,rail,institution_code,institution_name,masked_identifier,resolved_account_name,provider_recipient_id,verification_fingerprint,verified_at,status,identifier_ciphertext,identifier_nonce,encryption_key_version) VALUES($1,$2,'paystack','NG','NGN','bank_transfer','001','Review bank','****9999','PRIVATE_ACCOUNT_NAME','PRIVATE_RECIPIENT',$3,now(),'active',decode('aabb','hex'),decode('ccdd','hex'),'test-v1')`, input.DestinationID, business, strings.Repeat("b", 64)); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM admin_financial_requests WHERE business_id=$1`, `DELETE FROM payout_destinations WHERE client_id=$1`} {
			if _, e := s.db.Exec(ctx, q, business); e != nil {
				t.Error(e)
			}
		}
	})
	return input
}
func financialDraft(t *testing.T, s *Service, staff Session, input PayoutReviewInput) FinancialRequestInput {
	t.Helper()
	terms, e := s.PreviewFinancialRequest(context.Background(), staff, input)
	if e != nil {
		t.Fatal(e)
	}
	return FinancialRequestInput{PayoutReviewInput: input, ExpectedFingerprint: terms.Fingerprint, Reason: "Investigated settlement and destination", RequestKey: uuid.New()}
}
func financialError(t *testing.T, e error, status int, code string) {
	t.Helper()
	var p *Problem
	if !errors.As(e, &p) || p.Status != status || (code != "" && p.Code != code) {
		t.Fatalf("want %d/%s, got %v", status, code, e)
	}
}
func noFinancialExecution(t *testing.T, s *Service, input PayoutReviewInput) {
	t.Helper()
	var count int
	var status string
	if e := s.db.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM payouts WHERE payment_allocation_id=$1),status FROM payment_allocations WHERE id=$1`, input.AllocationID).Scan(&count, &status); e != nil {
		t.Fatal(e)
	}
	if count != 0 || status != "eligible" {
		t.Fatalf("approval moved funds: payouts=%d allocation=%s", count, status)
	}
}
func TestFinancialRequestIndependentApprovalAndReplay(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	reviewer, _ := enrolledReleaseStaff(t, s, owner, "finance@example.test", "finance")
	input := seedFinancialReview(t, s)
	draft := financialDraft(t, s, owner, input)
	detail, e := s.FinancePaymentDetail(ctx, func() uuid.UUID {
		var id uuid.UUID
		if e := s.db.QueryRow(ctx, `SELECT payment_id FROM payment_allocations WHERE id=$1`, input.AllocationID).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}())
	if e != nil || len(detail.Destinations) != 1 || detail.Destinations[0].ID != input.DestinationID {
		t.Fatal("destination selector", detail.Destinations, e)
	}
	projected, _ := json.Marshal(detail)
	for _, secret := range []string{"PRIVATE_ACCOUNT_NAME", "PRIVATE_RECIPIENT", "identifier_ciphertext", "verification_fingerprint"} {
		if strings.Contains(string(projected), secret) {
			t.Fatal("selector leaked", secret)
		}
	}

	var wg sync.WaitGroup
	results := make(chan FinancialRequest, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); v, e := s.CreateFinancialRequest(ctx, owner, draft); results <- v; errs <- e }()
	}
	wg.Wait()
	first, second := <-results, <-results
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if e := <-errs; e != nil {
		t.Fatal(e)
	}
	if first.ID != second.ID || first.Status != "pending" {
		t.Fatal("duplicate request", first, second)
	}
	raw, _ := json.Marshal(first)
	if !strings.Contains(string(raw), `"9007199254740393"`) {
		t.Fatal("lost exact amount", string(raw))
	}
	for _, secret := range []string{"PRIVATE_ACCOUNT_NAME", "PRIVATE_RECIPIENT", "identifier_ciphertext", "calculation_snapshot", "verification_fingerprint"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret leaked", secret)
		}
	}
	changed := draft
	changed.Reason = "Changed reason"
	_, e = s.CreateFinancialRequest(ctx, owner, changed)
	financialError(t, e, 409, "idempotency_conflict")
	changed = draft
	changed.RequestKey = uuid.New()
	_, e = s.CreateFinancialRequest(ctx, owner, changed)
	financialError(t, e, 409, "request_exists")
	decision := FinancialDecision{Action: "approve", Reason: "Independently checked exact payout terms", ExpectedRevision: 1, RequestKey: uuid.New()}
	_, e = s.DecideFinancialRequest(ctx, owner, first.ID, decision)
	financialError(t, e, 403, "independent_reviewer_required")
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.DecideFinancialRequest(ctx, reviewer, first.ID, decision)
			results <- v
			errs <- e
		}()
	}
	competingCreate := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, e := s.CreateFinancialRequest(ctx, owner, changed)
		competingCreate <- e
	}()
	wg.Wait()
	financialError(t, <-competingCreate, 409, "request_exists")
	approved, replay := <-results, <-results
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	if e = <-errs; e != nil {
		t.Fatal(e)
	}
	if approved.Status != "approved" || approved.Revision != 2 || replay.Revision != 2 || approved.ReviewerID == nil || *approved.ReviewerID != reviewer.Staff.ID {
		t.Fatal("approval", approved, replay)
	}
	changedDecision := decision
	changedDecision.Reason = "Changed approval"
	_, e = s.DecideFinancialRequest(ctx, reviewer, first.ID, changedDecision)
	financialError(t, e, 409, "idempotency_conflict")
	again, e := s.CreateFinancialRequest(ctx, owner, draft)
	if e != nil || again.Status != "approved" {
		t.Fatal("creation retry after decision", e)
	}
	var count int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM admin_audit_events WHERE entity_id=$1`, first.ID).Scan(&count); e != nil || count != 2 {
		t.Fatal("duplicate or missing audit", count, e)
	}
	noFinancialExecution(t, s, input)
	withdrawn, e := s.DecideFinancialRequest(ctx, owner, first.ID, FinancialDecision{Action: "withdraw", Reason: "Destination needs another investigation", ExpectedRevision: 2, RequestKey: uuid.New()})
	if e != nil || withdrawn.Status != "withdrawn" || withdrawn.Revision != 3 {
		t.Fatal("withdraw", e)
	}
	_, e = s.DecideFinancialRequest(ctx, reviewer, first.ID, decision)
	financialError(t, e, 409, "")
	fresh := draft
	fresh.RequestKey = uuid.New()
	if _, e = s.CreateFinancialRequest(ctx, owner, fresh); e != nil {
		t.Fatal("withdrawal did not release allocation request", e)
	}
	noFinancialExecution(t, s, input)
}
func TestFinancialRequestStaleTermsAndAuthority(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	reviewer, _ := enrolledReleaseStaff(t, s, owner, "finance@example.test", "finance")
	input := seedFinancialReview(t, s)
	draft := financialDraft(t, s, owner, input)
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	// A destination identity change must invalidate terms even if updated_at is unchanged.
	run(`UPDATE payout_destinations SET provider_recipient_id='PRIVATE_NEW_RECIPIENT' WHERE id=$1`, input.DestinationID)
	_, e := s.CreateFinancialRequest(ctx, owner, draft)
	financialError(t, e, 409, "record_changed")
	draft = financialDraft(t, s, owner, input)
	request, e := s.CreateFinancialRequest(ctx, owner, draft)
	if e != nil {
		t.Fatal(e)
	}
	decision := FinancialDecision{Action: "approve", Reason: "Reviewed terms", ExpectedRevision: 1, RequestKey: uuid.New()}
	run(`UPDATE payment_allocations SET business_net_amount_minor=business_net_amount_minor-1,adjustment_amount_minor=adjustment_amount_minor+1 WHERE id=$1`, input.AllocationID)
	_, e = s.DecideFinancialRequest(ctx, reviewer, request.ID, decision)
	financialError(t, e, 409, "record_changed")
	unchanged, e := s.FinancialRequest(ctx, request.ID)
	if e != nil || unchanged.Status != "pending" || unchanged.Revision != 1 {
		t.Fatal("stale approval wrote state", e)
	}
	decision.Action = "reject"
	rejected, e := s.DecideFinancialRequest(ctx, reviewer, request.ID, decision)
	if e != nil || rejected.Status != "rejected" {
		t.Fatal("reject stale terms", e)
	}
	draft = financialDraft(t, s, reviewer, input)
	request, e = s.CreateFinancialRequest(ctx, reviewer, draft)
	if e != nil {
		t.Fatal(e)
	}
	run(`UPDATE admin_staff SET status='suspended' WHERE id=$1`, reviewer.Staff.ID)
	decision.Action = "approve"
	decision.RequestKey = uuid.New()
	_, e = s.DecideFinancialRequest(ctx, owner, request.ID, decision)
	financialError(t, e, 409, "requester_unavailable")
	_, e = s.DecideFinancialRequest(ctx, reviewer, request.ID, decision)
	financialError(t, e, 401, "")
	run(`UPDATE admin_staff SET status='active',role='support' WHERE id=$1`, reviewer.Staff.ID)
	_, e = s.DecideFinancialRequest(ctx, owner, request.ID, decision)
	financialError(t, e, 409, "requester_unavailable")
	_, e = s.PreviewFinancialRequest(ctx, reviewer, input)
	if e == nil {
		t.Fatal("changed permission accepted")
	}
	decision.Action = "withdraw"
	if _, e = s.DecideFinancialRequest(ctx, owner, request.ID, decision); e != nil {
		t.Fatal(e)
	}
	noFinancialExecution(t, s, input)
}
func TestFinancialRequestAuditRollbackAndEligibility(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	reviewer, _ := enrolledReleaseStaff(t, s, owner, "finance@example.test", "finance")
	input := seedFinancialReview(t, s)
	draft := financialDraft(t, s, owner, input)
	run := func(q string, args ...any) {
		t.Helper()
		if _, e := s.db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	blockAudit := func() {
		run(`ALTER TABLE admin_audit_events ADD CONSTRAINT financial_test_audit CHECK(entity_type<>'financial_request') NOT VALID`)
	}
	unblock := func() { run(`ALTER TABLE admin_audit_events DROP CONSTRAINT IF EXISTS financial_test_audit`) }
	t.Cleanup(unblock)
	blockAudit()
	if _, e := s.CreateFinancialRequest(ctx, owner, draft); e == nil {
		t.Fatal("request committed without audit")
	}
	var count int
	if e := s.db.QueryRow(ctx, `SELECT count(*) FROM admin_financial_requests WHERE business_id=$1`, input.BusinessID).Scan(&count); e != nil || count != 0 {
		t.Fatal("request rollback", count, e)
	}
	unblock()
	request, e := s.CreateFinancialRequest(ctx, owner, draft)
	if e != nil {
		t.Fatal(e)
	}
	blockAudit()
	decision := FinancialDecision{Action: "approve", Reason: "Reviewed payout", ExpectedRevision: 1, RequestKey: uuid.New()}
	if _, e = s.DecideFinancialRequest(ctx, reviewer, request.ID, decision); e == nil {
		t.Fatal("approval committed without audit")
	}
	saved, e := s.FinancialRequest(ctx, request.ID)
	if e != nil || saved.Status != "pending" || saved.Revision != 1 {
		t.Fatal("approval rollback", e)
	}
	unblock()
	run(`UPDATE payout_destinations SET status='disabled' WHERE id=$1`, input.DestinationID)
	_, e = s.DecideFinancialRequest(ctx, reviewer, request.ID, decision)
	financialError(t, e, 409, "payout_unavailable")
	run(`UPDATE payout_destinations SET status='active' WHERE id=$1`, input.DestinationID)
	run(`UPDATE payment_allocations SET available_for_payout_at=now()+interval '1 day' WHERE id=$1`, input.AllocationID)
	_, e = s.PreviewFinancialRequest(ctx, owner, input)
	financialError(t, e, 409, "payout_unavailable")
	wrong := input
	wrong.BusinessID = uuid.New()
	_, e = s.PreviewFinancialRequest(ctx, owner, wrong)
	financialError(t, e, 409, "payout_unavailable")
	noFinancialExecution(t, s, input)
}
func TestFinancialRequestHTTPBoundariesAndList(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	input := seedFinancialReview(t, s)
	draft := financialDraft(t, s, owner, input)
	request, e := s.CreateFinancialRequest(ctx, owner, draft)
	if e != nil {
		t.Fatal(e)
	}
	h := s.Handler(slog.New(slog.NewTextHandler(io.Discard, nil)))
	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	paths := []struct {
		method, path string
		body         any
	}{{"GET", "/finance/requests", nil}, {"GET", "/finance/requests/" + request.ID.String(), nil}, {"POST", "/finance/requests/preview", input}, {"POST", "/finance/requests", draft}, {"POST", "/finance/requests/" + request.ID.String() + "/decision", FinancialDecision{Action: "approve", Reason: "Check", ExpectedRevision: 1, RequestKey: uuid.New()}}}
	for _, role := range []string{"super_admin", "finance", "operations", "support", "analyst"} {
		if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role=$2 WHERE id=$1`, owner.Staff.ID, role); e != nil {
			t.Fatal(e)
		}
		for i, p := range paths {
			want := 403
			if (role == "finance" || role == "super_admin") && i != 4 {
				want = 200
			}
			w := call(p.method, p.path, owner.Token, p.body)
			if w.Code != want {
				t.Fatalf("%s %s got %d want %d: %s", role, p.path, w.Code, want, w.Body.String())
			}
		}
	}
	for _, token := range []string{"", "customer-token", "provider-token"} {
		for _, p := range paths {
			w := call(p.method, p.path, token, p.body)
			if w.Code != 401 {
				t.Fatal("wrong realm", w.Code)
			}
		}
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_staff SET role='super_admin' WHERE id=$1`, owner.Staff.ID); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/finance/requests?booking=" + uuid.NewString(), "/finance/requests?business=bad", "/finance/requests?status=paid", "/finance/requests?from=bad", "/finance/requests?currency=N1!", "/finance/requests?cursor=bad"} {
		if w := call("GET", path, owner.Token, nil); w.Code != 422 {
			t.Fatal("invalid filter", path, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"business=" + uuid.NewString(), "currency=USD", "status=approved", "q=%25"} {
		w := call("GET", "/finance/requests?"+query, owner.Token, nil)
		var page FinancialRequestPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 {
			t.Fatal("filter ignored", query, w.Body.String())
		}
	}
	if w := call("POST", "/finance/requests/"+request.ID.String()+"/execute", owner.Token, nil); w.Code != 404 {
		t.Fatal("execution exposed", w.Code)
	}
	if _, e = s.db.Exec(ctx, `UPDATE admin_sessions SET stage='login' WHERE id=$1`, owner.ID); e != nil {
		t.Fatal(e)
	}
	for _, p := range paths {
		w := call(p.method, p.path, owner.Token, p.body)
		if w.Code != 401 {
			t.Fatal("incomplete MFA", w.Code, w.Body.String())
		}
	}
	noFinancialExecution(t, s, input)
}

func TestFinancialRequestPaginationAndCompetingDecisions(t *testing.T) {
	s, _ := integrationService(t)
	ctx := context.Background()
	owner, _, _ := fullSession(t, s)
	reviewer, _ := enrolledReleaseStaff(t, s, owner, "finance@example.test", "finance")
	input := seedFinancialReview(t, s)
	draft := financialDraft(t, s, owner, input)
	request, e := s.CreateFinancialRequest(ctx, owner, draft)
	if e != nil {
		t.Fatal(e)
	}
	// Two different decisions of the same reviewed revision cannot both commit.
	errs := make(chan error, 2)
	for _, action := range []string{"approve", "reject"} {
		go func(action string) {
			_, e := s.DecideFinancialRequest(ctx, reviewer, request.ID, FinancialDecision{Action: action, Reason: "Concurrent review", ExpectedRevision: 1, RequestKey: uuid.New()})
			errs <- e
		}(action)
	}
	first, second := <-errs, <-errs
	if (first == nil) == (second == nil) {
		t.Fatal("expected one winner", first, second)
	}
	if first != nil {
		financialError(t, first, 409, "record_changed")
	}
	if second != nil {
		financialError(t, second, 409, "record_changed")
	}
	// Equal timestamps exercise the ID tie-breaker and filter-bound cursor.
	for range 52 {
		_, e = s.db.Exec(ctx, `INSERT INTO admin_financial_requests(id,kind,requester_id,request_key,business_id,allocation_id,destination_id,terms,terms_fingerprint,reason,status,reviewer_id,decision_reason,decision_key,decided_at,created_at)
  SELECT $2,kind,requester_id,$3,business_id,allocation_id,destination_id,terms,terms_fingerprint,reason,'withdrawn',requester_id,'Fixture withdrawal',$4,created_at,created_at FROM admin_financial_requests WHERE id=$1`, request.ID, uuid.New(), uuid.New(), uuid.New())
		if e != nil {
			t.Fatal(e)
		}
	}
	f := FinanceFilter{BusinessID: &input.BusinessID}
	page, e := s.FinancialRequests(ctx, f)
	if e != nil || len(page.Items) != 50 || page.NextCursor == "" {
		t.Fatal("first page", len(page.Items), e)
	}
	f.Cursor = page.NextCursor
	next, e := s.FinancialRequests(ctx, f)
	if e != nil || len(next.Items) != 3 || next.NextCursor != "" {
		t.Fatal("second page", len(next.Items), e)
	}
	seen := map[uuid.UUID]bool{}
	for _, row := range append(page.Items, next.Items...) {
		if seen[row.ID] {
			t.Fatal("duplicate row")
		}
		seen[row.ID] = true
	}
	for _, change := range []func(*FinanceFilter){func(x *FinanceFilter) { x.Currency = "USD" }, func(x *FinanceFilter) { x.Status = "withdrawn" }, func(x *FinanceFilter) { x.BusinessID = nil }, func(x *FinanceFilter) { x.Q = "check" }} {
		changed := f
		change(&changed)
		if _, e = s.FinancialRequests(ctx, changed); e == nil {
			t.Fatal("changed scope accepted cursor")
		}
	}
	noFinancialExecution(t, s, input)
}
