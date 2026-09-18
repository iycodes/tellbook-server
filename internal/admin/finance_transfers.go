package admin

import (
	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"time"
)

// These are bounded read projections over the existing payout/refund owners.
// No provider dispatch or reconciliation method is called by investigation.
type FinanceTransfer struct {
	ID           uuid.UUID   `json:"id"`
	BusinessID   uuid.UUID   `json:"business_id"`
	BusinessName string      `json:"business_name"`
	BookingID    uuid.UUID   `json:"booking_id"`
	PaymentID    *uuid.UUID  `json:"payment_id"`
	Reference    string      `json:"reference"`
	Status       string      `json:"status"`
	CurrencyCode string      `json:"currency_code"`
	AmountMinor  money.Minor `json:"amount_minor"`
	CreatedAt    time.Time   `json:"created_at"`
}
type FinanceTransferPage struct {
	Items      []FinanceTransfer `json:"items"`
	NextCursor string            `json:"next_cursor"`
	From       string            `json:"from"`
	To         string            `json:"to"`
}

const payoutSelect = `SELECT p.id,p.client_id,b.business_name,m.booking_id,m.id,p.reference,p.status,p.currency_code,p.amount_minor,p.created_at FROM payouts p JOIN payment_allocations a ON a.id=p.payment_allocation_id AND a.client_id=p.client_id JOIN payments m ON m.id=a.payment_id JOIN client_profiles b ON b.client_id=p.client_id `
const refundSelect = `SELECT p.id,m.client_id,b.business_name,p.booking_id,NULL::uuid,''::text,p.status,p.currency_code,p.amount_minor,p.created_at FROM booking_refund_requests p JOIN bookings m ON m.id=p.booking_id JOIN client_profiles b ON b.client_id=m.client_id `

func scanTransfer(row pgx.Row) (FinanceTransfer, error) {
	var p FinanceTransfer
	var amount int64
	e := row.Scan(&p.ID, &p.BusinessID, &p.BusinessName, &p.BookingID, &p.PaymentID, &p.Reference, &p.Status, &p.CurrencyCode, &amount, &p.CreatedAt)
	p.AmountMinor = money.Minor(amount)
	p.CreatedAt = p.CreatedAt.UTC()
	return p, e
}
func (s *Service) FinanceTransfers(ctx context.Context, kind string, f FinanceFilter) (FinanceTransferPage, error) {
	out := FinanceTransferPage{Items: []FinanceTransfer{}}
	var query, search, business, booking string
	var statuses []string
	switch kind {
	case "payouts":
		query = payoutSelect
		search = `p.reference||' '||p.provider_reference||' '||p.id::text||' '||m.booking_id::text||' '||b.business_name`
		business = "p.client_id"
		booking = "m.booking_id"
		statuses = []string{"created", "pending", "requires_action", "successful", "failed", "reversed", "cancelled", "unknown"}
	case "refunds":
		query = refundSelect
		search = `p.id::text||' '||p.command_id::text||' '||p.booking_id::text||' '||b.business_name`
		business = "m.client_id"
		booking = "p.booking_id"
		statuses = []string{"queued", "processing", "successful", "failed", "cancelled", "manual_review"}
	default:
		return out, problem(404, "not_found", "Financial record type not found.")
	}
	w, e := financeWindow(kind, &f, statuses, s.now())
	if e != nil {
		return out, e
	}
	out.From, out.To = w.From, w.To
	// SQL identifiers originate only from the two fixed cases above.
	rows, e := s.db.Query(ctx, query+`WHERE p.created_at >= $1 AND p.created_at < $2 AND ($3='' OR p.status=$3) AND ($4='' OR p.currency_code=$4) AND ($5::uuid IS NULL OR `+business+`=$5) AND ($6::uuid IS NULL OR `+booking+`=$6) AND ($7='' OR position(lower($7) in lower(`+search+`))>0) AND ($8::timestamptz IS NULL OR (p.created_at,p.id)<($8,$9::uuid)) ORDER BY p.created_at DESC,p.id DESC LIMIT 51`, w.Start, w.End, f.Status, f.Currency, f.BusinessID, f.BookingID, f.Q, w.After, w.AfterID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		p, e := scanTransfer(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, p)
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

type FinancePayoutDetail struct {
	FinanceTransfer
	AllocationID      uuid.UUID   `json:"allocation_id"`
	Provider          string      `json:"provider"`
	Rail              string      `json:"rail"`
	ProviderReference string      `json:"provider_reference"`
	FeeMinor          money.Minor `json:"fee_minor"`
	Institution       string      `json:"institution"`
	MaskedIdentifier  string      `json:"masked_identifier"`
	ProviderStatus    string      `json:"provider_status"`
	FailureCode       string      `json:"failure_code"`
	InitiatedAt       *time.Time  `json:"initiated_at"`
	CompletedAt       *time.Time  `json:"completed_at"`
	ReconciledAt      *time.Time  `json:"reconciled_at"`
	ReversedAt        *time.Time  `json:"reversed_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

func (s *Service) FinancePayoutDetail(ctx context.Context, id uuid.UUID) (FinancePayoutDetail, error) {
	var out FinancePayoutDetail
	base, e := scanTransfer(s.db.QueryRow(ctx, payoutSelect+`WHERE p.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Payout not found.")
	}
	if e != nil {
		return out, e
	}
	// Reuse the ledger lookup, with the owning business as a record scope (not an actor).
	p, e := payments.NewLedgerRepository(s.db).GetFinancialPayout(ctx, base.BusinessID, id)
	if e != nil {
		return out, e
	}
	out.FinanceTransfer = base
	out.Status = string(p.Status)
	out.AmountMinor = money.Minor(p.AmountMinor)
	out.AllocationID, out.Provider, out.Rail, out.ProviderReference = p.PaymentAllocationID, p.Provider, p.Rail, p.ProviderReference
	out.FeeMinor = money.Minor(p.FeeMinor)
	out.UpdatedAt = p.UpdatedAt
	var destination struct {
		Institution string `json:"institution_name"`
		Masked      string `json:"masked_identifier"`
	}
	if e = json.Unmarshal(p.DestinationSnapshot, &destination); e != nil {
		return out, e
	}
	out.Institution, out.MaskedIdentifier = destination.Institution, destination.Masked
	var version int64
	e = s.db.QueryRow(ctx, `SELECT provider_status,failure_code,initiated_at,completed_at,last_reconciled_at,reversed_at,version FROM payouts WHERE id=$1`, id).Scan(&out.ProviderStatus, &out.FailureCode, &out.InitiatedAt, &out.CompletedAt, &out.ReconciledAt, &out.ReversedAt, &version)
	if e == nil && version != p.Version {
		return out, conflict
	}
	return out, e
}

type FinanceRefundAttempt struct {
	ID                   uuid.UUID   `json:"id"`
	PaymentID            uuid.UUID   `json:"payment_id"`
	Provider             string      `json:"provider"`
	TransactionReference string      `json:"transaction_reference"`
	ProviderReference    string      `json:"provider_reference"`
	Status               string      `json:"status"`
	ProviderStatus       string      `json:"provider_status"`
	CurrencyCode         string      `json:"currency_code"`
	AmountMinor          money.Minor `json:"amount_minor"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}
type FinanceRefundDetail struct {
	FinanceTransfer
	CommandID    uuid.UUID              `json:"command_id"`
	Reason       string                 `json:"reason"`
	UpdatedAt    time.Time              `json:"updated_at"`
	CompletedAt  *time.Time             `json:"completed_at"`
	Attempts     []FinanceRefundAttempt `json:"attempts"`
	MoreAttempts bool                   `json:"more_attempts"`
}

func (s *Service) FinanceRefundDetail(ctx context.Context, id uuid.UUID) (FinanceRefundDetail, error) {
	out := FinanceRefundDetail{Attempts: []FinanceRefundAttempt{}}
	base, e := scanTransfer(s.db.QueryRow(ctx, refundSelect+`WHERE p.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return out, problem(404, "not_found", "Refund request not found.")
	}
	if e != nil {
		return out, e
	}
	out.FinanceTransfer = base
	e = s.db.QueryRow(ctx, `SELECT status,command_id,reason,updated_at,completed_at FROM booking_refund_requests WHERE id=$1`, id).Scan(&out.Status, &out.CommandID, &out.Reason, &out.UpdatedAt, &out.CompletedAt)
	if e != nil {
		return out, e
	}
	rows, e := s.db.Query(ctx, `SELECT id,payment_id,provider,transaction_reference,coalesce(provider_reference,''),status,provider_status,currency_code,amount_minor,created_at,updated_at FROM booking_refund_attempts WHERE request_id=$1 ORDER BY created_at DESC,id DESC LIMIT 26`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var a FinanceRefundAttempt
		var amount int64
		if e = rows.Scan(&a.ID, &a.PaymentID, &a.Provider, &a.TransactionReference, &a.ProviderReference, &a.Status, &a.ProviderStatus, &a.CurrencyCode, &amount, &a.CreatedAt, &a.UpdatedAt); e != nil {
			rows.Close()
			return out, e
		}
		a.AmountMinor = money.Minor(amount)
		out.Attempts = append(out.Attempts, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Attempts) > 25 {
		out.MoreAttempts = true
		out.Attempts = out.Attempts[:25]
	}
	var updated time.Time
	e = s.db.QueryRow(ctx, `SELECT updated_at FROM booking_refund_requests WHERE id=$1`, id).Scan(&updated)
	if e == nil && !updated.Equal(out.UpdatedAt) {
		return out, conflict
	}
	return out, e
}
