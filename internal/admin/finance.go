package admin

import (
	"context"
	"errors"
	"regexp"
	"time"

	"booking/go-server/internal/money"
	"booking/go-server/internal/payments"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Platform investigation is a safe projection of the existing ledger, not a
// second money model. Customer checkout tokens and provider payloads stay private.
type FinancePayment struct {
	ID                uuid.UUID   `json:"id"`
	BookingID         uuid.UUID   `json:"booking_id"`
	BusinessID        uuid.UUID   `json:"business_id"`
	BusinessName      string      `json:"business_name"`
	Reference         string      `json:"reference"`
	ProviderReference string      `json:"provider_reference"`
	Provider          string      `json:"provider"`
	Purpose           string      `json:"purpose"`
	Status            string      `json:"status"`
	CurrencyCode      string      `json:"currency_code"`
	AmountMinor       money.Minor `json:"amount_minor"`
	CreatedAt         time.Time   `json:"created_at"`
}
type FinanceFilter struct {
	From, To, Q, Status, Currency, Cursor string
	BusinessID, BookingID                 *uuid.UUID
}
type FinancePaymentPage struct {
	Items      []FinancePayment `json:"items"`
	NextCursor string           `json:"next_cursor"`
	From       string           `json:"from"`
	To         string           `json:"to"`
}
type financeCursor struct {
	At    time.Time `json:"at"`
	ID    uuid.UUID `json:"id"`
	Scope string    `json:"scope"`
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func (s *Service) FinancePayments(ctx context.Context, f FinanceFilter) (FinancePaymentPage, error) {
	out := FinancePaymentPage{Items: []FinancePayment{}}
	w, e := financeWindow("payments", &f, []string{"created", "pending", "requires_action", "paid", "partially_refunded", "refunded", "disputed", "reversed", "failed", "expired", "cancelled"}, s.now())
	if e != nil {
		return out, e
	}
	out.From, out.To = w.From, w.To
	rows, e := s.db.Query(ctx, `SELECT p.id,p.booking_id,p.client_id,b.business_name,p.reference,p.provider_reference,p.provider,p.purpose,p.status,p.currency_code,p.amount_minor,p.created_at
 FROM payments p JOIN client_profiles b ON b.client_id=p.client_id
 WHERE p.created_at >= $1 AND p.created_at < $2 AND ($3='' OR p.status=$3) AND ($4='' OR p.currency_code=$4)
 AND ($5::uuid IS NULL OR p.client_id=$5) AND ($6::uuid IS NULL OR p.booking_id=$6)
 AND ($7='' OR position(lower($7) in lower(p.reference||' '||p.provider_reference||' '||p.id::text||' '||p.booking_id::text||' '||b.business_name))>0)
 AND ($8::timestamptz IS NULL OR (p.created_at,p.id)<($8,$9::uuid)) ORDER BY p.created_at DESC,p.id DESC LIMIT 51`, w.Start, w.End, f.Status, f.Currency, f.BusinessID, f.BookingID, f.Q, w.After, w.AfterID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var p FinancePayment
		var amount int64
		if e = rows.Scan(&p.ID, &p.BookingID, &p.BusinessID, &p.BusinessName, &p.Reference, &p.ProviderReference, &p.Provider, &p.Purpose, &p.Status, &p.CurrencyCode, &amount, &p.CreatedAt); e != nil {
			return out, e
		}
		p.AmountMinor = money.Minor(amount)
		p.CreatedAt = p.CreatedAt.UTC()
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

type FinanceAllocation struct {
	ID                  uuid.UUID   `json:"id"`
	CurrencyCode        string      `json:"currency_code"`
	Gross               money.Minor `json:"gross_minor"`
	CollectionFee       money.Minor `json:"collection_fee_minor"`
	PlatformFee         money.Minor `json:"platform_fee_minor"`
	Tax                 money.Minor `json:"tax_minor"`
	Adjustment          money.Minor `json:"adjustment_minor"`
	BusinessNet         money.Minor `json:"business_net_minor"`
	Status              string      `json:"status"`
	SettlementStatus    string      `json:"settlement_status"`
	SettlementReference string      `json:"settlement_reference"`
	AvailableAt         *time.Time  `json:"available_at"`
}
type FinanceAdjustment struct {
	ID           uuid.UUID   `json:"id"`
	Kind         string      `json:"kind"`
	Status       string      `json:"status"`
	CurrencyCode string      `json:"currency_code"`
	AmountMinor  money.Minor `json:"amount_minor"`
	Reference    string      `json:"reference"`
	OccurredAt   time.Time   `json:"occurred_at"`
}
type FinanceException struct {
	ID                  uuid.UUID   `json:"id"`
	Kind                string      `json:"kind"`
	Status              string      `json:"status"`
	CurrencyCode        string      `json:"currency_code"`
	ObservedAmountMinor money.Minor `json:"observed_amount_minor"`
	EvidenceSource      string      `json:"evidence_source"`
	EvidenceReference   string      `json:"evidence_reference"`
	CreatedAt           time.Time   `json:"created_at"`
}

// Destination selectors reuse ledger identities and expose only masked context.
type FinanceDestination struct {
	ID               uuid.UUID `json:"id"`
	InstitutionName  string    `json:"institution_name"`
	MaskedIdentifier string    `json:"masked_identifier"`
	Provider         string    `json:"provider"`
	CurrencyCode     string    `json:"currency_code"`
}
type FinancePaymentDetail struct {
	Destinations []FinanceDestination `json:"destinations"`
	FinancePayment
	Method               string              `json:"method"`
	ProviderStatus       string              `json:"provider_status"`
	ReconciliationReason string              `json:"reconciliation_reason"`
	FailureCode          string              `json:"failure_code"`
	PaidAt               *time.Time          `json:"paid_at"`
	ReconciledAt         *time.Time          `json:"reconciled_at"`
	UpdatedAt            time.Time           `json:"updated_at"`
	Allocation           *FinanceAllocation  `json:"allocation"`
	Adjustments          []FinanceAdjustment `json:"adjustments"`
	Exceptions           []FinanceException  `json:"exceptions"`
	MoreAdjustments      bool                `json:"more_adjustments"`
	MoreExceptions       bool                `json:"more_exceptions"`
}

func (s *Service) FinancePaymentDetail(ctx context.Context, id uuid.UUID) (FinancePaymentDetail, error) {
	out := FinancePaymentDetail{Destinations: []FinanceDestination{}, Adjustments: []FinanceAdjustment{}, Exceptions: []FinanceException{}}
	ledger := payments.NewLedgerRepository(s.db)
	p, e := ledger.GetPaymentByID(ctx, id)
	if errors.Is(e, payments.ErrLedgerRecordNotFound) {
		return out, problem(404, "not_found", "Payment not found.")
	}
	if e != nil {
		return out, e
	}
	out.FinancePayment = FinancePayment{ID: p.ID, BookingID: p.BookingID, BusinessID: p.ClientID, Reference: p.Reference, ProviderReference: p.ProviderReference, Provider: p.Provider, Purpose: string(p.Purpose), Status: string(p.Status), CurrencyCode: p.CurrencyCode, AmountMinor: p.AmountMinor, CreatedAt: p.CreatedAt.UTC()}
	if e = s.db.QueryRow(ctx, `SELECT business_name FROM client_profiles WHERE client_id=$1`, p.ClientID).Scan(&out.BusinessName); e != nil {
		return out, e
	}
	out.Method, out.ProviderStatus, out.ReconciliationReason, out.FailureCode = p.Method, p.ProviderStatus, p.ReconciliationReason, p.FailureCode
	out.PaidAt, out.ReconciledAt, out.UpdatedAt = p.PaidAt, p.LastReconciledAt, p.UpdatedAt
	var allocationID uuid.UUID
	e = s.db.QueryRow(ctx, `SELECT id FROM payment_allocations WHERE payment_id=$1`, id).Scan(&allocationID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if e == nil {
		a, err := ledger.GetPaymentAllocation(ctx, p.ClientID, allocationID)
		if err != nil {
			return out, err
		}
		out.Allocation = &FinanceAllocation{ID: a.ID, CurrencyCode: a.CurrencyCode, Gross: money.Minor(a.Amounts.GrossMinor), CollectionFee: money.Minor(a.Amounts.ProviderFeeMinor), PlatformFee: money.Minor(a.Amounts.PlatformFeeMinor), Tax: money.Minor(a.Amounts.TaxMinor), Adjustment: money.Minor(a.Amounts.AdjustmentMinor), BusinessNet: money.Minor(a.Amounts.BusinessNetAmountMinor), Status: a.Status, SettlementStatus: a.SettlementStatus, SettlementReference: a.SettlementReference, AvailableAt: a.AvailableForPayoutAt}
	}
	if out.Allocation != nil {
		destinations, err := ledger.ListPayoutDestinations(ctx, p.ClientID)
		if err != nil {
			return out, err
		}
		for _, d := range destinations {
			if d.Status == "active" && d.CurrencyCode == out.Allocation.CurrencyCode {
				out.Destinations = append(out.Destinations, FinanceDestination{ID: d.ID, InstitutionName: d.InstitutionName, MaskedIdentifier: d.MaskedIdentifier, Provider: d.Provider, CurrencyCode: d.CurrencyCode})
			}
		}
	}
	rows, e := s.db.Query(ctx, `SELECT id,kind,status,currency_code,amount_minor,provider_reference,occurred_at FROM payment_adjustments WHERE payment_id=$1 ORDER BY occurred_at DESC,id DESC LIMIT 26`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var a FinanceAdjustment
		var amount int64
		if e = rows.Scan(&a.ID, &a.Kind, &a.Status, &a.CurrencyCode, &amount, &a.Reference, &a.OccurredAt); e != nil {
			rows.Close()
			return out, e
		}
		a.AmountMinor = money.Minor(amount)
		out.Adjustments = append(out.Adjustments, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Adjustments) > 25 {
		out.MoreAdjustments = true
		out.Adjustments = out.Adjustments[:25]
	}
	rows, e = s.db.Query(ctx, `SELECT id,exception_kind,status,currency_code,observed_amount_minor,evidence_source,evidence_reference,created_at FROM payment_exceptions WHERE payment_id=$1 ORDER BY created_at DESC,id DESC LIMIT 26`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var x FinanceException
		var amount int64
		if e = rows.Scan(&x.ID, &x.Kind, &x.Status, &x.CurrencyCode, &amount, &x.EvidenceSource, &x.EvidenceReference, &x.CreatedAt); e != nil {
			rows.Close()
			return out, e
		}
		x.ObservedAmountMinor = money.Minor(amount)
		out.Exceptions = append(out.Exceptions, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Exceptions) > 25 {
		out.MoreExceptions = true
		out.Exceptions = out.Exceptions[:25]
	}
	// Do not render a mixed payment version if a worker changed it during assembly.
	latest, e := ledger.GetPaymentByID(ctx, id)
	if e != nil {
		return out, e
	}
	if latest.Version != p.Version || !latest.UpdatedAt.Equal(p.UpdatedAt) {
		return out, conflict
	}
	return out, nil
}
