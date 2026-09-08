package appdata

import (
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestTessaPaymentFiltersCountAndListTheSameRows(t *testing.T) {
	ctx, pool := openTessaIntegrationPool(t)
	client, other := insertTessaTestClient(t, ctx, pool), insertTessaTestClient(t, ctx, pool)
	from, to, err := tessaDateRange("2026-09-01", "2026-09-30", "Africa/Lagos")
	if err != nil {
		t.Fatal(err)
	}
	add := func(owner uuid.UUID, status, payment string, i int) {
		t.Helper()
		customer := uuid.New()
		start := from.Add(time.Duration(i+1) * time.Hour)
		if _, err := pool.Exec(ctx, `INSERT INTO customers(id,client_id,full_name) VALUES($1,$2,'Payment filter customer')`, customer, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO bookings(id,client_id,customer_id,title,status,payment_status,agreement_status,start_at,end_at,occupied_start_at,occupied_end_at,currency_code,country_code)
		 VALUES($1,$2,$3,'Consultation',$4,$5,'not_required',$6,$7,$6,$7,'NGN','NG')`, uuid.New(), owner, customer, status, payment, start, start.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 12; i++ {
		add(client, "confirmed", []string{"unpaid", "full_payment_pending", "deposit_pending"}[i%3], i)
	}
	add(client, "pending", "paid_in_full", 13)
	add(client, "confirmed", "deposit_paid_balance_due", 14)
	add(client, "cancelled", "unpaid", 16)
	add(client, "canceled", "unpaid", 17)
	add(other, "confirmed", "unpaid", 15)
	repo := NewRepository(pool)
	for _, tc := range []struct {
		state    string
		statuses []string
		excluded []string
		count    int64
	}{
		{"any", nil, nil, 16}, {"unpaid", nil, nil, 14}, {"unpaid", []string{"pending"}, nil, 0}, {"balance_due", nil, nil, 1}, {"paid_in_full", []string{"pending"}, nil, 1},
		{"unpaid", nil, []string{"cancelled"}, 12}, {"any", []string{"cancelled"}, nil, 2}, {"any", nil, []string{"confirmed", "cancelled"}, 1},
	} {
		filter := TessaBookingFilter{Statuses: tc.statuses, ExcludedStatuses: tc.excluded, PaymentState: tc.state}
		count, err := repo.CountTessaBookings(ctx, client, from, to, filter)
		if err != nil || count.TotalBookings != tc.count {
			t.Fatal(tc, count, err)
		}
		list, err := repo.SearchTessaBookings(ctx, client, from, to, filter, 8, true)
		if err != nil || len(list.Items) != int(min(tc.count, 8)) || list.HasMore != (tc.count > 8) {
			t.Fatal(tc, list, err)
		}
	}
}
