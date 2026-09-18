package admin

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type OverviewBusinesses struct {
	Total      int64 `json:"total"`
	Verified   int64 `json:"verified"`
	Unverified int64 `json:"unverified"`
	Restricted int64 `json:"restricted"`
}
type OverviewStatus struct {
	Status string `json:"status"`
	Count  int64  `json:"count"`
}
type OverviewDay struct {
	Date  string `json:"date"`
	To    string `json:"to"`
	Count int64  `json:"count"`
}
type OverviewBookings struct {
	Total    int64            `json:"total"`
	Statuses []OverviewStatus `json:"statuses"`
	Days     []OverviewDay    `json:"days"`
}
type OverviewSupport struct {
	Active     int64 `json:"active"`
	Unassigned int64 `json:"unassigned"`
	Due        int64 `json:"due"`
	Waiting    int64 `json:"waiting"`
}
type Overview struct {
	From       string             `json:"from"`
	To         string             `json:"to"`
	AsOf       time.Time          `json:"as_of"`
	Today      string             `json:"today"`
	Businesses OverviewBusinesses `json:"businesses"`
	Bookings   OverviewBookings   `json:"bookings"`
	Support    *OverviewSupport   `json:"support,omitempty"`
}

// Platform report: provider dashboards use a single business's timezone and
// currency projections. Here appointment counts use the canonical admin register
// scope in UTC. No private records or cross-currency monetary totals are selected.
func (s *Service) Overview(ctx context.Context, session Session, from, to string) (Overview, error) {
	out := Overview{AsOf: s.now().UTC(), Bookings: OverviewBookings{Statuses: []OverviewStatus{}, Days: []OverviewDay{}}}
	if !allowed(session.Staff.Role, "reports.read") {
		return out, forbidden
	}
	start, end, e := bookingWindow(BookingFilter{From: from, To: to}, out.AsOf)
	if e != nil {
		return out, e
	}
	out.From, out.To, out.Today = start.Format(time.DateOnly), end.Format(time.DateOnly), out.AsOf.Format(time.DateOnly)
	tx, e := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	// Business snapshot matches the canonical directory; restriction and verification overlap.
	e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE p.verified),count(*) FILTER(WHERE NOT p.verified),count(*) FILTER(WHERE p.platform_restricted) FROM clients c JOIN client_profiles p ON p.client_id=c.id`).Scan(&out.Businesses.Total, &out.Businesses.Verified, &out.Businesses.Unverified, &out.Businesses.Restricted)
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT (b.start_at AT TIME ZONE 'UTC')::date::text,b.status,count(*)`+bookingJoins+`WHERE b.start_at >= $1 AND b.start_at < $2 GROUP BY 1,2`, start, end)
	if e != nil {
		return out, e
	}
	daily := map[string]int64{}
	statuses := map[string]int64{}
	for rows.Next() {
		var date, status string
		var count int64
		if e = rows.Scan(&date, &status, &count); e != nil {
			rows.Close()
			return out, e
		}
		daily[date] += count
		statuses[status] += count
		out.Bookings.Total += count
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return out, e
	}
	for date := start; date.Before(end); date = date.AddDate(0, 0, 1) {
		day := date.Format(time.DateOnly)
		out.Bookings.Days = append(out.Bookings.Days, OverviewDay{day, date.AddDate(0, 0, 1).Format(time.DateOnly), daily[day]})
	}
	for status, count := range statuses {
		out.Bookings.Statuses = append(out.Bookings.Statuses, OverviewStatus{status, count})
	}
	sort.Slice(out.Bookings.Statuses, func(i, j int) bool { return out.Bookings.Statuses[i].Status < out.Bookings.Statuses[j].Status })
	if allowed(session.Staff.Role, "support.read") {
		out.Support = &OverviewSupport{}
		e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE assignee_id IS NULL),count(*) FILTER(WHERE follow_up<=$1::date),count(*) FILTER(WHERE status='waiting') FROM admin_support_cases WHERE status<>'resolved'`, out.Today).Scan(&out.Support.Active, &out.Support.Unassigned, &out.Support.Due, &out.Support.Waiting)
		if e != nil {
			return out, e
		}
	}
	return out, tx.Commit(ctx)
}
