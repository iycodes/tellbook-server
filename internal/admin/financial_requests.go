package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"booking/go-server/internal/payments"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type PayoutReviewInput struct {
	Kind          string    `json:"kind"`
	BusinessID    uuid.UUID `json:"business_id"`
	AllocationID  uuid.UUID `json:"allocation_id"`
	DestinationID uuid.UUID `json:"destination_id"`
}
type FinancialRequestInput struct {
	PayoutReviewInput
	ExpectedFingerprint string    `json:"expected_fingerprint"`
	Reason              string    `json:"reason"`
	RequestKey          uuid.UUID `json:"request_key"`
}
type FinancialDecision struct {
	Action           string    `json:"action"`
	Reason           string    `json:"reason"`
	ExpectedRevision int       `json:"expected_revision"`
	RequestKey       uuid.UUID `json:"request_key"`
}
type FinancialRequest struct {
	RequesterName  string                `json:"requester_name"`
	ReviewerName   string                `json:"reviewer_name"`
	ID             uuid.UUID             `json:"id"`
	Kind           string                `json:"kind"`
	RequesterID    uuid.UUID             `json:"requester_id"`
	Terms          payments.PayoutReview `json:"terms"`
	Reason         string                `json:"reason"`
	Status         string                `json:"status"`
	Revision       int                   `json:"revision"`
	ReviewerID     *uuid.UUID            `json:"reviewer_id"`
	DecisionReason string                `json:"decision_reason"`
	DecidedAt      *time.Time            `json:"decided_at"`
	CreatedAt      time.Time             `json:"created_at"`
	UpdatedAt      time.Time             `json:"updated_at"`
	decisionKey    *uuid.UUID
}
type FinancialRequestPage struct {
	Items      []FinancialRequest `json:"items"`
	NextCursor string             `json:"next_cursor"`
	From       string             `json:"from"`
	To         string             `json:"to"`
}

const financialRequestSelect = `SELECT id,kind,requester_id,terms,reason,status,revision,reviewer_id,decision_reason,decided_at,created_at,updated_at,decision_key,(SELECT full_name FROM admin_staff WHERE id=requester_id),coalesce((SELECT full_name FROM admin_staff WHERE id=reviewer_id),'') FROM admin_financial_requests`

func scanFinancialRequest(row pgx.Row) (FinancialRequest, error) {
	var out FinancialRequest
	var raw []byte
	e := row.Scan(&out.ID, &out.Kind, &out.RequesterID, &raw, &out.Reason, &out.Status, &out.Revision, &out.ReviewerID, &out.DecisionReason, &out.DecidedAt, &out.CreatedAt, &out.UpdatedAt, &out.decisionKey, &out.RequesterName, &out.ReviewerName)
	if e == nil {
		e = json.Unmarshal(raw, &out.Terms)
	}
	if errors.Is(e, pgx.ErrNoRows) {
		e = problem(404, "not_found", "Financial request not found.")
	}
	return out, e
}
func financialReason(raw string) (string, error) {
	reason := strings.TrimSpace(raw)
	if len([]rune(reason)) < 1 || len([]rune(reason)) > 1000 {
		return "", problem(422, "invalid_reason", "Give a reason between 1 and 1,000 characters.")
	}
	return reason, nil
}
func (s *Service) payoutReviewTx(ctx context.Context, tx pgx.Tx, input PayoutReviewInput) (payments.PayoutReview, error) {
	if input.Kind != "payout" || input.BusinessID == uuid.Nil || input.AllocationID == uuid.Nil || input.DestinationID == uuid.Nil {
		return payments.PayoutReview{}, problem(422, "invalid_request", "Choose a payout allocation and destination belonging to the same business.")
	}
	review, e := payments.NewLedgerRepository(s.db).ReviewPayoutTx(ctx, tx, payments.CreateFinancialPayoutInput{ClientID: input.BusinessID, PaymentAllocationID: input.AllocationID, PayoutDestinationID: input.DestinationID})
	if e != nil {
		// Do not expose destination identifiers or internal provider evidence.
		var active *payments.ActivePayoutError
		if errors.As(e, &active) || errors.Is(e, pgx.ErrNoRows) || errors.Is(e, payments.ErrLedgerRecordNotFound) {
			return review, problem(409, "payout_unavailable", "This allocation or destination is unavailable. Review the current financial records.")
		}
		if errors.Is(e, payments.ErrPayoutAllocationIneligible) || errors.Is(e, payments.ErrPayoutDestinationMismatch) {
			return review, problem(409, "payout_unavailable", "This allocation or destination is not eligible for payout.")
		}
	}
	return review, e
}
func (s *Service) PreviewFinancialRequest(ctx context.Context, session Session, input PayoutReviewInput) (payments.PayoutReview, error) {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return payments.PayoutReview{}, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "finance.manage"); e != nil {
		return payments.PayoutReview{}, e
	}
	return s.payoutReviewTx(ctx, tx, input)
}
func (s *Service) CreateFinancialRequest(ctx context.Context, session Session, input FinancialRequestInput) (FinancialRequest, error) {
	var out FinancialRequest
	reason, e := financialReason(input.Reason)
	if e != nil {
		return out, e
	}
	_, fingerprintError := hex.DecodeString(input.ExpectedFingerprint)
	if input.RequestKey == uuid.Nil || len(input.ExpectedFingerprint) != 64 || fingerprintError != nil || input.ExpectedFingerprint != strings.ToLower(input.ExpectedFingerprint) {
		return out, problem(422, "invalid_request", "Review current payout terms before creating a request.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "finance.manage"); e != nil {
		return out, e
	}
	// Same staff/key retries serialize before lookup, including concurrent submits.
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "financial-request:"+session.Staff.ID.String()+":"+input.RequestKey.String()); e != nil {
		return out, e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_financial_requests WHERE requester_id=$1 AND request_key=$2)`, session.Staff.ID, input.RequestKey).Scan(&exists); e != nil {
		return out, e
	}
	if exists {
		out, e = scanFinancialRequest(tx.QueryRow(ctx, financialRequestSelect+` WHERE requester_id=$1 AND request_key=$2`, session.Staff.ID, input.RequestKey))
		if e != nil {
			return out, e
		}
		if out.Kind != input.Kind || out.Reason != reason || out.Terms.BusinessID != input.BusinessID || out.Terms.AllocationID != input.AllocationID || out.Terms.DestinationID != input.DestinationID || out.Terms.Fingerprint != input.ExpectedFingerprint {
			return out, problem(409, "idempotency_conflict", "This request key was used for different terms.")
		}
		return out, nil
	}
	terms, e := s.payoutReviewTx(ctx, tx, input.PayoutReviewInput)
	if e != nil {
		return out, e
	}
	if terms.Fingerprint != input.ExpectedFingerprint {
		return out, conflict
	}
	// The allocation lock serializes new requests. Check open metadata without
	// locking it: a reviewer locks the request before the allocation, so waiting
	// for its unique-index update here would reverse that lock order.
	var open bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_financial_requests WHERE allocation_id=$1 AND status IN ('pending','approved'))`, terms.AllocationID).Scan(&open); e != nil {
		return out, e
	}
	if open {
		return out, problem(409, "request_exists", "This allocation already has a pending or approved request.")
	}
	raw, e := json.Marshal(terms)
	if e != nil {
		return out, e
	}
	id := uuid.New()
	_, e = tx.Exec(ctx, `INSERT INTO admin_financial_requests(id,kind,requester_id,request_key,business_id,allocation_id,destination_id,terms,terms_fingerprint,reason) VALUES($1,'payout',$2,$3,$4,$5,$6,$7,$8,$9)`, id, session.Staff.ID, input.RequestKey, terms.BusinessID, terms.AllocationID, terms.DestinationID, raw, terms.Fingerprint, reason)
	if e != nil {
		var pgerr *pgconn.PgError
		if errors.As(e, &pgerr) && pgerr.Code == "23505" {
			return out, problem(409, "request_exists", "This allocation already has a pending or approved request.")
		}
		return out, e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "finance.request_created", "financial_request", &id, reason, map[string]any{"kind": "payout", "fingerprint": terms.Fingerprint}); e != nil {
		return out, e
	}
	out, e = scanFinancialRequest(tx.QueryRow(ctx, financialRequestSelect+` WHERE id=$1`, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) DecideFinancialRequest(ctx context.Context, session Session, id uuid.UUID, input FinancialDecision) (FinancialRequest, error) {
	var out FinancialRequest
	reason, e := financialReason(input.Reason)
	if e != nil {
		return out, e
	}
	status := map[string]string{"approve": "approved", "reject": "rejected", "withdraw": "withdrawn"}[input.Action]
	if status == "" || input.ExpectedRevision < 1 || input.RequestKey == uuid.Nil {
		return out, problem(422, "invalid_decision", "Choose a supported decision and the reviewed request version.")
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = s.authorizeTx(ctx, tx, session, "finance.manage"); e != nil {
		return out, e
	}
	out, e = scanFinancialRequest(tx.QueryRow(ctx, financialRequestSelect+` WHERE id=$1 FOR UPDATE`, id))
	if e != nil {
		return out, e
	}
	if out.decisionKey != nil && *out.decisionKey == input.RequestKey && out.ReviewerID != nil && *out.ReviewerID == session.Staff.ID {
		if out.Status != status || out.DecisionReason != reason || input.ExpectedRevision != out.Revision-1 {
			return out, problem(409, "idempotency_conflict", "This request key was used for a different decision.")
		}
		return out, nil
	}
	if out.Revision != input.ExpectedRevision {
		return out, conflict
	}
	if out.Status != "pending" && !(input.Action == "withdraw" && out.Status == "approved") {
		return out, problem(409, "decision_unavailable", "This request no longer accepts that decision.")
	}
	if input.Action != "withdraw" && out.RequesterID == session.Staff.ID {
		return out, problem(403, "independent_reviewer_required", "A different authorized staff member must review this request.")
	}
	if input.Action == "approve" {
		requester, e := scanStaff(tx.QueryRow(ctx, `SELECT `+staffColumns+` FROM admin_staff WHERE id=$1 FOR SHARE`, out.RequesterID))
		if e != nil {
			return out, e
		}
		if requester.Status != "active" || !allowed(requester.Role, "finance.manage") {
			return out, problem(409, "requester_unavailable", "The requester no longer has financial authority. Withdraw this request.")
		}
		terms, e := s.payoutReviewTx(ctx, tx, PayoutReviewInput{Kind: out.Kind, BusinessID: out.Terms.BusinessID, AllocationID: out.Terms.AllocationID, DestinationID: out.Terms.DestinationID})
		if e != nil {
			return out, e
		}
		if terms.Fingerprint != out.Terms.Fingerprint {
			return out, conflict
		}
	}
	_, e = tx.Exec(ctx, `UPDATE admin_financial_requests SET status=$2,revision=revision+1,reviewer_id=$3,decision_reason=$4,decision_key=$5,decided_at=now(),updated_at=now() WHERE id=$1`, id, status, session.Staff.ID, reason, input.RequestKey)
	if e != nil {
		return out, e
	}
	if e = audit(ctx, tx, &session.Staff.ID, "finance.request_"+status, "financial_request", &id, reason, map[string]any{"kind": out.Kind, "fingerprint": out.Terms.Fingerprint, "revision": out.Revision + 1}); e != nil {
		return out, e
	}
	out, e = scanFinancialRequest(tx.QueryRow(ctx, financialRequestSelect+` WHERE id=$1`, id))
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) FinancialRequest(ctx context.Context, id uuid.UUID) (FinancialRequest, error) {
	return scanFinancialRequest(s.db.QueryRow(ctx, financialRequestSelect+` WHERE id=$1`, id))
}
func (s *Service) FinancialRequests(ctx context.Context, f FinanceFilter) (FinancialRequestPage, error) {
	out := FinancialRequestPage{Items: []FinancialRequest{}}
	if f.BookingID != nil {
		return out, problem(422, "invalid_filter", "Financial requests support business scope, not booking scope.")
	}
	w, e := financeWindow("requests", &f, []string{"pending", "approved", "rejected", "withdrawn"}, s.now())
	if e != nil {
		return out, e
	}
	out.From, out.To = w.From, w.To
	rows, e := s.db.Query(ctx, financialRequestSelect+` WHERE created_at >= $1 AND created_at < $2 AND ($3='' OR status=$3) AND ($4::uuid IS NULL OR business_id=$4) AND ($5='' OR terms->>'currency_code'=$5) AND ($6='' OR position(lower($6) in lower(id::text||' '||reason))>0) AND ($7::timestamptz IS NULL OR (created_at,id)<($7,$8::uuid)) ORDER BY created_at DESC,id DESC LIMIT 51`, w.Start, w.End, f.Status, f.BusinessID, f.Currency, f.Q, w.After, w.AfterID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanFinancialRequest(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > 50 {
		out.Items = out.Items[:50]
		last := out.Items[49]
		out.NextCursor = financeNextCursor(last.CreatedAt, last.ID, w.Scope)
	}
	return out, nil
}
