package admin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"booking/go-server/internal/appdata"
	"booking/go-server/internal/money"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Booking struct {
	updatedAt        time.Time
	ID               uuid.UUID   `json:"id"`
	BusinessID       uuid.UUID   `json:"business_id"`
	BusinessName     string      `json:"business_name"`
	ContactID        uuid.UUID   `json:"contact_id"`
	AccountID        *uuid.UUID  `json:"account_id"`
	CustomerName     string      `json:"customer_name"`
	Title            string      `json:"title"`
	StartAt          time.Time   `json:"start_at"`
	EndAt            time.Time   `json:"end_at"`
	Status           string      `json:"status"`
	PaymentStatus    string      `json:"payment_status"`
	AgreementStatus  string      `json:"agreement_status"`
	TotalAmountMinor money.Minor `json:"total_amount_minor"`
	CurrencyCode     string      `json:"currency_code"`
	Source           string      `json:"source"`
}

const bookingColumns = `b.id,b.client_id,p.business_name,b.customer_id,b.marketplace_customer_id,c.full_name,b.title,b.start_at,b.end_at,b.status,b.payment_status,b.agreement_status,b.total_amount_minor,b.currency_code,b.source,b.updated_at`
const bookingJoins = ` FROM bookings b JOIN client_profiles p ON p.client_id=b.client_id JOIN customers c ON c.id=b.customer_id AND c.client_id=b.client_id `

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	var amount int64
	e := row.Scan(&b.ID, &b.BusinessID, &b.BusinessName, &b.ContactID, &b.AccountID, &b.CustomerName, &b.Title, &b.StartAt, &b.EndAt, &b.Status, &b.PaymentStatus, &b.AgreementStatus, &amount, &b.CurrencyCode, &b.Source, &b.updatedAt)
	b.TotalAmountMinor = money.Minor(amount)
	b.StartAt = b.StartAt.UTC()
	b.EndAt = b.EndAt.UTC()
	return b, e
}

type BookingFilter struct {
	From, To, Q, Status, Cursor      string
	BusinessID, ContactID, AccountID *uuid.UUID
}
type BookingPage struct {
	Items      []Booking `json:"items"`
	NextCursor string    `json:"next_cursor"`
	From       string    `json:"from"`
	To         string    `json:"to"`
}
type bookingCursor struct {
	Start time.Time `json:"start"`
	ID    uuid.UUID `json:"id"`
	Scope string    `json:"scope"`
}

func bookingWindow(f BookingFilter, now time.Time) (time.Time, time.Time, error) {
	start := now.UTC().Truncate(24 * time.Hour)
	var e error
	if f.From != "" {
		start, e = time.Parse("2006-01-02", f.From)
		if e != nil {
			return start, start, problem(422, "invalid_date", "Use a valid start date.")
		}
	}
	end := start.AddDate(0, 0, 7)
	if f.To != "" {
		end, e = time.Parse("2006-01-02", f.To)
		if e != nil {
			return start, end, problem(422, "invalid_date", "Use a valid end date.")
		}
	}
	if !end.After(start) || end.Sub(start) > 31*24*time.Hour {
		return start, end, problem(422, "invalid_window", "Choose a date window of 1–31 days. The end date is exclusive.")
	}
	return start, end, nil
}
func (s *Service) Bookings(ctx context.Context, f BookingFilter) (BookingPage, error) {
	out := BookingPage{Items: []Booking{}}
	start, end, e := bookingWindow(f, s.now())
	if e != nil {
		return out, e
	}
	out.From, out.To = start.Format("2006-01-02"), end.Format("2006-01-02")
	f.Q = strings.TrimSpace(f.Q)
	if len([]rune(f.Q)) > 100 {
		return out, problem(422, "invalid_search", "Search up to 100 characters.")
	}
	switch f.Status {
	case "", "booked", "confirmed", "completed", "no_show", "cancelled", "canceled", "declined", "expired":
	default:
		return out, problem(422, "invalid_status", "Choose a valid booking status.")
	}
	fingerprint, _ := json.Marshal([]any{out.From, out.To, f.Q, f.Status, f.BusinessID, f.ContactID, f.AccountID})
	scope := fmt.Sprintf("%x", sha256.Sum256(fingerprint))
	var after any
	var afterID any
	if f.Cursor != "" {
		var c bookingCursor
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil || len(raw) > 500 || json.Unmarshal(raw, &c) != nil || c.Scope != scope || c.ID == uuid.Nil || c.Start.IsZero() {
			return out, problem(422, "invalid_cursor", "This page belongs to different filters. Start from the first page.")
		}
		after, afterID = c.Start, c.ID
	}
	rows, e := s.db.Query(ctx, `SELECT `+bookingColumns+bookingJoins+`WHERE b.start_at >= $1 AND b.start_at < $2
 AND ($3='' OR b.status=$3) AND ($4='' OR position(lower($4) in lower(b.title||' '||c.full_name||' '||p.business_name||' '||b.id::text))>0)
 AND ($5::uuid IS NULL OR b.client_id=$5) AND ($6::uuid IS NULL OR b.customer_id=$6) AND ($7::uuid IS NULL OR b.marketplace_customer_id=$7)
 AND ($8::timestamptz IS NULL OR (b.start_at,b.id)>($8,$9::uuid)) ORDER BY b.start_at,b.id LIMIT 51`, start, end, f.Status, f.Q, f.BusinessID, f.ContactID, f.AccountID, after, afterID)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		b, e := scanBooking(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, b)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > 50 {
		out.Items = out.Items[:50]
		last := out.Items[49]
		raw, _ := json.Marshal(bookingCursor{last.StartAt, last.ID, scope})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

type BookingDetail struct {
	UpdatedAt      time.Time `json:"updated_at"`
	AllowedActions []string  `json:"allowed_actions"`
	Booking
	Timezone       string                             `json:"timezone"`
	Location       string                             `json:"location"`
	Notes          string                             `json:"notes"`
	BookingEmail   string                             `json:"booking_email"`
	AgreementTitle string                             `json:"agreement_title"`
	Payments       []appdata.BookingDetailPaymentItem `json:"payments"`
	Refunds        []appdata.BookingDetailRefundItem  `json:"refunds"`
	History        []appdata.BookingDetailEvent       `json:"history"`
}

func (s *Service) BookingDetail(ctx context.Context, id uuid.UUID) (BookingDetail, error) {
	b, e := scanBooking(s.db.QueryRow(ctx, `SELECT `+bookingColumns+bookingJoins+`WHERE b.id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		return BookingDetail{}, problem(404, "not_found", "Booking not found.")
	}
	if e != nil {
		return BookingDetail{}, e
	}
	// Reuse the existing domain detail/history owner; provider action permissions
	// and delivery internals are deliberately not part of the staff read model.
	d, e := s.bookings.GetBookingDetails(ctx, b.BusinessID, id)
	if e != nil {
		return BookingDetail{}, e
	}
	state, e := s.bookings.StaffBookingCommands(ctx, id)
	if e != nil {
		return BookingDetail{}, e
	}
	// Never bind a newly read command version to an older displayed snapshot.
	if !state.UpdatedAt.Equal(b.updatedAt) {
		return BookingDetail{}, conflict
	}
	out := BookingDetail{UpdatedAt: state.UpdatedAt, AllowedActions: state.Actions, Booking: b, Timezone: d.Timezone, Location: d.Location, Notes: d.Notes, Payments: d.PaymentHistory, Refunds: d.RefundHistory, History: d.ChangeHistory}
	e = s.db.QueryRow(ctx, `SELECT coalesce(customer_email_snapshot,''),agreement_title_snapshot FROM bookings WHERE id=$1`, id).Scan(&out.BookingEmail, &out.AgreementTitle)
	return out, e
}
